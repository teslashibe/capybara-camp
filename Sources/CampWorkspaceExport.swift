import Foundation

struct CloudWorkspaceExport: Codable, Sendable {
    var id: String; var campId: String; var sessionId: String; var ownerUserId: String; var deviceId: String
    var workspaceId: String; var workspaceRevision: Int64; var sourceEpoch: Int64; var sourceRevision: Int64
    var transcriptRevision: Int64; var permissionRevision: Int64; var kind: String; var paths: [String]
    var state: String; var resultSha256: String; var resultBytes: Int; var expiresAt: Date
}

struct WorkspaceExportJournal: Codable, Sendable {
    var key: String
    var session: CloudSession
    var kind: String
    var paths: [String]
    var export: CloudWorkspaceExport?
    var cancelling = false
}

// One resumable export per Camp. The caller schedules advance while its review
// is active; this actor owns no timer or provider process. Exporting never moves
// execution ownership. The handoff must separately verify a prepared destination.
actor CampWorkspaceExport {
    private let cache: CampCache
    private let scope: CloudScope
    private let credential: CloudCredential
    private let transport: any CampAccountTransport
    private let epoch: Int64
    private var busy = false
    private static let chunkBytes = 256 << 10

    init(cache: CampCache, scope: CloudScope, credential: CloudCredential, transport: any CampAccountTransport, epoch: Int64) throws {
        guard scope.origin == credential.origin, scope.userId == credential.actor.userId,
              [scope.campId, scope.userId, credential.actor.deviceId].allSatisfy({ UUID(uuidString: $0) != nil }) else { throw CampCloudError.invalidResponse }
        self.cache = cache; self.scope = scope; self.credential = credential; self.transport = transport; self.epoch = epoch
    }

    private func authority() async throws {
        try Task.checkCancellation()
        guard try await cache.epoch(scope) == epoch else { throw CancellationError() }
    }
    private func save(_ journal: WorkspaceExportJournal?, initial: Bool = false, discardingKey: String? = nil) async throws {
        try await authority()
        try await cache.saveWorkspaceExport(journal, scope: scope, expectedEpoch: epoch, initial:initial, discardingKey:discardingKey)
    }
    func current() async throws -> WorkspaceExportJournal? {
        try await authority()
        let raw = try await cache.metadata("workspace-export:" + scope.key)
        try await authority()
        guard let raw else { return nil }
        let j = try CampWire.decoder().decode(WorkspaceExportJournal.self, from: raw)
        guard UUID(uuidString:j.key) != nil, validSession(j.session), Self.validPaths(j.paths),
              ["inspect", "capture"].contains(j.kind), j.kind != "inspect" || j.paths.isEmpty else { throw CampCloudError.invalidResponse }
        if let value = j.export { try validate(value, journal:j) }
        return j
    }
    private func validSession(_ session: CloudSession) -> Bool {
        session.campId == scope.campId && session.ownerUserId == scope.userId &&
        UUID(uuidString:session.id) != nil && UUID(uuidString:session.executionWorkspaceId ?? "") != nil &&
        session.executionLocation == "cloud" && (session.executionEpoch ?? 0) > 0 &&
        session.revision > 0 && session.transcriptRevision >= 0 && session.permissionRevision > 0
    }
    private static func validPaths(_ paths: [String]) -> Bool {
        paths.count <= 256 && Set(paths).count == paths.count && paths.allSatisfy {
            !$0.isEmpty && $0.utf8.count <= 1024 && !$0.contains(where: { $0 == "\\" || $0 == ":" }) &&
            !$0.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) &&
            !$0.split(separator:"/", omittingEmptySubsequences:false).contains(where: { $0.isEmpty || $0 == "." || $0 == ".." })
        }
    }
    private func base(_ j: WorkspaceExportJournal) -> String { "/v1/camps/\(scope.campId)/sessions/\(j.session.id)/exports" }
    private func request<T: Decodable>(_ type: T.Type, _ method: String, _ path: String, body: Data? = nil, key: String? = nil) async throws -> T {
        try await authority()
        let raw = try await transport.data(method, path:path, body:body, key:key, token:credential.token)
        try await authority()
        return try CampWire.decoder().decode(type, from:raw)
    }
    private func validate(_ e: CloudWorkspaceExport, journal j: WorkspaceExportJournal) throws {
        guard UUID(uuidString:e.id) != nil, e.campId == scope.campId, e.ownerUserId == scope.userId,
              UUID(uuidString:e.deviceId) != nil, e.sessionId == j.session.id,
              e.workspaceId == j.session.executionWorkspaceId, e.workspaceRevision > 0,
              e.sourceEpoch == j.session.executionEpoch, e.sourceRevision == j.session.revision,
              e.transcriptRevision == j.session.transcriptRevision, e.permissionRevision == j.session.permissionRevision,
              e.kind == j.kind, e.paths == j.paths, ["pending","claimed","ready","cancelled","failed","expired"].contains(e.state),
              (0...(j.kind == "inspect" ? 8 << 20 : 46 << 20)).contains(e.resultBytes) else { throw CampCloudError.invalidResponse }
        if e.state == "ready" {
            guard e.resultBytes >= 2, e.resultSha256.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil else { throw CampCloudError.invalidResponse }
        }
        if let previous = j.export {
            guard e.id == previous.id, e.deviceId == previous.deviceId, e.workspaceRevision == previous.workspaceRevision,
                  // Camp's persisted dates have second precision; PostgreSQL
                  // responses can include fractional seconds. Never extend
                  // the stored deadline when restoring a download.
                  e.expiresAt.timeIntervalSince1970.rounded(.down) == previous.expiresAt.timeIntervalSince1970.rounded(.down),
                  previous.resultSha256.isEmpty || (e.resultSha256 == previous.resultSha256 && e.resultBytes == previous.resultBytes) else { throw CampCloudError.invalidResponse }
        }
    }

    func begin(session: CloudSession, kind: String, paths: [String] = []) async throws {
        guard !busy else { throw CampError.message("A workspace export is already being updated") }
        busy = true; defer { busy = false }
        guard validSession(session), Self.validPaths(paths), ["inspect","capture"].contains(kind), kind != "inspect" || paths.isEmpty else { throw CampCloudError.invalidResponse }
        guard try await current() == nil else { throw CampError.message("Finish or cancel the current workspace export first") }
        // Persist before the first POST so a lost create response keeps its key.
        try await save(WorkspaceExportJournal(key:UUID().uuidString.lowercased(), session:session, kind:kind, paths:paths), initial:true)
    }

    @discardableResult func advance() async throws -> WorkspaceExportJournal? {
        guard !busy else { throw CampError.message("A workspace export is already being updated") }
        busy = true; defer { busy = false }
        guard var j = try await current() else { return nil }
        let value: CloudWorkspaceExport
        if let e = j.export {
            value = try await request(CloudWorkspaceExport.self, j.cancelling ? "POST" : "GET", base(j)+"/"+e.id+(j.cancelling ? "/cancel" : ""), body:j.cancelling ? Data("{}".utf8) : nil)
        } else {
            let s = j.session
            value = try await request(CloudWorkspaceExport.self, "POST", base(j), body:CampWire.body([
                "kind":j.kind,"paths":j.paths,"expected_epoch":s.executionEpoch!,"expected_revision":s.revision,
                "expected_transcript_revision":s.transcriptRevision,"expected_permission_revision":s.permissionRevision
            ]), key:j.key)
        }
        try validate(value,journal:j)
        // Cancellation can be requested while a network request is in flight.
        j.cancelling = try await current()?.cancelling ?? j.cancelling
        j.export = value
        try await save(j)
        if j.cancelling || value.state != "ready" { return j }
        guard value.expiresAt > Date() else { throw CampError.message("This workspace export expired. Capture it again") }
        let inventory = try await cache.workspaceExportChunkInventory(scope:scope,id:value.id)
        for index in 0..<((value.resultBytes + Self.chunkBytes - 1) / Self.chunkBytes) {
            if inventory[index] == nil {
                let chunk = try await fetchChunk(j,index:index)
                guard try await current()?.cancelling == false else { return try await current() }
                try await cache.saveWorkspaceExportChunk(chunk, scope:scope, id:value.id, index:index, expectedEpoch:epoch)
                break // Bound each step to one chunk and one database write.
            }
        }
        return j
    }
    func downloadedBytes() async throws -> Int {
        guard let j = try await current(), !j.cancelling, let e = j.export else { return 0 }
        let inventory = try await cache.workspaceExportChunkInventory(scope:scope,id:e.id)
        try await authority()
        guard inventory.allSatisfy({ $0.key < (e.resultBytes + Self.chunkBytes - 1) / Self.chunkBytes && $0.value == min(Self.chunkBytes,e.resultBytes-$0.key*Self.chunkBytes) }) else { throw CampCloudError.invalidResponse }
        return inventory.values.reduce(0,+)
    }
    private struct Chunk: Decodable { var data: Data; var sha256: String }
    private func fetchChunk(_ j: WorkspaceExportJournal, index: Int) async throws -> Data {
        guard let e = j.export, e.state == "ready", e.expiresAt > Date() else { throw CampCloudError.invalidResponse }
        let value = try await request(Chunk.self,"GET",base(j)+"/"+e.id+"/chunks/\(index)")
        guard value.data.count == min(Self.chunkBytes,e.resultBytes-index*Self.chunkBytes), PublishMessage.hash(value.data) == value.sha256 else { throw CampCloudError.invalidResponse }
        return value.data
    }
    func payload() async throws -> Data? {
        guard !busy else { throw CampError.message("A workspace export is already being updated") }
        busy = true; defer { busy = false }
        return try await readPayload()
    }
    // Called only while payload() or installLocally() owns the actor's busy guard.
    private func readPayload() async throws -> Data? {
        guard let j = try await current(), !j.cancelling, let e = j.export, e.state == "ready" else { return nil }
        var bytes = Data(); bytes.reserveCapacity(e.resultBytes)
        for index in 0..<((e.resultBytes + Self.chunkBytes - 1) / Self.chunkBytes) {
            guard let part = try await cache.workspaceExportChunk(scope:scope,id:e.id,index:index) else { return nil }
            guard part.count == min(Self.chunkBytes,e.resultBytes-index*Self.chunkBytes) else { throw CampCloudError.invalidResponse }
            bytes.append(part)
        }
        guard PublishMessage.hash(bytes) == e.resultSha256 else { throw CampCloudError.invalidResponse }
        // The chunk route rechecks current source, project, Git and provider
        // authority. A metadata GET alone does not grant access to cached bytes.
        let first = try await fetchChunk(j,index:0)
        guard bytes.prefix(first.count) == first, try await current()?.cancelling == false else { throw CancellationError() }
        try await authority()
        return bytes
    }
    func requestCancellation() async throws {
        guard var j = try await current() else { return }
        j.cancelling = true
        try await save(j)
    }
    func inspection() async throws -> TransferChanges? {
        guard let j = try await current(), j.kind == "inspect", let raw = try await payload() else { return nil }
        let value = try CampWire.decoder().decode(TransferChanges.self,from:raw)
        guard value.baseCommit.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil,
              value.branch.utf8.count <= 512, value.files.count <= 4096,
              Set(value.files.map(\.path)).count == value.files.count,
              value.files.allSatisfy({ ["added","modified","deleted"].contains($0.kind) && $0.path.utf8.count <= 4096 }) else { throw CampCloudError.invalidResponse }
        return value
    }
    func review(callLocal: @Sendable (String,Data) async throws -> Data = { try await Bridge.call($0,input:$1) }) async throws -> TransferCapture? {
        guard let j = try await current(), j.kind == "capture", let raw = try await payload() else { return nil }
        let result = try await callLocal("transfer-review",raw)
        try await authority()
        guard let latest = try await current(), latest.key == j.key, !latest.cancelling else { throw CancellationError() }
        let value = try CampWire.decoder().decode(TransferCapture.self,from:result)
        let paths = value.files.map(\.path) + value.excluded.map(\.path)
        guard value.valid, value.payload == nil, value.history == nil,
              value.payloadSha256 == PublishMessage.hash(raw), paths.count == j.paths.count,
              Set(paths) == Set(j.paths) else { throw CampCloudError.invalidResponse }
        return value
    }
    func discard() async throws {
        guard !busy else { throw CampError.message("A workspace export is already being updated") }
        busy = true; defer { busy = false }
        guard let j = try await current() else { return }
        // Even an uncertain create must be reconciled and cancelled before reset.
        guard let e = j.export, ["cancelled","expired","failed"].contains(e.state) else { throw CampError.message("Cancel this workspace export before starting another") }
        try await save(nil,discardingKey:j.key)
    }

    func installLocally(history: CampTransferHistory, transferID: String, destination: String,
                        target: TransferInstallationTarget, reviewedSHA256: String,
                        callLocal: @Sendable (String,Data) async throws -> Data = { try await Bridge.call($0,input:$1) }) async throws -> TransferInstallationResult {
        guard !busy else { throw CampError.message("A workspace export is already being updated") }
        busy=true; defer { busy=false }
        guard let j=try await current(), !j.cancelling, let e=j.export,
              let raw=try await readPayload(), let archive=try await history.archive(), archive.session==j.session else { throw CampCloudError.invalidResponse }
        let review=try CampWire.decoder().decode(TransferCapture.self,from:await callLocal("transfer-review",raw))
        guard review.valid, review.history==nil, review.reviewSha256==reviewedSHA256,
              review.payloadSha256==e.resultSha256,
              review.files.count+review.excluded.count==j.paths.count,
              Set(review.files.map(\.path)+review.excluded.map(\.path))==Set(j.paths) else { throw CampCloudError.invalidResponse }
        try await authority()
        guard let before=try await current(), before.key==j.key, !before.cancelling else { throw CancellationError() }
        let result=try await CampTransferInstallation.install(scope:scope,transferID:transferID,destination:destination,
            target:target,export:e,payload:raw,review:review,history:archive,callLocal:callLocal)
        // Installation can outlive an authorization change. Do not hand its
        // receipt to the handoff caller after cancellation or scope erasure.
        _ = try await fetchChunk(j,index:0)
        try await history.verifySource(j.session)
        try await authority()
        guard let after=try await current(), after.key==j.key, !after.cancelling else { throw CancellationError() }
        return result
    }
}
