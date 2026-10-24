import Foundation

// Large attachment responses arrive in bounded chunks. Ordinary JSON and SSE
// keep their existing transports and limits. This session retains no cache.
final class CampBoundedHTTP: @unchecked Sendable {
    private let delegate = CampBodyDelegate()
    private let session: URLSession
    init(configuration: URLSessionConfiguration) {
        let configuration = configuration.copy() as! URLSessionConfiguration
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    }
    deinit { cancel() }
    func data(_ request: URLRequest, limit: Int) async throws -> (Data, HTTPURLResponse) {
        guard limit > 0 else { throw CampCloudError.responseTooLarge }
        let operation = CampBodyRequest(limit: limit, delegate: delegate)
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                operation.start(session: session, request: request, continuation: continuation)
            }
        } onCancel: { operation.cancel() }
    }
    func cancel() { delegate.cancelAll(); session.invalidateAndCancel() }
}

private final class CampBodyDelegate: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [Int: CampBodyRequest] = [:]
    func register(_ request: CampBodyRequest, task: Int) -> Bool {
        lock.lock(); defer { lock.unlock() }
        guard requests.count < 2 else { return false }
        requests[task] = request
        return true
    }
    func remove(_ task: Int) { lock.lock(); requests.removeValue(forKey: task); lock.unlock() }
    private func request(_ task: Int) -> CampBodyRequest? { lock.lock(); defer { lock.unlock() }; return requests[task] }
    func cancelAll() {
        lock.lock(); let active = Array(requests.values); lock.unlock()
        for request in active { request.cancel() }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        completionHandler(request(dataTask.taskIdentifier)?.receive(response) == true ? .allow : .cancel)
    }
    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        request(dataTask.taskIdentifier)?.receive(data)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        request(task.taskIdentifier)?.finish(error)
    }
}

private final class CampBodyRequest: @unchecked Sendable {
    private let lock = NSLock()
    private let limit: Int
    private weak var delegate: CampBodyDelegate?
    private var task: URLSessionDataTask?
    private var continuation: CheckedContinuation<(Data, HTTPURLResponse), Error>?
    private var response: HTTPURLResponse?
    private var bytes = Data()
    private var finished = false
    init(limit: Int, delegate: CampBodyDelegate) { self.limit = limit; self.delegate = delegate }
    func start(session: URLSession, request: URLRequest, continuation: CheckedContinuation<(Data, HTTPURLResponse), Error>) {
        lock.lock()
        guard !finished else { lock.unlock(); continuation.resume(throwing: CancellationError()); return }
        self.continuation = continuation
        let task = session.dataTask(with: request)
        self.task = task
        let admitted = delegate?.register(self, task: task.taskIdentifier) == true
        lock.unlock()
        guard admitted else { finish(CampCloudError.server(429, "attachment_busy")); return }
        task.resume()
    }
    func receive(_ value: URLResponse) -> Bool {
        guard let value = value as? HTTPURLResponse else { finish(CampCloudError.invalidResponse); return false }
        if value.expectedContentLength > Int64(limit) { finish(CampCloudError.responseTooLarge); return false }
        lock.lock(); defer { lock.unlock() }
        guard !finished else { return false }
        response = value
        bytes.reserveCapacity(min(max(Int(value.expectedContentLength), 0), limit))
        return true
    }
    func receive(_ chunk: Data) {
        lock.lock()
        guard !finished else { lock.unlock(); return }
        guard chunk.count <= limit - bytes.count else { lock.unlock(); finish(CampCloudError.responseTooLarge); return }
        bytes.append(chunk)
        lock.unlock()
    }
    func cancel() { finish(CancellationError()) }
    func finish(_ error: Error?) {
        lock.lock()
        guard !finished else { lock.unlock(); return }
        finished = true
        let task = self.task, continuation = self.continuation
        let result: Result<(Data, HTTPURLResponse), Error>
        if let error { result = .failure(error) }
        else if let response { result = .success((bytes, response)) }
        else { result = .failure(CampCloudError.invalidResponse) }
        self.task = nil; self.continuation = nil; response = nil; bytes = Data()
        lock.unlock()
        if let task { delegate?.remove(task.taskIdentifier); if error != nil { task.cancel() } }
        continuation?.resume(with: result)
    }
}
