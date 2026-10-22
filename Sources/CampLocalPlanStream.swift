import Foundation

// One in-flight snapshot and the latest replacement keep a slow connection
// from accumulating per-token work. Final output uses the durable finish path.
@MainActor final class CampLocalPlanStream {
    private struct Pending { var body: Data; var seq: Int64; var settings: ReportedModelSettings?; var text: String? }
    private let request: CloudRequest
    private let transport: any CampPublishingTransport
    private var seq: Int64
    private var text = ""
    private var settings = ReportedModelSettings()
    private var sentText = ""
    private var sentSettings = ReportedModelSettings()
    private var pending: Pending?
    private var task: Task<Void, Never>?
    private var stopped = false
    var failed: ((String) -> Void)?
    init(request: CloudRequest, transport: any CampPublishingTransport) {
        self.request = request; self.transport = transport; seq = request.streamSeq ?? 0
    }
    deinit { task?.cancel() }
    func update(text: String, settings: ReportedModelSettings) {
        guard !stopped, text.utf8.count <= 8192 else { return }
        self.text = text; self.settings = settings
        guard task == nil else { return }
        task = Task { [weak self] in
            var delay = 0.25
            while !Task.isCancelled {
                do {
                    guard let more = try await self?.send(), more else { self?.task = nil; return }
                    delay = 0.25
                    await Task.yield()
                } catch is CancellationError { return }
                catch CampCloudError.server(let status, _) where [400, 401, 403, 404, 409].contains(status) {
                    self?.failed?("Live planning output is unavailable"); self?.stop(); return
                } catch {
                    do { try await Task.sleep(for: .seconds(delay)) } catch { return }
                    delay = min(delay * 2, 2)
                }
            }
        }
    }
    func stop() { stopped = true; task?.cancel(); task = nil; pending = nil; text = "" }
    private func send() async throws -> Bool {
        guard !stopped else { return false }
        if pending == nil {
            var event: [String: Any]
            var changedSettings: ReportedModelSettings?, changedText: String?
            if settings != sentSettings {
                changedSettings = settings; event = ["type": "settings"]
                if let model = settings.model { event["model"] = model }
                if let effort = settings.effort { event["effort"] = effort }
                if let speed = settings.speed { event["speed"] = speed }
            } else if text != sentText { changedText = text; event = ["type": "text", "text": text] }
            else { return false }
            pending = Pending(body: try CampWire.body(["seq": seq + 1, "event": event, "lease_token": request.leaseToken ?? ""]), seq: seq + 1, settings: changedSettings, text: changedText)
        }
        guard let pending else { return false }
        let operation = CampPublication(kind: "planning-stream", method: "POST", path: "/v1/camps/\(request.campId)/requests/\(request.id)/planning-events", body: pending.body)
        let data = try await transport.perform(operation)
        try Task.checkCancellation()
        guard !stopped else { return false }
        struct Ack: Decodable { var seq: Int64 }
        guard try CampWire.decoder().decode(Ack.self, from: data).seq == pending.seq else { throw CampCloudError.invalidResponse }
        seq = pending.seq
        if let settings = pending.settings { sentSettings = settings }
        if let text = pending.text { sentText = text }
        self.pending = nil
        return true
    }
}
