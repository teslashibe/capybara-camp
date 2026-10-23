import Foundation

actor PromotionFixture: CampPublishingTransport {
    let scope: CloudScope
    let actor: CloudActor
    var profiles: [String: CloudProfile] = [:]
    var sessions: [String: CloudSession] = [:]
    var messages: [String: [String: CloudMessage]] = [:]
    var receipts: [String: Data] = [:]
    var plans: [String: CloudPlanningResult] = [:]
    var lostTranscript = false
    var lostImport = false
    var imports = 0
    var attachmentBodies: [String:Data] = [:]
    var holdProfiles = false
    var profileGates: [CheckedContinuation<Void, Never>] = []
    init(scope: CloudScope, actor: CloudActor) { self.scope = scope; self.actor = actor }
    func allSessions() -> [CloudSession] { Array(sessions.values) }
    func counts() -> (Int, Int) { (sessions.count, imports) }
    func suspendProfiles() { holdProfiles = true }
    func waitingForProfile() -> Bool { !profileGates.isEmpty }
    func releaseProfiles() { holdProfiles = false; let gates = profileGates; profileGates = []; for gate in gates { gate.resume() } }
    func loseNextImport() { lostImport = false }
    func perform(_ operation: CampPublication) async throws -> Data {
        let path = String(operation.path.split(separator: "?", maxSplits: 1)[0])
        let parts = path.split(separator: "/").map(String.init)
        if operation.method == "GET" {
            let id = parts[4]
            guard let session = sessions[id] else { throw CampCloudError.server(404, "unavailable") }
            if parts.last == "transcript" {
                let query = URLComponents(string: "https://camp.example" + operation.path)!.queryItems ?? []
                let before = Int64(query.first { $0.name == "before" }?.value ?? "0") ?? 0
                let revision = Int64(query.first { $0.name == "revision" }?.value ?? "0") ?? 0
                let all = (messages[id] ?? [:]).values.filter { (before == 0 || $0.ordinal < before) && $0.revision <= revision }.sorted { $0.ordinal < $1.ordinal }
                let values = Array(all.suffix(200))
                return try CampWire.encoder().encode(CloudTranscript(revision: revision, messages: values,
                    before: all.count > 200 ? values[0].ordinal : 0, hasMore: all.count > 200))
            }
            return try CampWire.encoder().encode(session)
        }
        if let prior = receipts[operation.key] { return prior }
        if path == "/v1/profiles" && holdProfiles { await withCheckedContinuation { profileGates.append($0) } }
        let body = try JSONSerialization.jsonObject(with: operation.body) as! [String: Any]
        let result: Data
        if operation.kind == "attachment" {
            let id = parts[4], hash = parts.last!
            let data = Data(base64Encoded: body["data"] as! String)!
            precondition(sessions[id]?.isHistoryOnly == true && PublishMessage.hash(data) == hash)
            attachmentBodies[id + ":" + hash] = data
            result = try CampWire.body(["session_id":id,"sha256":hash,"bytes":data.count])
        } else if path == "/v1/profiles" {
            let localID = body["local_id"] as! String
            let profile = profiles[localID] ?? CloudProfile(id: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId,
                provider: body["provider"] as! String, localId: localID, label: body["label"] as! String)
            profiles[localID] = profile; result = try CampWire.encoder().encode(profile)
        } else if path.hasSuffix("/planning/import") {
            let input = try CampWire.decoder().decode(LocalPlanImportInput.self, from: operation.body)
            if let previous = plans[input.sourceId] { result = try CampWire.encoder().encode(previous) }
            else {
                precondition(input.versions.allSatisfy { $0.planners.allSatisfy { sessions[$0.sessionId]?.isHistoryOnly == true } })
                precondition(input.implementations.allSatisfy { (messages[$0.sessionId] ?? [:]).count == $0.messageCount })
                let version = input.versions.last!
                let value = CloudPlanningResult(id: UUID().uuidString, title: input.title, projectId: input.projectId,
                    version: .init(version: version.version, state: version.state == "ready" ? "ready" : "needs_review", brief: version.brief,
                        planners: version.planners.enumerated().map { .init(slot: $0.offset, sessionId: $0.element.sessionId,
                            provider: sessions[$0.element.sessionId]!.provider, model: $0.element.model, effort: $0.element.effort, speed: $0.element.speed) },
                        steps: version.steps.map { .init(ordinal: $0.ordinal, slot: $0.ordinal == 4 ? version.mergeSlot : $0.ordinal % 2,
                            phase: $0.ordinal < 2 ? "draft" : $0.ordinal < 4 ? "review" : "merge", state: $0.displayedState, output: $0.output, usage: $0.usage) },
                        sources: version.sources, mergeSlot: version.mergeSlot, budgetPolicy: version.budgetPolicy, mode: version.mode,
                        imported: .init(state: version.state, localSources: version.localSources, steps: version.steps)),
                    campId: scope.campId, creatorUserId: actor.userId, shared: false, revision: 1, activeVersion: version.version, updatedAt: Date())
                precondition(value.valid)
                plans[input.sourceId] = value; imports += 1; result = try CampWire.encoder().encode(value)
            }
        } else if path.hasSuffix("/transcript") {
            let id = parts[4], expected = (body["expected_revision"] as! NSNumber).int64Value
            guard sessions[id]?.transcriptRevision == expected else { throw CampCloudError.server(409, "revision_conflict") }
            let values = try CampWire.decoder().decode([PublishMessage].self, from: JSONSerialization.data(withJSONObject: body["messages"]!))
            for value in values {
                for ref in value.attachments ?? [] where ref.state == "embedded" {
                    precondition(attachmentBodies[id + ":" + ref.sha256!]?.count == ref.size)
                }
                messages[id, default: [:]][value.id] = CloudMessage(id: value.id, ordinal: value.ordinal, role: value.role, text: value.text,
                    removed: value.removed, model: value.model, effort: value.effort, speed: value.speed, revision: expected + 1, attachments: value.attachments)
            }
            sessions[id]?.transcriptRevision = expected + 1
            result = try CampWire.body(["revision": expected + 1])
        } else {
            let id = body["id"] as! String, profileID = body["profile_id"] as! String
            let profile = profiles.values.first { $0.id == profileID }!
            let value = CloudSession(id: id, campId: scope.campId, projectId: body["project_id"] as! String, profileId: profileID,
                provider: profile.provider, profileLabel: profile.label, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId,
                localSessionId: body["local_session_id"] as! String, title: body["title"] as! String, model: body["model"] as! String,
                state: "idle", stage: "Ready", shared: false, allowRequests: false, externalOwner: false, revision: 1,
                transcriptRevision: 0, permissionRevision: 1, updatedAt: Date(), executionLocation: "local", executionEpoch: 1, historyOnly: true)
            precondition(sessions[id] == nil && body["history_only"] as? Bool == true)
            sessions[id] = value; result = try CampWire.encoder().encode(value)
        }
        receipts[operation.key] = result
        if path.hasSuffix("/transcript"), !lostTranscript { lostTranscript = true; throw URLError(.networkConnectionLost) }
        if path.hasSuffix("/planning/import"), !lostImport { lostImport = true; throw URLError(.networkConnectionLost) }
        return result
    }
}

