import Foundation

actor WorkerFixture: CampPublishingTransport {
    var request: CloudRequest
    let session: CloudSession
    var receipts: [String: Data] = [:]
    var calls: [String: [String]] = [:]
    var loseClaim = false
    var loseStart = false
    var loseFinish = false
    var held = false
    var gate: CheckedContinuation<Void, Never>?
    var holdClaim = false
    var holdStartReply = false
    var planningPaused = false
    var finalPayload: [String: String] = [:]
    var streamedText = ""
    var streamEvents: [Data] = []
    var controls: [[String: Any]] = []
    var loseStream = false
    func loseNextStream() { loseStream = true }
    func addControl(_ input: [String: Any]) { controls.append(input) }
    func events() -> [Data] { streamEvents }
    func liveText() -> String { streamedText }
    func resultPayload() -> [String: String] { finalPayload }
    init(request: CloudRequest, session: CloudSession) { self.request = request; self.session = session }
    func configure(claim: Bool = false, start: Bool = false, finish: Bool = false, hold: Bool = false, startReply: Bool = false) {
        loseClaim = claim; loseStart = start; loseFinish = finish; holdClaim = hold; holdStartReply = startReply
    }
    func state(_ state: String) { request.state = state }
    func restrictContent() { request.contentRestricted = true; request.prompt = ""; request.sources = [] }
    func pausePlanning(_ paused: Bool) { planningPaused = paused }
    func deadline(_ seconds: TimeInterval) { request.leaseUntil = Date().addingTimeInterval(seconds) }
    func snapshot() -> (CloudRequest, [String: [String]], Bool) { (request, calls, held) }
    func release() { gate?.resume(); gate = nil }
    func perform(_ operation: CampPublication) async throws -> Data {
        if operation.kind == "handoff" { return Data() }
        calls[operation.kind, default: []].append(operation.key)
        if operation.kind == "read" {
            if operation.path.contains("/sessions/") { return try CampWire.encoder().encode(session) }
            var value = request; value.leaseToken = nil
            return try CampWire.encoder().encode(value)
        }
        if let value = receipts[operation.key] { return value }
        if operation.kind == "claim", holdClaim {
            held = true; await withCheckedContinuation { gate = $0 }
        }
        let body = try JSONSerialization.jsonObject(with: operation.body) as! [String: Any]
        if operation.kind == "worker-controls" { return try JSONSerialization.data(withJSONObject: controls) }
        if operation.kind == "implementation-stream" {
            guard body["lease_token"] as? String == request.leaseToken, request.state == "started" else { throw CampCloudError.server(409, "invalid_transition") }
            let seq = (body["seq"] as! NSNumber).int64Value
            let event = body["event"] as! [String: Any]
            let raw = try JSONSerialization.data(withJSONObject: event, options: .sortedKeys)
            if seq <= Int64(streamEvents.count) { precondition(streamEvents[Int(seq)-1] == raw) }
            else {
                precondition(seq == Int64(streamEvents.count)+1)
                streamEvents.append(raw); request.streamSeq = seq
                if event["type"] as? String == "session" { request.providerSessionId = event["sessionID"] as? String }
                if event["type"] as? String == "delta" { streamedText += event["text"] as? String ?? "" }
                if event["type"] as? String == "approvalResolved" || event["type"] as? String == "steerAccepted" {
                    controls.removeAll { ($0["control"] as? [String: Any])?["id"] as? String == event["requestID"] as? String }
                }
            }
            if loseStream { loseStream = false; throw URLError(.networkConnectionLost) }
            return try CampWire.body(["seq": seq])
        }
        if operation.kind == "planning-stream" {
            guard body["lease_token"] as? String == request.leaseToken, request.state == "started" else { throw CampCloudError.server(409, "invalid_transition") }
            let seq = (body["seq"] as! NSNumber).int64Value
            request.streamSeq = seq
            if let event = body["event"] as? [String: Any], let text = event["text"] as? String { streamedText = text }
            return try CampWire.body(["seq": seq])
        }
        if operation.kind == "claim" {
            let ids = body["session_ids"] as! [String]
            if request.isPlanning && planningPaused { return Data("null".utf8) }
            guard !request.isPlanning || body["include_planning"] as? Bool == true else { return Data("null".utf8) }
            guard request.state == "queued", ids.contains(request.sessionId) else { return Data("null".utf8) }
            request.state = "claimed"; request.leaseToken = UUID().uuidString; request.leaseUntil = Date().addingTimeInterval(60)
        } else {
            guard body["lease_token"] as? String == request.leaseToken else { throw CampCloudError.server(409, "invalid_transition") }
            switch operation.kind {
            case "start":
                if let digest = request.quotedContextDigest, body["quoted_context_digest"] as? String != digest { throw CampCloudError.server(400, "context_not_acknowledged") }
                if request.isImplementation, body["execution_context"] as? String != request.executionContext { throw CampCloudError.server(400, "unreviewed_context") }
                request.state = "started"
            case "cancel": request.state = "cancelled"
            case "release": request.state = "queued"; request.leaseToken = nil
            case "heartbeat": break
            case "finish", "reconcile":
                request.state = body["outcome"] as! String
                finalPayload["output"] = body["output"] as? String
                if let usage = body["usage"] as? [String: Int] { finalPayload["input_tokens"] = usage["input_tokens"].map(String.init) }
            default: throw CampCloudError.invalidResponse
            }
        }
        request.revision += 1
        var response = request
        if operation.kind != "claim" { response.leaseToken = nil }
        let data = try CampWire.encoder().encode(response); receipts[operation.key] = data
        if operation.kind == "start", holdStartReply { held = true; await withCheckedContinuation { gate = $0 }; held = false }
        if operation.kind == "claim", loseClaim { loseClaim = false; throw URLError(.networkConnectionLost) }
        if operation.kind == "start", loseStart { loseStart = false; throw URLError(.networkConnectionLost) }
        if operation.kind == "finish", loseFinish { loseFinish = false; throw URLError(.networkConnectionLost) }
        return data
    }
}

