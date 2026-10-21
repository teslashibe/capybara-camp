import Foundation
import SwiftUI

struct LocalShareStatus {
    var model: String; var state: String; var stage: String; var externalOwner: Bool
}

@MainActor final class CampSharing: ObservableObject {
    @Published private(set) var shares: [LocalShare] = []
    var localSession: ((String) -> CampSession?)?
    var localStatus: ((CampSession) -> LocalShareStatus)?
    var published: ((CloudSession, CloudScope, Int64) async -> Void)?
    var changed: (() -> Void)?
    private var scope: CloudScope?
    private var actor: CloudActor?
    private var transport: (any CampPublishingTransport)?
    private let cache: CampCache?
    private let export: @Sendable (CampSession, Int64) async throws -> TranscriptExport
    private let watch = TranscriptWatch()
    private var generation = UUID()
    private var task: Task<Void, Never>?
    private var dirty = false
    private var retryDelay: Double = 1
    private var cacheEpoch: Int64 = 0

    init(cache: CampCache?, export: @escaping @Sendable (CampSession, Int64) async throws -> TranscriptExport = { local, after in
        let session = try JSONSerialization.jsonObject(with: JSONEncoder().encode(local))
        let input = try CampWire.body(["session": session, "after": after, "limit": 200])
        return try JSONDecoder().decode(TranscriptExport.self, from: await Bridge.call("export-page", input: input))
    }) { self.cache = cache; self.export = export }
    deinit { task?.cancel() }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?) {
        self.transport = transport
        guard self.scope != scope || self.actor?.deviceId != actor?.deviceId else { return }
        task?.cancel(); task = nil; watch.stop(); generation = UUID()
        self.scope = scope; self.actor = actor; shares = []; changed?()
        guard let scope, let cache else { return }
        let token = generation
        task = Task { [weak self] in
            guard let epoch = try? await cache.epoch(scope), let values = try? await cache.shares(scope), self?.generation == token else { return }
            self?.cacheEpoch = epoch
            self?.shares = values; self?.changed?(); self?.task = nil
            self?.observe(); self?.wake()
        }
    }
    func binding(for local: CampSession) -> LocalShare? { shares.first { $0.local.id == local.id || "camp:" + $0.id == local.id } }
    func native(for id: String) -> CampSession? {
        guard let value = shares.first(where: { "camp:" + $0.id == id }) else { return nil }
        return localSession?(value.local.id) ?? value.local
    }
    func configure(local: CampSession, projectId: String, shared: Bool, allowRequests: Bool, adopt: Bool = false) async throws {
        guard let scope, let actor, let cache, transport != nil, UUID(uuidString: projectId) != nil, !local.isCloud else { throw CampCloudError.cacheUnavailable }
        task?.cancel(); task = nil; generation = UUID()
        let token = generation
        var value = binding(for: local) ?? LocalShare(id: UUID().uuidString.lowercased(), local: local, projectId: projectId, shared: shared, allowRequests: allowRequests)
        value.local = local; value.projectId = projectId; value.shared = shared; value.allowRequests = allowRequests
        value.adoptRequested = adopt
        value.deviceId = actor.deviceId
        // A private metadata row fences any delayed first publication. Do not
        // replay a formerly approved share after the user's intent changes.
        if value.remote != nil || (value.pending?.kind == "session" && value.pending?.sent == true) { value.needsWithdrawalFence = true }
        value.pending = nil
        value.state = "Waiting to sync"
        if let profile = value.profile, profile.ownerDeviceId != actor.deviceId { value.profile = nil }
        try await cache.saveShare(value, scope: scope, expectedEpoch: cacheEpoch)
        try check(token)
        set(value); observe(); wake()
    }
    func localChanged() { if !shares.isEmpty { wake() } }
    func retry() { retryDelay = 1; wake() }
    private func set(_ value: LocalShare) {
        if let index = shares.firstIndex(where: { $0.id == value.id }) { shares[index] = value } else { shares.append(value) }
        changed?()
    }
    private func observe() {
        var paths = Set<String>()
        for value in shares where value.shared {
            let local = localSession?(value.local.id) ?? value.local
            if !local.path.isEmpty {
                paths.insert(local.path); paths.insert(URL(fileURLWithPath: local.path).deletingLastPathComponent().path)
            }
            if local.provider == "Codex" {
                for name in ["thread_history_1.sqlite", "thread_history_1.sqlite-wal", "state_5.sqlite", "state_5.sqlite-wal"] { paths.insert(local.root + "/" + name) }
                paths.insert(local.root)
            }
        }
        watch.observe(paths.sorted()) { [weak self] in self?.wake() }
    }
    private func wake() {
        guard scope != nil, actor != nil, transport != nil else { return }
        dirty = true
        guard task == nil else { return }
        let token = generation
        task = Task { [weak self] in
            while !Task.isCancelled, self?.generation == token, self?.dirty == true {
                self?.dirty = false
                let failed = await self?.cycle(generation: token) ?? false
                guard !Task.isCancelled, self?.generation == token else { return }
                if failed {
                    let delay = self?.retryDelay ?? 15
                    self?.retryDelay = min(delay * 2, 30)
                    do { try await Task.sleep(for: .seconds(delay)) } catch { return }
                    self?.dirty = true
                } else { self?.retryDelay = 1 }
            }
            if self?.generation == token { self?.task = nil }
        }
    }
    private func cycle(generation token: UUID) async -> Bool {
        var retry = false
        for id in shares.map(\.id) {
            guard !Task.isCancelled, generation == token else { return false }
            do { try await synchronize(id, generation: token) }
            catch is CancellationError { return false }
            catch CampCloudError.server(let code, _) where [400, 401, 403, 404].contains(code) {
                await state(id, code == 401 ? "Sign in to Camp again" : "Needs review", generation: token)
            } catch CampCloudError.server(409, _) {
                // A rejected revision has no committed effect. Read current state
                // and create a fresh command, preserving the user's latest intent.
                if var value = shares.first(where: { $0.id == id }), generation == token, let scope {
                    value.pending = nil; value.state = "Reconciling changes"
                    try? await cache?.saveShare(value, scope: scope, expectedEpoch: cacheEpoch); set(value)
                }
                retry = true
            } catch {
                await state(id, "Waiting to sync", generation: token); retry = true
            }
        }
        return retry
    }
    private func state(_ id: String, _ text: String, generation token: UUID) async {
        guard generation == token, let scope, var value = shares.first(where: { $0.id == id }), value.state != text else { return }
        value.state = text; try? await cache?.saveShare(value, scope: scope, expectedEpoch: cacheEpoch)
        guard generation == token else { return }; set(value)
    }
    private func check(_ token: UUID) throws { if generation != token || Task.isCancelled { throw CancellationError() } }
    private func commit(_ value: LocalShare, hashes: [PublishedDigest] = [], generation token: UUID) async throws {
        try check(token)
        guard let cache, let scope else { throw CampCloudError.cacheUnavailable }
        try await cache.saveShare(value, scope: scope, published: hashes, expectedEpoch: cacheEpoch)
        try check(token); set(value)
    }
    private func perform(_ value: LocalShare, generation token: UUID) async throws -> LocalShare {
        try check(token)
        var updated = value
        guard var command = value.pending, let transport, let scope else { return value }
        if command.sent != true {
            command.sent = true; updated.pending = command
            if command.kind == "session" { updated.needsWithdrawalFence = true }
            try await commit(updated, generation: token)
        }
        let data = try await transport.perform(command)
        try check(token)
        switch command.kind {
        case "profile": updated.profile = try CampWire.decoder().decode(CloudProfile.self, from: data)
        case "session", "ownership":
            updated.remote = try CampWire.decoder().decode(CloudSession.self, from: data)
            if command.kind == "ownership" { updated.adoptRequested = false }
        case "transcript":
            let response = try CampWire.decoder().decode([String: Int64].self, from: data)
            guard let revision = response["revision"], var remote = updated.remote, revision >= remote.transcriptRevision else { throw CampCloudError.invalidResponse }
            remote.transcriptRevision = revision; updated.remote = remote
        default: throw CampCloudError.invalidResponse
        }
        updated.pending = nil
        try await commit(updated, hashes: command.hashes, generation: token)
        if let remote = updated.remote {
            try await cache?.ingest(scope: scope, session: remote, expectedEpoch: cacheEpoch)
            try check(token); await published?(remote, scope, cacheEpoch)
        }
        return updated
    }
    private func synchronize(_ id: String, generation token: UUID) async throws {
        guard let scope, let actor, let cache, let transport, var value = shares.first(where: { $0.id == id }) else { return }
        if (value.deviceId != nil && value.deviceId != actor.deviceId) || (value.profile != nil && value.profile?.ownerDeviceId != actor.deviceId) {
            value.profile = nil; value.pending = nil; value.adoptRequested = false; value.deviceId = actor.deviceId
            try await commit(value, generation: token)
        }
        if value.pending != nil { value = try await perform(value, generation: token) }
        try check(token)
        guard let local = localSession?(value.local.id) else {
            await state(id, "Local session unavailable", generation: token); return
        }
        value.local = local
        if !value.shared && value.remote == nil && value.needsWithdrawalFence != true { await state(id, "Private", generation: token); return }
        if value.profile == nil {
            value.pending = CampPublication(kind: "profile", method: "POST", path: "/v1/profiles", body: try CampWire.body([
                "local_id": PublishMessage.hash(Data(local.sourceID.utf8)), "provider": local.provider, "label": String(local.sourceLabel.prefix(120))]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        let get = CampPublication(kind: "read", method: "GET", path: "/v1/camps/\(scope.campId)/sessions/\(id)", body: Data())
        do { value.remote = try CampWire.decoder().decode(CloudSession.self, from: await transport.perform(get)) }
        catch CampCloudError.server(404, _) where value.remote == nil {}
        try check(token)
        guard let profile = value.profile, profile.ownerDeviceId == actor.deviceId else { throw CampCloudError.invalidResponse }
        if let remote = value.remote, remote.ownerDeviceId != actor.deviceId {
            guard value.adoptRequested == true else { await state(id, "Reconnect ownership on this Mac", generation: token); return }
            value.pending = CampPublication(kind: "ownership", method: "POST", path: get.path + "/ownership", body: try CampWire.body(["profile_id": profile.id, "expected_revision": remote.revision]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        let status = localStatus?(local) ?? LocalShareStatus(model: local.model, state: "idle", stage: value.remote?.stage ?? "Ready", externalOwner: false)
        let remote = value.remote
        if remote == nil || remote?.title != local.title || remote?.model != status.model || remote?.state != status.state || remote?.externalOwner != status.externalOwner || remote?.projectId != value.projectId || remote?.shared != value.shared || remote?.allowRequests != value.allowRequests {
            value.pending = CampPublication(kind: "session", method: "PUT", path: get.path, body: try CampWire.body([
                "id": id, "project_id": value.projectId, "profile_id": profile.id, "local_session_id": local.sessionID,
                "title": local.title, "model": status.model, "state": status.state, "stage": remote?.stage ?? status.stage,
                "shared": value.shared, "allow_requests": value.allowRequests, "external_owner": status.externalOwner,
                "expected_revision": remote?.revision ?? 0]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        guard value.shared else { await state(id, "Private", generation: token); return }
        await state(id, "Syncing transcript", generation: token)
        var after: Int64 = -1
        for _ in 0..<1000 {
            let page = try await export(local, after)
            try check(token)
            guard page.messages.count <= 200, !page.hasMore || page.after > after else { throw CampCloudError.invalidResponse }
            let messages = try await cache.unpublished(page.messages, scope: scope, sessionId: id)
            if !messages.isEmpty {
                guard let remote = value.remote else { throw CampCloudError.invalidResponse }
                struct Upload: Encodable { let expectedRevision: Int64; let messages: [PublishMessage] }
                value.pending = CampPublication(kind: "transcript", method: "POST", path: get.path + "/transcript",
                    body: try CampWire.encoder().encode(Upload(expectedRevision: remote.transcriptRevision, messages: messages)),
                    hashes: messages.map { PublishedDigest(id: $0.id, digest: $0.digest) })
                try await commit(value, generation: token); value = try await perform(value, generation: token)
            }
            if !page.hasMore { await state(id, "Shared", generation: token); return }
            after = page.after
        }
        await state(id, "Transcript exceeds 200,000 messages", generation: token)
    }
}
