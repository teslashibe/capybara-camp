import Foundation
import SwiftUI

@MainActor final class CampConnection: ObservableObject {
    @Published var server = "https://api.capybaracamp.com"
    @Published var email = ""
    @Published var code = ""
    @Published var codeRequested = false
    @Published var busy = false
    @Published var status = "Local on this Mac"
    @Published var error = ""
    @Published var camps: [CloudCamp] = []
    @Published var projects: [CloudProject] = []
    @Published var members: [CloudMember] = []
    @Published var sessions: [CloudSession] = []
    @Published var requests: [CloudRequest] = []
    @Published var pending: [CloudOutboxItem] = []
    @Published var selectedCampId = ""
    @Published var signedIn = false

    var sessionsChanged: (([CloudSession], [CloudProject], CloudScope?) -> Void)?
    var transcriptChanged: ((String) -> Void)?
    var requestChanged: ((CloudRequest) -> Void)?
    private(set) var credential: CloudCredential?
    private(set) var scope: CloudScope?
    private(set) var cache: CampCache?
    private(set) var client: CampHTTP?
    lazy var worker = CampLocalWorker(cache: cache)
    private let vault: any CampCredentialStore
    private let allowLoopback: Bool
    private var generation = UUID()
    private var streamTask: Task<Void, Never>?
    private var publishTask: Task<Void, Never>?
    private var flushTask: Task<Void, Never>?
    private var booted = false
    private var sessionIndex: [String: CloudSession] = [:]
    private var requestIndex: [String: CloudRequest] = [:]
    private var lastRenewal = Date.distantPast
    private var cacheEpoch: Int64 = 0

    init(directory: URL, vault: any CampCredentialStore = CampKeychain(), allowLoopback: Bool = false) {
        self.vault = vault; self.allowLoopback = allowLoopback
        do { cache = try CampCache(directory: directory) }
        catch { self.error = "The local Camp cache could not be opened" }
    }
    deinit { streamTask?.cancel(); publishTask?.cancel(); flushTask?.cancel() }

    func restore() async {
        guard !booted else { return }; booted = true
        do {
            if let data = try await cache?.metadata("server"), let value = String(data: data, encoding: .utf8) { server = value }
            let endpoint = try CampEndpoint(server, allowLoopback: allowLoopback)
            guard let saved = try vault.read(origin: endpoint.origin) else { return }
            credential = saved; email = saved.actor.email; signedIn = true
            client = CampHTTP(endpoint: endpoint)
            if let data = try await cache?.metadata("selected:" + saved.actor.userId), let value = String(data: data, encoding: .utf8) { selectedCampId = value }
            if !selectedCampId.isEmpty {
                let selected = CloudScope(origin: endpoint.origin, userId: saved.actor.userId, campId: selectedCampId)
                scope = selected; await loadCached(scope: selected)
            }
            await refresh()
        } catch { self.error = error.localizedDescription; status = "Camp unavailable" }
    }

    func sendCode() async {
        guard !busy else { return }; busy = true; defer { busy = false }
        do {
            let endpoint = try CampEndpoint(server, allowLoopback: allowLoopback)
            let api = CampHTTP(endpoint: endpoint)
            _ = try await api.data("POST", path: "/v1/auth/code", body: CampWire.body(["email": email]))
            client = api; codeRequested = true; code = ""; error = ""
        } catch { self.error = error.localizedDescription }
    }
    func verifyCode() async {
        guard !busy, let client else { return }; busy = true; defer { busy = false }
        do {
            let endpoint = try CampEndpoint(server, allowLoopback: allowLoopback)
            guard endpoint.origin == client.endpoint.origin else { codeRequested = false; throw CampCloudError.invalidEndpoint }
            let body = try CampWire.body(["email": email, "code": code, "device_name": Host.current().localizedName ?? "Mac"])
            let data = try await client.data("POST", path: "/v1/auth/verify", body: body)
            let login = try CampWire.decoder().decode(CloudLogin.self, from: data)
            guard UUID(uuidString: login.actor.userId) != nil, UUID(uuidString: login.actor.deviceId) != nil,
                  login.token.hasPrefix("camp_"), login.expiresAt > Date() else { throw CampCloudError.invalidResponse }
            let saved = CloudCredential(origin: endpoint.origin, actor: login.actor, token: login.token, expiresAt: login.expiresAt)
            try vault.save(saved)
            credential = saved; signedIn = true; codeRequested = false; code = ""; error = ""
            try await cache?.setMetadata("server", Data(endpoint.origin.utf8))
            await refresh()
        } catch { self.error = error.localizedDescription }
    }

