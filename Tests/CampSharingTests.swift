import Foundation

actor PublishingFixture: CampPublishingTransport {
    let scope: CloudScope
    let actor: CloudActor
    var profile: CloudProfile?
    var remote: CloudSession?
    var receipts: [String: Data] = [:]
    var keys: [String] = []
    var messages: [String: PublishMessage] = [:]
    var lostResponse = true
    var transcriptWrites = 0
    var holdSession = false
    var sessionHeld = false
    var releaseGate: CheckedContinuation<Void, Never>?
    init(scope: CloudScope, actor: CloudActor) { self.scope = scope; self.actor = actor }
    func holdNextSession() { holdSession = true; lostResponse = false }
    func isSessionHeld() -> Bool { sessionHeld }
    func moveProject(_ id:String) { remote?.projectId=id;remote?.revision += 1 }
    func releaseSession() { releaseGate?.resume(); releaseGate = nil; holdSession = false }
    func perform(_ operation: CampPublication) async throws -> Data {
        if operation.method == "GET" {
            guard let remote else { throw CampCloudError.server(404, "not_found") }
            return try CampWire.encoder().encode(remote)
        }
        keys.append(operation.key)
        if let receipt = receipts[operation.key] { return receipt }
        if operation.kind == "session" && holdSession && !sessionHeld {
            sessionHeld = true
            await withCheckedContinuation { releaseGate = $0 }
            if let receipt = receipts[operation.key] { return receipt }
        }
        let object = try JSONSerialization.jsonObject(with: operation.body) as! [String: Any]
        let result: Data
        switch operation.kind {
        case "profile":
            let value = CloudProfile(id: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, provider: object["provider"] as! String, localId: object["local_id"] as! String, label: object["label"] as! String)
            profile = value; result = try CampWire.encoder().encode(value)
        case "session":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.revision ?? 0 else { throw CampCloudError.server(409, "revision_conflict") }
            let value = CloudSession(id: object["id"] as! String, campId: scope.campId, projectId: object["project_id"] as! String, profileId: object["profile_id"] as! String, provider: profile!.provider, profileLabel: profile!.label, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: object["local_session_id"] as! String, title: (object["title"] as! String).trimmingCharacters(in: .whitespacesAndNewlines), model: object["model"] as! String, state: object["state"] as! String, stage: object["stage"] as! String, shared: object["shared"] as! Bool, allowRequests: object["allow_requests"] as! Bool, externalOwner: object["external_owner"] as! Bool, revision: expected + 1, transcriptRevision: remote?.transcriptRevision ?? 0, permissionRevision: 1, updatedAt: Date())
            remote = value; result = try CampWire.encoder().encode(value)
        case "transcript":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.transcriptRevision else { throw CampCloudError.server(409, "revision_conflict") }
            let encoded = try JSONSerialization.data(withJSONObject: object["messages"]!)
            let values = try CampWire.decoder().decode([PublishMessage].self, from: encoded)
            for value in values { messages[value.id] = value }
            remote?.transcriptRevision += 1; transcriptWrites += 1
            result = try CampWire.body(["revision": remote!.transcriptRevision])
        default: throw CampCloudError.invalidResponse
        }
        receipts[operation.key] = result
        if operation.kind == "transcript" && lostResponse { lostResponse = false; throw URLError(.networkConnectionLost) }
        return result
    }
    func snapshot() -> (CloudSession?, Int, [String], [String: PublishMessage]) { (remote, transcriptWrites, keys, messages) }
}

