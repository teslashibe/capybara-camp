import Foundation

struct CloudHandoff: Codable, Sendable {
    var id: String; var transferId: String; var campId: String; var sessionId: String; var ownerUserId: String
    var sourceDeviceId: String; var sourceLocation: String; var sourceWorkspaceId: String?
    var sourceEpoch: Int64; var workspaceId: String; var workspaceRevision: Int64
    var state: String; var claimId: String?; var revision: Int64; var expiresAt: Date
    var commitNonce: String?; var executionEpoch: Int64?
}
struct CloudTransferSource: Codable, Sendable {
    struct Transfer: Codable, Sendable {
        var id: String; var campId: String; var sessionId: String; var ownerUserId: String
        var sourceLocation: String; var sourceEpoch: Int64; var sourceRevision: Int64
        var sourceTranscriptRevision: Int64; var sourcePermissionRevision: Int64
        var payloadSha256: String; var reviewSha256: String; var payloadBytes: Int; var uploadedBytes: Int; var chunkBytes: Int; var state: String
    }
    var handoff: CloudHandoff; var transfer: Transfer; var session: CloudSession
}

// Only the original source Mac consumes these jobs. Its durable claim excludes
// credentials and payloads; reviewed files stay in memory for the live guard.
@MainActor final class CampLocalHandoff {
    private struct Journal: Codable {
        var id: String; var claim: String; var device: String
        var local: Data?; var handoff: CloudHandoff?; var commitStarted: Bool?
    }
    private struct Context {
        var scope: CloudScope; var actor: CloudActor; var transport: any CampPublishingTransport; var generation = UUID()
    }
    private let cache: CampCache?
    private var context: Context?
    private var journal: Journal?
    private var loaded = false
    private var guardProcess: (any LocalSourceGuard)?
    private var source: CloudTransferSource?
    private var epoch: Int64?
    private var nextDiscovery = Date.distantPast
    var nativeSession: ((CloudSession) -> CampSession?)?
    var available: ((CampSession) -> Bool)?
    var acceptSession: ((CloudSession, CloudScope, Int64) async throws -> Void)?
    var makeGuard: (CampSession, Data, Date, String) -> any LocalSourceGuard = { LocalTransferHold(session: $0, payload: $1, deadline: $2, handoffID:$3) }
    var resolveFence: (CampSession, String) async throws -> Void = { session, handoffID in
        let body = try CampWire.body(["session": JSONSerialization.jsonObject(with:JSONEncoder().encode(session)), "handoffID":handoffID])
        struct Result: Decodable { var ok: Bool }
        let result = try JSONDecoder().decode(Result.self,from:await Bridge.call("transfer-resolve",input:body))
        guard result.ok else { throw CampCloudError.invalidResponse }
    }
    private(set) var status = ""

