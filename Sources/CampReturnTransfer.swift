import Foundation
import SwiftUI

// Screen state over the existing durable export, installation and handoff journals.
// Closing the sheet does not discard an approved move or launch a provider.
@MainActor final class CampReturnTransfer: ObservableObject {
    private struct Connection {
        var scope: CloudScope; var credential: CloudCredential; var transport: any CampAccountTransport
        var generation = UUID(); var epoch: Int64?
    }
    private struct Operations {
        var files: CampWorkspaceExport; var history: CampTransferHistory
        var destination: CampLocalDestination; var handoff: CampReturnHandoff
    }
    @Published private(set) var export: WorkspaceExportJournal?
    @Published private(set) var changes: TransferChanges?
    @Published private(set) var review: TransferCapture?
    @Published private(set) var history: TransferHistoryJournal?
    @Published private(set) var destination: LocalDestinationJournal?
    @Published private(set) var handoff: ReturnHandoffJournal?
    @Published private(set) var completed: LocalContinuationBinding?
    @Published private(set) var working = false
    @Published private(set) var error = ""
    @Published private(set) var downloadedBytes = 0
    var adopted: ((LocalContinuationBinding, CloudScope, Int64) async throws -> Void)?
    var callLocal: @Sendable (String, Data) async throws -> Data = { try await Bridge.call($0, input: $1) }
    private let cache: CampCache?
    private var connection: Connection?
    private var task: Task<Void, Never>?
    private let automatic: Bool
    init(cache: CampCache?, automatic: Bool = true) { self.cache = cache; self.automatic = automatic }
    deinit { task?.cancel() }
    var ownerUserID: String? { connection?.scope.userId }
    var endMessage: String {
        switch export?.export?.state {
        case "expired": return "Transfer expired · Start again to capture the current files"
        case "failed": return "Transfer failed · Start again to capture the current files"
        default: return "Transfer cancelled · Any restored files are kept"
        }
    }
    var sessionID: String? { completed?.session.id ?? handoff?.destination.session.id ?? destination?.session.id ?? export?.session.id }
    var phase: String {
        if completed != nil { return "complete" }
        if let handoff { return handoff.cancelling && handoff.phase != "cancelled" ? "cancelling" : handoff.phase }
        if let destination { return destination.cancelling && destination.phase != "cancelled" ? "cancelling" : destination.phase }
        guard let export else { return "none" }
        if export.cancelling { return ["cancelled", "expired", "failed"].contains(export.export?.state ?? "") ? "cancelled" : "cancelling" }
        if ["cancelled", "expired", "failed"].contains(export.export?.state ?? "") { return "cancelled" }
        if export.kind == "inspect" { return changes == nil ? "inspecting" : "select" }
        return review != nil && history?.complete == true ? "review" : "downloading"
    }
    private var advancing: Bool { !["none", "select", "review", "confirm", "complete", "cancelled"].contains(phase) && error.isEmpty }
    func attach(scope: CloudScope?, credential: CloudCredential?, transport: (any CampAccountTransport)?) {
        if let c = connection, c.scope == scope, c.credential.token == credential?.token { return }
        task?.cancel(); task = nil; connection = nil
        export = nil; changes = nil; review = nil; history = nil; destination = nil; handoff = nil; completed = nil
        working = false; error = ""; downloadedBytes = 0
        if let scope, let credential, let transport {
            connection = Connection(scope: scope, credential: credential, transport: transport)
            wake()
        }
    }
    private func check(_ c: Connection) throws {
        guard connection?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func operations(_ c: Connection) async throws -> Operations {
        guard let cache else { throw CampCloudError.cacheUnavailable }
        let epoch = try await cache.epoch(c.scope); try check(c)
        if let saved = connection?.epoch { guard epoch == saved else { throw CancellationError() } }
        connection?.epoch = epoch
        return try Operations(files: CampWorkspaceExport(cache:cache,scope:c.scope,credential:c.credential,transport:c.transport,epoch:epoch),
            history: CampTransferHistory(cache:cache,scope:c.scope,credential:c.credential,transport:c.transport,epoch:epoch),
            destination: CampLocalDestination(cache:cache,scope:c.scope,credential:c.credential,transport:c.transport,epoch:epoch,callLocal:callLocal),
            handoff: CampReturnHandoff(cache:cache,scope:c.scope,credential:c.credential,transport:c.transport,epoch:epoch,callLocal:callLocal))
    }
    private func read(_ o: Operations, _ c: Connection) async throws {
        let h = try await o.handoff.current(), d = try await o.destination.current()
        let e = try await o.files.current(), archive = try await o.history.current()
        try check(c)
        if export?.key != e?.key { changes = nil; review = nil; downloadedBytes = 0 }
        handoff = h; destination = d; export = e; history = archive
    }
    private func perform(_ action: (Operations, Connection) async throws -> Void) async {
        guard !working, let c = connection else { return }
        working = true; error = ""
        defer { if connection?.generation == c.generation { working = false } }
        do { let o = try await operations(c); try await action(o,c); try await read(o,c) }
        catch { if connection?.generation == c.generation, !(error is CancellationError) { self.error = error.localizedDescription } }
    }
    func restore() async { await perform { o,c in try await read(o,c) }; wake() }
    func inspect(_ session: CloudSession) async {
        await perform { o,c in
            guard session.ownerUserId == c.scope.userId, session.executionLocation == "cloud", try await o.files.current() == nil,
                  try await o.destination.current() == nil, try await o.handoff.current() == nil else { throw CampCloudError.invalidResponse }
            completed = nil
            try await o.files.begin(session:session,kind:"inspect")
        }
        wake()
    }
    func capture(paths: [String]) async {
        await perform { o,_ in
            guard let saved = try await o.files.current(), saved.kind == "inspect", let available = changes,
                  paths.count <= 256, Set(paths).isSubset(of:Set(available.files.filter { $0.excluded == nil }.map(\.path))) else { throw CampCloudError.invalidResponse }
            try await o.files.requestCancellation(); _ = try await o.files.advance(); try await o.files.discard()
            try await o.history.discard()
            try await o.files.begin(session:saved.session,kind:"capture",paths:paths.sorted())
        }
        wake()
    }
    func prepare(source: CampSource, purpose: String) async {
        await perform { o,c in
            guard let export = try await o.files.current(), export.kind == "capture", let review,
                  source.provider == export.session.provider, source.root.hasPrefix("/"), !source.id.isEmpty,
                  ["planning", "implementation"].contains(purpose), history?.complete == true else { throw CampCloudError.invalidResponse }
            let localID = PublishMessage.hash(Data(source.id.utf8))
            let body = try CampWire.body(["local_id":localID,"provider":source.provider,"label":HistorySourceSnapshot.limited(source.label,bytes:120)])
            let raw = try await c.transport.data("POST",path:"/v1/profiles",body:body,key:UUID().uuidString,token:c.credential.token)
            try check(c)
            let profile = try CampWire.decoder().decode(CloudProfile.self,from:raw)
            guard profile.ownerUserId == c.scope.userId, profile.ownerDeviceId == c.credential.actor.deviceId,
                  profile.localId == localID, profile.provider == source.provider else { throw CampCloudError.invalidResponse }
            let target = TransferInstallationTarget(profileID:profile.id,sourceID:source.id,provider:source.provider,root:source.root,sessionID:UUID().uuidString.lowercased(),purpose:purpose)
            try await o.destination.begin(target:target,reviewedSHA256:review.reviewSha256)
        }
        wake()
    }
    func confirm(contextApproved: Bool, executionApproved: Bool, permissions: PermissionSettings) async {
        await perform { o,_ in try await o.handoff.confirm(contextContinuationApproved:contextApproved,executionApproved:executionApproved,permissions:permissions) }
        wake()
    }
    func cancel() async {
        guard let c = connection else { return }
        error = ""
        do {
            let o = try await operations(c)
            if try await o.handoff.current() != nil { try await o.handoff.requestCancellation() }
            else if try await o.destination.current() != nil { try await o.destination.requestCancellation() }
            else { try await o.files.requestCancellation() }
            try await read(o,c)
        } catch { if connection?.generation == c.generation { self.error = error.localizedDescription } }
        wake()
    }
    func reset() async {
        await perform { o,_ in
            guard phase == "cancelled" || phase == "complete" else { throw CampCloudError.invalidResponse }
            // Adoption already cleared the journals; only the finished summary remains.
            if completed != nil, handoff == nil, destination == nil, export == nil { completed = nil; return }
            if try await o.handoff.current() != nil { try await o.handoff.discardCancelled() }
            else if try await o.destination.current() != nil { try await o.destination.discardCancelled() }
            else { try await o.files.discard(); try await o.history.discard() }
            completed = nil
        }
    }
    func advance() async {
        await perform { o,c in
            try await read(o,c)
            if let h = handoff {
                if h.cancelling || h.phase != "complete" && h.phase != "confirm" { _ = try await o.handoff.advance() }
                if try await o.handoff.current()?.phase == "complete", let value = try await o.handoff.adoptCompleted() {
                    try check(c); completed = value
                }
            } else if let d = destination {
                if d.phase == "installed" && !d.cancelling { try await o.handoff.begin() }
                else if d.phase != "cancelled" { _ = try await o.destination.advance() }
            } else if let e = export, !["cancelled", "expired", "failed"].contains(e.export?.state ?? "") {
                _ = try await o.files.advance()
                if !e.cancelling {
                    let bytes = try await o.files.downloadedBytes(); try check(c); downloadedBytes = bytes
                    if e.kind == "inspect" { let value = try await o.files.inspection(); try check(c); changes = value }
                    else {
                        if review == nil { let value = try await o.files.review(callLocal:callLocal); try check(c); review = value }
                        if review != nil {
                            if try await o.history.current() == nil { try await o.history.begin(session:e.session) }
                            _ = try await o.history.advance()
                        }
                    }
                }
            }
            try check(c)
            // Adoption already persisted and cleared the transient journals.
            // Retry a failed board/cache refresh without repeating the handoff.
            if let completed {
                guard let epoch = connection?.epoch else { throw CampCloudError.cacheUnavailable }
                try await adopted?(completed,c.scope,epoch)
            }
        }
    }
    func retry() { error = ""; wake() }
    func preview(_ path: String) async throws -> String {
        guard let c = connection, review?.files.contains(where: { $0.path == path }) == true else { throw CampCloudError.invalidResponse }
        let o = try await operations(c)
        guard let payload = try await o.files.payload() else { throw CampCloudError.invalidResponse }
        let text = try await Task.detached { try TransferCapture.preview(path, payload: payload) }.value
        try check(c)
        return text
    }
    private func wake() {
        guard automatic, error.isEmpty, task == nil, let generation = connection?.generation else { return }
        task = Task { [weak self] in
            defer { if self?.connection?.generation == generation { self?.task = nil } }
            repeat {
                await self?.advance()
                guard self?.connection?.generation == generation, self?.advancing == true else { return }
                do { try await Task.sleep(for:.seconds(1)) } catch { return }
            } while !Task.isCancelled
        }
    }
}