@main struct CampPlanPromotionTests {
    @MainActor static func until(_ reason: String, _ condition: () async -> Bool) async throws {
        for _ in 0..<500 { if await condition() { return }; try await Task.sleep(for: .milliseconds(20)) }
        preconditionFailure(reason)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("plan-promotion-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://camp.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let project = UUID().uuidString
        func planner(_ provider: String) -> LocalPlanner {
            .init(session: .init(id: provider + ":session", sessionID: UUID().uuidString, provider: provider, sourceID: provider + ":account",
                sourceLabel: provider, root: "/private-profile", title: provider, cwd: "/private-project", updated: 1, path: "", accountID: "", archived: false, model: provider),
                model: provider + "-version", tuning: .init(effort: "high", speed: "fast"))
        }
        let a = planner("Codex"), b = planner("Claude")
        let first = LocalPlanVersion(number: 1, brief: "Single plan", planners: [a], budgetPolicy: "calls", state: "ready",
            steps: [.init(ordinal: 0, slot: 0, phase: "draft", state: "completed", output: "First draft")], mode: .single)
        let second = LocalPlanVersion(number: 2, brief: "Compare plans", planners: [a, b], budgetPolicy: "calls", state: "ready",
            steps: (0..<5).map { .init(ordinal: $0, slot: $0 == 4 ? 0 : $0 % 2, phase: $0 < 2 ? "draft" : $0 < 4 ? "review" : "combined", state: "completed", output: "Saved output \($0)") }, mode: .comparison)
        let attachmentData = Data("Saved implementation attachment".utf8)
        let attachment = TranscriptAttachment(id:"a:0",kind:"document",source:"inline",sourceDigest:String(repeating:"e",count:64),state:"embedded",mediaType:"text/plain",sha256:PublishMessage.hash(attachmentData),size:attachmentData.count)
        var execution = LocalImplementation(id: UUID().uuidString, planVersion: 2, planDigest: String(repeating: "a", count: 64), executor: b,
            context: .init(fingerprint: String(repeating: "b", count: 64), cwd: "/private-project", profile: "/private-profile", provider: "Claude", permissionSummary: "Read only"),
            providerSessionID: UUID().uuidString, state: "complete", initialPrompt: "Implement the reviewed plan", messages:
                (0..<230).map { .init(id: "message-\($0)", role: "assistant", text: $0 == 200 ? String(repeating: "界", count: 100_000) : "Answer \($0)", model: "saved-model", effort: "ultra", speed: "fast") })
        execution.messages[0].attachments = [attachment]
        let plan = LocalPlan(id: "plan:" + UUID().uuidString, title: "A complete saved ticket", updated: 1, versions: [first, second], implementations: [execution])
        let original = try CampWire.encoder().encode(plan)
        let cache = try CampCache(directory: root)
        let api = PromotionFixture(scope: scope, actor: actor)
        let sourceConversationID = execution.providerSessionID
        func connect(_ sharing: CampSharing, _ promotion: CampPlanPromotion) async {
            sharing.attachmentBytes = { source,child,id,ref in
                precondition(source.sessionID == sourceConversationID && source.sessionID != b.session.sessionID)
                precondition(source.root == "/private-profile" && child == nil && id == "message-0" && ref == attachment)
                return attachmentData
            }
            sharing.published = { [weak promotion] _, _, _ in promotion?.attach(scope: scope, actor: actor, transport: api, sessions: await api.allSessions()) }
            promotion.upload.installed = { precondition($0.valid) }
            sharing.attach(scope: scope, actor: actor, transport: api)
            promotion.attach(scope: scope, actor: actor, transport: api, sessions: await api.allSessions())
        }
        let sharing = CampSharing(cache: cache, export: { _, _ in preconditionFailure("Promotion read a live CLI") })
        let promotion = CampPlanPromotion(cache: cache, sharing: sharing)
        await connect(sharing, promotion)
        try await until("Promotion did not restore") { promotion.ready && sharing.ready }
        var active = plan; active.versions[1].state = "running"
        do { try await promotion.approve(active, projectID: project); preconditionFailure("Active work was imported") } catch {}
        var foreign = plan
        let foreignID = UUID().uuidString
        foreign.versions[0].sources = [SessionReference(id: "camp:" + foreignID, scope: "another-camp", cloud:
            .init(sessionId: foreignID, transcriptRevision: 1, permissionRevision: 1, messageIds: ["one"]))]
        do { try await promotion.approve(foreign, projectID: project); preconditionFailure("A foreign source was uploaded") } catch {}
        let before = await api.counts(); precondition(before == (0, 0))
        try await promotion.approve(plan, projectID: project)
        try await until("Lost import response did not pause") { !promotion.working && !promotion.error.isEmpty }
        let partial = await api.counts(); precondition(partial == (3, 1) && promotion.job?.phase == "importing")
        promotion.attach(scope: nil, actor: nil, transport: nil, sessions: []); sharing.attach(scope: nil, actor: nil, transport: nil)
        let resumedSharing = CampSharing(cache: cache, export: { _, _ in preconditionFailure("Recovery read a live CLI") })
        let resumed = CampPlanPromotion(cache: cache, sharing: resumedSharing)
        await connect(resumedSharing, resumed)
        try await until("Restored promotion did not finish") { resumed.linkedPlan(plan.id) != nil }
        let completed = await api.counts(); precondition(completed == (3, 1), "Recovery duplicated a source or plan")
        precondition(resumed.replaces(plan))
        var newer = plan; newer.updated += 1
        precondition(!resumed.replaces(newer), "An older copy hid newer local work")
        let originalAfter = try CampWire.encoder().encode(plan)
        precondition(resumed.job == nil, "Completed import retained its job")
        precondition(!resumed.working, "Completed import remained busy")
        precondition(originalAfter == original, "Import changed the local ticket")
        let saved = try await cache.metadata("plan-promotion:" + scope.key)!
        let state = try JSONDecoder().decode(PlanPromotionState.self, from: saved)
        precondition(state.links[plan.id] != nil && state.job == nil)
        let savedPlanID = resumed.linkedPlan(plan.id)
        let foreignScope = CloudScope(origin: scope.origin, userId: UUID().uuidString, campId: scope.campId)
        let other = try await cache.metadata("plan-promotion:" + foreignScope.key); precondition(other == nil)
        resumed.attach(scope: nil, actor: nil, transport: nil, sessions: []); resumedSharing.attach(scope: nil, actor: nil, transport: nil)
        try await cache.clear(scope)
        let cleared = try await cache.metadata("plan-promotion:" + scope.key); precondition(cleared == nil)
        let afterClearSharing = CampSharing(cache: cache, export: { _, _ in preconditionFailure("Cache recovery read a CLI") })
        let afterClear = CampPlanPromotion(cache: cache, sharing: afterClearSharing)
        await connect(afterClearSharing, afterClear)
        try await until("Cleared-cache controller did not restore") { afterClear.ready && afterClearSharing.ready }
        try await afterClear.approve(plan, projectID: project)
        try await until("Cleared cache duplicated or lost the saved plan") { afterClear.linkedPlan(plan.id) != nil }
        let afterClearCounts = await api.counts()
        precondition(afterClear.linkedPlan(plan.id) == savedPlanID && afterClearCounts == (3, 1))
        let empty = LocalPlan(id: "plan:" + UUID().uuidString, title: "Empty draft", updated: 3,
            versions: [.init(number: 1, brief: "", planners: [a], budgetPolicy: "calls", mode: .single)])
        await api.loseNextImport()
        try await afterClear.approve(empty, projectID: project)
        try await until("Lost response was not reported for manual retry") { !afterClear.working && !afterClear.error.isEmpty }
        afterClear.retry()
        try await until("Empty single-model draft did not save") { afterClear.linkedPlan(empty.id) != nil }
        let finalCounts = await api.counts(); precondition(finalCounts == (3, 2), "Unchanged planner history was duplicated")
        afterClear.attach(scope: nil, actor: nil, transport: nil, sessions: []); afterClearSharing.attach(scope: nil, actor: nil, transport: nil)
        let interruptedCache = try CampCache(directory: root.appendingPathComponent("interrupted"))
        let interruptedAPI = PromotionFixture(scope: scope, actor: actor)
        await interruptedAPI.suspendProfiles()
        let interruptedSharing = CampSharing(cache: interruptedCache, export: { _, _ in preconditionFailure("Interrupted copy read a CLI") })
        let interrupted = CampPlanPromotion(cache: interruptedCache, sharing: interruptedSharing)
        interruptedSharing.attach(scope: scope, actor: actor, transport: interruptedAPI)
        interrupted.attach(scope: scope, actor: actor, transport: interruptedAPI, sessions: [])
        try await until("Interrupted controller did not restore") { interrupted.ready && interruptedSharing.ready }
        try await interrupted.approve(plan, projectID: project)
        try await until("Publication did not reach the held request") { await interruptedAPI.waitingForProfile() }
        interrupted.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        interruptedSharing.attach(scope: nil, actor: nil, transport: nil)
        try await interruptedCache.clear(scope)
        await interruptedAPI.releaseProfiles()
        try await Task.sleep(for: .milliseconds(60))
        let interruptedCounts = await interruptedAPI.counts()
        let discarded = try await interruptedCache.metadata("plan-promotion:" + scope.key)
        precondition(interruptedCounts == (0, 0) && interrupted.job == nil && interrupted.links.isEmpty && discarded == nil,
            "A late profile response recreated signed-out work")
        print("PASS: reviewed promotion, frozen source publication, complete history, response-loss recovery, original preservation and scope isolation")
    }
}