@main struct CampWorkerTests {
    static func check(_ value: Bool, _ message: String = "Worker check failed") { precondition(value, message) }
    @MainActor static func eventually(_ label: String, _ test: () async throws -> Bool) async throws {
        for _ in 0..<200 { if try await test() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-worker-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let local = CampSession(id: "local-fixture", sessionID: UUID().uuidString, provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Fixture", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
        let remote = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: local.provider, profileLabel: "Fixture", ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: local.sessionID, title: "Fixture", model: "fixture", state: "idle", stage: "Ready", shared: true, allowRequests: true, externalOwner: false, revision: 1, transcriptRevision: 1, permissionRevision: 1, updatedAt: Date())
        func request() -> CloudRequest { CloudRequest(id: UUID().uuidString, campId: scope.campId, sessionId: remote.id, requesterUserId: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, permissionRevision: 1, revision: 1, prompt: "Synthetic approved request", model: "fixture", effort: "low", speed: "standard", state: "queued", createdAt: Date(), sources: []) }
        let cache = try CampCache(directory: root.appendingPathComponent("first"))
        let api = WorkerFixture(request: request(), session: remote)
        await api.configure(claim: true, finish: true)
        let worker = CampLocalWorker(readFences:{[]},cache: cache, automatic: false, heartbeatInterval: 0)
        worker.nativeSession = { _ in local }; worker.available = { _ in true }; worker.readHistory = { _ in [] }
        var starts = 0
        var finish: ((ConversationResult) -> Void)?
        worker.execute = { native, _, prompt, history, _, completed in starts += 1; finish = completed; return LocalConversation(session: native, history: history, prompt: prompt) }
        worker.attach(scope: scope, actor: actor, transport: api, sessions: [remote])
        await worker.poll(); check(starts == 0)
        await worker.poll(); check(starts == 1)
        let claimCalls = await api.snapshot().1["claim"]!
        check(claimCalls.count == 2 && claimCalls[0] == claimCalls[1], "Lost claim reply changed idempotency key")
        check(try await cache.attempts(scope).first?.state == "running")
        await worker.poll(); check(await api.snapshot().1["heartbeat"]?.count == 1)
        finish?(ConversationResult(state: "completed", message: "", accepted: true))
        try await eventually("Completion was not persisted") { try await cache.attempts(scope).first?.state == "finish_pending" }
        await worker.poll()
        check(try await cache.attempts(scope).isEmpty)
        check(starts == 1, "Recovery repeated provider work"); check(await api.snapshot().0.state == "completed")
        let idleClaims = await api.snapshot().1["claim"]?.count
        await worker.poll(); await worker.poll()
        check(await api.snapshot().1["claim"]?.count == idleClaims, "Idle polling kept writing claim receipts")
        worker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        // Shared implementations use their own provider identity and deliver
        // persisted stream events and owner controls through the real pipe.
        for provider in ["Codex", "Claude"] {
            let implCache = try CampCache(directory: root.appendingPathComponent("implementation-" + provider))
            var impl = request(); impl.kind = "implementation"; impl.mode = "implementation"
            impl.planId = UUID().uuidString; impl.planVersion = 1; impl.implementationId = UUID().uuidString
            impl.executionLocation = "local"; impl.executionContext = String(repeating: "a", count: 64)
            impl.permissions = provider == "Codex" ? ["sandbox": "workspace-write", "approval": "on-request"] : ["claudeMode": "manual"]
            impl.prompt = "Request synthetic approval"
            let source = CampSession(id: local.id, sessionID: local.sessionID, provider: provider, sourceID: local.sourceID, sourceLabel: local.sourceLabel, root: local.root, title: local.title, cwd: local.cwd, updated: 1, path: "", accountID: "", archived: false)
            var remoteSource = remote; remoteSource.provider = provider
            let implAPI = WorkerFixture(request: impl, session: remoteSource)
            await implAPI.loseNextStream()
            let implWorker = CampLocalWorker(readFences: { [] }, cache: implCache, automatic: false, heartbeatInterval: 0)
            implWorker.nativeSession = { _ in source }; implWorker.available = { _ in true }
            implWorker.readHistory = { _ in preconditionFailure("Implementation adopted the source chat history") }
            implWorker.inspectExecution = { value, permissions in
                check(value.sessionID == source.sessionID && permissions.valid(for: provider))
                return ExecutionContext(fingerprint: String(repeating: "a", count: 64), cwd: value.cwd, profile: value.root, provider: provider, permissionSummary: "Fixture")
            }
            var implStarts = 0
            implWorker.execute = { value, work, prompt, history, contexts, completed in
                check(value.sessionID != source.sessionID && value.id != source.id && history.isEmpty && work.isImplementation)
                let run = LocalConversation(session: value, history: [], prompt: prompt)
                run.completed = completed
                do { try run.start(prompt: prompt, model: work.model, tuning: .init(), contexts: contexts, mode: .implementation, executionApproved: true, newSession: (work.providerSessionId ?? "").isEmpty, executionContext: work.executionContext!, permissions: work.permissionSettings, makeProcess: { fixtureProcess("ui-audit") }) }
                catch { preconditionFailure(error.localizedDescription) }
                implStarts += 1; return run
            }
            implWorker.attach(scope: scope, actor: actor, transport: implAPI, sessions: [remoteSource])
            await implWorker.poll()
            try await eventually("Implementation approval never reached shared stream") {
                await implAPI.events().contains { String(decoding: $0, as: UTF8.self).contains("\"type\":\"approval\"") }
            }
            check(await implAPI.liveText().contains("Latest synthetic streaming marker"))
            let journal = try await implCache.attempts(scope).first!
            check(journal.request.providerSessionId != nil && journal.request.providerSessionId != source.sessionID && journal.pendingStream == nil)
            check(implStarts == 1, "Lost stream response relaunched the provider")
            let direction = UUID().uuidString
            await implAPI.addControl(["id": direction, "control": ["type": "steer", "id": direction, "text": "Keep the test focused"]])
            await implWorker.poll()
            try await eventually("Shared steering was not acknowledged") {
                await implAPI.events().contains { String(decoding: $0, as: UTF8.self).contains("steerAccepted") }
            }
            let afterSteer = try await implCache.attempts(scope).first!
            check(afterSteer.deliveredControls?.contains(direction) == true, "Control intent was not saved before delivery")
            await implAPI.addControl(["id": UUID().uuidString, "control": ["type": "approval", "id": "e8a1e25a-2b71-4fb9-8122-9ad03a842993", "allow": true, "answers": [:]]])
            await implWorker.poll()
            try await eventually("Approved shared implementation did not finish") { try await implCache.attempts(scope).isEmpty }
            check(await implAPI.snapshot().0.state == "completed" && implStarts == 1)
            check(await implAPI.events().contains { String(decoding: $0, as: UTF8.self).contains("approvalResolved") })
            implWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

            var followup = impl; followup.id = UUID().uuidString; followup.providerSessionId = journal.request.providerSessionId
            followup.prompt = "complete this follow-up"
            let followAPI = WorkerFixture(request: followup, session: remoteSource)
            implWorker.attach(scope: scope, actor: actor, transport: followAPI, sessions: [remoteSource])
            await implWorker.poll()
            try await eventually("Implementation follow-up did not finish") { try await implCache.attempts(scope).isEmpty }
            check(await followAPI.snapshot().0.providerSessionId == journal.request.providerSessionId && implStarts == 2)
            check(!(await followAPI.events()).contains { String(decoding: $0, as: UTF8.self).contains("\"type\":\"session\"") }, "Follow-up created a new provider conversation")
            implWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

            let staleCache = try CampCache(directory: root.appendingPathComponent("stale-permissions-" + provider))
            let staleAPI = WorkerFixture(request: impl, session: remoteSource)
            let stale = CampLocalWorker(readFences: { [] }, cache: staleCache, automatic: false)
            stale.nativeSession = { _ in source }; stale.available = { _ in true }
            stale.inspectExecution = { value, _ in ExecutionContext(fingerprint: String(repeating: "b", count: 64), cwd: value.cwd, profile: value.root, provider: provider, permissionSummary: "Changed") }
            stale.execute = { _,_,_,_,_,_ in preconditionFailure("Changed permissions launched provider work") }
            stale.attach(scope: scope, actor: actor, transport: staleAPI, sessions: [remoteSource])
            await stale.poll()
            check(await staleAPI.snapshot().0.state == "cancelled")
            check(try await staleCache.attempts(scope).isEmpty)
            stale.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        }

        let planCache = try CampCache(directory: root.appendingPathComponent("planning"))
        var planRequest = request(); planRequest.kind = "planning"; planRequest.planId = UUID().uuidString; planRequest.planVersion = 1; planRequest.planStep = 0
        planRequest.quotedSources = [ImportedPlanningSource(id: String(repeating: "a", count: 64), title: "Saved context", revision: String(repeating: "b", count: 64), messages: [Message(id: "quoted", role: "assistant", text: "Retained planning context")])]
        planRequest.quotedContextDigest = String(repeating: "c", count: 64)
        let planAPI = WorkerFixture(request: planRequest, session: remote)
        await planAPI.configure(finish: true)
        let planWorker = CampLocalWorker(readFences:{[]},cache: planCache, automatic: false)
        planWorker.nativeSession = { _ in local }; planWorker.available = { _ in true }
        planWorker.readHistory = { _ in preconditionFailure("Planning must not read or resume the anchor conversation") }
        var planStarts = 0
        var planComplete: ((ConversationResult) -> Void)?
        planWorker.execute = { native, work, prompt, history, contexts, completed in
            check(work.isPlanning && work.planStep == 0 && history.isEmpty)
            check(contexts.count == 1 && PlanningQuoteLink(id: contexts[0].id)?.planID == work.planId && contexts[0].messages.first?.text == "Retained planning context", "Quoted context did not reach the local CLI")
            planStarts += 1; planComplete = completed
            return LocalConversation(session: native, history: [], prompt: prompt)
        }
        planWorker.attach(scope: scope, actor: actor, transport: planAPI, sessions: [remote])
        await planWorker.poll()
        check(planStarts == 1 && planWorker.usesPlanningProvider("Codex") && planWorker.activePlanningCount == 1)
        planComplete?(ConversationResult(state: "completed", message: "", accepted: true, output: "Frozen planning output", usage: ConversationUsage(inputTokens: 73, outputTokens: 11)))
        try await eventually("Planning result was not persisted") { try await planCache.attempts(scope).first?.state == "finish_pending" }
        let durableResult = try await planCache.attempts(scope).first
        check(durableResult?.output == "Frozen planning output" && durableResult?.usage?.inputTokens == 73)
        await planWorker.poll()
        check(try await planCache.attempts(scope).isEmpty && planStarts == 1, "Lost planning finish replayed the provider")
        let planningPayload = await planAPI.resultPayload()
        check(planningPayload["output"] == "Frozen planning output" && planningPayload["input_tokens"] == "73", "Planning output and usage did not reach the server")
        check(!planWorker.usesPlanningProvider("Codex") && planWorker.activePlanningCount == 0)
        planWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let streamCache = try CampCache(directory: root.appendingPathComponent("planning-stream"))
        let streamAPI = WorkerFixture(request: planRequest, session: remote)
        let streamWorker = CampLocalWorker(readFences: { [] }, cache: streamCache, automatic: false, heartbeatInterval: 0)
        streamWorker.nativeSession = { _ in local }; streamWorker.available = { _ in true }
        streamWorker.execute = { native, work, prompt, _, contexts, completed in
            let run = LocalConversation(session: native, history: [], prompt: prompt)
            run.completed = completed
            do { try run.start(prompt: prompt, model: work.model, tuning: .init(), contexts: contexts, mode: .planning, makeProcess: { fixtureProcess("planning-stream") }) }
            catch { return nil }
            return run
        }
        streamWorker.attach(scope: scope, actor: actor, transport: streamAPI, sessions: [remote])
        await streamWorker.poll()
        try await eventually("Live provider text was not published before completion") { await streamAPI.liveText().contains("Live planning stream before completion") }
        check(await streamAPI.snapshot().0.state == "started", "Streaming required a completed planning turn")
        await streamAPI.restrictContent()
        await streamWorker.poll()
        try await eventually("Stopped streaming worker did not finish") { try await streamCache.attempts(scope).isEmpty }
        streamWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let resumeCache = try CampCache(directory: root.appendingPathComponent("planning-resume"))
        let resumeAPI = WorkerFixture(request: planRequest, session: remote)
        await resumeAPI.pausePlanning(true)
        let resumeWorker = CampLocalWorker(readFences:{[]},cache: resumeCache, automatic: false)
        resumeWorker.nativeSession = { _ in local }; resumeWorker.available = { _ in true }
        var resumedStarts = 0
        resumeWorker.execute = { native, _, prompt, _, _, _ in
            resumedStarts += 1
            return LocalConversation(session: native, history: [], prompt: prompt)
        }
        resumeWorker.attach(scope: scope, actor: actor, transport: resumeAPI, sessions: [remote])
        await resumeWorker.poll(); await resumeWorker.poll()
        let pausedCalls = await resumeAPI.snapshot().1["claim"]?.count
        check(resumedStarts == 0 && pausedCalls == 1, "Paused planning should become idle")
        await resumeAPI.pausePlanning(false)
        resumeWorker.scan() // A planning.changed event must wake a worker even when its queued request did not change.
        await resumeWorker.poll()
        check(resumedStarts == 1, "Resumed planning did not wake an idle worker")
        resumeWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        var retained = durableResult!; retained.state = "uncertain"
        try await planCache.saveWorkerAttempt(retained, scope: scope, expectedEpoch: 0)
        try await planCache.clear(scope)
        let scrubbed = try await planCache.attempts(scope).first
        check(scrubbed?.output == nil && scrubbed?.usage == nil && scrubbed?.request.prompt == "" && scrubbed?.request.quotedSources == nil && scrubbed?.request.quotedContextDigest == nil, "Signing out retained private planning output")

        let cancelledCache = try CampCache(directory: root.appendingPathComponent("cancel-during-start"))
        let cancelledAPI = WorkerFixture(request: planRequest, session: remote)
        await cancelledAPI.configure(startReply: true)
        let cancelledWorker = CampLocalWorker(readFences:{[]},cache: cancelledCache, automatic: false)
        cancelledWorker.nativeSession = { _ in local }; cancelledWorker.available = { _ in true }
        cancelledWorker.execute = { _, _, _, _, _, _ in preconditionFailure("A processed cancellation was ignored before provider submission") }
        cancelledWorker.attach(scope: scope, actor: actor, transport: cancelledAPI, sessions: [remote])
        let cancelling = Task { await cancelledWorker.poll() }
        try await eventually("Start acknowledgement was not held") { await cancelledAPI.snapshot().2 }
        await cancelledAPI.state("cancel_requested")
        cancelledWorker.changed(await cancelledAPI.snapshot().0)
        await cancelledAPI.release(); await cancelling.value
        check(await cancelledAPI.snapshot().0.state == "cancelled")
        check(try await cancelledCache.attempts(scope).isEmpty)
        cancelledWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let batchCache = try CampCache(directory: root.appendingPathComponent("eligible-batches"))
        var lastRemote = remote; lastRemote.id = "ffffffff-ffff-4fff-8fff-ffffffffffff"
        var lastRequest = request(); lastRequest.sessionId = lastRemote.id
        let batchAPI = WorkerFixture(request: lastRequest, session: lastRemote)
        let batchWorker = CampLocalWorker(readFences:{[]},cache: batchCache, automatic: false)
        batchWorker.nativeSession = { _ in local }; batchWorker.available = { _ in true }; batchWorker.readHistory = { _ in [] }
        var batchStarts = 0
        batchWorker.execute = { native, _, prompt, history, _, _ in
            batchStarts += 1
            return LocalConversation(session: native, history: history, prompt: prompt)
        }
        var manySessions: [CloudSession] = (0..<200).map { index in
            var item = remote
            item.id = String(format: "00000000-0000-4000-8000-%012d", index)
            return item
        }
        manySessions.append(lastRemote)
        batchWorker.attach(scope: scope, actor: actor, transport: batchAPI, sessions: manySessions)
        await batchWorker.poll(); check(batchStarts == 0)
        await batchWorker.poll(); check(batchStarts == 1, "Sessions after the first 200 were starved")
        check(await batchAPI.snapshot().1["claim"]?.count == 2)
        batchWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])

