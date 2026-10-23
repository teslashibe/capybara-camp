import Foundation

final class ImportHTTPFixture: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

actor PlanImportFixture: CampPublishingTransport {
    var results: [String: Data] = [:]
    var keys: [String] = []
    var bodies: [Data] = []
    var loseResponse = true
    var hold = false
    var waiter: CheckedContinuation<Void, Never>?
    let scope: CloudScope
    let providers: [String: String]
    init(scope: CloudScope, sessions: [CloudSession]) {
        self.scope = scope; providers = Dictionary(uniqueKeysWithValues: sessions.map { ($0.id, $0.provider) })
    }
    func setHold() { hold = true }
    func waiting() -> Bool { waiter != nil }
    func release() { hold = false; waiter?.resume(); waiter = nil }
    func counts() -> (Int, Int) { (keys.count, results.count) }
    func identicalRetries() -> Bool { Set(keys).count == 1 && Set(bodies).count == 1 }
    func perform(_ operation: CampPublication) async throws -> Data {
        precondition(operation.method == "POST" && operation.path == "/v1/camps/\(scope.campId)/planning/import")
        keys.append(operation.key); bodies.append(operation.body)
        if hold { await withCheckedContinuation { waiter = $0 } }
        if let result = results[operation.key] { return result }
        let input = try CampWire.decoder().decode(LocalPlanImportInput.self, from: operation.body)
        let last = input.versions.last!
        let state = ["draft", "paused", "needs_review"].contains(last.state) ? "needs_review" : last.state
        let result = CloudPlanningResult(id: UUID().uuidString, title: input.title, projectId: input.projectId,
            version: .init(version: last.version, state: state, brief: last.brief,
                planners: last.planners.enumerated().map { index, planner in
                    .init(slot: index, sessionId: planner.sessionId, provider: providers[planner.sessionId]!, model: planner.model, effort: planner.effort, speed: planner.speed)
                }, steps: last.steps.map { step in
                    .init(ordinal: step.ordinal, slot: step.ordinal == 4 ? last.mergeSlot : step.ordinal % 2,
                        phase: step.ordinal < 2 ? "draft" : step.ordinal < 4 ? "review" : "merge",
                        state: step.displayedState, output: step.output, usage: step.usage)
                }, sources: last.sources, mergeSlot: last.mergeSlot, budgetPolicy: last.budgetPolicy, mode: last.mode,
                imported: .init(state: last.state, localSources: last.localSources, steps: last.steps)),
            campId: scope.campId, creatorUserId: scope.userId, shared: false, revision: 1, activeVersion: last.version, updatedAt: Date())
        precondition(result.valid)
        let raw = try CampWire.encoder().encode(result); results[operation.key] = raw
        if loseResponse { loseResponse = false; throw URLError(.networkConnectionLost) }
        return raw
    }
}

