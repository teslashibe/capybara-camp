import Foundation

struct CloudGitConnection: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var ownerUserId: String
    var githubUserId: Int64?
    var login: String
    var state: String
    var revision: Int64
    var epoch: Int64
    var accessExpiresAt: Date?
    func valid(owner: String) -> Bool {
        UUID(uuidString: id) != nil && ownerUserId == owner && revision > 0 && epoch > 0 &&
        ["pending", "connected", "needs_login", "disconnected"].contains(state) &&
        login.utf8.count <= 39 && (login.isEmpty || login.range(of: "^[A-Za-z0-9][A-Za-z0-9-]*$", options: .regularExpression) != nil) &&
        (state != "connected" || (githubUserId ?? 0) > 0 && !login.isEmpty)
    }
    var statusLabel: String {
        switch state { case "connected": return "Connected"; case "needs_login": return "Sign in again"; case "disconnected": return "Disconnected"; default: return "Not signed in" }
    }
}
struct CloudGitLogin: Codable, Equatable, Sendable {
    var id: String
    var connectionId: String
    var state: String
    var failureCode: String?
    var expiresAt: Date
    var url: String?
    var active: Bool { ["waiting", "exchanging"].contains(state) }
    func valid(connection: String, origin: String) -> Bool {
        UUID(uuidString: id) != nil && connectionId == connection &&
        ["waiting", "exchanging", "connected", "cancelled", "expired", "failed"].contains(state) &&
        [nil, "", "provider_rejected", "identity_mismatch", "connection_changed", "exchange_unknown"].contains(failureCode) &&
        (state == "waiting" ? GitHubURL.authorization(url ?? "", origin: origin) : (url ?? "").isEmpty)
    }
}
struct CloudGitStatus: Codable, Sendable { var connection: CloudGitConnection; var login: CloudGitLogin? }
struct CloudGitOverview: Codable, Sendable { var connections: [CloudGitConnection]; var installUrl: String }
struct CloudGitUser: Codable, Sendable { var id: Int64; var login: String }
struct CloudGitInstallation: Codable, Identifiable, Sendable {
    var id: Int64; var account: CloudGitUser; var contents: String; var pullRequests: String; var suspended: Bool
}
struct CloudGitInstallations: Codable, Sendable { var installations: [CloudGitInstallation]; var nextPage: Int? }
struct CloudGitRepository: Codable, Identifiable, Sendable {
    var id: Int64; var installationId: Int64; var fullName: String; var defaultBranch: String
    var `private`: Bool; var read: Bool; var write: Bool; var pullRequests: Bool
    var url: String { "https://github.com/" + fullName }
}
struct CloudGitRepositories: Codable, Sendable { var repositories: [CloudGitRepository]; var nextPage: Int? }