        let lostCache = try CampCache(directory: root.appendingPathComponent("lost-start"))
        let lostAPI = WorkerFixture(request: request(), session: remote)
        await lostAPI.configure(start: true)
        let lostWorker = CampLocalWorker(readFences:{[]},cache: lostCache, automatic: false)
        lostWorker.nativeSession = { _ in local }; lostWorker.available = { _ in true }; lostWorker.readHistory = { _ in [] }
        lostWorker.execute = { _, _, _, _, _, _ in preconditionFailure("Unknown Start acknowledgement executed the provider") }
        lostWorker.attach(scope: scope, actor: actor, transport: lostAPI, sessions: [remote])
        await lostWorker.poll()
        check(try await lostCache.attempts(scope).first?.state == "uncertain")
        await lostWorker.poll()
        check(await lostAPI.snapshot().1["start"]?.count == 1, "Unknown Start was retried")
        await lostAPI.state("outcome_unknown")
        await lostWorker.confirm(lostWorker.issues[0], outcome: "cancelled")
        check(try await lostCache.attempts(scope).isEmpty)
        check(await lostAPI.snapshot().0.state == "cancelled")

        let clearedCache = try CampCache(directory: root.appendingPathComponent("cleared"))
        let clearedAPI = WorkerFixture(request: request(), session: remote)
        await clearedAPI.configure(hold: true)
        let clearedWorker = CampLocalWorker(readFences:{[]},cache: clearedCache, automatic: false)
        clearedWorker.nativeSession = { _ in local }; clearedWorker.available = { _ in true }
        clearedWorker.execute = { _, _, _, _, _, _ in preconditionFailure("Revoked claim executed") }
        clearedWorker.attach(scope: scope, actor: actor, transport: clearedAPI, sessions: [remote])
        let pending = Task { await clearedWorker.poll() }
        try await eventually("Claim did not reach fixture") { await clearedAPI.snapshot().2 }
        try await clearedCache.clear(scope)
        clearedWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        await clearedAPI.release(); await pending.value
        check(try await clearedCache.attempts(scope).isEmpty, "Late reply restored a revoked execution")

