import Foundation

actor ImplementationReaderFixture: CampPublishingTransport {
    let plan: CloudPlanningResult
    let records: [ImportedImplementationRecord]
    var hold = ""
    var held = false
    var cancelled = false
    var gate: CheckedContinuation<Void, Never>?
    var invalid = false
    init(plan: CloudPlanningResult, records: [ImportedImplementationRecord]) { self.plan = plan; self.records = records }
    func holdNext(_ suffix: String) { hold = suffix; cancelled = false }
    func waiting() -> Bool { held }
    func wasCancelled() -> Bool { cancelled }
    func release() { gate?.resume(); gate = nil }
    func malformed(_ value: Bool) { invalid = value }
    func perform(_ op: CampPublication) async throws -> Data {
        guard op.method == "GET" else { throw CampCloudError.invalidResponse }
        let data: Data
        if !op.path.contains("/implementations") {
            data = op.path.hasSuffix("/planning") ? try CampWire.encoder().encode([CloudPlanSummary(plan)]) : try CampWire.encoder().encode(plan)
        } else if !op.path.contains(records[0].id) && !op.path.contains(records[1].id) {
            let second = op.path.contains("after=1")
            let values = invalid ? [records[0],records[0]] : [records[second ? 1 : 0]]
            data = try CampWire.encoder().encode(ImportedImplementationPage(implementations: values, next: second ? 0 : 1))
        } else {
            let record = op.path.contains(records[0].id) ? records[0] : records[1]
            if op.path.contains("/transcript") {
                let earlier = op.path.contains("before=30")
                let indexes = record.ordinal == 2 ? 0..<1 : earlier ? 0..<30 : 30..<230
                let messages = indexes.map { index in CloudMessage(id: "m\(index)", ordinal: Int64(index), role: "assistant", text: record.ordinal == 2 ? "Second implementation" : index == 0 ? String(repeating: "🦫", count: 75000) : "Message \(index)", removed: false, model: record.model, effort: record.effort, speed: record.speed, revision: 1) }
                data = try CampWire.encoder().encode(CloudTranscript(revision: 2, messages: messages, before: Int64(indexes.lowerBound), hasMore: record.ordinal == 1 && !earlier))
            } else if op.path.contains("/attempts") {
                let last = op.path.contains("after=10")
                let values = (last ? 11...11 : 1...10).map { index in ImportedAttempt(ordinal: index, id: UUID().uuidString, prompt: "Attempt \(index)", model: "saved-model", effort: "high", speed: "fast", state: index == 11 ? "uncertain" : "completed", error: index == 11 ? "Unconfirmed outcome" : "") }
                data = try CampWire.encoder().encode(ImportedAttemptPage(attempts: values, next: last ? 0 : 10))
            } else { data = try CampWire.encoder().encode(record) }
        }
        if !hold.isEmpty && op.path.contains(hold) {
            hold = ""; held = true
            await withCheckedContinuation { gate = $0 }
            cancelled = Task.isCancelled; held = false
        }
        return data
    }
}

@main struct ImportedImplementationTests {
    @MainActor static func eventually(_ label: String, _ check: () async -> Bool) async throws {
        for _ in 0..<500 { if await check() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-implementation-history-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let source = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId, ownerDeviceId: actor.deviceId, localSessionId: UUID().uuidString, title: "Source", model: "saved-model", state: "idle", stage: "Ready", shared: false, allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 2, permissionRevision: 1, updatedAt: Date())
        let plan = CloudPlanningResult(id: UUID().uuidString, title: "Saved plan", projectId: source.projectId, version: .init(version: 1, state: "ready", brief: "A plan", planners: [.init(slot: 0, sessionId: source.id, provider: "Codex", model: "saved-model", effort: "high", speed: "fast")], steps: [], sources: [], mode: .single), campId: scope.campId, creatorUserId: scope.userId, shared: false, revision: 1, activeVersion: 1, updatedAt: Date())
        let record = ImportedImplementationRecord(id: UUID().uuidString, ordinal: 1, attemptCount: 11, sourceId: String(repeating: "a", count: 64), planVersion: 1, sessionId: source.id, transcriptRevision: 2, messageCount: 230, provider: "Codex", state: "complete", model: "saved-model", effort: "high", speed: "fast", planDigest: String(repeating: "b", count: 64), contextFingerprint: String(repeating: "c", count: 64), initialPrompt: "Implement the saved plan")
        var second = record; second.id = UUID().uuidString; second.sourceId = String(repeating: "d", count: 64); second.ordinal = 2; second.messageCount = 1; second.attemptCount = 0
        let fixture = ImplementationReaderFixture(plan: plan, records: [record,second])
        let planning = CampPlanning(cache: try CampCache(directory: root))
        planning.attach(scope: scope, actor: actor, transport: fixture, sessions: [source])
        try await eventually("Planning did not attach") { planning.ready }
        let history = ImportedImplementationHistory(planning: planning)
        history.bind(plan)
        try await eventually("Implementation list did not load") { !history.loading && history.implementations.count == 1 }
        precondition(history.implementationNext == 1)
        history.loadImplementations()
        try await eventually("Second implementation page did not load") { !history.loading && history.implementations.count == 2 }
        precondition(history.implementationNext == 0)
        history.select(record)
        try await eventually("Transcript did not load") { !history.loading && history.messages.count == 200 }
        history.loadEarlier()
        try await eventually("Earlier messages were lost") { !history.loading && history.messages.count == 230 }
        precondition(history.messages.first?.text.utf8.count == 300000 && !history.hasEarlier)
        history.loadAttempts()
        try await eventually("Attempts did not load") { !history.loading && history.attempts.count == 10 }
        history.loadAttempts()
        try await eventually("Last attempt was lost") { !history.loading && history.attempts.count == 11 }
        precondition(history.attempts.last?.state == "uncertain" && history.attempts.last?.error == "Unconfirmed outcome")
        await fixture.holdNext(record.id + "/transcript")
        history.select(record)
        try await eventually("Read was not held") { await fixture.waiting() }
        history.select(second)
        await fixture.release()
        try await eventually("Selection mixed implementation histories") { !history.loading && history.selected?.id == second.id && history.messages.first?.text == "Second implementation" }
        let cancelled = await fixture.wasCancelled()
        precondition(cancelled)
        await fixture.holdNext(record.id + "/transcript")
        let revoked = Task { try await planning.readImplementationResource(plan, suffix: "/\(record.id)/transcript", sourceID: source.id) }
        try await eventually("Revocation read was not held") { await fixture.waiting() }
        planning.attach(scope: scope, actor: actor, transport: fixture, sessions: [])
        await fixture.release()
        do { _ = try await revoked.value; preconditionFailure("An implementation read survived source revocation") } catch {}
        planning.attach(scope: scope, actor: actor, transport: fixture, sessions: [source])
        await fixture.holdNext(record.id + "/transcript")
        history.select(record)
        try await eventually("Sign-out read was not held") { await fixture.waiting() }
        history.clear(); planning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        await fixture.release()
        try await eventually("Cancelled read did not end") { !(await fixture.waiting()) }
        precondition(history.selected == nil && history.messages.isEmpty && history.attempts.isEmpty)
        planning.attach(scope: scope, actor: actor, transport: fixture, sessions: [source])
        try await eventually("Planning did not reattach") { planning.ready }
        await fixture.malformed(true); history.bind(plan)
        try await eventually("Malformed list did not fail") { !history.loading && !history.error.isEmpty }
        precondition(history.implementations.isEmpty)
        history.clear(); planning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        print("PASS: implementation and attempt pagination, long transcripts, cancellation, sign-out and invalid-page rejection")
    }
}
