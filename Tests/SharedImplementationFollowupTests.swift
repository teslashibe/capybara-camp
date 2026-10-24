import Foundation

actor FollowupFixture: CampPublishingTransport {
    let plan: CloudPlanningResult
    let run: SharedImplementationSummary
    let source: CloudSession
    var receipts: [String: Data] = [:]
    var bodies: [String: Data] = [:]
    var keys: [String] = []
    var hold = true
    var gate: CheckedContinuation<Void, Never>?
    var rejection = false
    init(plan: CloudPlanningResult, run: SharedImplementationSummary, source: CloudSession) { self.plan = plan; self.run = run; self.source = source }
    func waiting() -> Bool { gate != nil }
    func release() { gate?.resume(); gate = nil }
    func reject() { rejection = true }
    func snapshot() -> (Int, [String]) { (receipts.count, keys) }
    func perform(_ op: CampPublication) async throws -> Data {
        if op.method == "GET" {
            if op.path.hasSuffix("/planning") { return try CampWire.encoder().encode([CloudPlanSummary(plan)]) }
            return try CampWire.encoder().encode(plan)
        }
        precondition(op.kind == "implementation-request" && op.path.hasSuffix("/implementation-runs/\(run.id)/requests"))
        keys.append(op.key)
        if let data = receipts[op.key] { precondition(bodies[op.key] == op.body); return data }
        if rejection { throw CampCloudError.server(409, "changed") }
        let body = try JSONSerialization.jsonObject(with: op.body) as! [String: Any]
        let request = CloudRequest(id: UUID().uuidString, campId: plan.campId!, sessionId: source.id, requesterUserId: plan.creatorUserId!,
            ownerUserId: source.ownerUserId, ownerDeviceId: source.ownerDeviceId, permissionRevision: 1, revision: 1,
            prompt: body["prompt"] as! String, model: body["model"] as! String, effort: body["effort"] as! String, speed: body["speed"] as! String,
            state: "pending_approval", createdAt: Date(), sources: [], kind: "implementation", planId: plan.id, planVersion: 1,
            implementationId: run.id, providerSessionId: "isolated-provider-conversation")
        let data = try CampWire.encoder().encode(request)
        receipts[op.key] = data; bodies[op.key] = op.body
        if hold { hold = false; await withCheckedContinuation { gate = $0 }; throw URLError(.networkConnectionLost) }
        return data
    }
}

