import Foundation

actor PlanningStreamFixture: CampPublishingTransport, CampAccountTransport {
    let requestID = UUID().uuidString
    var values: [Int64: Data] = [:]
    var attempts = 0
    var lose = true
    var hold = false
    var held: CheckedContinuation<Void, Never>?
    var headHold = false
    var heldHead: CheckedContinuation<Void, Never>?
    var paths: [String] = []
    var reject = false
    func suspendWrite() { hold = true }
    func waiting() -> Bool { held != nil }
    func release() { hold = false; held?.resume(); held = nil }
    func suspendHead() { headHold = true }
    func headWaiting() -> Bool { heldHead != nil }
    func releaseHead() { heldHead?.resume(); heldHead = nil }
    func deny() { reject = true }
    func stats() -> (Int, Int, String, [String]) {
        let texts = values.sorted { $0.key < $1.key }.compactMap { try? JSONSerialization.jsonObject(with: $0.value) as? [String: Any] }.compactMap { $0["text"] as? String }
        return (attempts, values.count, texts.last ?? "", paths)
    }
    func perform(_ operation: CampPublication) async throws -> Data {
        attempts += 1
        if hold { await withCheckedContinuation { held = $0 } }
        if reject { throw CampCloudError.server(403, "revoked") }
        let body = try JSONSerialization.jsonObject(with: operation.body) as! [String: Any]
        let seq = (body["seq"] as! NSNumber).int64Value
        let event = try CampWire.body(body["event"] as! [String: Any])
        if let saved = values[seq] { precondition(saved == event, "Response loss changed the retry body") }
        else { precondition(seq == Int64(values.count + 1)); values[seq] = event }
        if lose { lose = false; throw URLError(.networkConnectionLost) }
        return try CampWire.body(["seq": seq])
    }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        paths.append(path)
        if reject { throw CampCloudError.server(403, "revoked") }
        if path.contains("/steps/") {
            if headHold {
                headHold = false
                await withCheckedContinuation { heldHead = $0 }
                return Data("null".utf8)
            }
            return try CampWire.encoder().encode(CloudStreamHead(id: requestID, state: "started", seq: Int64(values.count), model: "selected-model", effort: "high", speed: "standard"))
        }
        precondition(path.contains("/requests/\(requestID)/events?after="), "Planning used the ordinary session stream")
        let after = Int64(path.split(separator: "=").last!)!
        let events = try values.sorted { $0.key < $1.key }.filter { $0.key > after }.map { (seq, data) -> [String: Any] in
            ["seq": seq, "event": try JSONSerialization.jsonObject(with: data)]
        }
        return try CampWire.body(["events": events, "next": values.keys.max() ?? 0])
    }
}

@main struct CampPlanningStreamTests {
    @MainActor static func until(_ label: String, _ test: () async -> Bool) async throws {
        for _ in 0..<500 { if await test() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let scope = CloudScope(origin: "https://camp.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let credential = CloudCredential(origin: scope.origin, actor: .init(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture"), token: "synthetic", expiresAt: .distantFuture)
        let sessionID = UUID().uuidString
        let fixture = PlanningStreamFixture()
        let request = CloudRequest(id: fixture.requestID, campId: scope.campId, sessionId: sessionID,
            requesterUserId: scope.userId, ownerUserId: scope.userId, ownerDeviceId: credential.actor.deviceId,
            permissionRevision: 1, revision: 1, prompt: "Private planning brief", model: "selected-model", effort: "high", speed: "standard", state: "started",
            leaseToken: UUID().uuidString, leaseUntil: .distantFuture, createdAt: Date(), sources: [], kind: "planning", planId: UUID().uuidString, planVersion: 1, planStep: 0)
        let publisher = CampLocalPlanStream(request: request, transport: fixture)
        await fixture.suspendWrite()
        publisher.update(text: "First", settings: .init(model: "actual-model", effort: "ultra", speed: "fast"))
        try await until("Publisher did not start") { await fixture.waiting() }
        for i in 0..<1000 { publisher.update(text: "Latest \(i)", settings: .init(model: "actual-model", effort: "ultra", speed: "fast")) }
        await fixture.release()
        try await until("Lost response did not replay and drain") { await fixture.stats().2 == "Latest 999" }
        let stats = await fixture.stats()
        precondition(stats.0 == 3 && stats.1 == 2, "Slow writes accumulated per-token updates")
        let live = CloudLiveConversation()
        live.watchPlanning(planID: request.planId!, version: 1, ordinal: 0, sessionID: sessionID, scope: scope, client: fixture, credential: credential)
        try await until("Partial output did not reach the open plan") { live.messages.first?.text == "Latest 999" }
        precondition(live.head?.model == "actual-model" && live.head?.effort == "ultra" && live.head?.speed == "fast")
        let paths = await fixture.stats().3
        precondition(paths.contains { $0.contains("/planning/\(request.planId!)/steps/0/stream?version=1") })
        publisher.update(text: String(repeating: "x", count: 8193), settings: .init())
        try await Task.sleep(for: .milliseconds(40))
        let unchanged = await fixture.stats(); precondition(unchanged.1 == 2)
        // A previous reader may complete after the user switches versions.
        await fixture.suspendHead(); live.invalidate(sessionID)
        try await until("Old read was not held") { await fixture.headWaiting() }
        live.watchPlanning(planID: request.planId!, version: 2, ordinal: 0, sessionID: sessionID, scope: scope, client: fixture, credential: credential)
        try await until("New version did not bind") { live.messages.first?.text == "Latest 999" }
        await fixture.releaseHead()
        try await Task.sleep(for: .milliseconds(40))
        precondition(live.messages.first?.text == "Latest 999", "Old null response cleared the new projection")
        await fixture.deny(); live.invalidate(sessionID)
        try await until("Revoked stream remained visible") { live.messages.isEmpty && live.sessionID == nil }
        publisher.stop()
        let stopped = await fixture.stats().0
        publisher.update(text: "After stop", settings: .init())
        try await Task.sleep(for: .milliseconds(40))
        let afterStop = await fixture.stats().0; precondition(stopped == afterStop)
        print("PASS: live planning snapshots, coalescing, lost-response replay, reported settings, version switching and revocation")
    }
}