    init(cache: CampCache?) { self.cache = cache }
    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?) {
        if let c = context, c.scope == scope, c.actor.deviceId == actor?.deviceId, let transport { context?.transport = transport; return }
        guardProcess?.stop(); guardProcess = nil; source = nil; journal = nil; loaded = false; epoch = nil; status = ""; context = nil
        nextDiscovery = .distantPast
        if let scope, let actor, let transport { context = Context(scope: scope, actor: actor, transport: transport) }
    }
    func wake() { nextDiscovery = .distantPast }
    private func check(_ c: Context) throws {
        guard context?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func checkAuthority(_ c: Context) async throws {
        try check(c)
        guard let cache, let epoch, try await cache.epoch(c.scope) == epoch else {
            if context?.generation == c.generation { guardProcess?.stop() }
            throw CancellationError()
        }
        try check(c)
    }
    private func call<T: Decodable>(_ type: T.Type, _ method: String, _ path: String, _ c: Context, body: Data = Data()) async throws -> T {
        try await checkAuthority(c)
        let raw = try await c.transport.perform(CampPublication(kind: "handoff", method: method, path: path, body: body))
        try await checkAuthority(c)
        return try CampWire.decoder().decode(type, from: raw)
    }
    private func save(_ value: Journal?, _ c: Context) async throws {
        guard let cache, let epoch else { throw CampCloudError.cacheUnavailable }
        try await cache.saveHandoffJournal(try CampWire.encoder().encode(value), scope: c.scope, expectedEpoch: epoch)
        try check(c); journal = value
    }
    private func clear(_ c: Context) async throws {
        guardProcess?.stop(); guardProcess = nil; source = nil
        try await save(nil, c); status = ""
    }
    private func validate(_ h: CloudHandoff, _ j: Journal, _ c: Context, terminal: Bool = false) throws {
        guard [h.id,h.transferId,h.sessionId,h.workspaceId].allSatisfy({ UUID(uuidString: $0) != nil }),
              h.id == j.id, h.campId == c.scope.campId, h.ownerUserId == c.actor.userId,
              h.sourceDeviceId == c.actor.deviceId, h.sourceLocation == "local", h.sourceWorkspaceId == nil,
              h.claimId == j.claim, h.sourceEpoch > 0, h.workspaceRevision > 0, h.revision > 0,
              ["claimed","acknowledged","committed"].contains(h.state) || (terminal && ["released","expired","invalidated"].contains(h.state)) else { throw CampCloudError.invalidResponse }
        if terminal && ["released","expired","invalidated"].contains(h.state) { return }
        if h.state == "committed" {
            guard h.executionEpoch == h.sourceEpoch + 1, UUID(uuidString:h.commitNonce ?? "") != nil else { throw CampCloudError.invalidResponse }
        } else {
            guard h.expiresAt > Date(), h.expiresAt.timeIntervalSinceNow <= 301 else { throw CampCloudError.invalidResponse }
        }
    }
    private func reconcileCancelled(_ h: CloudHandoff, _ j: Journal, _ c: Context) async throws {
        try validate(h,j,c,terminal:true)
        guard ["released","expired","invalidated"].contains(h.state), let localData = j.local else { throw CampCloudError.invalidResponse }
        let local = try JSONDecoder().decode(CampSession.self,from:localData)
        let value = try await call(CloudSession.self,"GET","/v1/camps/\(c.scope.campId)/sessions/\(h.sessionId)",c)
        guard value.id == h.sessionId, value.campId == c.scope.campId, value.ownerUserId == c.actor.userId,
              value.executionEpoch == h.sourceEpoch, value.executionLocation == "local", value.executionWorkspaceId == nil else { throw CampCloudError.invalidResponse }
        guardProcess?.stop(); guardProcess = nil
        try await resolveFence(local,j.id)
        try await checkAuthority(c)
        try await clear(c)
    }
    private func acceptCommitted(_ h: CloudHandoff, _ c: Context) async throws {
        guard let epoch, let acceptSession, let completedEpoch = h.executionEpoch else { throw CampCloudError.invalidResponse }
        let value = try await call(CloudSession.self,"GET","/v1/camps/\(c.scope.campId)/sessions/\(h.sessionId)",c)
        guard value.id == h.sessionId, value.campId == c.scope.campId, value.ownerUserId == c.actor.userId,
              let currentEpoch = value.executionEpoch, currentEpoch >= completedEpoch,
              currentEpoch != completedEpoch || (value.executionLocation == "cloud" && value.executionWorkspaceId == h.workspaceId) else { throw CampCloudError.invalidResponse }
        // Publish the new routing synchronously before releasing the local lock.
        try await acceptSession(value,c.scope,epoch)
        try await checkAuthority(c)
        try await clear(c)
    }
    private func prepare(_ h: CloudHandoff, _ j: Journal, _ c: Context) async throws {
        let body = try CampWire.body(["claim_id":j.claim]), base = "/v1/handoffs/" + j.id
        let value = try await call(CloudTransferSource.self,"POST",base+"/source",c,body:body)
        try validate(value.handoff,j,c)
        let t = value.transfer, s = value.session
        guard value.handoff.transferId == h.transferId, t.id == h.transferId, t.campId == c.scope.campId,
              t.sessionId == h.sessionId, t.ownerUserId == c.actor.userId, t.sourceLocation == "local",
              t.sourceEpoch == h.sourceEpoch, s.executionEpoch == h.sourceEpoch, s.executionLocation == "local", s.executionWorkspaceId == nil,
              s.id == h.sessionId, s.campId == c.scope.campId, s.ownerUserId == c.actor.userId, s.ownerDeviceId == c.actor.deviceId,
              !s.externalOwner, s.revision == t.sourceRevision, s.transcriptRevision == t.sourceTranscriptRevision, s.permissionRevision == t.sourcePermissionRevision,
              t.state == "ready", (2...(46 << 20)).contains(t.payloadBytes), t.payloadBytes == t.uploadedBytes, t.chunkBytes == 256 << 10,
              [t.payloadSha256,t.reviewSha256].allSatisfy({ $0.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil }),
              let local = nativeSession?(s), local.provider == s.provider, local.sessionID == s.localSessionId,
              available?(local) == true else { throw CampCloudError.invalidResponse }
        struct Chunk: Decodable { var data: Data; var sha256: String }
        var payload = Data(); payload.reserveCapacity(t.payloadBytes)
        while payload.count < t.payloadBytes {
            let chunk = try await call(Chunk.self,"POST",base+"/chunks/\(payload.count/t.chunkBytes)",c,body:body)
            guard chunk.data.count == min(t.chunkBytes,t.payloadBytes-payload.count), PublishMessage.hash(chunk.data) == chunk.sha256 else { throw CampCloudError.invalidResponse }
            payload.append(chunk.data)
        }
        guard PublishMessage.hash(payload) == t.payloadSha256 else { throw CampCloudError.invalidResponse }
        try check(c)
        var saved = j; saved.local = try JSONEncoder().encode(local); saved.handoff = h
        try await save(saved,c)
        let guardProcess = makeGuard(local,payload,h.expiresAt,h.id)
        self.guardProcess?.stop(); self.guardProcess = guardProcess; source = value
    }

    // Returns true while a handoff owns this worker's admission slot.
    func poll() async throws -> Bool {
        guard let c = context, let cache else { return false }
        do {
            if !loaded {
                epoch = try await cache.epoch(c.scope)
                let raw = try await cache.metadata("source-handoff:"+c.scope.key); try check(c)
                journal = try raw.map { try CampWire.decoder().decode(Journal?.self,from:$0) } ?? nil
                if let j = journal {
                    guard UUID(uuidString:j.id) != nil, UUID(uuidString:j.claim) != nil else { throw CampCloudError.invalidResponse }
                    if j.device != c.actor.deviceId { try await clear(c) }
                }
                loaded = true
            }
            guard try await cache.epoch(c.scope) == epoch else { guardProcess?.stop(); throw CancellationError() }
            if journal == nil {
                guard Date() >= nextDiscovery else { return false }
                nextDiscovery = Date().addingTimeInterval(10)
                struct Next: Decodable { var id: String }
                let raw = try await c.transport.perform(CampPublication(kind:"handoff",method:"GET",path:"/v1/camps/\(c.scope.campId)/handoffs/next",body:Data()))
                try await checkAuthority(c)
                if raw.isEmpty || raw == Data("null".utf8) { return false }
                let next = try CampWire.decoder().decode(Next.self,from:raw)
                guard UUID(uuidString:next.id) != nil else { throw CampCloudError.invalidResponse }
                try await save(Journal(id:next.id,claim:UUID().uuidString.lowercased(),device:c.actor.deviceId),c)
            }
            guard let j = journal else { return false }
            let body = try CampWire.body(["claim_id":j.claim]), base = "/v1/handoffs/"+j.id
            if j.commitStarted == true, let saved = j.handoff {
                let status = try await call(CloudHandoff.self,"GET","/v1/camps/\(c.scope.campId)/sessions/\(saved.sessionId)/handoffs/\(j.id)",c)
                try validate(status,j,c,terminal:true)
                if status.state == "committed" { try await acceptCommitted(status,c); return true }
                if ["released","expired","invalidated"].contains(status.state) { try await reconcileCancelled(status,j,c); return true }
            }
            let h = try await call(CloudHandoff.self,"POST",base+"/claim",c,body:body)
            try validate(h,j,c)
            if h.state == "committed" { try await acceptCommitted(h,c); return true }
            if guardProcess?.active != true { try await prepare(h,j,c) }
            guard let guardProcess, let source else { throw CampCloudError.invalidResponse }
            if h.commitNonce != nil {
                guard var saved = journal, saved.local != nil else { throw CampCloudError.invalidResponse }
                saved.handoff = h; saved.commitStarted = true
                try await save(saved,c)
            }
            let receipt: SourceVerification
            do {
                if h.commitNonce == nil { receipt = try await guardProcess.verify() }
                else { receipt = try await guardProcess.prepareCommit() }
                try check(c)
            }
            catch {
                guard context?.generation == c.generation else { guardProcess.stop(); throw CancellationError() }
                self.guardProcess?.stop(); self.guardProcess = nil
                // A definitive local verification failure invalidates this
                // source claim. Cancellation or connection loss retains it.
                if !(error is CancellationError) {
                    let rejected = try await call(CloudHandoff.self,"POST",base+"/reject",c,body:body)
                    if let saved = journal, saved.commitStarted == true { try await reconcileCancelled(rejected,saved,c) }
                    else { try await clear(c) }
                }
                throw error
            }
            guard guardProcess.active, receipt.payloadSHA256 == source.transfer.payloadSha256, receipt.reviewSHA256 == source.transfer.reviewSha256 else { throw CampCloudError.invalidResponse }
            if let nonce = h.commitNonce {
                guard h.state == "acknowledged", UUID(uuidString:nonce) != nil else { throw CampCloudError.invalidResponse }
                let proof = try CampWire.body(["claim_id":j.claim,"stopped":true,"payload_sha256":receipt.payloadSHA256,"review_sha256":receipt.reviewSHA256,"commit_nonce":nonce])
                let result = try await call(CloudHandoff.self,"POST",base+"/finish",c,body:proof)
                try validate(result,j,c)
                guard result.state == "committed", result.commitNonce == nonce else { throw CampCloudError.invalidResponse }
                try await acceptCommitted(result,c)
                return true
            }
            if h.state != "acknowledged" {
                let proof = try CampWire.body(["claim_id":j.claim,"stopped":true,"payload_sha256":receipt.payloadSHA256,"review_sha256":receipt.reviewSHA256])
                let result = try await call(CloudHandoff.self,"POST",base+"/ack",c,body:proof)
                try validate(result,j,c)
                guard result.state == "acknowledged" else { throw CampCloudError.invalidResponse }
            }
            status = "Source verified · Waiting for transfer"
            return true
        } catch {
            if context?.generation == c.generation {
                if journal?.commitStarted != true, case CampCloudError.server(let code, _) = error, [400,403,404,409,422].contains(code) { try await clear(c) }
                status = "Transfer paused · " + error.localizedDescription
            }
            throw error
        }
    }
}