@main struct SharedImplementationFollowupTests {
    @MainActor static func until(_ name: String, _ predicate: () async -> Bool) async throws {
        for _ in 0..<600 { if await predicate() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(name)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("implementation-followup-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "creator@example.com", name: "Creator")
        let source = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString,
            provider: "Codex", profileLabel: "Owner", ownerUserId: UUID().uuidString, ownerDeviceId: UUID().uuidString, localSessionId: UUID().uuidString,
            title: "Account selector", model: "original", state: "idle", stage: "Ready", shared: true, allowRequests: true, externalOwner: false,
            revision: 1, transcriptRevision: 0, permissionRevision: 1, updatedAt: Date(), executionLocation: "local", executionEpoch: 1)
        let plan = CloudPlanningResult(id: UUID().uuidString, title: "Plan", projectId: source.projectId,
            version: .init(version: 1, state: "ready", brief: "Implement", planners: [.init(slot: 0, sessionId: source.id, provider: "Codex", model: "planner", effort: "high", speed: "standard")], steps: [], sources: [], mode: .single),
            campId: scope.campId, creatorUserId: scope.userId, shared: true, revision: 1, activeVersion: 1, updatedAt: Date())
        let run = SharedImplementationSummary(id: UUID().uuidString, approvalId: UUID().uuidString, planVersion: 1, createdAt: Date(), requestId: UUID().uuidString,
            sessionId: source.id, ownerUserId: source.ownerUserId, provider: "Codex", model: "original", effort: "high", speed: "standard", state: "completed", executionLocation: "local")
        let api = FollowupFixture(plan: plan, run: run, source: source)
        let cache = try CampCache(directory: root)
        let first = CampPlanning(cache: cache); first.attach(scope: scope, actor: actor, transport: api, sessions: [source])
        try await until("Attach failed") { first.ready }
        try await first.queueImplementationPrompt(run, plan: plan, text: "Review edge cases", model: "third-model", tuning: ModelTuning(effort: "ultra", speed: "ultrafast"))
        try await until("Request was not sent") { await api.waiting() }
        first.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        let restored = CampPlanning(cache: cache)
        var replies: [CloudRequest] = []; restored.implementationDispatched = { replies.append($0) }
        restored.attach(scope: scope, actor: actor, transport: api, sessions: [source]); await api.release()
        try await until("Saved follow-up did not recover") { replies.count == 1 && !restored.pending(plan.id) }
        let snapshot = await api.snapshot()
        precondition(snapshot.0 == 1 && snapshot.1.count == 2 && Set(snapshot.1).count == 1)
        precondition(replies[0].implementationId == run.id && replies[0].planId == plan.id && replies[0].state == "pending_approval")
        precondition(replies[0].model == "third-model" && replies[0].effort == "ultra" && replies[0].speed == "ultrafast")
        var wrongPlan = plan; wrongPlan.activeVersion = 2
        do { try await restored.queueImplementationPrompt(run, plan: wrongPlan, text: "Wrong version", model: "model", tuning: ModelTuning()); preconditionFailure("Old plan version accepted") } catch {}
        var claude = run; claude.provider = "Claude"
        do { try await restored.queueImplementationPrompt(claude, plan: plan, text: "Wrong speed", model: "model", tuning: ModelTuning(speed: "ultrafast")); preconditionFailure("Claude Ultra Fast accepted") } catch {}
        await api.reject()
        try await restored.queueImplementationPrompt(run, plan: plan, text: "Keep my rejected prompt", model: "model", tuning: ModelTuning())
        try await until("Rejected prompt disappeared") { restored.failedImplementationPrompts.count == 1 && !restored.pending(plan.id) }
        restored.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        let reopened = CampPlanning(cache: cache); reopened.attach(scope: scope, actor: actor, transport: api, sessions: [source])
        try await until("Rejected prompt did not survive restart") { reopened.ready && reopened.failedImplementationPrompts.count == 1 }
        let before = await api.snapshot().1.count
        reopened.refresh(); try await Task.sleep(for: .milliseconds(50))
        let after = await api.snapshot().1.count
        precondition(before == after, "Rejected follow-up automatically retried")
        let recovered = try await reopened.recoverImplementationPrompt(reopened.failedImplementationPrompts[0].id)
        precondition(recovered == "Keep my rejected prompt" && reopened.failedImplementationPrompts.isEmpty)
        var revoked = source; revoked.permissionRevision = 2
        reopened.attach(scope: scope, actor: actor, transport: api, sessions: [revoked])
        do {
            try await reopened.queueImplementationPrompt(run, plan: plan, text: "Stale source", model: "model", tuning: ModelTuning(), sources: [.init(sessionId: source.id, transcriptRevision: 1, permissionRevision: 1)])
            preconditionFailure("Stale source grant accepted")
        } catch {}
        // Work belongs to the implementation ticket, never its source chat.
        let store = CampStore(stateDirectory: root.appendingPathComponent("native"))
        var running = replies[0]; running.state = "started"
        store.cloud.requests = [running]; store.applyCloud([source], projects: [])
        let imported = store.sessions.first { $0.id == "camp:" + source.id }!
        precondition(!store.isBusy(imported) && store.stage(imported) == .ready)
        running.kind = "chat"; running.planId = nil; running.planVersion = nil; running.implementationId = nil
        store.cloud.requests = [running]; store.applyCloud([source], projects: [])
        precondition(store.isBusy(imported) && store.stage(imported) == .progress)
        store.cloud.requests = []; store.applyCloud([source], projects: [])
        store.sharedPlanning.attach(scope: scope, actor: actor, transport: api, sessions: [source])
        try await until("Board planning did not attach") { store.sharedPlanning.ready }
        var implemented = plan; implemented.implementation = run; implemented.implementation?.state = "started"
        try await store.sharedPlanning.install(implemented)
        let boardID = "camp-plan:" + plan.id
        let card = store.sessions.first { $0.id == boardID }!
        precondition(card.provider == source.provider && card.title == plan.title && card.model == run.model && store.stage(card) == .progress)
        precondition(store.boardSessions(.progress).contains { $0.id == boardID })
        store.open(card)
        store.loadHistory()
        precondition(!store.loading && store.banner.isEmpty, "Shared implementation entered the local transcript reader")
        precondition(!store.start(card, prompt: "Wrong transport", model: run.model, tuning: ModelTuning()), "Shared implementation entered the ordinary local chat transport")
        implemented.implementation?.state = "completed"
        try await store.sharedPlanning.install(implemented)
        precondition(store.stage(card) == .attention && store.boardSessions(.attention).contains { $0.id == boardID })
        precondition(store.sessions.filter { $0.id == boardID }.count == 1, "Implementation duplicated the planning card")
        let encoded = try CampWire.encoder().encode(CloudPlanSummary(implemented))
        let decoded = try CampWire.decoder().decode(CloudPlanSummary.self, from: encoded)
        precondition(decoded.implementation?.id == run.id && decoded.stage == .attention)
        store.sharedPlanning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        reopened.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        print("PASS: durable implementation follow-ups, exact replay, owner approval, rejected-prompt recovery, grant/version checks, source activity isolation and same-ticket board conversion")
    }
}