@main struct LocalPlanImportTests {
    static func rejects(_ body: () throws -> Void) {
        do { try body(); preconditionFailure("Invalid import accepted") } catch {}
    }
    @MainActor static func until(_ condition: () async -> Bool) async throws {
        for _ in 0..<400 { if await condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Import did not settle")
    }
    static func checkHTTPBounds() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ImportHTTPFixture.self]
        let client = CampHTTP(endpoint: try CampEndpoint("https://camp.example"), configuration: configuration)
        let base = "/v1/camps/\(UUID().uuidString)/planning/import"
        _ = try await client.data("POST", path: base, body: Data(repeating: 32, count: 3 << 20))
        for (method, path, count) in [("POST", base, (16 << 20) + 1), ("PUT", base, 3 << 20),
                                     ("POST", base + "?large=1", 3 << 20), ("POST", base + "/extra", 3 << 20),
                                     ("POST", "/v1/camps/not-a-camp/planning/import", 3 << 20), ("POST", "/v1/profiles", 3 << 20)] {
            do { _ = try await client.data(method, path: path, body: Data(repeating: 32, count: count)); preconditionFailure("Upload allowance escaped its exact route") }
            catch CampCloudError.responseTooLarge {} catch { preconditionFailure("Unexpected transport rejection: \(error)") }
        }
        let attachment = "/v1/camps/\(UUID().uuidString)/sessions/\(UUID().uuidString)/attachments/" + String(repeating:"a",count:64)
        _ = try await client.data("PUT", path:attachment, body:Data(repeating:32,count:23<<20))
        for (method,path,count) in [("PUT",attachment,(23<<20)+1),("POST",attachment,3<<20),
                                   ("PUT",attachment+"?large=1",3<<20),("PUT",attachment+"/extra",3<<20),
                                   ("PUT",attachment.replacingOccurrences(of:"/attachments/",with:"/transcript/"),3<<20),
                                   ("PUT",String(attachment.dropLast()),3<<20)] {
            do {_ = try await client.data(method,path:path,body:Data(repeating:32,count:count));preconditionFailure("Attachment allowance escaped its exact route")}
            catch CampCloudError.responseTooLarge {} catch {preconditionFailure("Unexpected attachment rejection: \(error)")}
        }
        await client.cancel()
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-plan-import-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://camp.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let project = UUID().uuidString
        func local(_ provider: String) -> CampSession {
            CampSession(id: provider + ":fixture", sessionID: UUID().uuidString, provider: provider, sourceID: provider + "-profile",
                sourceLabel: provider, root: "/root-private-canary", title: provider, cwd: "/cwd-private-canary", updated: 1,
                path: "/transcript-private-canary", accountID: "account-private-canary", archived: false, model: provider + "-historical")
        }
        func binding(_ local: CampSession) -> LocalShare {
            let profile = CloudProfile(id: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId,
                provider: local.provider, localId: PublishMessage.hash(Data(local.sourceID.utf8)), label: local.sourceLabel)
            let remote = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: project, profileId: profile.id,
                provider: local.provider, profileLabel: local.sourceLabel, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId,
                localSessionId: local.sessionID, title: local.title, model: local.model, state: "idle", stage: "Ready",
                shared: false, allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 7, permissionRevision: 3, updatedAt: Date())
            return LocalShare(id: remote.id, local: local, projectId: project, shared: false, allowRequests: false, profile: profile, remote: remote)
        }
        let codex = local("Codex"), claude = local("Claude")
        let a = LocalPlanner(session: codex, model: "codex-historical", tuning: .init(effort: "minimal", speed: "ultrafast"))
        let b = LocalPlanner(session: claude, model: "claude-historical", tuning: .init(effort: "ultra", speed: "fast"))
        let aBinding = binding(codex), bBinding = binding(claude)
        var sources = [codex.id: aBinding, claude.id: bBinding]
        var visible = [aBinding.remote!.id: aBinding.remote!, bBinding.remote!.id: bBinding.remote!]
        let quoteMessages = [Message(id: "quoted", role: "assistant", text: "Pinned evidence", model: "older-model", effort: "ultra", speed: "fast")]
        let quote = SessionReference(id: "private-source-id", title: "Local evidence", messages: quoteMessages,
            digest: PublishMessage.hash(try CampWire.encoder().encode(quoteMessages.map(\.contextMessage))))
        let remoteRef = CloudSourceRef(sessionId: bBinding.remote!.id, transcriptRevision: 2, permissionRevision: 3, messageIds: ["remote-message"])
        let cloudRef = SessionReference(id: "camp:" + remoteRef.sessionId, scope: scope.key, cloud: remoteRef)
        let first = LocalPlanVersion(number: 1, brief: "First brief", planners: [a], sources: [quote, cloudRef], budgetPolicy: "calls", state: "ready",
            steps: [.init(ordinal: 0, slot: 0, phase: "draft", state: "completed", output: "First plan", usage: .init(inputTokens: 7, outputTokens: 11))], mode: .single)
        let second = LocalPlanVersion(number: 2, brief: "Compare", planners: [a, b], budgetPolicy: "calls", mergeSlot: 1, state: "ready",
            steps: (0..<5).map { .init(ordinal: $0, slot: $0 == 4 ? 1 : $0 % 2, phase: $0 < 2 ? "draft" : $0 < 4 ? "review" : "combined", state: "completed", output: "Output \($0)") }, mode: .comparison)
        let third = LocalPlanVersion(number: 3, brief: "Paused revision", planners: [b], state: "needs_review",
            steps: [.init(ordinal: 0, slot: 0, phase: "draft", state: "unknown", output: "Partial", error: "Interrupted")], mode: .single)
        let executionID = UUID().uuidString, providerSession = UUID().uuidString
        let localMessages = (0..<230).map { Message(id: "m\($0)", role: $0 == 0 ? "user" : "assistant",
            text: $0 == 3 ? String(repeating: "界", count: 100_000) : "Message \($0)", model: "recorded-model", effort: "ultra", speed: "fast") }
        let execution = LocalImplementation(id: executionID, planVersion: 2, planDigest: String(repeating: "a", count: 64), executor: b,
            context: .init(fingerprint: String(repeating: "b", count: 64), cwd: "/cwd-private-canary", profile: "/root-private-canary", provider: "Claude", permissionSummary: "Auto"),
            providerSessionID: providerSession, providerSessionCreated: true, state: "needs_review", initialPrompt: "Approved implementation prompt",
            attempts: [.init(id: UUID().uuidString, prompt: "Follow-up", model: "prior-model", tuning: .init(effort: "high", speed: "fast"), state: "uncertain",
                error: "Lost provider outcome", usage: .init(inputTokens: 12, outputTokens: 23), permissions: .init(claudeMode: "auto"), permissionFingerprint: String(repeating: "c", count: 64))], messages: localMessages)
        var plan = LocalPlan(id: "plan:" + UUID().uuidString, title: "Import the entire ticket", updated: 1, versions: [first, second, third], implementations: [execution])
        var implementationSession = bBinding.remote!; implementationSession.id = UUID().uuidString; implementationSession.localSessionId = providerSession
        visible[implementationSession.id] = implementationSession
        let transcript = ImportedTranscriptBinding(session: implementationSession, revision: 6,
            messages: localMessages.enumerated().map { .init(id: $0.element.id, ordinal: Int64($0.offset), role: $0.element.role, text: $0.element.text,
                removed: false, model: $0.element.model, effort: $0.element.effort, speed: $0.element.speed, revision: 6) })
        func prepare(_ current: LocalPlan = plan, transcripts: [String: ImportedTranscriptBinding]? = nil) throws -> PreparedLocalPlanImport {
            try LocalPlanImport.prepare(current, scope: scope, projectID: project, planners: sources,
                implementations: transcripts ?? [executionID: transcript], visible: visible)
        }
        let prepared = try prepare()
        precondition(prepared.valid && prepared.input.versions.count == 3 && prepared.input.versions[1].steps.count == 5)
        precondition(prepared.input.versions[0].localSources[0].messages == quoteMessages)
        precondition(prepared.input.versions[0].sources == [remoteRef] && prepared.input.versions[2].steps[0].state == "unknown")
        precondition(prepared.input.implementations[0].messageCount == 230 && prepared.input.implementations[0].transcriptRevision == 6)
        precondition(prepared.input.implementations[0].attempts[0].state == "uncertain" && prepared.input.implementations[0].attempts[0].usage.outputTokens == 23)
        let json = String(decoding: try prepared.body, as: UTF8.self)
        for forbidden in ["root-private-canary", "cwd-private-canary", "transcript-private-canary", "account-private-canary", "execution_location"] { precondition(!json.contains(forbidden)) }
        precondition(json.contains("minimal") && json.contains("ultrafast") && json.contains("prior-model"))
        let restored = try CampWire.decoder().decode(PreparedLocalPlanImport.self, from: CampWire.encoder().encode(prepared))
        precondition(restored == prepared)
        let originalSources = sources
        for planner in [a, b] {
            let localID = LocalPlanImport.plannerHistoryID(planner.session)
            let local = LocalPlanImport.historySession(planner.session, id: localID, title: planner.session.title, model: planner.model)
            let empty = HistorySourceSnapshot(messages: [])
            var remote = originalSources[planner.session.id]!.remote!
            remote.id = HistorySourceSnapshot.cloudID(localID: localID, scope: scope)
            remote.localSessionId = localID; remote.transcriptRevision = 0; remote.historyOnly = true
            sources[planner.session.id] = LocalShare(id: remote.id, local: local, projectId: project, shared: false, allowRequests: false,
                remote: remote, publishedHistory: .init(parentId: PublishMessage.hash(Data(localID.utf8)), mainRevision: empty.revision, children: [], transcriptRevision: 0), historySnapshot: empty)
            visible[remote.id] = remote
        }
        var savedTranscript = transcript
        let savedID = LocalPlanImport.implementationHistoryID(execution)
        savedTranscript.session.id = HistorySourceSnapshot.cloudID(localID: savedID, scope: scope)
        savedTranscript.session.localSessionId = savedID; savedTranscript.session.historyOnly = true
        visible[savedTranscript.session.id] = savedTranscript.session
        let fromCopies = try prepare(transcripts: [executionID: savedTranscript])
        precondition(fromCopies.input.versions[0].planners[0].sessionId == sources[codex.id]!.id)
        precondition(fromCopies.input.implementations[0].sessionId == savedTranscript.session.id)
        sources[codex.id]?.publishedHistory = nil
        rejects { _ = try prepare(transcripts: [executionID: savedTranscript]) }
        sources = originalSources
        var many = plan; many.implementations = []
        many.versions = (1...100).map { number in var version = first; version.number = number; return version }
        let fullHistory = try prepare(many); precondition(fullHistory.input.versions.count == 100)
        var extra = first; extra.number = 101; many.versions.append(extra); rejects { _ = try prepare(many) }
        var bad = plan; bad.versions[2].state = "running"; rejects { _ = try prepare(bad) }
        bad = plan; bad.versions[0].sources[0].digest = String(repeating: "d", count: 64); rejects { _ = try prepare(bad) }
        bad = plan; bad.versions[0].sources[1].scope = "foreign"; rejects { _ = try prepare(bad) }
        bad = plan; bad.implementations![0].attempts[0].state = "running"; rejects { _ = try prepare(bad) }
        bad = plan; bad.implementations![0].messages[0] = Message(id: "m0", role: "user", text: "Changed"); rejects { _ = try prepare(bad) }
        var wrong = transcript; wrong.messages.removeLast(); rejects { _ = try prepare(transcripts: [executionID: wrong]) }
        wrong = transcript; wrong.session.ownerUserId = UUID().uuidString; rejects { _ = try prepare(transcripts: [executionID: wrong]) }
        let savedBinding = sources[codex.id]!; sources[codex.id]?.profile?.localId = "wrong"; rejects { _ = try prepare() }; sources[codex.id] = savedBinding
        let savedVisible = visible; visible.removeValue(forKey: remoteRef.sessionId); rejects { _ = try prepare() }; visible = savedVisible
        var oversized = prepared; oversized.input.versions[0].planners[0].effort = String(repeating: "x", count: 65); precondition(!oversized.valid)
        let cache = try CampCache(directory: root)
        let fixture = PlanImportFixture(scope: scope, sessions: Array(visible.values))
        let manager = CampPlanImport(cache: cache)
        manager.attach(scope: scope, actor: actor, transport: fixture, sessions: Array(visible.values))
        try await until { manager.ready }
        try await manager.review(prepared)
        var changed = plan; changed.versions[2].brief = "Edited after review"
        do { try await manager.submit(current: changed); preconditionFailure("Changed review was submitted") } catch {}
        try await manager.submit(current: plan)
        try await until { !manager.working }
        let firstCounts = await fixture.counts()
        precondition(firstCounts == (1, 1) && manager.imported == nil && manager.journal?.approved == true)
        manager.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        let resumed = CampPlanImport(cache: cache)
        var failInstall = true
        resumed.installed = { _ in
            if failInstall { failInstall = false; throw CampError.message("Synthetic local cache failure") }
        }
        resumed.attach(scope: scope, actor: actor, transport: fixture, sessions: Array(visible.values))
        try await until { resumed.ready && !resumed.working && !resumed.error.isEmpty }
        precondition(resumed.journal?.approved == true, "An installation failure lost the durable request")
        resumed.retry()
        try await until { resumed.imported != nil }
        precondition(resumed.imported?.activeVersion == 3 && resumed.journal == nil)
        let retried = await fixture.identicalRetries(); precondition(retried)
        let afterSuccess = try await cache.metadata("planning-import:" + scope.key); precondition(afterSuccess == nil)
        try await resumed.review(prepared)
        await fixture.setHold(); try await resumed.submit(current: plan)
        try await until { await fixture.waiting() }
        resumed.attach(scope: scope, actor: actor, transport: fixture, sessions: [])
        await fixture.release()
        try await Task.sleep(for: .milliseconds(40))
        precondition(resumed.imported == nil && !resumed.working, "A late response survived source revocation")
        resumed.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        try await cache.clear(scope)
        let cleared = try await cache.metadata("planning-import:" + scope.key); precondition(cleared == nil)
        do { try await cache.savePlanImport(PlanImportJournal(prepared: prepared), scope: scope, expectedEpoch: 0); preconditionFailure("Old cache epoch accepted") } catch {}
        let foreign = CloudScope(origin: scope.origin, userId: UUID().uuidString, campId: scope.campId)
        let foreignJournal = try await cache.metadata("planning-import:" + foreign.key); precondition(foreignJournal == nil)
        try await checkHTTPBounds()
        plan.updated += 1
        print("LocalPlanImportTests passed: complete mapping, pinned history, private fields, review changes, durable retry, revocation and cache epochs")
    }
}
