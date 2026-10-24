import Foundation

struct CloudExecutionTarget: Codable, Equatable, Sendable {
    var location: String
    var epoch: Int64
    var workspaceId: String?
    var mode: String
    var permissions: [String: String]
    var available: Bool
    var requiresTransfer: Bool? = nil
    var valid: Bool {
        ["local", "cloud"].contains(location) && epoch > 0 && ["chat", "implementation"].contains(mode) &&
        (location == "local" ? workspaceId == nil : workspaceId.flatMap(UUID.init(uuidString:)) != nil)
    }
}
struct CloudWorkspace: Codable, Identifiable, Equatable, Sendable {
    var id: String
    var campId: String
    var sessionId: String
    var projectId: String
    var ownerUserId: String
    var providerAccountId: String
    var providerInstallationId: String
    var providerEpoch: Int64
    var gitConnectionId: String
    var gitEpoch: Int64
    var githubUserId: Int64
    var githubLogin: String
    var githubInstallationId: Int64
    var repositoryId: Int64
    var repositoryName: String
    var requestedRef: String
    var baseCommit: String
    var branch: String
    var purpose: String
    var sourceRevision: Int64
    var projectRevision: Int64
    var state: String
    var revision: Int64
    var createdAt: Date
    var updatedAt: Date
    var failureCode: String?
    var transferId: String? = nil
    var remoteBaseCommit: String? = nil
    func valid(session: CloudSession, owner: String) -> Bool {
        [id, providerAccountId, providerInstallationId, gitConnectionId].allSatisfy { UUID(uuidString: $0) != nil } &&
        campId == session.campId && sessionId == session.id && UUID(uuidString: projectId) != nil && ownerUserId == owner &&
        providerEpoch > 0 && gitEpoch > 0 && githubUserId > 0 && githubInstallationId > 0 && repositoryId > 0 && revision > 0 && sourceRevision > 0 && projectRevision > 0 &&
        ["queued", "preparing", "ready", "failed", "cancel_requested", "cancelled"].contains(state) &&
        ["planning", "implementation"].contains(purpose) && baseCommit.range(of: "^[a-f0-9]{40}$", options: .regularExpression) != nil &&
        branch == "camp/\(sessionId)/\(id)" && repositoryName.utf8.count <= 140 && requestedRef.utf8.count <= 512
    }
}
struct CloudWorkspaceInput: Codable, Sendable {
    var providerAccountId: String
    var gitConnectionId: String
    var githubInstallationId: Int64
    var repositoryId: Int64
    var installationPage: Int
    var repositoryPage: Int
    var ref: String
    var purpose: String
    var expectedSessionRevision: Int64
    var expectedProjectRevision: Int64
}

