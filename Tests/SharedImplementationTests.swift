import Foundation

actor SharedImplementationFixture: CampPublishingTransport, CampAccountTransport {
    let plan: CloudPlanningResult
    let run: SharedImplementationSummary
    var request: CloudRequest
    var hold = false
    var gate: CheckedContinuation<Void, Never>?
    var reject = false
    var emptyDiscovery = false
    var paths: [String] = []
    init(plan: CloudPlanningResult, run: SharedImplementationSummary, request: CloudRequest) { self.plan = plan; self.run = run; self.request = request }
    func suspend() { hold = true }
    func waiting() -> Bool { gate != nil }
    func release() { gate?.resume(); gate = nil }
    func restrict() { request = request.restrictingContent() }
    func deny() { reject = true }
    func empty(_ value: Bool) { emptyDiscovery = value }
    func seenPaths() -> [String] { paths }
    func perform(_ op: CampPublication) async throws -> Data {
        paths.append(op.path)
        let data: Data
        if op.path.contains("implementation-runs") {
            if reject { throw CampCloudError.server(404, "not_available") }
            if op.path.contains("/requests?") {
                let older = op.path.contains("before=")
                var value = request; value.leaseToken = nil
                if older { value.id = "00000000-0000-4000-8000-000000000001"; value.prompt = "Earlier request" }
                data = try CampWire.encoder().encode(SharedImplementationRequests(id: run.id, approvalId: run.approvalId, planId: plan.id, planVersion: 1,
                    createdAt: run.createdAt, requests: [value], requestsBefore: older ? nil : value.id))
            } else { data = try CampWire.encoder().encode(SharedImplementationPage(implementations: emptyDiscovery ? [] : [run])) }
            if hold { hold = false; await withCheckedContinuation { gate = $0 } }
        } else if op.path.hasSuffix("/planning") { data = try CampWire.encoder().encode([CloudPlanSummary(plan)]) }
        else { data = try CampWire.encoder().encode(plan) }
        return data
    }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        paths.append(path)
        if path.contains("/events?") {
            return try CampWire.body(["events": [
                ["seq": 1, "event": ["type": "settings", "model": "actual-model", "effort": "ultra", "speed": "fast"]],
                ["seq": 2, "event": ["type": "delta", "text": "Live implementation output"]]
            ], "next": 2])
        }
        return try CampWire.encoder().encode(request)
    }
}

@main struct SharedImplementationTests {
    @MainActor static func until(_ label: String, _ predicate: () async -> Bool) async throws {
        for _ in 0..<400 { if await predicate() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("shared-implementation-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        var source = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId, ownerDeviceId: actor.deviceId, localSessionId: UUID().uuidString, title: "Executor", model: "selected", state: "idle", stage: "Ready", shared: false, allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 0, permissionRevision: 1, updatedAt: Date())
        let plan = CloudPlanningResult(id: UUID().uuidString, title: "Plan", projectId: source.projectId,
            version: .init(version: 1, state: "ready", brief: "Implement it", planners: [.init(slot: 0, sessionId: source.id, provider: "Codex", model: "selected", effort: "high", speed: "standard")], steps: [], sources: [], mode: .single),
            campId: scope.campId, creatorUserId: scope.userId, shared: false, revision: 1, activeVersion: 1, updatedAt: Date())
        let run = SharedImplementationSummary(id: UUID().uuidString, approvalId: UUID().uuidString, planVersion: 1, createdAt: Date(), requestId: UUID().uuidString,
            sessionId: source.id, ownerUserId: scope.userId, provider: "Codex", model: "selected", effort: "high", speed: "standard", state: "started", executionLocation: "local")
        let request = CloudRequest(id: run.requestId, campId: scope.campId, sessionId: source.id, requesterUserId: scope.userId, ownerUserId: scope.userId, ownerDeviceId: actor.deviceId,
            permissionRevision: 1, revision: 1, prompt: "Approved implementation", model: "selected", effort: "high", speed: "standard", state: "started", createdAt: Date(), sources: [], kind: "implementation", planId: plan.id, planVersion: 1, executionLocation: "local", streamSeq: 2, implementationId: run.id)
        let api = SharedImplementationFixture(plan: plan, run: run, request: request)
        let planning = CampPlanning(cache: try CampCache(directory: root))
        planning.attach(scope: scope, actor: actor, transport: api, sessions: [source])
        try await until("Planning did not attach") { planning.ready }
        let history = SharedImplementationHistory(planning: planning)
        await api.empty(true); await api.suspend()
        history.bind(plan)
        try await until("Discovery was not held") { await api.waiting() }
        await api.empty(false); history.synchronize([request]); await api.release()
        try await until("Run not discovered after reopen") { !history.loading && history.runs.count == 1 }
        let displayedRun = history.runs[0]
        history.select(displayedRun)
        try await until("Requests not loaded") { !history.loading && history.selectedRequest?.id == request.id }
        history.loadEarlier()
        try await until("Older requests not loaded") { !history.loading && history.selectedRequest?.prompt == "Earlier request" }
        precondition(history.requests.count == 1 && history.requestsBefore == nil, "Old pages accumulated instead of being replaced")
        // A permission revision changes without removing the source row. The
        // stale response must still be fenced, including an empty history page.
        await api.suspend()
        let pending = Task { try await planning.readImplementationRuns(plan) }
        try await until("Read was not held") { await api.waiting() }
        source.permissionRevision += 1
        planning.attach(scope: scope, actor: actor, transport: api, sessions: [source])
        await api.release()
        do { _ = try await pending.value; preconditionFailure("Changed grant accepted a stale response") } catch is CancellationError {}
        await api.suspend(); history.select(displayedRun)
        try await until("Detail was not held") { await api.waiting() }
        history.clear(); await api.release()
        try await Task.sleep(for: .milliseconds(30))
        precondition(history.requests.isEmpty && history.selected == nil, "Closed view retained late content")
        let live = CloudLiveConversation()
        let credential = CloudCredential(origin: scope.origin, actor: actor, token: "synthetic", expiresAt: .distantFuture)
        live.watchImplementation(request, scope: scope, client: api, credential: credential)
        try await until("Implementation did not stream") { live.messages.first?.text == "Live implementation output" }
        precondition(live.head?.effort == "ultra" && live.head?.model == "actual-model" && live.head?.speed == "fast")
        live.invalidate(source.id)
        try await Task.sleep(for: .milliseconds(30))
        precondition(live.head?.effort == "ultra" && live.head?.model == "actual-model", "Refresh replaced reported settings with defaults")
        let paths = await api.seenPaths()
        precondition(!paths.contains { $0.contains("/sessions/\(source.id)/stream") }, "Implementation followed the account source chat")
        await api.restrict(); live.invalidate(source.id)
        try await until("Restricted request retained text") { live.messages.isEmpty && live.sessionID == nil }
        await api.deny(); history.bind(plan)
        try await until("Denied history retained content") { !history.loading && !history.error.isEmpty }
        precondition(history.runs.isEmpty && history.selected == nil)
        planning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        print("PASS: implementation discovery, paged history, permission-revision fencing, stream identity, actual settings and revoked content")
    }
}