@main struct CampSharingTests {
    @MainActor static func eventually(_ label: String, _ condition: @MainActor () async -> Bool) async {
        for _ in 0..<150 {
            if await condition() { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-sharing-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let cache = try CampCache(directory: root)
        let native = CampSession(id: "Claude:/local:fixture", sessionID: UUID().uuidString, provider: "Claude", sourceID: "Claude:/local", sourceLabel: "Fixture account", root: "/local", title: "  A synthetic session  ", cwd: "/local/project", updated: 1, path: "/local/session.jsonl", accountID: "", archived: false, model: "fixture-model")
        let server = PublishingFixture(scope: scope, actor: actor)
        let export: @Sendable (CampSession, Int64) async throws -> TranscriptExport = { _, after in
            let message = PublishMessage(id: after < 0 ? "first" : "second", ordinal: after < 0 ? 0 : 1, role: "assistant", text: after < 0 ? "First message" : "Second message", removed: false)
            return TranscriptExport(messages: [message], after: message.ordinal, hasMore: after < 0, fingerprint: "fixture")
        }
        let sharing = CampSharing(cache: cache, export: export)
        sharing.localSession = { id in id == native.id ? native : nil }
        sharing.attach(scope: scope, actor: actor, transport: server)
        let project = UUID().uuidString
        try await sharing.configure(local: native, projectId: project, shared: true, allowRequests: true)
        await eventually("sharing did not recover lost response") { sharing.shares.first?.state == "Shared" }
        let first = await server.snapshot()
        precondition(first.0?.shared == true && first.0?.allowRequests == true)
        precondition(first.1 == 2 && first.3.count == 2, "lost response repeated provider persistence")
        precondition(first.2.count > Set(first.2).count, "transport did not reuse the command key")
        let saved = try await cache.shares(scope)
        precondition(saved.first?.local == native && saved.first?.pending == nil, "native identifiers did not round-trip")
        precondition(saved.first?.profile?.localId.count == 64, "raw local path was transmitted as profile id")
        let id = saved[0].id
        sharing.attach(scope: nil, actor: nil, transport: nil)
        let resumed = CampSharing(cache: cache, export: export)
        resumed.localSession = { _ in native }
        resumed.attach(scope: scope, actor: actor, transport: server)
        await eventually("sharing did not restore") { resumed.shares.first?.state == "Shared" }
        resumed.retry()
        try? await Task.sleep(for: .milliseconds(100))
        let unchanged = await server.snapshot()
        precondition(unchanged.1 == 2, "unchanged transcript was uploaded again")
        precondition(unchanged.0?.revision == first.0?.revision, "Title whitespace repeatedly republished metadata")
        let reassigned=UUID().uuidString
        await server.moveProject(reassigned);resumed.retry()
        await eventually("Publisher reverted the remote project assignment") { resumed.shares.first?.projectId==reassigned }
        let afterAssignment=await server.snapshot()
        precondition(afterAssignment.0?.projectId==reassigned)
        precondition(resumed.shares.first?.id == id, "reconnect changed shared session identity")
        try await resumed.configure(local: native, projectId: project, shared: false, allowRequests: false)
        await eventually("unshare did not complete") { resumed.shares.first?.state == "Private" }
        let withdrawn = await server.snapshot()
        precondition(withdrawn.0?.shared == false)
        resumed.attach(scope: nil, actor: nil, transport: nil)
        let racingServer = PublishingFixture(scope: scope, actor: actor)
        await racingServer.holdNextSession()
        let racing = CampSharing(cache: try CampCache(directory: root.appendingPathComponent("race")), export: export)
        racing.localSession = { _ in native }
        racing.attach(scope: scope, actor: actor, transport: racingServer)
        try await racing.configure(local: native, projectId: project, shared: true, allowRequests: true)
        await eventually("first publication did not start") { await racingServer.isSessionHeld() }
        try await racing.configure(local: native, projectId: project, shared: false, allowRequests: false)
        await eventually("withdrawal raced with first publication") { racing.shares.first?.state == "Private" }
        await racingServer.releaseSession()
        try? await Task.sleep(for: .milliseconds(40))
        let raceResult = await racingServer.snapshot()
        precondition(raceResult.0?.shared == false, "late publication reopened withdrawn sharing")
        racing.attach(scope: nil, actor: nil, transport: nil)
        let epoch = try await cache.epoch(scope)
        try await cache.clear(scope)
        do {
            try await cache.saveShare(saved[0], scope: scope, expectedEpoch: epoch)
            preconditionFailure("revoked publisher restored a local sharing grant")
        } catch {}
        let purged = try await cache.shares(scope)
        precondition(purged.isEmpty)
        print("Native sharing recovery, transcript pagination, privacy and restart checks passed")
    }
}
