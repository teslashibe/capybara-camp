import Foundation

actor ExecutionFixture: CampAccountTransport {
    let session: CloudSession
    var target = CloudExecutionTarget(location: "local", epoch: 1, mode: "chat", permissions: [:], available: true)
    var workspaces: [CloudWorkspace] = []
    var receipts: [String: Data] = [:]
    var keys: [String] = []
    var prepares = 0
    var assignments = 0
    var lose: Set<String> = []
    var reject: Int?
    var foreignWorkspace = false
    var holdRead = false
    var held = false
    var gate: CheckedContinuation<Void, Never>?
    init(_ session: CloudSession) { self.session = session }
    func loseNext(_ kind: String) { lose.insert(kind) }
    func rejectNext(_ status: Int) { reject = status }
    func makeForeign(_ value: Bool) { foreignWorkspace = value }
    func ready() { workspaces[0].state = "ready"; workspaces[0].revision += 1 }
    func hold() { holdRead = true }
    func isHeld() -> Bool { held }
    func release() { gate?.resume(); gate = nil }
    func counts() -> (Int, Int, [String]) { (prepares, assignments, keys) }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        if method == "GET" {
            if holdRead { holdRead = false; held = true; await withCheckedContinuation { gate = $0 }; held = false }
            if path.hasSuffix("/execution") { return try CampWire.encoder().encode(target) }
            var values = workspaces
            if foreignWorkspace, !values.isEmpty { values[0].ownerUserId = UUID().uuidString }
            return try CampWire.encoder().encode(values)
        }
        guard let key, let body else { throw CampCloudError.invalidResponse }
        keys.append(key)
        if let reject { self.reject = nil; throw CampCloudError.server(reject, "changed") }
        if let data = receipts[key] { return data }
        let kind: String
        let data: Data
        if path.hasSuffix("/workspaces") {
            kind = "prepare"; prepares += 1
            let input = try CampWire.decoder().decode(CloudWorkspaceInput.self, from: body)
            let id = UUID().uuidString.lowercased()
            let value = CloudWorkspace(id: id, campId: session.campId, sessionId: session.id, projectId: session.projectId,
                ownerUserId: session.ownerUserId, providerAccountId: input.providerAccountId, providerInstallationId: UUID().uuidString,
                providerEpoch: 1, gitConnectionId: input.gitConnectionId, gitEpoch: 1, githubUserId: 123, githubLogin: "fixture",
                githubInstallationId: input.githubInstallationId, repositoryId: input.repositoryId, repositoryName: "fixture/repo",
                requestedRef: input.ref, baseCommit: String(repeating: "a", count: 40), branch: "camp/\(session.id)/\(id)",
                purpose: input.purpose, sourceRevision: session.revision, projectRevision: 1, state: "queued", revision: 1,
                createdAt: Date(), updatedAt: Date())
            workspaces = [value]; data = try CampWire.encoder().encode(value)
        } else if path.hasSuffix("/execution") {
            kind = "assign"; assignments += 1
            let value = try JSONSerialization.jsonObject(with: body) as! [String: Any]
            target.location = value["location"] as! String; target.epoch += 1
            target.workspaceId = value["workspace_id"] as? String
            data = try CampWire.encoder().encode(target)
        } else if path.hasSuffix("/cancel"), !workspaces.isEmpty {
            kind = "cancel"; workspaces[0].state = "cancelled"; workspaces[0].revision += 1
            data = try CampWire.encoder().encode(workspaces[0])
        } else { throw CampCloudError.invalidResponse }
        receipts[key] = data
        if lose.remove(kind) != nil { throw URLError(.networkConnectionLost) }
        return data
    }
}

