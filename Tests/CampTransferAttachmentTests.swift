import Foundation

private actor TransferAttachmentFixture: CampAccountTransport {
    let session: CloudSession
    let child: CloudChildHistory
    let bytes = Data(repeating: 255, count: 16 << 20)
    var mode = ""
    var waiting = false
    var reads = 0
    init(_ session: CloudSession) {
        self.session = session
        child = CloudChildHistory(id: UUID().uuidString, parentId: session.id,
            sourceKey: String(repeating: "a", count: 64), sourceRevision: String(repeating: "b", count: 64),
            name: "Child", phase: "review", workflow: "Plan", state: "done", model: "fixture",
            complete: true, removed: false, revision: 2, transcriptRevision: 1)
    }
    func configure(_ mode: String) { self.mode = mode; waiting = false }
    func status() -> (Bool, Int) { (waiting, reads) }
    func reference(_ id: String = "a:0") -> TranscriptAttachment {
        TranscriptAttachment(id: id, kind: "document", source: "inline", sourceDigest: String(repeating: "c", count: 64),
            state: "embedded", mediaType: "application/octet-stream", sha256: PublishMessage.hash(bytes), size: bytes.count)
    }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        precondition(token == "fixture" && key == nil)
        let encoder = CampWire.encoder(); encoder.outputFormatting.insert(.withoutEscapingSlashes)
        if path.hasSuffix("/attachment") {
            precondition(method == "POST")
            let input = try JSONSerialization.jsonObject(with: body!) as! [String: Any]
            precondition(input["revision"] as? Int == 3)
            reads += 1
            if mode == "lost" { mode = ""; throw URLError(.networkConnectionLost) }
            if mode == "delay" { waiting = true; try await Task.sleep(for: .milliseconds(250)) }
            let isChild = input["child_id"] != nil
            precondition(input["message_id"] as? String == (isChild ? "child" : "main"))
            if mode == "revoke-after-read" { mode = "revoked" }
            struct Response: Encodable {
                var sessionId: String; var messageId: String; var revision: Int64; var permissionRevision: Int64
                var attachment: TranscriptAttachment; var data: Data
            }
            return try encoder.encode(Response(sessionId: isChild ? child.id : session.id,
                messageId: isChild ? "child" : "main", revision: 3, permissionRevision: 2,
                attachment: reference(), data: bytes))
        }
        precondition(method == "GET" && body == nil)
        if path.contains("/children?") {
            return try encoder.encode(CloudChildHistoryList(parentRevision: 3, permissionRevision: 2, children: [child]))
        }
        if path.contains("/transcript?") {
            let isChild = path.contains("/children/")
            var refs = [reference(), reference("a:1"), TranscriptAttachment(id: "a:2", kind: "image", source: "remote",
                sourceDigest: String(repeating: "d", count: 64), state: "reference")]
            if mode == "over-limit" {
                refs = (0..<9).map { i in
                    var ref = reference("a:\(i)")
                    ref.sha256 = String(format: "%064x", i)
                    return ref
                }
            }
            let message = CloudMessage(id: isChild ? "child" : "main", ordinal: 0, role: "user", text: "With attachments",
                removed: false, revision: 1, attachments: refs)
            let page = CloudTranscript(revision: isChild ? 1 : 3, messages: [message], before: 0, hasMore: false)
            if isChild {
                return try encoder.encode(CloudChildTranscript(parentRevision: 3, permissionRevision: 2, child: child, transcript: page))
            }
            return try encoder.encode(page)
        }
        precondition(path.hasSuffix(session.id))
        if mode == "revoked" { throw CampCloudError.server(403, "denied") }
        return try encoder.encode(session)
    }
}

