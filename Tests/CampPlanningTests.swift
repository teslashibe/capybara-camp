import Foundation

actor SharedPlanFixture: CampPublishingTransport {
    let scope: CloudScope
    var result: CloudPlanningResult?
    var receipts: [String: Data] = [:]
    var creates = 0
    var createKeys: [String] = []
    var loseReply = false
    var holdReply = false
    var held = false
    var gate: CheckedContinuation<Void, Never>?
    init(scope: CloudScope) { self.scope = scope }
    func configure(lose: Bool = false, hold: Bool = false) { loseReply = lose; holdReply = hold }
    func release() { gate?.resume(); gate = nil }
    func snapshot() -> (CloudPlanningResult?, Int, [String], Bool) { (result, creates, createKeys, held) }
    func perform(_ op: CampPublication) async throws -> Data {
        if op.method == "GET" {
            if op.path.hasSuffix("/planning") { return try CampWire.encoder().encode(result.map { [try! CloudPlanSummary($0)] } ?? []) }
            guard let result else { throw CampCloudError.server(404, "not_available") }
            return try CampWire.encoder().encode(result)
        }
        if op.kind == "create-plan" { createKeys.append(op.key) }
        if let saved = receipts[op.key] { return saved }
        if op.kind == "create-plan" {
            let input = try CampWire.decoder().decode(CloudPlanningInput.self, from: op.body)
            guard input.start && input.budgetPolicy == "calls" else { throw CampCloudError.server(400, "invalid_input") }
            creates += 1
            result = CloudPlanningResult(id: UUID().uuidString, title: input.title, projectId: input.projectId,
                version: .init(version: 1, state: "running", brief: input.brief,
                    planners: input.planners.enumerated().map { slot, p in .init(slot: slot, sessionId: p.sessionId, provider: slot == 0 ? "Codex" : "Claude", model: p.model, effort: p.effort, speed: p.speed, execution: p.execution) }, steps: [], sources: input.sources, mode: input.mode),
                campId: scope.campId, creatorUserId: scope.userId, shared: input.shared, revision: 1, activeVersion: 1, updatedAt: Date())
        } else {
            let body = try JSONSerialization.jsonObject(with: op.body) as! [String: Any]
            guard var current = result else { throw CampCloudError.server(404, "not_available") }
            switch body["action"] as? String {
            case "cancel": current.version.state = "cancelled"
            case "pause": current.version.state = "paused"
            case "resume", "start": current.version.state = "running"
            case "restart":
                let input = try CampWire.decoder().decode(CloudPlanningInput.self, from: JSONSerialization.data(withJSONObject: body["input"]!))
                current.version.version += 1; current.activeVersion = current.version.version
                current.version.state = "running"; current.version.brief = input.brief
                current.version.mode = input.mode
                current.version.planners = input.planners.enumerated().map { slot, p in .init(slot: slot, sessionId: p.sessionId, provider: slot == 0 ? "Codex" : "Claude", model: p.model, effort: p.effort, speed: p.speed, execution: p.execution) }
            default: throw CampCloudError.server(400, "invalid_input")
            }
            current.revision = (current.revision ?? 0) + 1; result = current
        }
        let data = try CampWire.encoder().encode(result!)
        receipts[op.key] = data
        if op.kind == "create-plan", holdReply { held = true; await withCheckedContinuation { gate = $0 }; held = false }
        if op.kind == "create-plan", loseReply { loseReply = false; throw URLError(.networkConnectionLost) }
        return data
    }
}