@main struct CampExecutionTests {
    @MainActor static func main() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("camp-execution-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let cache = try CampCache(directory: directory)
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let credential = CloudCredential(origin: scope.origin, actor: CloudActor(userId: scope.userId, deviceId: UUID().uuidString,
            email: "fixture@example.com", name: "Fixture"), token: "fixture-execution-secret", expiresAt: Date().addingTimeInterval(3600))
        let session = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString,
            provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId, ownerDeviceId: credential.actor.deviceId,
            localSessionId: UUID().uuidString, title: "Synthetic execution", model: "fixture", state: "idle", stage: "Ready",
            shared: false, allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 1, permissionRevision: 1, updatedAt: Date())
        let input = CloudWorkspaceInput(providerAccountId: UUID().uuidString, gitConnectionId: UUID().uuidString,
            githubInstallationId: 7, repositoryId: 5, installationPage: 1, repositoryPage: 1, ref: "main", purpose: "planning",
            expectedSessionRevision: 1, expectedProjectRevision: 1)
        let fixture = ExecutionFixture(session)
        let first = CampExecution(cache: cache); first.attach(scope: scope, credential: credential, transport: fixture)
        await first.refresh(session)
        precondition(first.target?.location == "local" && first.error.isEmpty)
        await fixture.loseNext("prepare"); await first.prepare(session, input: input)
        precondition(first.pending && !first.error.isEmpty, "Lost response must retain its command key")
        let raw = try await cache.metadata("execution-setup:" + scope.key)!
        precondition(!String(decoding: raw, as: UTF8.self).contains(credential.token), "Execution journal contains bearer token")
        let restored = CampExecution(cache: cache); restored.attach(scope: scope, credential: credential, transport: fixture)
        await restored.refresh(session)
        precondition(restored.pending && restored.workspaces.count == 1, "Read must not repeat a pending mutation")
        await restored.retry(session)
        var counts = await fixture.counts()
        precondition(!restored.pending && counts.0 == 1 && counts.2.count == 2 && counts.2[0] == counts.2[1], "Relaunch repeated workspace creation")
        await fixture.ready(); await restored.refresh(session)
        let workspace = restored.workspaces[0]
        await fixture.loseNext("assign")
        await restored.assign(session, workspace: workspace, permissions: PermissionSettings(), approved: false)
        precondition(restored.pending)
        await restored.retry(session); counts = await fixture.counts()
        precondition(!restored.pending && restored.target?.location == "cloud" && counts.1 == 1, "Lost assignment duplicated execution handoff")
        await fixture.rejectNext(409)
        await restored.assign(session, workspace: nil, permissions: PermissionSettings(), approved: false)
        precondition(!restored.pending && !restored.error.isEmpty && restored.target?.location == "cloud", "Stale review left setup stuck or moved the session")
        await restored.assign(session, workspace: nil, permissions: PermissionSettings(), approved: false)
        precondition(restored.target?.location == "local", "Fresh review could not recover after rejection")
        await fixture.makeForeign(true); await restored.refresh(session)
        precondition(!restored.error.isEmpty && restored.workspaces.first?.ownerUserId == scope.userId, "Foreign workspace was published to UI")
        await fixture.makeForeign(false)
        var foreign = session; foreign.ownerUserId = UUID().uuidString
        let before = await fixture.counts()
        await restored.prepare(foreign, input: input)
        let after = await fixture.counts()
        precondition(before.2.count == after.2.count, "Foreign session created a request")
        await fixture.hold()
        let refresh = Task { await restored.refresh(session) }
        for _ in 0..<100 where !(await fixture.isHeld()) { try await Task.sleep(for: .milliseconds(5)) }
        restored.attach(scope: nil, credential: nil, transport: nil)
        await fixture.release(); await refresh.value
        precondition(restored.target == nil && restored.workspaces.isEmpty && restored.error.isEmpty && !restored.working, "Late read repopulated signed-out state")

        let forged = try CampWire.body(["pending": ["id": UUID().uuidString, "session_id": session.id, "method": "POST",
            "path": "/v1/camps/\(scope.campId)/../provider-accounts", "body": Data("{}".utf8).base64EncodedString(), "kind": "prepare"]])
        try await cache.setMetadata("execution-setup:" + scope.key, forged)
        let guarded = CampExecution(cache: cache); guarded.attach(scope: scope, credential: credential, transport: fixture)
        await guarded.retry(session)
        let guardedCounts = await fixture.counts()
        precondition(!guarded.error.isEmpty && guardedCounts.2.count == after.2.count, "Malformed persisted path was replayed")
        let epoch = try await cache.epoch(scope)
        try await cache.clear(scope)
        do { try await cache.saveExecutionJournal(raw, scope: scope, expectedEpoch: epoch); preconditionFailure("Revoked cache accepted execution intent") } catch {}
        let cleared = try await cache.metadata("execution-setup:" + scope.key)
        precondition(cleared == nil, "Sign-out retained execution setup")
        print("PASS: Native execution recovery, assignments, stale review, ownership, sign-out and journal fences")
    }
}