@MainActor final class CampExecution: ObservableObject {
    private struct Connection {
        var scope: CloudScope
        var credential: CloudCredential
        var transport: any CampAccountTransport
        var generation = UUID()
        var epoch: Int64?
        var key: String { "execution-setup:" + scope.key }
    }
    private struct Intent: Codable {
        var id: String
        var sessionId: String
        var method: String
        var path: String
        var body: Data
        var kind: String
    }
    private struct Journal: Codable { var pending: Intent? }
    @Published private(set) var target: CloudExecutionTarget?
    @Published private(set) var workspaces: [CloudWorkspace] = []
    @Published private(set) var sessionID: String?
    @Published private(set) var working = false
    @Published private(set) var error = ""
    @Published private(set) var pending = false
    var pendingSessionID: String? { journal.pending?.sessionId }
    var changed: ((String) -> Void)?
    private let cache: CampCache?
    private var connection: Connection?
    private var journal = Journal()
    private var loaded = false
    init(cache: CampCache?) { self.cache = cache }
    func attach(scope: CloudScope?, credential: CloudCredential?, transport: (any CampAccountTransport)?) {
        if let c = connection, c.scope == scope, c.credential.token == credential?.token { return }
        connection = nil; target = nil; workspaces = []; sessionID = nil; working = false; error = ""; loaded = false; journal = Journal(); pending = false
        if let scope, let credential, let transport { connection = Connection(scope: scope, credential: credential, transport: transport) }
    }
    private func check(_ c: Connection) throws {
        guard connection?.generation == c.generation,!Task.isCancelled else { throw CancellationError() }
    }
    private func load(_ c: Connection) async throws {
        guard !loaded, let cache else { return }
        let epoch = try await cache.epoch(c.scope); try check(c)
        let raw = try await cache.metadata(c.key); try check(c)
        let value = try raw.map { try CampWire.decoder().decode(Journal.self, from: $0) } ?? Journal()
        if let i = value.pending {
            guard UUID(uuidString: i.id) != nil, UUID(uuidString: i.sessionId) != nil,
                  i.body.count <= 65536, (try? JSONSerialization.jsonObject(with: i.body)) is [String: Any] else { throw CampCloudError.invalidResponse }
            let sessionPath = "/v1/camps/\(c.scope.campId)/sessions/\(i.sessionId)"
            switch i.kind {
            case "prepare": guard i.method == "POST", i.path == sessionPath + "/workspaces" else { throw CampCloudError.invalidResponse }
            case "assign": guard i.method == "PUT", i.path == sessionPath + "/execution" else { throw CampCloudError.invalidResponse }
            case "cancel":
                let pieces = i.path.split(separator: "/")
                guard i.method == "POST", pieces.count == 6, pieces[0] == "v1", pieces[1] == "camps", pieces[2] == c.scope.campId,
                      pieces[3] == "workspaces", UUID(uuidString: String(pieces[4])) != nil, pieces[5] == "cancel" else { throw CampCloudError.invalidResponse }
            default: throw CampCloudError.invalidResponse
            }
        }
        guard try await cache.epoch(c.scope) == epoch else { throw CancellationError() }
        try check(c); connection?.epoch = epoch
        journal = value; pending = journal.pending != nil; loaded = true
    }
    private func save(_ value: Journal, _ c: Connection) async throws {
        guard let cache else { throw CampCloudError.cacheUnavailable }
        try check(c)
        guard let epoch = connection?.epoch else { throw CampCloudError.cacheUnavailable }
        try await cache.saveExecutionJournal(CampWire.encoder().encode(value), scope: c.scope, expectedEpoch: epoch); try check(c)
        journal = value; pending = value.pending != nil
    }
    private func read(_ session: CloudSession, _ c: Connection) async throws {
        guard session.campId == c.scope.campId, session.ownerUserId == c.credential.actor.userId else { throw CampCloudError.invalidResponse }
        let raw = try await c.transport.data("GET", path: "/v1/camps/\(c.scope.campId)/sessions/\(session.id)/execution", body: nil, key: nil, token: c.credential.token)
        try check(c)
        let value = try CampWire.decoder().decode(CloudExecutionTarget.self, from: raw)
        guard value.valid else { throw CampCloudError.invalidResponse }
        let list = try await c.transport.data("GET", path: "/v1/camps/\(c.scope.campId)/sessions/\(session.id)/workspaces", body: nil, key: nil, token: c.credential.token)
        try check(c)
        let spaces = try CampWire.decoder().decode([CloudWorkspace].self, from: list)
        guard spaces.count <= 25, Set(spaces.map(\.id)).count == spaces.count, spaces.allSatisfy({ $0.valid(session: session, owner: c.credential.actor.userId) }) else { throw CampCloudError.invalidResponse }
        target = value; workspaces = spaces; sessionID = session.id
    }
    private func finish(_ c: Connection, session: CloudSession) async throws {
        guard let i = journal.pending, i.sessionId == session.id else { throw CampCloudError.invalidResponse }
        let data = try await c.transport.data(i.method, path: i.path, body: i.body, key: i.id, token: c.credential.token)
        try check(c)
        if i.kind == "assign" {
            guard try CampWire.decoder().decode(CloudExecutionTarget.self,from:data).valid else {throw CampCloudError.invalidResponse}
        } else {
            let result=try CampWire.decoder().decode(CloudWorkspace.self,from:data)
            guard result.valid(session: session, owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
        }
        try check(c); try await save(Journal(), c); changed?(i.sessionId)
    }
    func refresh(_ session: CloudSession) async {
        guard !working, let c = connection else { return }; working = true
        defer { if connection?.generation == c.generation { working = false } }
        do { try await load(c); try await read(session, c); error = "" }
        catch is CancellationError {} catch { if connection?.generation==c.generation {present(error)} }
    }
    func retry(_ session: CloudSession) async {
        await mutate(session) { c in
            guard self.journal.pending?.sessionId==session.id else{throw CampCloudError.invalidResponse}
            try await self.finish(c, session: session)
        }
    }
    private func mutate(_ session: CloudSession, operation: (Connection) async throws -> Void) async {
        guard !working, let c = connection else { return }; working = true
        defer { if connection?.generation == c.generation { working = false } }
        do {
            guard session.campId == c.scope.campId, session.ownerUserId == c.credential.actor.userId, UUID(uuidString: session.id) != nil else { throw CampCloudError.invalidResponse }
            try await load(c); try check(c); error = ""; try await operation(c); try await read(session, c)
        } catch is CancellationError {} catch {
            guard connection?.generation == c.generation else { return }
            if case CampCloudError.server(let status, _) = error, [400, 403, 404, 409, 422].contains(status) {
                // A definitive rejection cannot be fixed by replaying the same
                // request. Read current state and require a fresh review.
                do { try await save(Journal(), c) } catch { present(error); return }
                try? await read(session, c)
            }
            if connection?.generation == c.generation { present(error) }
        }
    }
    func prepare(_ session: CloudSession, input: CloudWorkspaceInput) async {
        await mutate(session) { c in
            guard self.journal.pending == nil, input.expectedSessionRevision == session.revision else { throw CampCloudError.invalidResponse }
            let intent = Intent(id: UUID().uuidString.lowercased(), sessionId: session.id, method: "POST", path: "/v1/camps/\(c.scope.campId)/sessions/\(session.id)/workspaces", body: try CampWire.encoder().encode(input), kind: "prepare")
            try await self.save(Journal(pending: intent), c); try await self.finish(c, session: session)
        }
    }
    func assign(_ session: CloudSession, workspace: CloudWorkspace?, permissions: PermissionSettings, approved: Bool) async {
        await mutate(session) { c in
            guard self.journal.pending == nil, let target = self.target, self.sessionID == session.id else { throw CampCloudError.invalidResponse }
            var body: [String: Any] = ["location": workspace == nil ? "local" : "cloud", "expected_epoch": target.epoch,
                "expected_session_revision": session.revision, "expected_transcript_revision": session.transcriptRevision, "expected_workspace_revision": workspace?.revision ?? 0,
                "execution_approved": approved]
            if let w = workspace {
                guard w.state == "ready", w.sourceRevision == session.revision, w.projectId == session.projectId, w.valid(session: session, owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
                body["workspace_id"] = w.id
                if w.purpose == "implementation" {
                    guard approved, permissions.valid(for: session.provider) else { throw CampCloudError.invalidResponse }
                    var values = ["profile": permissions.profile, "sandbox": permissions.sandbox, "approval": permissions.approval, "reviewer": permissions.reviewer, "claudeMode": permissions.claudeMode]
                    values = values.filter { !$0.value.isEmpty }; body["permissions"] = values
                }
            }
            let intent = Intent(id: UUID().uuidString.lowercased(), sessionId: session.id, method: "PUT", path: "/v1/camps/\(c.scope.campId)/sessions/\(session.id)/execution", body: try CampWire.body(body), kind: "assign")
            try await self.save(Journal(pending: intent), c); try await self.finish(c, session: session)
        }
    }
    func cancel(_ workspace: CloudWorkspace, session: CloudSession) async {
        await mutate(session) { c in
            guard self.journal.pending == nil, workspace.valid(session: session, owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
            let intent = Intent(id: UUID().uuidString.lowercased(), sessionId: session.id, method: "POST", path: "/v1/camps/\(c.scope.campId)/workspaces/\(workspace.id)/cancel", body: try CampWire.body(["expected_revision": workspace.revision]), kind: "cancel")
            try await self.save(Journal(pending: intent), c); try await self.finish(c, session: session)
        }
    }
    private func present(_ error: Error) {
        switch error {
        case CampCloudError.server(409, _): self.error = "This session or workspace changed · Refresh its status before retrying"
        case CampCloudError.server(403, _), CampCloudError.server(404, _): self.error = "This account or workspace is no longer available to you"
        case CampCloudError.server(401, _): self.error = "Sign in to Camp again"
        case CampCloudError.invalidResponse: self.error = "The execution settings could not be validated"
        default: self.error = "Cloud setup could not be confirmed · Your request is saved for retry"
        }
    }
}