@main struct CampPlanningTests {
    static func check(_ condition: Bool, _ message: String = "Planning check failed") { precondition(condition, message) }
    @MainActor static func eventually(_ name: String, _ check: () async throws -> Bool) async throws {
        for _ in 0..<400 { if try await check() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(name)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-planning-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let sessions = ["Codex", "Claude"].map { provider in
            CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: provider, profileLabel: provider, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: UUID().uuidString, title: provider, model: provider + "-fixture", state: "idle", stage: "Ready", shared: true, allowRequests: true, externalOwner: false, revision: 1, transcriptRevision: 1, permissionRevision: 1, updatedAt: Date(), executionLocation: "local", executionEpoch: 1)
        }
        let cache = try CampCache(directory: root)
        let api = SharedPlanFixture(scope: scope)
        let manager = CampPlanning(cache: cache)
        manager.attach(scope: scope, actor: actor, transport: api, sessions: sessions)
        try await eventually("Planning cache did not load") { manager.ready }
        let input = CloudPlanningInput(title: "Shared plan", projectId: sessions[0].projectId, brief: "Synthetic brief", shared: false,
            planners: sessions.map { CloudPlannerInput(sessionId: $0.id, model: $0.model, effort: "high", speed: "standard") }, budgetPolicy: "calls")
        let id = try await manager.newDraft(input: input)
        manager.selectModel(id, slot: 0, model: "alternate")
        manager.edit(id) { $0.planners[0].effort = "low" }
        manager.selectModel(id, slot: 0, model: input.planners[0].model)
        check(manager.draft(id)?.input.planners[0].effort == "high", "Model settings were not preserved")
        await api.configure(lose: true)
        await manager.start(id, sources: [])
        try await eventually("Lost response was not recovered") { manager.drafts.isEmpty && manager.tickets.count == 1 }
        let snapshot = await api.snapshot()
        check(snapshot.1 == 1 && snapshot.2.count == 2 && Set(snapshot.2).count == 1, "A lost create reply duplicated the plan")
        check(try await cache.planning(scope).operations.isEmpty)
        check(try await cache.planning(CloudScope(origin: scope.origin, userId: UUID().uuidString, campId: scope.campId)).tickets.isEmpty)

        let cancelling = try await manager.newDraft(input: input)
        await api.configure(hold: true)
        await manager.start(cancelling, sources: [])
        try await eventually("Create reply was not held") { await api.snapshot().3 }
        await manager.cancelDraft(cancelling)
        await api.release()
        try await eventually("Queued cancellation was lost") { await api.snapshot().0?.version.state == "cancelled" && !manager.pending(cancelling) }
        check(manager.draft(cancelling) == nil)

        let cancelled = await api.snapshot().0!
        try await manager.restartDraft(cancelled)
        manager.edit(cancelled.id) { $0.brief = "A new version with retained history"; $0.budgetPolicy = "calls" }
        await manager.start(cancelled.id, sources: [])
        try await eventually("Restart did not preserve ticket identity") { await api.snapshot().0?.version.version == 2 && manager.draft(cancelled.id) == nil }
        check(await api.snapshot().0?.id == cancelled.id)
        do {
            try await manager.restartDraft(cancelled)
            preconditionFailure("Opening an old version allowed restarting an active plan")
        } catch {}
        manager.open(cancelled.id)
        try await eventually("Opened result was not cached") { manager.detail(cancelled.id)?.version.version == 2 }

        var historical = await api.snapshot().0!
        for version in 1...101 {
            historical.version.version = version; historical.activeVersion = version
            try await manager.install(historical)
        }
        historical.version.version = 1
        try await manager.install(historical)
        check(manager.detail(cancelled.id, version: 1)?.version.version == 1, "Cache eviction discarded a reopened historical version")
        check(try await cache.planning(scope).details.count == 100, "Planning history cache exceeded its bound")

        let singleCache = try CampCache(directory: root.appendingPathComponent("single"))
        let singleAPI = SharedPlanFixture(scope: scope)
        let singleManager = CampPlanning(cache: singleCache)
        singleManager.attach(scope: scope, actor: actor, transport: singleAPI, sessions: [sessions[0]])
        try await eventually("Single planner cache did not open") { singleManager.ready }
        var singleInput = input; singleInput.mode = .single; singleInput.planners = [input.planners[0]]
        let singleID = try await singleManager.newDraft(input: singleInput)
        await singleManager.start(singleID, sources: [])
        try await eventually("One shared session could not start a single plan") { singleManager.tickets.count == 1 }
        let singleResult = await singleAPI.snapshot().0!
        check(singleResult.valid && singleResult.version.workflow == .single && singleResult.version.planners.count == 1)
        check(singleManager.tickets[0].valid && singleManager.tickets[0].workflow == .single)
        var bad = singleResult; bad.version.mode = .comparison
        check(!bad.valid, "Comparison accepted a missing planner")
        var claudeOnly = singleResult; claudeOnly.version.planners[0].provider = "Claude"
        check(claudeOnly.valid, "Single planning incorrectly requires Codex")
        singleManager.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let reviewAPI = SharedPlanFixture(scope: scope)
        let reviewManager = CampPlanning(cache: try CampCache(directory: root.appendingPathComponent("locations")))
        reviewManager.attach(scope: scope, actor: actor, transport: reviewAPI, sessions: sessions)
        try await eventually("Location test did not open") { reviewManager.ready }
        let reviewID = try await reviewManager.newDraft(input: input)
        check(reviewManager.draft(reviewID)?.input.planners[0].execution?.epoch == 1)
        var moved = sessions
        moved[0].executionLocation = "cloud"; moved[0].executionEpoch = 2; moved[0].executionWorkspaceId = UUID().uuidString
        reviewManager.attach(scope: scope, actor: actor, transport: reviewAPI, sessions: moved)
        check(reviewManager.executionNeedsReview(reviewManager.draft(reviewID)!))
        await reviewManager.start(reviewID, sources: [])
        check(await reviewAPI.snapshot().1 == 0, "Starting silently followed a changed assignment")
        check(reviewManager.draft(reviewID)?.input.planners[0].execution?.location == "local", "Background sync rewrote the reviewed location")
        reviewManager.reviewExecution(reviewID)
        check(!reviewManager.executionNeedsReview(reviewManager.draft(reviewID)!))
        await reviewManager.start(reviewID, sources: [])
        try await eventually("Reviewed location did not start") { await reviewAPI.snapshot().1 == 1 }
        check(await reviewAPI.snapshot().0?.version.planners[0].execution?.workspaceId == moved[0].executionWorkspaceId)
        var invalid = await reviewAPI.snapshot().0!
        invalid.version.planners[0].execution?.epoch = 0
        check(!invalid.valid, "A response accepted an invalid execution epoch")
        invalid.version.planners[0].execution = nil
        check(invalid.valid, "Historical plans with unrecorded locations must remain readable")
        reviewManager.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let destinationCache = try CampCache(directory: root.appendingPathComponent("destinations"))
        let destinationAPI = SharedPlanFixture(scope: scope)
        let destinations = CampPlanning(cache: destinationCache)
        var localSources = sessions
        localSources[1].projectId = localSources[0].projectId
        var cloudSource = localSources[0]
        cloudSource.id = UUID().uuidString
        cloudSource.executionLocation = "cloud"; cloudSource.executionEpoch = 3; cloudSource.executionWorkspaceId = UUID().uuidString
        destinations.attach(scope: scope, actor: actor, transport: destinationAPI, sessions: localSources + [cloudSource])
        try await eventually("Destination test did not open") { destinations.ready }
        let destinationID = try await destinations.newDraft(input: input)
        let before = destinations.draft(destinationID)!.input
        let expected = before.planners[0]
        check(destinations.selectDestination(destinationID, slot: 0, expected: expected, provider: "Codex", source: cloudSource, scope: scope))
        let changed = destinations.draft(destinationID)!.input
        check(changed.planners[0].sessionId == cloudSource.id && changed.planners[0].execution?.location == "cloud")
        check(changed.planners[1] == before.planners[1] && changed.brief == before.brief && changed.sources == before.sources)
        check(changed.planners[0].model == expected.model && changed.planners[0].effort == expected.effort && changed.planners[0].speed == expected.speed,
              "Location selection overwrote model settings")
        check(!destinations.selectDestination(destinationID, slot: 0, expected: expected, provider: "Codex", source: localSources[0], scope: scope),
              "A stale open location form overwrote a newer selection")
        let current = changed.planners[0]
        check(!destinations.selectDestination(destinationID, slot: 0, expected: current, provider: "Claude", source: localSources[1], scope: scope),
              "A location control changed the planner's provider")
        var foreign = cloudSource; foreign.ownerUserId = UUID().uuidString
        var anotherMac = localSources[0]; anotherMac.id = UUID().uuidString; anotherMac.ownerDeviceId = UUID().uuidString
        var anotherProject = cloudSource; anotherProject.id = UUID().uuidString; anotherProject.projectId = UUID().uuidString
        destinations.attach(scope: scope, actor: actor, transport: destinationAPI, sessions: localSources + [cloudSource, foreign, anotherMac, anotherProject])
        for source in [foreign, anotherMac, anotherProject] {
            check(!destinations.selectDestination(destinationID, slot: 0, expected: current, provider: "Codex", source: source, scope: scope),
                  "An invalid owner, Mac or project was accepted")
        }
        var renewed = cloudSource; renewed.executionEpoch = 4
        destinations.attach(scope: scope, actor: actor, transport: destinationAPI, sessions: localSources + [renewed])
        check(!destinations.selectDestination(destinationID, slot: 0, expected: current, provider: "Codex", source: cloudSource, scope: scope),
              "An old execution assignment passed a fresh destination review")
        check(destinations.selectDestination(destinationID, slot: 0, expected: current, provider: "Codex", source: localSources[0], scope: scope),
              "A cloud planner could not return to a selected local source")
        await destinations.flush()
        let savedDestinations = try await destinationCache.planning(scope)
        check(savedDestinations.drafts.first?.input.planners[0].execution?.location == "local", "Selected destination was not saved")
        destinations.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        check(!destinations.selectDestination(destinationID, slot: 0, expected: expected, provider: "Codex", source: cloudSource, scope: scope),
              "A late location form wrote after sign-out")

        manager.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        try await cache.clear(scope)
        check(try await cache.planning(scope).tickets.isEmpty, "Sign-out retained shared planning data")
        do {
            try await cache.savePlanning(CampPlanningState(revision: 999), scope: scope, expectedEpoch: 0)
            preconditionFailure("Late response recreated a revoked plan cache")
        } catch {}
        print("PASS: single and comparison shared plans, response-loss replay, queued cancellation, same-ticket restart and cache isolation")
    }
}
