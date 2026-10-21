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
    init(request: CloudRequest, session: CloudSession) { self.request = request; self.session = session }
    func configure(claim: Bool = false, start: Bool = false, finish: Bool = false, hold: Bool = false) {
        loseClaim = claim; loseStart = start; loseFinish = finish; holdClaim = hold
    }
    func state(_ state: String) { request.state = state }
    func deadline(_ seconds: TimeInterval) { request.leaseUntil = Date().addingTimeInterval(seconds) }
    func snapshot() -> (CloudRequest, [String: [String]], Bool) { (request, calls, held) }
    func release() { gate?.resume(); gate = nil }
    func perform(_ operation: CampPublication) async throws -> Data {
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
        if operation.kind == "claim" {
            let ids = body["session_ids"] as! [String]
            guard request.state == "queued", ids.contains(request.sessionId) else { return Data("null".utf8) }
            request.state = "claimed"; request.leaseToken = UUID().uuidString; request.leaseUntil = Date().addingTimeInterval(60)
        } else {
            guard body["lease_token"] as? String == request.leaseToken else { throw CampCloudError.server(409, "invalid_transition") }
            switch operation.kind {
            case "start": request.state = "started"
            case "release": request.state = "queued"; request.leaseToken = nil
            case "heartbeat": break
            case "finish", "reconcile": request.state = body["outcome"] as! String
            default: throw CampCloudError.invalidResponse
            }
        }
        request.revision += 1
        var response = request
        if operation.kind != "claim" { response.leaseToken = nil }
        let data = try CampWire.encoder().encode(response); receipts[operation.key] = data
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
        let worker = CampLocalWorker(cache: cache, automatic: false, heartbeatInterval: 0)
        worker.nativeSession = { _ in local }; worker.available = { _ in true }; worker.readHistory = { _ in [] }
        var starts = 0
        var finish: ((ConversationResult) -> Void)?
        worker.execute = { native, _, prompt, history, completed in starts += 1; finish = completed; return LocalConversation(session: native, history: history, prompt: prompt) }
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

        let batchCache = try CampCache(directory: root.appendingPathComponent("eligible-batches"))
        var lastRemote = remote; lastRemote.id = "ffffffff-ffff-4fff-8fff-ffffffffffff"
        var lastRequest = request(); lastRequest.sessionId = lastRemote.id
        let batchAPI = WorkerFixture(request: lastRequest, session: lastRemote)
        let batchWorker = CampLocalWorker(cache: batchCache, automatic: false)
        batchWorker.nativeSession = { _ in local }; batchWorker.available = { _ in true }; batchWorker.readHistory = { _ in [] }
        var batchStarts = 0
        batchWorker.execute = { native, _, prompt, history, _ in
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
        let lostWorker = CampLocalWorker(cache: lostCache, automatic: false)
        lostWorker.nativeSession = { _ in local }; lostWorker.available = { _ in true }; lostWorker.readHistory = { _ in [] }
        lostWorker.execute = { _, _, _, _, _ in preconditionFailure("Unknown Start acknowledgement executed the provider") }
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
        let clearedWorker = CampLocalWorker(cache: clearedCache, automatic: false)
        clearedWorker.nativeSession = { _ in local }; clearedWorker.available = { _ in true }
        clearedWorker.execute = { _, _, _, _, _ in preconditionFailure("Revoked claim executed") }
        clearedWorker.attach(scope: scope, actor: actor, transport: clearedAPI, sessions: [remote])
        let pending = Task { await clearedWorker.poll() }
        try await eventually("Claim did not reach fixture") { await clearedAPI.snapshot().2 }
        try await clearedCache.clear(scope)
        clearedWorker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        await clearedAPI.release(); await pending.value
        check(try await clearedCache.attempts(scope).isEmpty, "Late reply restored a revoked execution")

        let invalidCache = try CampCache(directory: root.appendingPathComponent("preflight"))
        let invalidAPI = WorkerFixture(request: request(), session: remote)
        let invalidWorker = CampLocalWorker(cache: invalidCache, automatic: false)
        invalidWorker.nativeSession = { _ in local }; invalidWorker.available = { _ in true }
        invalidWorker.readHistory = { _ in throw CampError.message("Fixture transcript is unavailable") }
        invalidWorker.execute = { _, _, _, _, _ in preconditionFailure("Failed preflight started a provider") }
        invalidWorker.attach(scope: scope, actor: actor, transport: invalidAPI, sessions: [remote])
        await invalidWorker.poll()
        check(await invalidAPI.snapshot().0.state == "failed")
        check(try await invalidCache.attempts(scope).isEmpty)

        let leaseCache = try CampCache(directory: root.appendingPathComponent("lease"))
        let leaseAPI = WorkerFixture(request: request(), session: remote)
        let leaseWorker = CampLocalWorker(cache: leaseCache, automatic: false, heartbeatInterval: 0)
        leaseWorker.nativeSession = { _ in local }; leaseWorker.available = { _ in true }; leaseWorker.readHistory = { _ in [] }
        leaseWorker.execute = { native, _, prompt, history, completed in
            let run = LocalConversation(session: native, history: history, prompt: prompt)
            run.completed = completed
            try! run.start(prompt: prompt, model: "", tuning: ModelTuning(), makeProcess: {
                let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
                p.arguments = ["-u", "-c", "import sys,json;sys.stdin.readline();print('{\"type\":\"started\"}',flush=True);sys.stdin.readline();print('{\"type\":\"cancelled\"}',flush=True)"]
                return p
            })
            return run
        }
        leaseWorker.attach(scope: scope, actor: actor, transport: leaseAPI, sessions: [remote])
        await leaseWorker.poll()
        await leaseAPI.deadline(5.1)
        await leaseWorker.poll()
        try await eventually("Expired lease did not stop the real subprocess") { await leaseAPI.snapshot().0.state == "cancelled" }
        check(Bridge.activeProcesses.isEmpty)
        print("PASS: worker claim replay, durable start, heartbeat, finish recovery, unknown-outcome review and revoked-response fencing")
    }
}
