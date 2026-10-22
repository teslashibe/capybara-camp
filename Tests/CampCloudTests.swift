import Foundation
import SQLite3

@main struct CampCloudTests {
    static var assertions = 0
    static func check(_ condition: Bool, _ label: String) {
        precondition(condition, label); assertions += 1
    }
    static func rejects(_ label: String, _ body: () throws -> Void) {
        do { try body(); preconditionFailure(label) } catch { assertions += 1 }
    }
    static func session(_ scope: CloudScope, id: String = UUID().uuidString, revision: Int64 = 1, transcript: Int64 = 0) -> CloudSession {
        CloudSession(id: id, campId: scope.campId, projectId: UUID().uuidString, profileId: UUID().uuidString, provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId, ownerDeviceId: UUID().uuidString, localSessionId: UUID().uuidString, title: "Synthetic session", model: "fixture", state: "idle", stage: "Ready", shared: true, allowRequests: true, externalOwner: false, revision: revision, transcriptRevision: transcript, permissionRevision: 1, updatedAt: Date())
    }
    static func request(_ scope: CloudScope, session: CloudSession) -> CloudRequest {
        CloudRequest(id: UUID().uuidString, campId: scope.campId, sessionId: session.id, requesterUserId: scope.userId, ownerUserId: scope.userId, ownerDeviceId: session.ownerDeviceId, permissionRevision: 1, revision: 1, prompt: "Synthetic prompt", model: "fixture", effort: "high", speed: "standard", state: "started", leaseToken: "synthetic-lease", leaseUntil: Date().addingTimeInterval(60), createdAt: Date(), sources: [])
    }
    @MainActor static func streamProjectionChecks() throws {
        func page(_ seq: Int64, _ events: [[String: Any]]) throws -> CloudStreamPage {
            let data = try JSONSerialization.data(withJSONObject: ["events": events.enumerated().map { ["seq": seq + Int64($0.offset), "event": $0.element] }, "next": seq + Int64(events.count) - 1])
            return try CampWire.decoder().decode(CloudStreamPage.self, from: data)
        }
        let live = CloudLiveConversation()
        try live.apply(page(1, [["type": "delta", "text": "First "], ["type": "delta", "text": "answer"]]))
        check(live.messages.map(\.text) == ["First answer"], "cloud deltas appear before completion")
        try live.apply(page(3, [["type": "boundary"], ["type": "delta", "text": "Second answer"]]))
        check(live.messages.map(\.text) == ["First answer", "Second answer"], "steered response keeps earlier output")
        rejects("stream gap accepted") { try live.apply(page(8, [["type": "done"]])) }
        let approval = ProviderApproval(id: UUID().uuidString, kind: "command", title: "Run command", detail: "echo fixture", questions: [], canApprove: true, acceptLabel: "Allow")
        var event = try JSONSerialization.jsonObject(with: JSONEncoder().encode(approval)) as! [String: Any]
        event["type"] = "approval"
        try live.apply(page(5, [event]))
        check(live.approvals == [approval], "cloud reuses provider approval shape")
        try live.apply(page(6, [["type": "approvalResolved", "requestID": approval.id]]))
        check(live.approvals.isEmpty, "resolved cloud approval removed")
        live.stop()
        check(live.messages.isEmpty && live.approvals.isEmpty && live.notice.isEmpty, "changing Camp clears streamed content")
    }
    @MainActor static func main() async throws {
        try streamProjectionChecks()
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-cloud-tests-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let endpoint = try CampEndpoint(" https://Camp.Example/ ")
        check(endpoint.origin == "https://camp.example", "canonical origin")
        check(try endpoint.requestURL("/v1/camps?q=hello%20world").absoluteString == "https://camp.example/v1/camps?q=hello%20world", "query encoding")
        for invalid in ["http://camp.example", "https://user:pass@camp.example", "https://camp.example/path", "https://camp.example?q=x", "https://camp.example/#x", "file:///tmp", "http://127.0.0.1:8080"] {
            rejects("untrusted endpoint accepted") { _ = try CampEndpoint(invalid) }
        }
        check(try CampEndpoint("http://127.0.0.1:8080", allowLoopback: true).origin == "http://127.0.0.1:8080", "explicit development loopback")
        rejects("remote cleartext with development enabled") { _ = try CampEndpoint("http://camp.example", allowLoopback: true) }
        for invalid in ["https://attacker.example/v1", "//attacker.example/v1", "/v1/../secret", "/v1/%2e%2e/secret", "/v1/./secret", "/v1\\secret", "/v1#fragment"] {
            rejects("unsafe relative path accepted") { _ = try endpoint.requestURL(invalid) }
        }
        let scope = CloudScope(origin: endpoint.origin, userId: UUID().uuidString, campId: UUID().uuidString)
        let otherUser = CloudScope(origin: endpoint.origin, userId: UUID().uuidString, campId: scope.campId)
        let otherOrigin = CloudScope(origin: "https://other.example", userId: scope.userId, campId: scope.campId)
        check(Set([scope.key, otherUser.key, otherOrigin.key]).count == 3, "scope isolation")
        let loginJSON = Data("""
        {"actor":{"user_id":"u","device_id":"d","email":"fixture@example.com","name":"Fixture"},"token":"synthetic","expires_at":"2026-10-21T10:20:30.123456789Z"}
        """.utf8)
        let login = try CampWire.decoder().decode(CloudLogin.self, from: loginJSON)
        check(login.actor.userId == "u" && login.expiresAt.timeIntervalSince1970 > 0, "server field and date decoding")
        let vault = MemoryCampCredentials()
        let credential = CloudCredential(origin: scope.origin, actor: login.actor, token: "synthetic", expiresAt: login.expiresAt)
        try vault.save(credential)
        check(try vault.read(origin: scope.origin)?.token == "synthetic", "credential saved")
        check(try vault.read(origin: otherOrigin.origin) == nil, "credentials bound to origin")
        try vault.remove(origin: scope.origin)
        check(try vault.read(origin: scope.origin) == nil, "credential removed")

        let cache = try CampCache(directory: root)
        let notice = CloudNotification(id: UUID().uuidString, kind: "finished", title: "Synthetic result", sessionId: UUID().uuidString, requestId: UUID().uuidString, revision: 1, read: false, createdAt: Date(timeIntervalSince1970: floor(Date().timeIntervalSince1970)))
        try await cache.saveNotifications([notice], scope: scope, expectedEpoch: 0)
        check(try await cache.notifications(scope) == [notice], "notification inbox persists")
        check(try await cache.notifications(otherUser).isEmpty, "notification recipient isolation")
        do { try await cache.saveNotifications([notice, notice], scope: scope, expectedEpoch: 0); preconditionFailure("duplicate notification accepted") }
        catch { assertions += 1 }
        let plan = CloudPlanningResult(id: UUID().uuidString, title: "Shared plan", projectId: UUID().uuidString,
            version: .init(version: 1, state: "ready", brief: "Test plan", planners: [
                .init(slot: 0, sessionId: UUID().uuidString, provider: "Codex", model: "fixture", effort: "high", speed: "standard"),
                .init(slot: 1, sessionId: UUID().uuidString, provider: "Claude", model: "fixture", effort: "high", speed: "fast")
            ], steps: [.init(ordinal: 4, slot: 0, phase: "merge", state: "completed", output: "Combined plan")], sources: []))
        let decodedPlan = try CampWire.decoder().decode(CloudPlanningResult.self, from: CampWire.encoder().encode(plan))
        check(decodedPlan.valid && decodedPlan.referencedSessions.count == 2, "shared plan uses server ordinal and phase contract")
        var value = session(scope)
        try await cache.ingest(scope: scope, cursor: 1, session: value)
        check(try await cache.cursor(scope) == 1, "cursor persisted")
        check(try await cache.sessions(otherUser).isEmpty, "other user cannot read cache")
        value.revision = 2; value.title = "Updated"
        try await cache.ingest(scope: scope, cursor: 2, session: value)
        var stale = value; stale.revision = 1; stale.title = "Stale"
        try await cache.ingest(scope: scope, cursor: 3, session: stale)
        check(try await cache.sessions(scope).first?.title == "Updated", "stale session cannot roll back")
        value.transcriptRevision = 3
        try await cache.ingest(scope: scope, session: value)
        stale = value; stale.transcriptRevision = 2
        try await cache.ingest(scope: scope, session: stale)
        check(try await cache.sessions(scope).first?.transcriptRevision == 3, "transcript revision monotonic")
        value.activityRevision = 3; value.state = "running"; value.stage = "In progress"
        try await cache.ingest(scope: scope, session: value)
        stale = value; stale.activityRevision = 2; stale.state = "idle"; stale.stage = "Ready"
        check(!stale.canReplace(value), "late idle response cannot replace running activity")
        try await cache.ingest(scope: scope, session: stale)
        check(try await cache.sessions(scope).first?.state == "running", "cached activity revision never regresses")
        value.activityRevision = 4; value.state = "idle"; value.stage = "Ready"
        try await cache.ingest(scope: scope, session: value)
        try await cache.ingest(scope: scope, cursor: 3, removed: (value.id, 4))
        check(try await cache.sessions(scope).count == 1, "duplicate event ignored atomically")
        try await cache.ingest(scope: scope, cursor: 4, removed: (value.id, 4))
        try await cache.ingest(scope: scope, cursor: 5, session: value)
        check(try await cache.sessions(scope).isEmpty, "tombstone prevents stale resurrection")
        value.revision = 5
        try await cache.ingest(scope: scope, cursor: 6, session: value)
        check(try await cache.sessions(scope).count == 1, "new explicit share can restore session")
        do { try await cache.replaceSessions([], scope: scope, cursor: 1); preconditionFailure("server history regressed") }
        catch { assertions += 1 }
        check(try await cache.sessions(scope).count == 1, "failed snapshot leaves records intact")

        let first = CloudMessage(id: "first", ordinal: 0, role: "user", text: "Question", removed: false, revision: 1)
        let second = CloudMessage(id: "second", ordinal: 1, role: "assistant", text: "Answer", removed: false, revision: 1)
        try await cache.saveTranscript(CloudTranscript(revision: 1, messages: [first, second], before: 0, hasMore: false), scope: scope, sessionId: value.id)
        check(try await cache.transcript(scope, sessionId: value.id).count == 2, "transcript saved")
        var amended = second; amended.revision = 2; amended.text = "Amended"
        try await cache.saveTranscript(CloudTranscript(revision: 2, messages: [amended], before: 1, hasMore: false), scope: scope, sessionId: value.id)
        check(try await cache.transcript(scope, sessionId: value.id).map(\.id) == ["second"], "server deletion removes cached message")
        try await cache.saveTranscript(CloudTranscript(revision: 1, messages: [second], before: 1, hasMore: false), scope: scope, sessionId: value.id)
        check(try await cache.transcript(scope, sessionId: value.id).first?.text == "Amended", "pinned older page cannot replace newer text")
        check(try await cache.transcript(otherUser, sessionId: value.id).isEmpty, "transcript tenancy")
        var work = request(scope, session: value)
        try await cache.ingest(scope: scope, request: work)
        check(try await cache.requests(scope).first?.leaseToken == nil, "UI cache does not store execution lease")
        work.revision = 0; work.prompt = "Stale"
        try await cache.ingest(scope: scope, request: work)
        check(try await cache.requests(scope).first?.prompt == "Synthetic prompt", "request revision monotonic")
        var revoked = value; revoked.revision += 1; revoked.permissionRevision += 1
        try await cache.ingest(scope: scope, session: revoked)
        do {
            try await cache.saveTranscript(CloudTranscript(revision: 3, messages: [second], before: 0, hasMore: false), scope: scope, sessionId: value.id, expectedPermissionRevision: value.permissionRevision)
            preconditionFailure("stale permission transcript accepted")
        } catch { assertions += 1 }

        let path = "/v1/camps/\(scope.campId)/sessions/\(value.id)/requests"
        let key = try await cache.enqueue(scope: scope, method: "POST", path: path, body: Data("{}".utf8))
        try await cache.markOutbox(key, state: "sending")
        let attempt = CloudAttempt(id: work.id, scope: scope.key, request: work, localSessionId: "fixture", state: "running", startKey: UUID().uuidString, finishKey: UUID().uuidString)
        try await cache.saveAttempt(attempt)
        let recovered = try CampCache(directory: root)
        check(try await recovered.outbox(scope).first?.state == "pending", "transport retry keeps original key")
        check(try await recovered.outbox(scope).first?.id == key, "idempotency key preserved across restart")
        check(try await recovered.attempts(scope).first?.state == "uncertain", "provider work never automatically repeats after restart")
        check(try await recovered.attempts(scope).first?.request.leaseToken == "synthetic-lease", "execution fence retained for reconciliation")
        do { _ = try await cache.enqueue(scope: scope, method: "POST", path: "/v1/camps/\(scope.campId)/../../other", body: Data()); preconditionFailure("escaping path queued") }
        catch { assertions += 1 }
        do { _ = try await cache.enqueue(scope: otherUser, method: "POST", path: "/v1/camps/\(UUID().uuidString)/requests", body: Data()); preconditionFailure("foreign Camp queued") }
        catch { assertions += 1 }
        try await cache.replaceSessions([], scope: scope, cursor: 7)
        check(try await cache.transcript(scope, sessionId: value.id).isEmpty, "snapshot removal purges transcript")
        do {
            try await cache.saveTranscript(CloudTranscript(revision: 3, messages: [second], before: 0, hasMore: false), scope: scope, sessionId: value.id)
            preconditionFailure("late response restored revoked transcript")
        } catch { assertions += 1 }
        try await cache.clear(scope)
        let nextEpoch = try await cache.epoch(scope)
        check(nextEpoch == 1, "revocation increments durable cache epoch")
        check(try await cache.notifications(scope).isEmpty, "revocation clears notification content")
        do { try await cache.saveNotifications([notice], scope: scope, expectedEpoch: 0); preconditionFailure("stale response restored notification content") }
        catch { assertions += 1 }
        do { try await cache.ingest(scope: scope, session: value, expectedEpoch: 0); preconditionFailure("revoked response restored cached session") }
        catch { assertions += 1 }
        do { _ = try await cache.enqueue(scope: scope, method: "POST", path: path, body: Data(), expectedEpoch: 0); preconditionFailure("revoked context queued a command") }
        catch { assertions += 1 }
        do { try await cache.replaceSessions([value], scope: scope, cursor: 20, expectedEpoch: 0); preconditionFailure("late snapshot restored revoked cache") }
        catch { assertions += 1 }
        check(try await cache.outbox(scope).isEmpty, "revocation clears queued prompts")
        check(try await cache.attempts(scope).first?.request.prompt == "", "revocation scrubs retained reconciliation attempt")
        let permissions = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("camp-cache.sqlite").path)
        check(permissions[.posixPermissions] as? Int == 0o600, "cache owner-only permissions")

