import Foundation

actor ExecutionReviewFixture {
    let plan: CloudPlanningResult
    let source: CloudSession
    var approval: SharedExecutionApproval?
    var receipts: [String: (Data, Data)] = [:]
    var calls: [String: [String]] = [:]
    var counts: [String: Int] = [:]
    var lose: Set<String> = ["prepare", "approve", "dispatch"]
    var stale = false
    var activeImplementation = false
    var holding = false
    var gate: CheckedContinuation<Void, Never>?
    init(plan: CloudPlanningResult, source: CloudSession) { self.plan = plan; self.source = source }
    func count(_ action: String) -> Int { counts[action] ?? 0 }
    func keys(_ action: String) -> [String] { calls[action] ?? [] }
    func setStale() { stale = true }
    func completeImplementation() { activeImplementation = false }
    func hold() { holding = true }
    func waiting() -> Bool { gate != nil }
    func release() { gate?.resume(); gate = nil }
    func perform(_ op: CampPublication, userID: String) async throws -> Data {
        if op.method == "GET" {
            if op.path.hasSuffix("/execution") { return try CampWire.encoder().encode(CloudExecutionTarget(location: "local", epoch: 1, mode: "chat", permissions: [:], available: true)) }
            if op.path.hasSuffix("/execution-approvals") {
                return try CampWire.encoder().encode(approval.map { [SharedExecutionStatus(id: $0.id, state: $0.state, revision: $0.revision, reviewRequired: stale)] } ?? [])
            }
            if op.path.contains("/execution-approvals/") {
                guard !stale, let approval else { throw CampCloudError.server(409, "changed") }
                return try CampWire.encoder().encode(approval)
            }
            if op.path.hasSuffix("/planning") { return try CampWire.encoder().encode([CloudPlanSummary(plan)]) }
            return try CampWire.encoder().encode(plan)
        }
        let action = String(op.kind.dropFirst("plan-execution-".count))
        calls[action, default: []].append(op.key)
        let key = userID + ":" + op.key
        if let saved = receipts[key] { precondition(saved.0 == op.body, "A retry changed the approved payload"); return saved.1 }
        let data: Data
        if action == "prepare" {
            precondition(userID == plan.creatorUserId)
            guard !activeImplementation else { throw CampCloudError.server(409, "active_implementation") }
            let input = try CampWire.decoder().decode(SharedExecutionInput.self, from: op.body)
            approval = SharedExecutionApproval(id: UUID().uuidString, campId: plan.campId!, planId: plan.id, planVersion: 1, planRevision: 1,
                planSha256: String(repeating: "a", count: 64), creatorUserId: userID, ownerUserId: source.ownerUserId, ownerDeviceId: source.ownerDeviceId,
                sessionId: source.id, profileId: source.profileId, sessionRevision: source.revision, transcriptRevision: source.transcriptRevision, permissionRevision: source.permissionRevision,
                provider: source.provider, model: input.executor.model, effort: input.executor.effort, speed: input.executor.speed, execution: input.executor.execution!, permissions: input.permissions,
                prompt: "Implement the frozen plan", state: "pending", revision: 1)
            data = try CampWire.encoder().encode(approval!)
        } else {
            guard var current = approval else { throw CampCloudError.server(404, "missing") }
            let body = try JSONSerialization.jsonObject(with: op.body) as! [String: Any]
            precondition((body["expected_revision"] as? Int64) == current.revision)
            if action == "approve" {
                precondition(userID == current.ownerUserId && current.state == "pending")
                current.executionContext = body["execution_context"] as? String
                precondition(current.executionContext == String(repeating: "a", count: 64))
                current.state = "approved"; current.revision += 1; approval = current
                data = try CampWire.encoder().encode(current)
            } else if action == "dispatch" {
                precondition(current.state == "approved")
                current.state = "dispatched"; current.revision += 1; approval = current
                activeImplementation = true
                let request = CloudRequest(id: UUID().uuidString, campId: current.campId, sessionId: current.sessionId, requesterUserId: current.creatorUserId,
                    ownerUserId: current.ownerUserId, ownerDeviceId: current.ownerDeviceId, permissionRevision: 1, revision: 1, prompt: current.prompt,
                    model: current.model, effort: current.effort, speed: current.speed, state: "queued", createdAt: Date(), sources: [], kind: "implementation",
                    planId: plan.id, planVersion: 1, implementationId: UUID().uuidString)
                data = try CampWire.encoder().encode(request)
            } else {
                precondition(action == "cancel")
                current.state = "cancelled"; current.revision += 1; approval = current; data = Data()
            }
        }
        counts[action, default: 0] += 1; receipts[key] = (op.body, data)
        if holding { holding = false; await withCheckedContinuation { gate = $0 } }
        if lose.remove(action) != nil { throw URLError(.networkConnectionLost) }
        return data
    }
}
struct ExecutionReviewClient: CampPublishingTransport {
    let fixture: ExecutionReviewFixture
    let userID: String
    func perform(_ operation: CampPublication) async throws -> Data { try await fixture.perform(operation, userID: userID) }
}

