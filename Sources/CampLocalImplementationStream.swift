import Foundation

// The pipe reader awaits each saved event. Only one bounded event is in flight;
// retries reuse its sequence, and a process restart never repeats a provider turn.
@MainActor final class CampLocalImplementationStream {
    private let request: CloudRequest
    private let scope: CloudScope
    private let epoch: Int64
    private let cache: CampCache
    private let transport: any CampPublishingTransport
    private var text = ""
    private var stopped = false
    // Newest text snapshot not yet sent. While one send is in flight later
    // snapshots replace it, so the provider is not held for a round trip per
    // token and the stream carries one delta per send instead of one per token.
    private var pendingText: String?
    private var drain: Task<Void, Never>?
    private var drainError: Error?
    init(request: CloudRequest, scope: CloudScope, epoch: Int64, cache: CampCache, transport: any CampPublishingTransport) {
        self.request = request; self.scope = scope; self.epoch = epoch; self.cache = cache; self.transport = transport
    }
    func stop() { stopped = true; text = ""; pendingText = nil }
    func emit(_ data: Data) async throws {
        guard !stopped, let value = try JSONSerialization.jsonObject(with: data) as? [String: Any], let kind = value["type"] as? String else { throw CancellationError() }
        if let drainError { throw drainError }
        if kind == "text" {
            guard let next = value["text"] as? String, next.utf8.count <= 2 << 20 else { throw CampCloudError.responseTooLarge }
            pendingText = next
            if drain == nil { drain = Task { await self.sendPendingText() } }
            return
        }
        // Every other event keeps its order behind the text before it.
        try await finishText()
        if kind == "boundary" { text = "" }
        try await send(value)
    }
    private func sendPendingText() async {
        defer { drain = nil }
        do { while let next = pendingText { pendingText = nil; try await sendText(next) } }
        catch { drainError = error; pendingText = nil }
    }
    private func finishText() async throws {
        while let task = drain { await task.value }
        if let drainError { throw drainError }
    }
    private func sendText(_ next: String) async throws {
        var delta = next
        if next.hasPrefix(text) { delta = String(next.dropFirst(text.count)) }
        else { try await send(["type": "reset"]) }
        text = next
        var bytes = Array(delta.utf8)
        while !bytes.isEmpty {
            var count = min(bytes.count, 32 << 10)
            while count < bytes.count && bytes[count] & 0xc0 == 0x80 { count -= 1 }
            try await send(["type": "delta", "text": String(decoding: bytes.prefix(count), as: UTF8.self)])
            bytes.removeFirst(count)
        }
    }
    private func send(_ event: [String: Any]) async throws {
        guard !stopped else { throw CancellationError() }
        let data = try CampWire.body(event)
        let body = try await cache.stageWorkerEvent(request.id, event: data, scope: scope, expectedEpoch: epoch)
        try await flush(body)
    }
    func flush(_ body: Data) async throws {
        struct Ack: Decodable { var seq: Int64 }
        guard let value = try JSONSerialization.jsonObject(with: body) as? [String: Any], let seq = (value["seq"] as? NSNumber)?.int64Value else { throw CampCloudError.invalidResponse }
        let endpoint = request.localContinuation == true ? "continuation-events" : "implementation-events"
        let command = CampPublication(kind: "implementation-stream", method: "POST", path: "/v1/camps/\(scope.campId)/requests/\(request.id)/\(endpoint)", body: body)
        for retry in 0..<3 {
            guard !stopped else { throw CancellationError() }
            do {
                let ack = try CampWire.decoder().decode(Ack.self, from: await transport.perform(command))
                guard ack.seq == seq else { throw CampCloudError.invalidResponse }
                try await cache.acknowledgeWorkerEvent(request.id, body: body, seq: seq, scope: scope, expectedEpoch: epoch)
                return
            } catch CampCloudError.server(let status, _) where [400,401,403,404,409].contains(status) { throw CampCloudError.server(status, "implementation_stream_unavailable") }
            catch {
                if retry == 2 { throw error }
                try await Task.sleep(for: .milliseconds(250 * (retry + 1)))
            }
        }
    }
}

struct LocalWorkerControl: Decodable, Sendable {
    var id: String
    var control: CloudControlEnvelope.Input
    var valid: Bool {
        UUID(uuidString: id) != nil && UUID(uuidString: control.id) != nil &&
        ["steer", "approval"].contains(control.type) &&
        (control.type == "steer" ? control.id == id && !(control.text ?? "").isEmpty : control.allow != nil)
    }
}
