import Foundation

actor ImportedPlanningFixture: CampPublishingTransport {
    var result: CloudPlanningResult
    var held = false
    var hold = false
    var gate: CheckedContinuation<Void, Never>?
    init(_ result: CloudPlanningResult) { self.result = result }
    func holdNext() { hold = true }
    func waiting() -> Bool { held }
    func release() { gate?.resume(); gate = nil }
    func replace(_ value: CloudPlanningResult) { result = value }
    func perform(_ operation: CampPublication) async throws -> Data {
        guard operation.method == "GET" else { throw CampCloudError.server(400, "fixture_read_only") }
        if operation.path.hasSuffix("/planning") { return try CampWire.encoder().encode([CloudPlanSummary(result)]) }
        let data = try CampWire.encoder().encode(result)
        if hold {
            hold = false; held = true
            await withCheckedContinuation { gate = $0 }
            held = false
        }
        return data
    }
}

@main struct ImportedPlanningTests {
    @MainActor static func eventually(_ message: String, _ predicate: () async -> Bool) async throws {
        for _ in 0..<400 { if await predicate() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(message)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-imported-planning-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let session = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString,
            provider: "Codex", profileLabel: "Codex", ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: UUID().uuidString,
            title: "Fixture", model: "current-model", state: "idle", stage: "Ready", shared: false, allowRequests: false, externalOwner: false,
            revision: 1, transcriptRevision: 1, permissionRevision: 1, updatedAt: Date(), executionLocation: "local", executionEpoch: 1)
        let source = ImportedPlanningSource(id: String(repeating: "a", count: 64), title: "Quoted source", revision: String(repeating: "b", count: 64),
            messages: [Message(id: "source-message", role: "assistant", text: "Saved source text", model: "historical-model", effort: "ultra", speed: "ultrafast")])
        let usage = ConversationUsage(inputTokens: 123, outputTokens: 45)
        let imported = ImportedPlanningSnapshot(state: "paused", localSources: [source], steps: [ImportedPlanningStep(ordinal: 0, state: "unknown", output: "Partial saved answer", error: "Provider outcome was unconfirmed", usage: usage)])
        let result = CloudPlanningResult(id: UUID().uuidString, title: "Imported plan", projectId: session.projectId,
            version: .init(version: 1, state: "needs_review", brief: "A saved brief", planners: [.init(slot: 0, sessionId: session.id, provider: "Codex", model: "saved-model", effort: "high", speed: "fast")],
                steps: [.init(ordinal: 0, slot: 0, phase: "draft", state: "outcome_unknown", output: "Partial saved answer", usage: usage)], sources: [], mode: .single, imported: imported),
            campId: scope.campId, creatorUserId: actor.userId, shared: false, revision: 1, activeVersion: 1, updatedAt: Date())
        precondition(result.valid)
        let decoded = try CampWire.decoder().decode(CloudPlanningResult.self, from: CampWire.encoder().encode(result))
        precondition(decoded.version == result.version && decoded.id == result.id, "Imported source metadata was discarded")
        var bad = result; bad.version.imported?.localSources[0].messages[0] = Message(id: "source-message", role: "unexpected", text: "Wrong role")
        precondition(!bad.valid)
        bad = result; bad.version.imported?.steps[0].state = "running"
        precondition(!bad.valid)
        bad = result; bad.version.steps[0].output = "Changed output"
        precondition(!bad.valid)
        bad = result; bad.version.planners[0].execution = .init(location: "local", epoch: 1)
        precondition(!bad.valid, "Imported history claimed an execution binding")
        bad = result; bad.version.imported?.steps[0].usage?.inputTokens = -1
        precondition(!bad.valid)
        let permissions = ["camp:" + session.id: session.permissionRevision]
        let inspection = try inspectImportedPlanningSource(result, sourceID: source.id, scope: scope, permissions: permissions)
        precondition(inspection.messages == source.messages && inspection.planID == result.id)
        precondition(inspection.remainsVisible(scope: scope.key, permissionRevision: nil, availablePermissions: permissions))
        precondition(!inspection.remainsVisible(scope: scope.key, permissionRevision: nil, availablePermissions: [:]))
        precondition(!inspection.remainsVisible(scope: "different", permissionRevision: nil, availablePermissions: permissions))
        do { _ = try inspectImportedPlanningSource(result, sourceID: source.id, scope: scope, permissions: [:]); preconditionFailure("Missing source access was accepted") } catch {}
        let cache = try CampCache(directory: root)
        let fixture = ImportedPlanningFixture(result)
        let manager = CampPlanning(cache: cache)
        manager.attach(scope: scope, actor: actor, transport: fixture, sessions: [session])
        try await eventually("Planning cache did not load") { manager.ready }
        try await manager.install(result)
        let cached = try await cache.planning(scope)
        let verified = try await manager.verifiedDetail(result)
        precondition(cached.details["\(result.id):1"]?.version.imported == imported)
        precondition(verified.version.imported == imported)
        await manager.act("resume", on: result)
        let afterResume = try await cache.planning(scope)
        precondition(!afterResume.operations.contains { $0.planID == result.id }, "Imported history queued a provider action")
        var ready = result; ready.version.state = "ready"
        ready.version.imported?.state = "ready"; ready.version.imported?.steps[0].state = "completed"; ready.version.steps[0].state = "completed"
        precondition(ready.valid)
        await fixture.replace(ready)
        try await manager.install(ready)
        do { try await manager.restartDraft(ready); preconditionFailure("Restart silently discarded quoted context") }
        catch { precondition(error.localizedDescription.contains("local source snapshots"), "The context-loss guard was not checked") }
        await fixture.replace(result)
        try await manager.install(result)
        await fixture.holdNext()
        let pending = Task { try await manager.verifiedDetail(result) }
        try await eventually("Read was not held") { await fixture.waiting() }
        manager.attach(scope: scope, actor: actor, transport: fixture, sessions: [])
        await fixture.release()
        do { _ = try await pending.value; preconditionFailure("Late read survived source revocation") } catch {}
        manager.attach(scope: scope, actor: actor, transport: fixture, sessions: [session])
        await fixture.holdNext()
        let signingOut = Task { try await manager.verifiedDetail(result) }
        try await eventually("Sign-out read was not held") { await fixture.waiting() }
        manager.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        await fixture.release()
        do { _ = try await signingOut.value; preconditionFailure("Late read survived sign-out") } catch {}
        let store = CampStore(stateDirectory: root.appendingPathComponent("store"))
        store.contextInspection = inspection
        store.applyCloud([], projects: [])
        precondition(store.contextInspection == nil)
        print("PASS: imported planning metadata, cache, source inspection, action fences and late-read revocation")
    }
}