@main struct SharedExecutionReviewTests {
    @MainActor static func until(_ label: String, _ predicate: () async -> Bool) async throws {
        for _ in 0..<600 { if await predicate() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("shared-execution-review-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let creator = CloudActor(userId: UUID().uuidString, deviceId: UUID().uuidString, email: "creator@example.com", name: "Creator")
        let owner = CloudActor(userId: UUID().uuidString, deviceId: UUID().uuidString, email: "owner@example.com", name: "Owner")
        let scope = CloudScope(origin: "https://fixture.example", userId: creator.userId, campId: UUID().uuidString)
        let ownerScope = CloudScope(origin: scope.origin, userId: owner.userId, campId: scope.campId)
        let source = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: "Codex", profileLabel: "Owner", ownerUserId: owner.userId, ownerDeviceId: owner.deviceId, localSessionId: UUID().uuidString, title: "Executor", model: "fixture", state: "idle", stage: "Ready", shared: true, allowRequests: true, externalOwner: false, revision: 1, transcriptRevision: 0, permissionRevision: 1, updatedAt: Date(), executionLocation: "local", executionEpoch: 1)
        let plan = CloudPlanningResult(id: UUID().uuidString, title: "Plan", projectId: source.projectId,
            version: .init(version: 1, state: "ready", brief: "A bounded plan", planners: [.init(slot: 0, sessionId: source.id, provider: source.provider, model: source.model, effort: "high", speed: "standard")], steps: [], sources: [], mode: .single),
            campId: scope.campId, creatorUserId: creator.userId, shared: true, revision: 1, activeVersion: 1, updatedAt: Date())
        // Existing snake-case cache data must retain acronym-bearing identities.
        let savedCommand = CampPublication(kind: "create-plan", method: "POST", path: "/fixture", body: Data(), publishedSessionID: source.id)
        let savedOperation = CampPlanOperation(command: savedCommand, draftID: UUID().uuidString, planID: plan.id)
        let restoredOperation = try CampWire.decoder().decode(CampPlanOperation.self, from: CampWire.encoder().encode(savedOperation))
        precondition(restoredOperation.planID == plan.id && restoredOperation.draftID == savedOperation.draftID && restoredOperation.command.publishedSessionID == source.id)
        let draft = CloudPlanDraft(input: CloudPlanningInput(title: "Restart", projectId: source.projectId, brief: "Brief", shared: true, planners: []), restartPlanID: plan.id)
        let restoredDraft = try CampWire.decoder().decode(CloudPlanDraft.self, from: CampWire.encoder().encode(draft))
        precondition(restoredDraft.restartPlanID == plan.id)
        let fixture = ExecutionReviewFixture(plan: plan, source: source)

        let client = ExecutionReviewClient(fixture: fixture, userID: creator.userId)
        let ownerClient = ExecutionReviewClient(fixture: fixture, userID: owner.userId)
        let cache = try CampCache(directory: root.appendingPathComponent("creator"))
        let first = CampPlanning(cache: cache)
        first.attach(scope: scope, actor: creator, transport: client, sessions: [source])
        try await until("Creator did not attach") { first.ready }
        let prepare = SharedExecutionReview(planning: first); prepare.bind(plan)
        try await until("Review did not load") { !prepare.loading }
        await prepare.loadTarget(source)
        // Lose the committed response, then restore the durable queue with a
        // new coordinator. Retrying must reuse its original command key.
        await fixture.hold()
        try await prepare.prepare(source: source, model: "executor-model", tuning: ModelTuning(effort: "ultra", speed: "fast"), permissions: PermissionSettings(sandbox: "workspace-write", approval: "on-request"))
        try await until("Prepare was not submitted") { await fixture.waiting() }
        let cached = try await cache.planning(scope)
        precondition(cached.operations.first?.planID == plan.id, "Cached command lost its plan binding")
        first.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        await fixture.release()
        let restored = CampPlanning(cache: cache)
        restored.attach(scope: scope, actor: creator, transport: client, sessions: [source])
        try await until("Persisted prepare did not recover") { let keys = await fixture.keys("prepare"); return restored.ready && !restored.pending(plan.id) && keys.count >= 2 }
        let prepareKeys = await fixture.keys("prepare")
        let prepareCount = await fixture.count("prepare")
        precondition(Set(prepareKeys).count == 1 && prepareCount == 1)
        precondition(!first.pending(plan.id), "A late failed response restored work into a detached context")
        let creatorReview = SharedExecutionReview(planning: restored); creatorReview.bind(plan)
        try await until("Creator review missing") { creatorReview.approval != nil }
        var inspected = false
        do { try await creatorReview.approve { _ in inspected = true; return String(repeating: "a", count: 64) }; preconditionFailure("Creator approved another person's account") } catch {}
        precondition(!inspected)
        let ownerPlanning = CampPlanning(cache: try CampCache(directory: root.appendingPathComponent("owner")))
        ownerPlanning.attach(scope: ownerScope, actor: owner, transport: ownerClient, sessions: [source])
        try await until("Owner did not attach") { ownerPlanning.ready }
        let ownerReview = SharedExecutionReview(planning: ownerPlanning); ownerReview.bind(plan)
        try await until("Owner did not receive consent") { ownerReview.approval?.state == "pending" }
        var wrongActor = owner; wrongActor.deviceId = UUID().uuidString
        let wrongDevice = CampPlanning(cache: try CampCache(directory: root.appendingPathComponent("wrong-device")))
        wrongDevice.attach(scope: ownerScope, actor: wrongActor, transport: ownerClient, sessions: [source])
        try await until("Other device did not attach") { wrongDevice.ready }
        let wrongReview = SharedExecutionReview(planning: wrongDevice); wrongReview.bind(plan)
        try await until("Other device did not read consent") { wrongReview.approval != nil }
        inspected = false
        do { try await wrongReview.approve { _ in inspected = true; return String(repeating: "a", count: 64) }; preconditionFailure("Wrong device approved local execution") } catch {}
        precondition(!inspected)
        wrongDevice.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        do {
            try await ownerReview.approve { _ in
                ownerReview.refresh()
                return String(repeating: "a", count: 64)
            }
            preconditionFailure("Changed review accepted an old inspection")
        } catch is CancellationError {}
        let beforeApproval = await fixture.count("approve"); precondition(beforeApproval == 0)
        try await until("Refreshed consent did not load") { ownerReview.approval?.state == "pending" }
        try await ownerReview.approve { value in

            precondition(value.model == "executor-model" && value.effort == "ultra" && value.speed == "fast")
            return String(repeating: "a", count: 64)
        }
        try await until("Approve did not recover") { let count = await fixture.count("approve"); return !ownerPlanning.pending(plan.id) && count == 1 }
        let approveKeys = await fixture.keys("approve"); precondition(approveKeys.count == 2 && Set(approveKeys).count == 1)
        creatorReview.refresh()
        try await until("Approved state missing") { creatorReview.approval?.state == "approved" }
        var dispatched: [CloudRequest] = []; restored.implementationDispatched = { dispatched.append($0) }
        try await creatorReview.dispatch()
        try await until("Dispatch did not recover") { !restored.pending(plan.id) && dispatched.count == 1 }
        let dispatchKeys = await fixture.keys("dispatch"); precondition(dispatchKeys.count == 2 && Set(dispatchKeys).count == 1)
        precondition(dispatched[0].planId == plan.id && dispatched[0].ownerUserId == owner.userId)
        // A stale unused review stays withdrawable without its saved prompt.
        await fixture.completeImplementation()
        try await prepareNewReview(restored, plan: plan, source: source, fixture: fixture)
        creatorReview.refresh()
        try await until("Second review missing") { creatorReview.approval?.state == "pending" }
        await fixture.setStale(); creatorReview.refresh()
        try await until("Stale review not surfaced") { !creatorReview.loading && creatorReview.status?.reviewRequired == true }
        precondition(creatorReview.approval == nil)
        try await creatorReview.cancel()
        try await until("Stale review could not be withdrawn") { let count = await fixture.count("cancel"); return !restored.pending(plan.id) && count == 1 }
        restored.attach(scope: nil, actor: nil, transport: nil, sessions: []); ownerPlanning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        print("PASS: independent owner consent, durable restart, lost prepare/approve/dispatch responses, exact settings, wrong-device refusal, changed inspection and stale withdrawal")
    }
    @MainActor static func prepareNewReview(_ planning: CampPlanning, plan: CloudPlanningResult, source: CloudSession, fixture: ExecutionReviewFixture) async throws {
        let review = SharedExecutionReview(planning: planning); review.bind(plan)
        try await until("History status did not load") { !review.loading }
        await review.loadTarget(source)
        try await review.prepare(source: source, model: "next", tuning: ModelTuning(), permissions: PermissionSettings(sandbox: "workspace-write"))
        try await until("Next review did not save") { let count = await fixture.count("prepare"); return !planning.pending(plan.id) && count == 2 }
    }
}