        let store = CampStore(stateDirectory: root.appendingPathComponent("ui"))
        let locals = (0..<15).map { index in CampSession(id: "fixture-\(index)", sessionID: "fixture", provider: index % 2 == 0 ? "Codex" : "Claude", sourceID: "fixture", sourceLabel: "Fixture", root: "/synthetic", title: "Fixture", cwd: "/synthetic/project", updated: Double(index), path: "", accountID: "", archived: false) }
        store.sessions = locals
        check(store.recentSessions.count == 10 && store.recentSessions.first?.id == "fixture-14", "ten most recent sessions")
        store.sourceFilter = "Codex"; store.setRecentType("Claude")
        check(store.recentSessions.count == 7 && store.recentSessions.allSatisfy { $0.provider == "Claude" }, "recent filter independent of board")
        store.applyCloud([value], projects: [])
        check(store.sessions.contains { $0.isCloud }, "shared sessions merged with local sessions")
        store.applyImport(ImportResult(sessions: locals, sources: [], errors: []))
        check(store.sessions.contains { $0.isCloud }, "local import preserves cloud sessions")
        store.selectedID = "camp:" + value.id
        store.applyCloud([], projects: [])
        check(store.selectedID == nil && store.messages.isEmpty, "revocation closes active cloud transcript")
        let indexDirectory = root.appendingPathComponent("index")
        try FileManager.default.createDirectory(at: indexDirectory, withIntermediateDirectories: true)
        await LocalSessionIndex.shared.save(ImportResult(sessions: locals, sources: [], errors: []), directory: indexDirectory)
        let warm = CampStore(stateDirectory: indexDirectory)
        check(warm.sessions == locals && warm.selectedID == locals.first?.id, "warm launch uses saved local index")
        let indexPermissions = try FileManager.default.attributesOfItem(atPath: indexDirectory.appendingPathComponent("session-index.json").path)
        check(indexPermissions[.posixPermissions] as? Int == 0o600, "local index owner-only permissions")
        print("\(assertions) native cloud checks passed")
    }
}
