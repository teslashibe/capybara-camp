import Foundation

private final class CampRedirectPolicy: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        // Never forward a Camp device credential through a redirect.
        completionHandler(nil)
    }
}

actor CampHTTP {
    let endpoint: CampEndpoint
    private let session: URLSession
    init(endpoint: CampEndpoint, configuration: URLSessionConfiguration = .ephemeral) {
        self.endpoint = endpoint
        configuration.httpCookieStorage = nil
        configuration.urlCredentialStorage = nil
        configuration.timeoutIntervalForRequest = 30
        configuration.timeoutIntervalForResource = 360
        session = URLSession(configuration: configuration, delegate: CampRedirectPolicy(), delegateQueue: nil)
    }
    deinit { session.invalidateAndCancel() }

    private func request(_ method: String, path: String, body: Data?, key: String?, token: String?) throws -> URLRequest {
        var request = URLRequest(url: try endpoint.requestURL(path))
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token { request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization") }
        if let body {
            guard body.count <= 2 << 20 else { throw CampCloudError.responseTooLarge }
            request.httpBody = body; request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        if let key { request.setValue(key, forHTTPHeaderField: "Idempotency-Key") }
        return request
    }
    func data(_ method: String, path: String, body: Data? = nil, key: String? = nil, token: String? = nil) async throws -> Data {
        let request = try request(method, path: path, body: body, key: key, token: token)
        let (bytes, response) = try await session.bytes(for: request)
        guard let response = response as? HTTPURLResponse else { throw CampCloudError.invalidResponse }
        var data = Data()
        data.reserveCapacity(min(max(Int(response.expectedContentLength), 0), 4 << 20))
        for try await byte in bytes {
            guard data.count < 4 << 20 else { throw CampCloudError.responseTooLarge }
            data.append(byte)
        }
        guard (200..<300).contains(response.statusCode) else {
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw CampCloudError.server(response.statusCode, object?["error"] as? String ?? "request_failed")
        }
        return data
    }
    func get<T: Decodable & Sendable>(_ type: T.Type, path: String, token: String) async throws -> T {
        let data = try await data("GET", path: path, token: token)
        do { return try CampWire.decoder().decode(type, from: data) }
        catch { throw CampCloudError.invalidResponse }
    }
    func events(camp: String, after: Int64, token: String, receive: @Sendable (CloudEvent) async throws -> Void) async throws {
        guard UUID(uuidString: camp) != nil, after >= 0 else { throw CampCloudError.invalidResponse }
        var request = try request("GET", path: "/v1/camps/\(camp)/events", body: nil, key: nil, token: token)
        request.timeoutInterval = 330
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        request.setValue(String(after), forHTTPHeaderField: "Last-Event-ID")
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw CampCloudError.invalidResponse }
        guard http.statusCode == 200 else { throw CampCloudError.server(http.statusCode, http.statusCode == 409 ? "snapshot_required" : "stream_unavailable") }
        var line = Data(), payload = Data()
        for try await byte in bytes {
            try Task.checkCancellation()
            if byte == 10 {
                if line.last == 13 { line.removeLast() }
                if line.isEmpty && !payload.isEmpty {
                    guard let event = try? CampWire.decoder().decode(CloudEvent.self, from: payload) else { throw CampCloudError.invalidResponse }
                    try await receive(event); payload.removeAll(keepingCapacity: true)
                } else if line.starts(with: Data("data:".utf8)) {
                    guard payload.count + line.count <= 256 << 10 else { throw CampCloudError.responseTooLarge }
                    if !payload.isEmpty { payload.append(10) }
                    payload.append(contentsOf: line.dropFirst(5).drop(while: { $0 == 32 }))
                }
                line.removeAll(keepingCapacity: true)
            } else {
                guard line.count < 256 << 10 else { throw CampCloudError.responseTooLarge }
                line.append(byte)
            }
        }
    }
    func cancel() { session.invalidateAndCancel() }
}
