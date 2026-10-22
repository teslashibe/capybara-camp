import Foundation
import SwiftUI

struct LocalShareStatus {
    var model: String; var state: String; var stage: String; var externalOwner: Bool
}

@MainActor final class CampSharing: ObservableObject {
    @Published private(set) var shares: [LocalShare] = []
    var localSession: ((String) -> CampSession?)?
    var localStatus: ((CampSession) -> LocalShareStatus)?
    var localChildren: ((CampSession) -> [ChildSession])?
    var published: ((CloudSession, CloudScope, Int64) async -> Void)?
    var changed: (() -> Void)?
    private var scope: CloudScope?
    private var actor: CloudActor?
    private var transport: (any CampPublishingTransport)?
    private let cache: CampCache?
    private let export: @Sendable (CampSession, Int64) async throws -> TranscriptExport
    var childCatalog: @Sendable (CampSession) async throws -> LocalChildHistoryCatalog = {try await ChildHistoryBridge.catalog($0)}
    var childPage: @Sendable (CampSession, LocalChildHistorySource, Int64) async throws -> LocalChildHistoryPage = {try await ChildHistoryBridge.page($0,$1,$2)}
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
    func configure(local: CampSession, projectId: String, shared: Bool, allowRequests: Bool, adopt: Bool = false, syncToCloud: Bool? = nil) async throws {
        guard let scope, let actor, let cache, transport != nil, UUID(uuidString: projectId) != nil, !local.isCloud else { throw CampCloudError.cacheUnavailable }
        task?.cancel(); task = nil; generation = UUID()
        let token = generation
        var value = binding(for: local) ?? LocalShare(id: UUID().uuidString.lowercased(), local: local, projectId: projectId, shared: shared, allowRequests: allowRequests)
        value.local = local; value.projectId = projectId; value.shared = shared; value.allowRequests = allowRequests
        if let syncToCloud { value.syncToCloud = syncToCloud }
        value.projectChangeRequested = true
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
        for value in shares where value.shared || value.syncToCloud == true {
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
    private func commit(_ value: LocalShare, hashes: [PublishedDigest] = [], publishedSessionID:String? = nil, generation token: UUID) async throws {
        try check(token)
        guard let cache, let scope else { throw CampCloudError.cacheUnavailable }
        try await cache.saveShare(value, scope: scope, published: hashes, publishedSessionID:publishedSessionID, expectedEpoch: cacheEpoch)
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
            if command.kind == "session" { updated.projectChangeRequested = false }
            if command.kind == "ownership" { updated.adoptRequested = false }
        case "transcript":
            let response = try CampWire.decoder().decode([String: Int64].self, from: data)
            guard let revision = response["revision"], var remote = updated.remote, revision >= remote.transcriptRevision else { throw CampCloudError.invalidResponse }
            remote.transcriptRevision = revision; updated.remote = remote
        case "child-transcript":
            let child=try CampWire.decoder().decode(CloudChildHistory.self,from:data)
            guard child.valid(parent:value.id),child.id==command.publishedSessionID,var remote=updated.remote,child.revision>=remote.transcriptRevision else{throw CampCloudError.invalidResponse}
            var children=updated.childHistories ?? [];children.removeAll{$0.id==child.id};children.append(child);updated.childHistories=children
            remote.transcriptRevision=child.revision;updated.remote=remote
        default: throw CampCloudError.invalidResponse
        }
        updated.pending = nil
        try await commit(updated, hashes: command.hashes, publishedSessionID:command.publishedSessionID, generation: token)
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
        let localTitle = local.title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !value.shared && value.syncToCloud != true && value.remote == nil && value.needsWithdrawalFence != true { await state(id, "Private", generation: token); return }
        if value.profile == nil {
            value.pending = CampPublication(kind: "profile", method: "POST", path: "/v1/profiles", body: try CampWire.body([
                "local_id": PublishMessage.hash(Data(local.sourceID.utf8)), "provider": local.provider, "label": String(local.sourceLabel.prefix(120))]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        let get = CampPublication(kind: "read", method: "GET", path: "/v1/camps/\(scope.campId)/sessions/\(id)", body: Data())
        do {
            value.remote = try CampWire.decoder().decode(CloudSession.self, from: await transport.perform(get))
            if value.projectChangeRequested != true, let project=value.remote?.projectId, project != value.projectId {
                value.projectId=project
                try await commit(value,generation:token)
            }
        }
        catch CampCloudError.server(404, _) where value.remote == nil {}
        try check(token)
        guard let profile = value.profile, profile.ownerDeviceId == actor.deviceId else { throw CampCloudError.invalidResponse }
        if let remote = value.remote, remote.ownerDeviceId != actor.deviceId {
            guard value.adoptRequested == true else { await state(id, "Reconnect ownership on this Mac", generation: token); return }
            value.pending = CampPublication(kind: "ownership", method: "POST", path: get.path + "/ownership", body: try CampWire.body(["profile_id": profile.id, "expected_revision": remote.revision]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        let status: LocalShareStatus
        let title: String
        if let remote=value.remote,remote.executionLocation=="cloud" {
            status=LocalShareStatus(model:remote.model,state:remote.state,stage:remote.stage,externalOwner:remote.externalOwner)
            title = remote.title
        } else {
            status = localStatus?(local) ?? LocalShareStatus(model: local.model, state: "idle", stage: value.remote?.stage ?? "Ready", externalOwner: false)
            title = localTitle
        }
        let remote = value.remote
        if remote == nil || remote?.title != title || remote?.model != status.model || remote?.state != status.state || remote?.externalOwner != status.externalOwner || remote?.projectId != value.projectId || remote?.shared != value.shared || remote?.allowRequests != value.allowRequests {
            value.pending = CampPublication(kind: "session", method: "PUT", path: get.path, body: try CampWire.body([
                "id": id, "project_id": value.projectId, "profile_id": profile.id, "local_session_id": local.sessionID,
                "title": title, "model": status.model, "state": status.state, "stage": remote?.stage ?? status.stage,
                "shared": value.shared, "allow_requests": value.allowRequests, "external_owner": status.externalOwner,
                "expected_revision": remote?.revision ?? 0]))
            try await commit(value, generation: token); value = try await perform(value, generation: token)
        }
        if value.remote?.executionLocation == "cloud" {
            value.state = "Runs in Cloud"
            if shares.first(where: { $0.id == id })?.remote != value.remote || shares.first(where: { $0.id == id })?.state != value.state {
                try await commit(value, generation: token)
                if let remote = value.remote {
                    try await cache.ingest(scope: scope, session: remote, expectedEpoch: cacheEpoch)
                    try check(token); await published?(remote, scope, cacheEpoch)
                }
            }
            return
        }
        guard value.shared || value.syncToCloud == true else { await state(id, "Private", generation: token); return }
        await state(id, "Syncing transcript", generation: token)
        var after: Int64 = -1
        var snapshotRevision:String?
        for _ in 0..<1000 {
            let page = try await export(local, after)
            try check(token)
            guard page.messages.count <= 200, !page.hasMore || page.after > after else { throw CampCloudError.invalidResponse }
            guard snapshotRevision==nil || snapshotRevision==page.snapshotRevision else {throw CampError.message("The conversation changed while syncing. Retrying the latest history")}
            snapshotRevision=page.snapshotRevision
            let messages = try await cache.unpublished(page.messages, scope: scope, sessionId: id)
            if !messages.isEmpty {
                guard let remote = value.remote else { throw CampCloudError.invalidResponse }
                struct Upload: Encodable { let expectedRevision: Int64; let messages: [PublishMessage] }
                value.pending = CampPublication(kind: "transcript", method: "POST", path: get.path + "/transcript",
                    body: try CampWire.encoder().encode(Upload(expectedRevision: remote.transcriptRevision, messages: messages)),
                    hashes: messages.map { PublishedDigest(id: $0.id, digest: $0.digest) })
                try await commit(value, generation: token); value = try await perform(value, generation: token)
            }
            if !page.hasMore {
                try await synchronizeChildren(&value,local:local,generation:token)
                let verified=try await export(local,-1);try check(token)
                guard verified.snapshotRevision==snapshotRevision else {throw CampError.message("The conversation changed while syncing. Retrying the latest history")}
                await state(id, value.shared ? "Shared" : "Saved to Camp", generation: token); return
            }
            after = page.after
        }
        await state(id, "Transcript exceeds 200,000 messages", generation: token)
    }

    private func synchronizeChildren(_ value:inout LocalShare,local:CampSession,generation token:UUID) async throws {
        guard let scope,let transport,let cache else{throw CampCloudError.cacheUnavailable}
        var catalog=try await childCatalog(local);try check(token)
        guard catalog.valid else{throw CampCloudError.invalidResponse}
        let observed=Dictionary((localChildren?(local) ?? []).map{($0.id,$0.status)},uniquingKeysWith:{_,new in new})
        for index in catalog.sources.indices {
            if let status=observed[catalog.sources[index].localID] {catalog.sources[index].status=Self.childState(status)}
        }
        let request=CampPublication(kind:"read",method:"GET",path:"/v1/camps/\(scope.campId)/sessions/\(value.id)/children/publication",body:Data())
        let snapshot=try CampWire.decoder().decode(CloudChildHistoryList.self,from:await transport.perform(request));try check(token)
        guard snapshot.valid(parent:value.id),snapshot.permissionRevision==value.remote?.permissionRevision else{throw CampCloudError.invalidResponse}
        value.childHistories=snapshot.children
        value.remote?.transcriptRevision=snapshot.parentRevision
        value.state="Syncing subagents";try await commit(value,generation:token)
        for source in catalog.sources {
            let old=value.childHistories?.first{$0.sourceKey==source.id}
            let childID=old?.id ?? Self.childID(parent:value.id,source:source.id)
            if let old,old.complete && !old.removed && old.sourceRevision==source.revision &&
                old.name==String(source.name.prefix(120)) && old.phase==String(source.phase.prefix(32)) &&
                old.workflow==String(source.workflow.prefix(64)) && old.model==source.model && old.state==Self.childState(source.status){continue}
            if old?.sourceRevision != source.revision || old?.complete != false || old?.removed == true {
                try await writeChild(&value,source:source,id:childID,messages:[],complete:false,removed:false,generation:token)
            }
            var after:Int64 = -1
            var finished=false
            for _ in 0..<1000 {
                let exported=try await childPage(local,source,after);try check(token)
                guard exported.parentID==catalog.parentID,exported.source.id==source.id,exported.source.revision==source.revision,
                      exported.page.fingerprint==source.revision,exported.page.messages.count<=200,
                      !exported.page.hasMore || exported.page.after>after else{throw CampCloudError.invalidResponse}
                let messages=try await cache.unpublished(exported.page.messages,scope:scope,sessionId:childID);try check(token)
                if !messages.isEmpty || !exported.page.hasMore {
                    try await writeChild(&value,source:source,id:childID,messages:messages,complete:!exported.page.hasMore,removed:false,generation:token)
                }
                if !exported.page.hasMore{finished=true;break}
                after=exported.page.after
            }
            guard finished else{throw CampError.message("A child transcript exceeds 200,000 messages")}
        }
        let keys=Set(catalog.sources.map(\.id))
        for old in (value.childHistories ?? []) where !old.removed && !keys.contains(old.sourceKey) {
            let source=LocalChildHistorySource(id:old.sourceKey,localID:"",name:old.name,phase:old.phase,workflow:old.workflow,status:old.state,model:old.model,revision:old.sourceRevision)
            try await writeChild(&value,source:source,id:old.id,messages:[],complete:true,removed:true,generation:token)
        }
        let verified=try await childCatalog(local);try check(token)
        guard verified.valid,verified.parentID==catalog.parentID,verified.revision==catalog.revision else{throw CampCloudError.server(409,"child_history_changed")}
    }

    private static func childState(_ state:String)->String {
        ["unknown","running","done","failed","stopped","cancelled"].contains(state) ? state : "unknown"
    }
    private static func childID(parent:String,source:String)->String {
        var hex=Array(PublishMessage.hash(Data((parent+"\0"+source).utf8)).prefix(32))
        hex[12]="8";hex[16]="8"
        return "\(String(hex[0..<8]))-\(String(hex[8..<12]))-\(String(hex[12..<16]))-\(String(hex[16..<20]))-\(String(hex[20..<32]))"
    }
    private func writeChild(_ value:inout LocalShare,source:LocalChildHistorySource,id:String,messages:[PublishMessage],complete:Bool,removed:Bool,generation token:UUID) async throws {
        guard let scope,let remote=value.remote,source.valid,source.model.utf8.count<=200 else{throw CampCloudError.invalidResponse}
        let previous=value.childHistories?.first{$0.id==id}
        let body=try CampWire.body([
            "source_key":source.id,"source_revision":source.revision,"name":String(source.name.prefix(120)),
            "phase":String(source.phase.prefix(32)),"workflow":String(source.workflow.prefix(64)),"state":Self.childState(source.status),"model":source.model,
            "complete":complete,"removed":removed,"expected_parent_revision":remote.transcriptRevision,
            "expected_transcript_revision":previous?.transcriptRevision ?? 0,
            "messages":JSONSerialization.jsonObject(with:CampWire.encoder().encode(messages))
        ])
        value.pending=CampPublication(kind:"child-transcript",method:"POST",path:"/v1/camps/\(scope.campId)/sessions/\(value.id)/children/\(id)/transcript",body:body,hashes:messages.map{PublishedDigest(id:$0.id,digest:$0.digest)},publishedSessionID:id)
        try await commit(value,generation:token);value=try await perform(value,generation:token)
    }
}