@main struct CampTransferAttachmentTests {
    static func rejects(_ label: String, _ action: () async throws -> Void) async {
        do { try await action(); preconditionFailure(label) } catch {}
    }
    static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("transfer-attachments-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let credential = CloudCredential(origin: scope.origin, actor: CloudActor(userId: scope.userId,
            deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture"), token: "fixture", expiresAt: Date().addingTimeInterval(3600))
        let session = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: UUID().uuidString,
            profileId: UUID().uuidString, provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId,
            ownerDeviceId: credential.actor.deviceId, localSessionId: UUID().uuidString, title: "Attachments", model: "fixture",
            state: "idle", stage: "Ready", shared: false, allowRequests: false, externalOwner: false,
            revision: 4, transcriptRevision: 3, permissionRevision: 2, updatedAt: Date(), executionLocation: "cloud",
            executionEpoch: 2, executionWorkspaceId: UUID().uuidString)
        let fixture = TransferAttachmentFixture(session)
        let cache = try CampCache(directory: root)
        func client() async throws -> CampTransferHistory {
            try CampTransferHistory(cache: cache, scope: scope, credential: credential, transport: fixture, epoch: await cache.epoch(scope))
        }
        let first = try await client()
        try await first.begin(session: session)
        try await first.advance()
        let pending = try await first.current()!
        precondition(!pending.complete && pending.attachments?.count == 1)
        await fixture.configure("lost")
        await rejects("Lost download committed progress") { try await first.advance() }
        let afterLoss = try await first.current()!
        precondition(afterLoss.downloadedAttachments == nil)
        let resumed = try await client()
        try await resumed.advance() // Parent body, resumed after the lost response.
        try await resumed.advance() // Child page.
        let partial = try await resumed.archive(); precondition(partial == nil)
        try await resumed.advance() // Same bytes, separately bound to the child.
        let archive = try await resumed.archive()!
        precondition(archive.version == 2 && archive.attachments?.count == 2)
        precondition(archive.conversations.allSatisfy { $0.messages[0].attachments?.count == 3 })
        let archiveSize = try encoderSize(archive)
        precondition(archiveSize < 16000, "Raw bodies leaked into the metadata archive")
        let completed = try await resumed.current()!
        for item in archive.attachments! {
            let body = try await resumed.attachmentBytes(item)
            precondition(body.count == 16 << 20 && PublishMessage.hash(body) == item.attachment.sha256)
        }
        let readCount = await fixture.status().1
        precondition(readCount == 3, "Duplicate descriptors caused extra physical reads")
        let staging = try await CampTransferInstallation.stageAttachments(archive.attachments!, read: { try await resumed.attachmentBytes($0) }, root: root)!
        let permissions = try FileManager.default.attributesOfItem(atPath: staging.path)[.posixPermissions] as! NSNumber
        precondition(permissions.intValue == 0o700)
        for item in archive.attachments! {
            let file = staging.appendingPathComponent("attachment-" + item.streamId + "-" + item.attachment.sha256!)
            let bytes = try Data(contentsOf: file)
            precondition(bytes.count == item.attachment.size && PublishMessage.hash(bytes) == item.attachment.sha256)
            let mode = try FileManager.default.attributesOfItem(atPath: file.path)[.posixPermissions] as! NSNumber
            precondition(mode.intValue == 0o600)
        }
        try FileManager.default.removeItem(at: staging)
        await rejects("Corrupt staging body accepted") {
            _ = try await CampTransferInstallation.stageAttachments(archive.attachments!, read: { _ in Data([1]) }, root: root)
        }
        let leftovers = try FileManager.default.contentsOfDirectory(atPath: root.path).filter { $0.hasPrefix("CapybaraTransfer-") }
        precondition(leftovers.isEmpty, "Failed staging retained temporary bodies")
        await fixture.configure("revoked")
        await rejects("Cached body bypassed revoked source") { _ = try await resumed.attachmentBytes(archive.attachments![0]) }
        await fixture.configure("")
        let item = archive.attachments![0]
        let blobKey = "transfer-history-attachment:" + scope.key + ":" + completed.id + ":" + item.key
        try await cache.setMetadata(blobKey, Data(repeating: 0, count: 16 << 20))
        await rejects("Corrupt body entered complete archive") { _ = try await resumed.archive() }
        try await resumed.discard()
        let erased = try await cache.metadata(blobKey); precondition(erased == nil)
        await rejects("Stale byte completion resurrected discarded history") {
            try await cache.saveTransferHistoryAttachment(completed, scope: scope, expectedEpoch: await cache.epoch(scope),
                previousRevision: completed.revision - 1, bytes: Data())
        }
        let excessive = try await client()
        try await excessive.begin(session: session)
        await fixture.configure("over-limit")
        await rejects("Accepted attachment bytes above session limit") { try await excessive.advance() }
        let unchanged = try await excessive.current()!
        precondition(unchanged.streams[0].pages == 0)
        try await excessive.discard()
        await fixture.configure("")
        let revoked = try await client()
        try await revoked.begin(session: session); try await revoked.advance()
        await fixture.configure("revoke-after-read")
        await rejects("Revocation during read committed bytes") { try await revoked.advance() }
        await fixture.configure("")
        let beforeClear = try await revoked.current()!
        precondition(beforeClear.downloadedAttachments == nil)
        await fixture.configure("delay")
        let running = Task { try await revoked.advance() }
        for _ in 0..<500 {
            if await fixture.status().0 { break }
            try await Task.sleep(for: .milliseconds(1))
        }
        let waiting = await fixture.status().0; precondition(waiting)
        try await cache.clear(scope)
        await rejects("Cleared scope accepted in-flight attachment") { _ = try await running.value }
        let missing = try await cache.transferHistoryAttachment(scope: scope, id: beforeClear.id, item: beforeClear.attachments![0])
        precondition(missing == nil)
        print("Transfer attachments passed: 16 MiB parent/child bodies, bounded archives, deduplication, restart, corruption, limits, revocation and cache erasure")
    }
    static func encoderSize(_ archive: TransferHistoryArchive) throws -> Int { try CampWire.encoder().encode(archive).count }
}