enum GitHubURL {
    static func authorization(_ value: String, origin: String) -> Bool {
        guard value.utf8.count <= 4096, let c = URLComponents(string: value), c.scheme == "https", c.host == "github.com",
              c.user == nil, c.password == nil, c.port == nil, c.fragment == nil, c.percentEncodedPath == "/login/oauth/authorize" else { return false }
        let items = c.queryItems ?? []
        let allowed = Set(["client_id", "redirect_uri", "state", "scope", "code_challenge", "code_challenge_method", "prompt"])
        guard Set(items.map(\.name)).count == items.count, items.allSatisfy({ allowed.contains($0.name) }) else { return false }
        let q = Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
        return !(q["client_id"] ?? "").isEmpty && q["redirect_uri"] == origin + "/v1/git/github/callback" &&
        q["scope"] == "offline_access" && q["code_challenge_method"] == "S256" && q["prompt"] == "select_account" &&
        [q["state"] ?? "", q["code_challenge"] ?? ""].allSatisfy { $0.range(of: "^[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil }
    }
    static func installation(_ value: String) -> Bool {
        guard let c = URLComponents(string: value), c.scheme == "https", c.host == "github.com", c.user == nil, c.password == nil,
              c.port == nil, c.query == nil, c.fragment == nil else { return false }
        return c.percentEncodedPath.range(of: "^/apps/[a-z0-9][a-z0-9-]{0,99}/installations/new$", options: .regularExpression) != nil
    }
}

@MainActor final class CampGitConnections: ObservableObject {
    private struct Intent: Codable { var connectionId: String; var key: String }
    private struct Journal: Codable { var selectedId = ""; var pending: Intent? }
    private struct Connection {
        var transport: any CampAccountTransport; var credential: CloudCredential; var generation = UUID()
        var key: String { "git-setup:" + credential.origin + ":" + credential.actor.userId }
    }
    @Published private(set) var connections: [CloudGitConnection] = []
    @Published private(set) var detail: CloudGitStatus?
    @Published private(set) var error = ""
    @Published private(set) var working = false
    @Published private(set) var available = true
    @Published private(set) var needsSignIn = false
    @Published private(set) var installURL = ""
    @Published private(set) var installations: [CloudGitInstallation] = []
    @Published private(set) var repositories: [CloudGitRepository] = []
    @Published private(set) var selectedInstallation: Int64 = 0
    @Published private(set) var installationPage = 1
    @Published private(set) var nextInstallationPage: Int?
    @Published private(set) var repositoryPage = 1
    @Published private(set) var nextRepositoryPage: Int?
    @Published private(set) var selectedId = ""
    private let cache: CampCache?
    private var connection: Connection?
    private var journal = Journal()
    private var loaded = false
    private var polling = false
    var pending: Bool { journal.pending != nil }
    var selection: CloudGitConnection? { connections.first { $0.id == selectedId } }
    var authorizationURL: URL? {
        guard let c = connection, let value = detail?.login?.url, GitHubURL.authorization(value, origin: c.credential.origin) else { return nil }
        return URL(string: value)
    }
    init(cache: CampCache?) { self.cache = cache }
    func attach(transport: (any CampAccountTransport)?, credential: CloudCredential?) {
        if let c = connection, let credential, c.credential.origin == credential.origin, c.credential.actor.userId == credential.actor.userId, c.credential.token == credential.token { return }
        connection = nil; connections = []; detail = nil; error = ""; working = false; available = true; needsSignIn = false
        selectedId = ""; journal = Journal(); loaded = false; polling = false; installURL = ""; clearRepositories()
        if let transport, let credential { connection = Connection(transport: transport, credential: credential) }
    }
    private func check(_ c: Connection) throws { guard connection?.generation == c.generation else { throw CancellationError() } }
    private func load(_ c: Connection) async throws {
        guard !loaded else { return }; guard let cache else { throw CampCloudError.cacheUnavailable }
        let raw = try await cache.metadata(c.key); try check(c)
        let value = try raw.map { try CampWire.decoder().decode(Journal.self, from: $0) } ?? Journal()
        guard value.selectedId.isEmpty || UUID(uuidString: value.selectedId) != nil,
              value.pending == nil || (UUID(uuidString: value.pending!.key) != nil && (value.pending!.connectionId.isEmpty || UUID(uuidString: value.pending!.connectionId) != nil)) else { throw CampCloudError.invalidResponse }
        journal = value; selectedId = value.selectedId; loaded = true
    }
    private func save(_ value: Journal, _ c: Connection) async throws {
        try check(c); guard let cache else { throw CampCloudError.cacheUnavailable }
        try await cache.setMetadata(c.key, CampWire.encoder().encode(value)); try check(c); journal = value
    }
    private func call<T: Decodable>(_ type: T.Type, _ method: String, _ path: String, body: Data? = nil, key: String? = nil, _ c: Connection) async throws -> T {
        let raw = try await c.transport.data(method, path: path, body: body, key: key, token: c.credential.token)
        try check(c); return try CampWire.decoder().decode(type, from: raw)
    }
    private func read(_ c: Connection) async throws {
        let value = try await call(CloudGitOverview.self, "GET", "/v1/git/connections", c)
        guard value.connections.count <= 8, Set(value.connections.map(\.id)).count == value.connections.count,
              value.connections.allSatisfy({ $0.valid(owner: c.credential.actor.userId) }), GitHubURL.installation(value.installUrl) else { throw CampCloudError.invalidResponse }
        connections = value.connections; installURL = value.installUrl; available = true
        if !connections.contains(where: { $0.id == selectedId }) { selectedId = connections.first?.id ?? ""; clearRepositories() }
        if !selectedId.isEmpty {
            let value = try await call(CloudGitStatus.self, "GET", "/v1/git/connections/" + selectedId, c)
            guard value.connection.id == selectedId, value.connection.valid(owner: c.credential.actor.userId), value.login?.valid(connection: selectedId, origin: c.credential.origin) ?? true else { throw CampCloudError.invalidResponse }
            detail = value
            if let index = connections.firstIndex(where: { $0.id == selectedId }) { connections[index] = value.connection }
            if value.connection.state != "connected" { clearRepositories() }
        } else { detail = nil }
        if journal.selectedId != selectedId { var next = journal; next.selectedId = selectedId; try await save(next, c) }
        error = ""; needsSignIn = false
    }
    func refresh() async {
        guard !working, !polling, let c = connection else { return }; polling = true
        defer { if connection?.generation == c.generation { polling = false } }
        do { try await load(c); if journal.pending != nil { try await finishBegin(c) }; try await read(c) }
        catch is CancellationError {} catch { present(error, c) }
    }
    func select(_ id: String) {
        guard !working, !pending, id != selectedId, connections.contains(where: { $0.id == id }) else { return }
        connection?.generation = UUID(); polling = false; selectedId = id; detail = nil; clearRepositories()
    }
    private func clearRepositories() { installations = []; repositories = []; selectedInstallation = 0; installationPage = 1; nextInstallationPage = nil; repositoryPage = 1; nextRepositoryPage = nil }
    private func mutate(_ action: (Connection) async throws -> Void) async {
        guard !working, let initial = connection else { return }; working = true
        defer { if connection?.generation == initial.generation { working = false } }
        do {
            while polling { try await Task.sleep(for: .milliseconds(25)); try check(initial) }
            try await load(initial); error = ""; needsSignIn = false
            try await action(initial)
        } catch is CancellationError {} catch { present(error, initial) }
    }
    private func finishBegin(_ c: Connection) async throws {
        guard let intent = journal.pending else { return }
        let login = try await call(CloudGitLogin.self, "POST", "/v1/git/logins", body: CampWire.body(["connection_id": intent.connectionId]), key: intent.key, c)
        guard UUID(uuidString: login.connectionId) != nil, login.valid(connection: login.connectionId, origin: c.credential.origin), intent.connectionId.isEmpty || intent.connectionId == login.connectionId else { throw CampCloudError.invalidResponse }
        selectedId = login.connectionId; var next = journal; next.pending = nil; next.selectedId = selectedId
        try await save(next, c); try await read(c)
    }
    func begin(reconnect: Bool = false) async {
        await mutate { c in
            if self.journal.pending == nil {
                var next = self.journal; next.pending = Intent(connectionId: reconnect ? self.selectedId : "", key: UUID().uuidString)
                try await self.save(next, c)
            }
            try await self.finishBegin(c)
        }
    }
    func cancel() async {
        await mutate { c in
            if self.journal.pending != nil { try await self.finishBegin(c) }
            guard let login = self.detail?.login, login.active else { return }
            _ = try await self.call(CloudGitLogin.self, "POST", "/v1/git/connections/\(self.selectedId)/logins/\(login.id)/cancel", key: UUID().uuidString, c)
            try await self.read(c)
        }
    }
    func disconnect() async {
        await mutate { c in
            guard !self.selectedId.isEmpty else { return }
            _ = try await self.call(CloudGitConnection.self, "POST", "/v1/git/connections/\(self.selectedId)/disconnect", key: UUID().uuidString, c)
            self.clearRepositories(); try await self.read(c)
        }
    }
    func loadInstallations(page: Int = 1) async {
        await mutate { c in
            guard self.selection?.state == "connected", (1...10000).contains(page) else { return }
            let value = try await self.call(CloudGitInstallations.self, "GET", "/v1/git/connections/\(self.selectedId)/installations?page=\(page)", c)
            guard value.installations.count <= 100, Set(value.installations.map(\.id)).count == value.installations.count,
                  value.installations.allSatisfy({ $0.id > 0 && $0.account.id > 0 && !$0.account.login.isEmpty && $0.account.login.utf8.count <= 39 }), value.nextPage == nil || value.nextPage == page + 1 else { throw CampCloudError.invalidResponse }
            self.installations = value.installations; self.installationPage = page; self.nextInstallationPage = value.nextPage
            self.repositories = []; self.selectedInstallation = 0
        }
    }
    func loadRepositories(installation: Int64, page: Int = 1) async {
        await mutate { c in
            guard let value = self.installations.first(where: { $0.id == installation }), !value.suspended, (1...10000).contains(page) else { return }
            let repos = try await self.call(CloudGitRepositories.self, "GET", "/v1/git/connections/\(self.selectedId)/installations/\(installation)/repositories?installation_page=\(self.installationPage)&page=\(page)", c)
            guard repos.repositories.count <= 100, Set(repos.repositories.map(\.id)).count == repos.repositories.count,
                  repos.repositories.allSatisfy({ $0.id > 0 && $0.installationId == installation && $0.fullName.range(of: "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$", options: .regularExpression) != nil }), repos.nextPage == nil || repos.nextPage == page + 1 else { throw CampCloudError.invalidResponse }
            self.repositories = repos.repositories; self.selectedInstallation = installation; self.repositoryPage = page; self.nextRepositoryPage = repos.nextPage
        }
    }
    private func present(_ error: Error, _ c: Connection) {
        guard connection?.generation == c.generation else { return }
        switch error {
        case CampCloudError.server(503, "git_not_configured"): available = false; self.error = "GitHub is not configured on this Camp server"
        case CampCloudError.server(401, _): needsSignIn = true; self.error = "Sign in to Camp again to manage GitHub"
        case CampCloudError.server(409, "git_sign_in_required"): self.error = "Reconnect this GitHub account to continue"
        case CampCloudError.server(403, "git_access_unavailable"): self.error = "Check the app installation and your organization's GitHub access"
        case CampCloudError.server(404, _): self.error = "This GitHub account or repository is no longer available"
        case CampCloudError.invalidResponse: self.error = "The server returned an invalid GitHub response"
        default: self.error = "GitHub could not be reached. Your sign-in request is saved for retry"
        }
    }
}