        let invalidCache = try CampCache(directory: root.appendingPathComponent("preflight"))
        let invalidAPI = WorkerFixture(request: request(), session: remote)
        let invalidWorker = CampLocalWorker(readFences:{[]},cache: invalidCache, automatic: false)
        invalidWorker.nativeSession = { _ in local }; invalidWorker.available = { _ in true }
        invalidWorker.readHistory = { _ in throw CampError.message("Fixture transcript is unavailable") }
        invalidWorker.execute = { _, _, _, _, _, _ in preconditionFailure("Failed preflight started a provider") }
        invalidWorker.attach(scope: scope, actor: actor, transport: invalidAPI, sessions: [remote])
        await invalidWorker.poll()
        check(await invalidAPI.snapshot().0.state == "failed")
        check(try await invalidCache.attempts(scope).isEmpty)

        let leaseCache = try CampCache(directory: root.appendingPathComponent("lease"))
        let leaseAPI = WorkerFixture(request: request(), session: remote)
        let leaseWorker = CampLocalWorker(readFences:{[]},cache: leaseCache, automatic: false, heartbeatInterval: 0)
        leaseWorker.nativeSession = { _ in local }; leaseWorker.available = { _ in true }; leaseWorker.readHistory = { _ in [] }
        leaseWorker.execute = { native, _, prompt, history, _, completed in
            let run = LocalConversation(session: native, history: history, prompt: prompt)
            run.completed = completed
            try! run.start(prompt: prompt, model: "", tuning: ModelTuning(), makeProcess: {
                fixtureProcess("cancel")
            })
            return run
        }
        leaseWorker.attach(scope: scope, actor: actor, transport: leaseAPI, sessions: [remote])
        await leaseWorker.poll()
        await leaseAPI.deadline(5.1)
        await leaseWorker.poll()
        try await eventually("Expired lease did not stop the real subprocess") { await leaseAPI.snapshot().0.state == "cancelled" }
        check(Bridge.activeProcesses.isEmpty)
        let historyCache = try CampCache(directory: root.appendingPathComponent("history-only"))
        var history = remote; history.historyOnly = true
        let historyAPI = WorkerFixture(request: request(), session: history)
        let historyWorker = CampLocalWorker(readFences: { [] }, cache: historyCache, automatic: false)
        historyWorker.nativeSession = { _ in local }; historyWorker.available = { _ in true }
        historyWorker.execute = { _, _, _, _, _, _ in preconditionFailure("Archived history started provider work") }
        historyWorker.attach(scope: scope, actor: actor, transport: historyAPI, sessions: [history])
        await historyWorker.poll()
        check(await historyAPI.snapshot().1["claim"] == nil, "Archived history was included in an execution claim")
        historyWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        print("PASS: worker replay and recovery, isolated Codex/Claude implementation streams, owner controls, permission checks, follow-up identity, revocation and history-only exclusion")
    }
}