    func refresh() async {
        guard let client, let credential else { return }
        do {
            try await renewIfNeeded()
            let values = try await client.get([CloudCamp].self, path: "/v1/camps", token: credential.token)
            guard self.credential?.token == credential.token else { return }
            guard values.allSatisfy({ UUID(uuidString: $0.id) != nil }) else { throw CampCloudError.invalidResponse }
            camps = values
            if !values.contains(where: { $0.id == selectedCampId }) { selectedCampId = values.first?.id ?? "" }
            if !selectedCampId.isEmpty { await selectCamp(selectedCampId) }
            else { status = "Create or join a Camp"; scope = nil; clearVisible() }
        } catch { self.error = error.localizedDescription; status = "Camp offline" }
    }
    func selectCamp(_ id: String) async {
        guard let credential, let client, UUID(uuidString: id) != nil else { return }
        streamTask?.cancel(); flushTask?.cancel(); publishTask?.cancel()
        flushTask = nil; publishTask = nil
        let token = UUID(); generation = token; selectedCampId = id
        let selected = CloudScope(origin: client.endpoint.origin, userId: credential.actor.userId, campId: id)
        scope = selected; error = ""; status = "Connecting to Camp"
        await loadCached(scope: selected)
        do {
            try await cache?.setMetadata("selected:" + credential.actor.userId, Data(id.utf8))
            try await synchronize(scope: selected, generation: token)
            guard generation == token else { return }
            status = "Connected to Camp"; startStream(scope: selected, generation: token); flush()
        } catch is CancellationError {} catch {
            guard generation == token else { return }
            self.error = error.localizedDescription; status = "Camp offline"; startStream(scope: selected, generation: token)
        }
    }
    private func loadCached(scope: CloudScope) async {
        guard let cache else { return }
        do {
            let token = generation
            let epoch = try await cache.epoch(scope)
            let cachedSessions = try await cache.sessions(scope), cachedRequests = try await cache.requests(scope)
            let cachedPending = try await cache.outbox(scope)
            let data = try await cache.metadata("snapshot:" + scope.key)
            guard self.scope == scope, generation == token else { return }
            cacheEpoch = epoch
            sessions = cachedSessions; requests = cachedRequests
            sessionIndex = Dictionary(uniqueKeysWithValues: sessions.map { ($0.id, $0) })
            requestIndex = Dictionary(uniqueKeysWithValues: requests.map { ($0.id, $0) })
            pending = cachedPending
            if let data, let snapshot = try? CampWire.decoder().decode(CloudSnapshot.self, from: data) {
                projects = snapshot.projects; members = snapshot.members
            } else { projects = []; members = [] }
            sessionsChanged?(sessions, projects, scope)
        } catch { self.error = error.localizedDescription }
    }
    private func synchronize(scope: CloudScope, generation token: UUID) async throws {
        guard let client, let credential, let cache else { throw CampCloudError.cacheUnavailable }
        let epoch = cacheEpoch
        let snapshot = try await client.get(CloudSnapshot.self, path: "/v1/camps/\(scope.campId)/snapshot", token: credential.token)
        var all: [CloudSession] = [], cursor = "", seen = Set<String>()
        repeat {
            guard generation == token else { throw CancellationError() }
            let suffix = cursor.isEmpty ? "" : "&before=" + cursor
            let page = try await client.get(CloudSessionPage.self, path: "/v1/camps/\(scope.campId)/sessions?limit=200" + suffix, token: credential.token)
            all.append(contentsOf: page.sessions)
            cursor = page.next
            guard all.count <= 200_000, cursor.isEmpty || seen.insert(cursor).inserted else { throw CampCloudError.responseTooLarge }
        } while !cursor.isEmpty
        let currentRequests = try await client.get([CloudRequest].self, path: "/v1/camps/\(scope.campId)/requests", token: credential.token)
        guard generation == token else { throw CancellationError() }
        try await cache.replaceSessions(all, scope: scope, cursor: snapshot.camp.cursor, expectedEpoch: epoch)
        for request in currentRequests { try await cache.ingest(scope: scope, request: request, expectedEpoch: epoch) }
        try await cache.saveSnapshot(snapshot, scope: scope, expectedEpoch: epoch)
        projects = snapshot.projects; members = snapshot.members
        await loadCached(scope: scope)
        worker.scan()
    }
    private func startStream(scope: CloudScope, generation token: UUID) {
        streamTask?.cancel()
        streamTask = Task { [weak self] in
            var delay: Double = 0.5
            while let self, !Task.isCancelled, self.generation == token {
                guard let client = self.client, let credential = self.credential, let cache = self.cache else { return }
                do {
                    try await self.renewIfNeeded()
                    let cursor = try await cache.cursor(scope)
                    try await client.events(camp: scope.campId, after: cursor, token: credential.token) { [weak self] event in
                        try await self?.receive(event, scope: scope, generation: token)
                    }
                    delay = 0.5
                } catch is CancellationError { return }
                catch CampCloudError.server(409, _) {
                    guard self.generation == token else { return }
                    do { try await self.synchronize(scope: scope, generation: token) }
                    catch { self.error = error.localizedDescription; self.status = "Camp needs review"; return }
                } catch CampCloudError.server(404, _) {
                    guard self.generation == token else { return }
                    try? await cache.clear(scope); self.clearVisible(); self.error = "This Camp is no longer shared with you"; self.status = "Camp access removed"; return
                } catch CampCloudError.server(401, _) {
                    guard self.generation == token else { return }
                    self.status = "Sign in to Camp again"; self.error = "Your Camp login has expired or been revoked"; return
                } catch {
                    guard !Task.isCancelled, self.generation == token else { return }
                    self.status = "Camp offline"; self.error = error.localizedDescription
                }
                do { try await Task.sleep(for: .seconds(delay)) } catch { return }
                delay = min(delay * 2, 15)
                if self.generation == token { self.flush() }
            }
        }
    }
    private func receive(_ event: CloudEvent, scope: CloudScope, generation token: UUID) async throws {
        guard generation == token, let client, let credential, let cache else { throw CancellationError() }
        let epoch = cacheEpoch
        var changedSession: CloudSession?, changedRequest: CloudRequest?, removed: (String, Int64)?
        if let id = event.payload.sessionId {
            guard UUID(uuidString: id) != nil else { throw CampCloudError.invalidResponse }
            do { changedSession = try await client.get(CloudSession.self, path: "/v1/camps/\(scope.campId)/sessions/\(id)", token: credential.token) }
            catch CampCloudError.server(404, _) {
                if event.kind.hasPrefix("session."), let revision = event.payload.revision { removed = (id, revision) }
            }
        }
        if let id = event.payload.requestId {
            guard UUID(uuidString: id) != nil else { throw CampCloudError.invalidResponse }
            do { changedRequest = try await client.get(CloudRequest.self, path: "/v1/camps/\(scope.campId)/requests/\(id)", token: credential.token) }
            catch CampCloudError.server(404, _) {} // Other members can see task activity, not its private request.
        }
        guard generation == token else { throw CancellationError() }
        try await cache.ingest(scope: scope, cursor: event.cursor, session: changedSession, request: changedRequest, removed: removed, expectedEpoch: epoch)
        if let value = changedSession, accepts(value) { sessionIndex[value.id] = value }
        if let value = changedRequest, value.revision >= (requestIndex[value.id]?.revision ?? 0) { requestIndex[value.id] = value; requestChanged?(value) }
        if let (id, revision) = removed, (sessionIndex[id]?.revision ?? 0) <= revision {
            sessionIndex[id] = nil
            requestIndex = requestIndex.filter { $0.value.sessionId != id }
        }
        if event.kind == "transcript.updated", let id = event.payload.sessionId { transcriptChanged?(id) }
        if event.payload.sessionId == nil {
            let snapshot = try await client.get(CloudSnapshot.self, path: "/v1/camps/\(scope.campId)/snapshot", token: credential.token)
            guard generation == token else { throw CancellationError() }
            projects = snapshot.projects; members = snapshot.members
            try await cache.saveSnapshot(snapshot, scope: scope, expectedEpoch: epoch)
        }
        status = "Connected to Camp"; error = ""; schedulePublish(generation: token)
    }
    private func accepts(_ value: CloudSession) -> Bool {
        guard let old = sessionIndex[value.id] else { return true }
        return value.revision > old.revision || (value.revision == old.revision && value.transcriptRevision >= old.transcriptRevision)
    }
    func acceptPublishedSession(_ value: CloudSession, scope expectedScope: CloudScope, epoch: Int64) async {
        guard let scope, scope == expectedScope, scope.campId == value.campId, let cache else { return }
        let token = generation
        do {
            try await cache.ingest(scope: scope, session: value, expectedEpoch: epoch)
            guard token == generation else { return }
            if accepts(value) { sessionIndex[value.id] = value }
            schedulePublish(generation: token)
        } catch { self.error = error.localizedDescription }
    }
    func acceptWorkerRequest(_ value: CloudRequest) async {
        guard let scope, value.campId == scope.campId, let cache else { return }
        let token = generation
        do {
            try await cache.ingest(scope: scope, request: value, expectedEpoch: cacheEpoch)
            guard token == generation else { return }
            if value.revision >= (requestIndex[value.id]?.revision ?? 0) { requestIndex[value.id] = value }
            schedulePublish(generation: token)
        } catch { self.error = error.localizedDescription }
    }
    private func schedulePublish(generation token: UUID) {
        guard publishTask == nil else { return }
        publishTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(50))
            guard let self, self.generation == token else { return }; self.publishTask = nil
            guard !Task.isCancelled else { return }
            self.sessions = self.sessionIndex.values.sorted { $0.updatedAt > $1.updatedAt }
            self.requests = self.requestIndex.values.sorted { $0.createdAt > $1.createdAt }
            self.sessionsChanged?(self.sessions, self.projects, self.scope)
        }
    }
    private func clearVisible() {
        sessions = []; projects = []; members = []; requests = []; pending = []
        sessionIndex = [:]; requestIndex = [:]; sessionsChanged?([], [], nil)
    }
    private func renewIfNeeded() async throws {
        guard let client, var saved = credential,
              Date().timeIntervalSince(lastRenewal) > 24 * 60 * 60 || saved.expiresAt.timeIntervalSinceNow < 7 * 24 * 60 * 60 else { return }
        struct Renewal: Decodable, Sendable { var expiresAt: Date }
        let data = try await client.data("POST", path: "/v1/auth/renew", token: saved.token)
        let renewal = try CampWire.decoder().decode(Renewal.self, from: data)
        guard credential?.token == saved.token else { throw CancellationError() }
        saved.expiresAt = renewal.expiresAt
        try vault.save(saved); credential = saved; lastRenewal = Date()
    }

    func createCamp(name: String) async {
        guard let client, let credential else { return }
        do {
            let data = try await client.data("POST", path: "/v1/camps", body: CampWire.body(["name": name]), key: UUID().uuidString, token: credential.token)
            let camp = try CampWire.decoder().decode(CloudCamp.self, from: data); selectedCampId = camp.id; await refresh()
        } catch { self.error = error.localizedDescription }
    }
    func createProject(name: String) async {
        guard let scope, let client, let credential else { return }
        do {
            _ = try await client.data("POST", path: "/v1/camps/\(scope.campId)/projects", body: CampWire.body(["name": name]), key: UUID().uuidString, token: credential.token)
            await refresh()
        } catch { self.error = error.localizedDescription }
    }
    func invite(email: String, role: String) async -> String? {
        guard let scope, let client, let credential else { return nil }
        do {
            let data = try await client.data("POST", path: "/v1/camps/\(scope.campId)/invitations", body: CampWire.body(["email": email, "role": role]), key: UUID().uuidString, token: credential.token)
            return try CampWire.decoder().decode(CloudInvitation.self, from: data).token
        } catch { self.error = error.localizedDescription; return nil }
    }
    func join(token: String) async {
        guard let client, let credential else { return }
        do {
            _ = try await client.data("POST", path: "/v1/invitations/accept", body: CampWire.body(["token": token]), key: UUID().uuidString, token: credential.token)
            await refresh()
        } catch { self.error = error.localizedDescription }
    }
    func queuePrompt(session: CloudSession, text: String, model: String, tuning: ModelTuning, sources: [CloudSourceRef] = []) async throws {
        guard let scope, let cache else { throw CampCloudError.cacheUnavailable }
        let refs = try JSONSerialization.jsonObject(with: CampWire.encoder().encode(sources))
        let body = try CampWire.body(["prompt": text, "model": model, "effort": tuning.effort, "speed": tuning.speed, "sources": refs])
        _ = try await cache.enqueue(scope: scope, method: "POST", path: "/v1/camps/\(scope.campId)/sessions/\(session.id)/requests", body: body, expectedEpoch: cacheEpoch)
        pending = try await cache.outbox(scope); flush()
    }
    func context(session: CloudSession, source: CloudSourceRef) async throws -> CloudTranscript {
        guard let scope, let client, let credential, session.campId == scope.campId,
              session.id == source.sessionId, session.permissionRevision == source.permissionRevision else { throw CampCloudError.invalidResponse }
        let origin = scope
        let data = try await client.data("POST", path: "/v1/camps/\(scope.campId)/sessions/\(session.id)/context", body: CampWire.encoder().encode(source), token: credential.token)
        guard self.scope == origin, self.credential?.token == credential.token else { throw CancellationError() }
        return try CampWire.decoder().decode(CloudTranscript.self, from: data)
    }
    func queueStage(session: CloudSession, stage: Stage) async throws {
        guard let scope, let cache else { throw CampCloudError.cacheUnavailable }
        let body = try CampWire.body(["stage": stage.rawValue, "expected_revision": session.revision])
        _ = try await cache.enqueue(scope: scope, method: "PATCH", path: "/v1/camps/\(scope.campId)/sessions/\(session.id)/board", body: body, expectedEpoch: cacheEpoch)
        pending = try await cache.outbox(scope); flush()
    }
    func queueAction(request: CloudRequest, action: String) async {
        guard let scope, let cache, ["approve", "cancel"].contains(action) else { return }
        do {
            _ = try await cache.enqueue(scope: scope, method: "POST", path: "/v1/camps/\(scope.campId)/requests/\(request.id)/actions", body: CampWire.body(["action": action]), expectedEpoch: cacheEpoch)
            pending = try await cache.outbox(scope); flush()
        } catch { self.error = error.localizedDescription }
    }
    func flush() {
        guard flushTask == nil, let scope, let client, let credential, let cache else { return }
        let token = generation
        let epoch = cacheEpoch
        flushTask = Task { [weak self] in
            defer { if self?.generation == token { self?.flushTask = nil } }
            do {
                for item in try await cache.outbox(scope, pendingOnly: true) {
                    guard !Task.isCancelled, self?.generation == token else { return }
                    try await cache.markOutbox(item.id, state: "sending")
                    do {
                        let data = try await client.data(item.method, path: item.path, body: item.body, key: item.id, token: credential.token)
                        if let request = try? CampWire.decoder().decode(CloudRequest.self, from: data) {
                            try await cache.ingest(scope: scope, request: request, expectedEpoch: epoch)
                            if self?.generation == token { self?.requestIndex[request.id] = request; self?.requestChanged?(request) }
                        }
                        if let session = try? CampWire.decoder().decode(CloudSession.self, from: data) {
                            try await cache.ingest(scope: scope, session: session, expectedEpoch: epoch)
                            if self?.generation == token, self?.accepts(session) == true { self?.sessionIndex[session.id] = session }
                        }
                        try await cache.acknowledge(item.id)
                    } catch CampCloudError.server(let status, let code) where status == 409 || status == 400 || status == 404 {
                        try await cache.markOutbox(item.id, state: status == 409 ? "conflict" : "failed", error: code)
                    } catch {
                        try await cache.markOutbox(item.id, state: "pending")
                        throw error
                    }
                }
                guard self?.generation == token else { return }
                self?.pending = try await cache.outbox(scope); self?.schedulePublish(generation: token)
            } catch {
                guard self?.generation == token else { return }
                self?.status = "Camp offline"; self?.error = error.localizedDescription
                self?.pending = (try? await cache.outbox(scope)) ?? []
            }
        }
    }
    func transcript(session: CloudSession, before: Int64 = 0, revision: Int64 = 0) async throws -> CloudTranscript {
        guard let scope, let client, let credential, let cache else { throw CampCloudError.cacheUnavailable }
        let epoch = cacheEpoch
        let value = try await client.get(CloudTranscript.self, path: "/v1/camps/\(scope.campId)/sessions/\(session.id)/transcript?limit=120&before=\(before)&revision=\(revision)", token: credential.token)
        guard self.scope == scope, self.credential?.token == credential.token else { throw CancellationError() }
        try await cache.saveTranscript(value, scope: scope, sessionId: session.id, before: before, expectedPermissionRevision: session.permissionRevision, expectedEpoch: epoch); return value
    }
    func cachedTranscript(session: CloudSession) async -> [CloudMessage] {
        guard let scope, let cache else { return [] }; return (try? await cache.transcript(scope, sessionId: session.id)) ?? []
    }
    func signOut() async {
        guard let client, let credential else { return }
        do {
            do { _ = try await client.data("POST", path: "/v1/auth/logout", token: credential.token) }
            catch CampCloudError.server(401, _) {} // Already revoked on the server.
            try vault.remove(origin: credential.origin)
            if let scope { try await cache?.clear(scope) }
            generation = UUID(); streamTask?.cancel(); flushTask?.cancel(); publishTask?.cancel()
            self.credential = nil; self.scope = nil; signedIn = false; camps = []; status = "Local on this Mac"; error = ""; clearVisible()
        } catch { self.error = "Could not revoke the Camp login. Reconnect and try signing out again" }
    }
}
