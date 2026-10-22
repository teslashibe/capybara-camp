import Foundation

struct CloudProviderAccount: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var ownerUserId: String
    var provider: String
    var label: String
    var verifiedSubject: String?
    var revision: Int64
    var installationId: String
    var location: String
    var state: String
    var epoch: Int64
    var credentialExpiresAt: Date?
    var workloadState: String? = nil
    var workloadError: String? = nil

    func valid(owner: String) -> Bool {
        UUID(uuidString: id) != nil && ownerUserId == owner && UUID(uuidString: installationId) != nil &&
        ["Codex", "Claude"].contains(provider) && location == "cloud" && revision > 0 && epoch > 0 &&
        ["pending", "connected", "needs_login", "disconnecting", "disconnected"].contains(state) &&
        !label.isEmpty && label.utf8.count <= 120 && !label.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) &&
        (verifiedSubject?.utf8.count ?? 0) <= 256 &&
        (workloadState == nil || ["pending", "starting", "ready", "erasing", "erased", "failed"].contains(workloadState!)) &&
        (workloadError == nil || ["", "capacity", "manual_grant", "cluster_unavailable", "resource_conflict", "workload_pending", "workload_failed"].contains(workloadError!))
    }
    var statusLabel: String {
        switch state {
        case "connected": return "Connected"
        case "needs_login": return "Sign in again"
        case "disconnecting": return "Disconnecting"
        case "disconnected": return "Disconnected"
        default: return "Not signed in"
        }
    }
    var workloadMessage: String? {
        switch workloadError {
        case "capacity": return "All cloud runner slots are in use"
        case "manual_grant": return "This account has a manually configured runner"
        case "cluster_unavailable": return "The cloud runner service is unavailable"
        case "resource_conflict": return "The cloud runner needs an operator to review its resources"
        case "workload_pending": return "Preparing the cloud runner"
        case "workload_failed": return "The cloud runner could not start"
        default: return nil
        }
    }
}

struct CloudProviderLogin: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var installationId: String
    var state: String
    var failureCode: String?
    var url: String?
    var userCode: String?
    var codeSubmitted: Bool?
    var codeConsumed: Bool?
    var expiresAt: Date
    var active: Bool { ["queued", "running", "waiting"].contains(state) }

    func valid(account: CloudProviderAccount) -> Bool {
        guard UUID(uuidString: id) != nil, installationId == account.installationId,
              ["queued", "running", "waiting", "connected", "failed", "cancelled", "expired"].contains(state),
              [nil, "", "provider_rejected", "identity_mismatch", "runner_replaced"].contains(failureCode) else { return false }
        if state == "waiting" { return ProviderSignInURL.valid(provider: account.provider, value: url ?? "", code: userCode ?? "") }
        return (url ?? "").isEmpty && (userCode ?? "").isEmpty
    }
}

struct CloudProviderStatus: Codable, Equatable, Sendable {
    var account: CloudProviderAccount
    var login: CloudProviderLogin?
    func valid(owner: String) -> Bool { account.valid(owner: owner) && (login?.valid(account: account) ?? true) }
}

// Match the backend's provider allowlist before offering a browser link. This
// protects the desktop even when a proxy returns an unexpected response.
enum ProviderSignInURL {
    static func valid(provider: String, value: String, code: String) -> Bool {
        guard value.utf8.count <= 4096, let c = URLComponents(string: value), c.scheme == "https",
              c.user == nil, c.password == nil, c.port == nil, c.fragment == nil else { return false }
        if provider == "Codex" {
            return c.host == "auth.openai.com" && c.percentEncodedPath == "/codex/device" && c.query == nil &&
                (4...32).contains(code.count) && code.unicodeScalars.allSatisfy { (65...90).contains($0.value) || (48...57).contains($0.value) || $0.value == 45 }
        }
        guard provider == "Claude", code.isEmpty,
              (c.host == "claude.com" && c.percentEncodedPath == "/cai/oauth/authorize") || (c.host == "claude.ai" && c.percentEncodedPath == "/oauth/authorize") else { return false }
        let items = c.queryItems ?? []
        let allowed = Set(["code", "client_id", "response_type", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method"])
        guard Set(items.map(\.name)).count == items.count, items.allSatisfy({ allowed.contains($0.name) }) else { return false }
        let q = Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
        guard q["client_id"] == "9d1c250a-e61b-44d9-88ed-5944d1962f5e", q["response_type"] == "code",
              q["code"] == nil || q["code"] == "" || q["code"] == "true", (16...256).contains(q["state"]?.count ?? 0),
              q["code_challenge_method"] == "S256", q["code_challenge"]?.count == 43,
              let redirect = q["redirect_uri"], let target = URLComponents(string: redirect),
              target.user == nil, target.password == nil, target.fragment == nil, target.query == nil else { return false }
        if ["https://platform.claude.com/oauth/code/callback", "https://console.anthropic.com/oauth/code/callback"].contains(redirect) { return true }
        return target.scheme == "http" && ["localhost", "127.0.0.1"].contains(target.host ?? "") &&
            (1...65535).contains(target.port ?? 0) && target.percentEncodedPath == "/callback"
    }
}

protocol CampAccountTransport: Sendable {
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data
}
extension CampHTTP: CampAccountTransport {}

@MainActor final class CampProviderAccounts: ObservableObject {
    struct AddIntent: Codable, Equatable { var provider: String; var label: String; var key: String }
    private struct StartIntent: Codable { var key: String; var previousLogin: String?; var reactivationKey: String? }
    private struct Journal: Codable { var add: AddIntent?; var starts: [String: StartIntent] = [:]; var selectedId: String? }
    private struct Connection {
        var transport: any CampAccountTransport
        var credential: CloudCredential
        var generation: UUID
        var key: String { "provider-setup:" + credential.origin + ":" + credential.actor.userId }
    }
    @Published private(set) var accounts: [CloudProviderAccount] = []
    @Published private(set) var detail: CloudProviderStatus?
    @Published private(set) var working = false
    @Published private(set) var error = ""
    @Published private(set) var available = true
    @Published private(set) var needsSignIn = false
    @Published private var journal = Journal()
    @Published var selectedId = ""
    private var connection: Connection?
    private var loaded = false
    private var polling = false
    private var actionError = ""
    private let cache: CampCache?

    init(cache: CampCache?) { self.cache = cache }
    var pendingAdd: AddIntent? { journal.add }
    var preparing: Bool { journal.starts[selectedId] != nil }
    var selection: CloudProviderAccount? { accounts.first { $0.id == selectedId } }
    func select(_ id: String) {
        guard !working, id != selectedId else { return }
        connection?.generation = UUID(); polling = false; selectedId = id; detail = nil
    }

    func attach(transport: (any CampAccountTransport)?, credential: CloudCredential?) {
        if let credential, let connection, connection.credential.origin == credential.origin,
           connection.credential.actor.userId == credential.actor.userId, connection.credential.token == credential.token { return }
        accounts = []; detail = nil; selectedId = ""; journal = Journal(); loaded = false; error = ""; actionError = ""; available = true; needsSignIn = false; polling = false; working = false
        if let transport, let credential {
            connection = Connection(transport: transport, credential: credential, generation: UUID())
        } else { connection = nil }
    }

    private func check(_ c: Connection) throws {
        guard connection?.generation == c.generation else { throw CancellationError() }
    }
    private func save(_ value: Journal, _ c: Connection) async throws {
        try check(c)
        guard let cache else { throw CampCloudError.cacheUnavailable }
        try await cache.setMetadata(c.key, CampWire.encoder().encode(value))
        try check(c); journal = value
    }
    private func load(_ c: Connection) async throws {
        guard !loaded else { return }
        guard let cache else { throw CampCloudError.cacheUnavailable }
        let data = try await cache.metadata(c.key)
        try check(c)
        guard !loaded else { return }
        let value = try data.map { try CampWire.decoder().decode(Journal.self, from: $0) } ?? Journal()
        guard value.starts.count <= 32, value.starts.allSatisfy({ UUID(uuidString: $0.key) != nil && UUID(uuidString: $0.value.key) != nil }),
              value.add == nil || (UUID(uuidString: value.add!.key) != nil && ["Codex", "Claude"].contains(value.add!.provider) && value.add!.label.utf8.count <= 120) else { throw CampCloudError.invalidResponse }
        journal = value; loaded = true
    }
    private func call<T: Decodable>(_ type: T.Type, _ method: String, _ path: String, body: Data? = nil, key: String? = nil, _ c: Connection) async throws -> T {
        let data = try await c.transport.data(method, path: path, body: body, key: key, token: c.credential.token)
        try check(c)
        return try CampWire.decoder().decode(type, from: data)
    }

    func refresh() async {
        guard !working, !polling, let c = connection else { return }
        polling = true
        defer { if connection?.generation == c.generation { polling = false } }
        do {
            try await load(c)
            guard !working else { return }
            if journal.add != nil { try await finishAdd(c) }
            let values = try await call([CloudProviderAccount].self, "GET", "/v1/provider-accounts", c)
            guard values.count <= 32, Set(values.map(\.id)).count == values.count, values.allSatisfy({ $0.valid(owner: c.credential.actor.userId) }) else { throw CampCloudError.invalidResponse }
            accounts = values; available = true
            if !values.contains(where: { $0.id == selectedId }) {
                selectedId = values.contains(where: { $0.id == journal.selectedId }) ? journal.selectedId! : values.first?.id ?? ""; detail = nil
            }
            if journal.selectedId != selectedId {
                var next = journal; next.selectedId = selectedId; try await save(next, c)
            }
            if !selectedId.isEmpty {
                try await readDetail(selectedId, c)
                guard !working else { return }
                if let intent = journal.starts[selectedId], let detail {
                    if detail.login?.id != nil && detail.login?.id != intent.previousLogin {
                        var next = journal; next.starts[selectedId] = nil; try await save(next, c)
                    } else {
                        do { try await begin(selectedId, intent, c) }
                        catch CampCloudError.server(409, _) { /* Runner provisioning is still pending. */ }
                    }
                }
            }
            error = actionError
        } catch is CancellationError {} catch { present(error, c) }
    }
    private func readDetail(_ id: String, _ c: Connection) async throws {
        let value = try await call(CloudProviderStatus.self, "GET", "/v1/provider-accounts/" + id, c)
        guard value.account.id == id, value.valid(owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
        if selectedId == id { detail = value }
        if let index = accounts.firstIndex(where: { $0.id == id }) { accounts[index] = value.account }
    }
    private func finishAdd(_ c: Connection) async throws {
        guard let intent = journal.add else { return }
        let value = try await call(CloudProviderAccount.self, "POST", "/v1/provider-accounts", body: CampWire.body(["provider": intent.provider, "label": intent.label]), key: intent.key, c)
        guard value.valid(owner: c.credential.actor.userId), value.provider == intent.provider else { throw CampCloudError.invalidResponse }
        var next = journal; next.add = nil
        next.starts[value.id] = StartIntent(key: UUID().uuidString, previousLogin: nil, reactivationKey: nil)
        next.selectedId = value.id
        try await save(next, c); selectedId = value.id
    }
    private func begin(_ id: String, _ intent: StartIntent, _ c: Connection) async throws {
        if let key = intent.reactivationKey {
            let account = try await call(CloudProviderAccount.self, "POST", "/v1/provider-accounts/" + id + "/reactivate", key: key, c)
            guard account.id == id, account.valid(owner: c.credential.actor.userId), !["disconnecting", "disconnected"].contains(account.state) else { throw CampCloudError.server(409, "request_state_changed") }
            if let index = accounts.firstIndex(where: { $0.id == id }) { accounts[index] = account }
            var next = journal; next.starts[id]?.reactivationKey = nil; try await save(next, c)
            try await readDetail(id, c)
            if let login = detail?.login, login.id != intent.previousLogin {
                next = journal; next.starts[id] = nil; try await save(next, c); return
            }
        }
        let login = try await call(CloudProviderLogin.self, "POST", "/v1/provider-accounts/" + id + "/logins", key: intent.key, c)
        guard let account = accounts.first(where: { $0.id == id }), login.valid(account: account) else { throw CampCloudError.invalidResponse }
        var next = journal; next.starts[id] = nil; try await save(next, c)
        if selectedId == id { detail = CloudProviderStatus(account: account, login: login) }
    }
    private func mutate(_ operation: (Connection) async throws -> Void) async {
        guard !working, let initial = connection else { return }
        working = true
        // A readiness poll can be starting a login. Observe its result before
        // Cancel reads the latest attempt, so a late start cannot escape it.
        do {
            while polling {
                try await Task.sleep(for: .milliseconds(25))
                try check(initial)
            }
        } catch {
            if connection?.generation == initial.generation { working = false }
            return
        }
        connection?.generation = UUID(); polling = false
        guard let c = connection else { return }
        working = true; actionError = ""; error = ""
        do { try await load(c); try await operation(c); try check(c); error = "" }
        catch is CancellationError {} catch {
            present(error, c)
            if connection?.generation == c.generation {
                switch error {
                case CampCloudError.server(400, _), CampCloudError.server(409, _), CampCloudError.invalidResponse, is DecodingError, is CampError: actionError = self.error
                default: break
                }
            }
        }
        guard connection?.generation == c.generation else { return }
        working = false
        await refresh()
    }

    func add(provider: String, label: String) async {
        let name = label.trimmingCharacters(in: .whitespacesAndNewlines)
        guard ["Codex", "Claude"].contains(provider), !name.isEmpty, name.utf8.count <= 120 else { error = "Choose a provider and an account name of up to 120 bytes"; return }
        await mutate { c in
            if let pending = journal.add, pending.provider != provider || pending.label != name {
                throw CampError.message("Finish the pending account setup before adding another")
            }
            if journal.add == nil {
                var next = journal; next.add = AddIntent(provider: provider, label: name, key: UUID().uuidString); try await save(next, c)
            }
            try await finishAdd(c)
        }
    }
    func retryAdd() async { await mutate { c in try await finishAdd(c) } }
    func connect(_ account: CloudProviderAccount) async {
        await mutate { c in
            guard account.valid(owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
            selectedId = account.id; try await readDetail(account.id, c)
            guard let current = detail?.account, current.state != "disconnecting" else { throw CampCloudError.server(409, "request_state_changed") }
            guard detail?.login?.active != true else { return }
            var next = journal
            if next.starts[account.id] == nil { next.starts[account.id] = StartIntent(key: UUID().uuidString, previousLogin: detail?.login?.id, reactivationKey: current.state == "disconnected" ? UUID().uuidString : nil) }
            try await save(next, c)
            do { try await begin(account.id, next.starts[account.id]!, c) }
            catch CampCloudError.server(409, _) { /* The same intent resumes when the runner is ready. */ }
        }
    }
    func cancel(_ account: CloudProviderAccount) async {
        await mutate { c in
            guard account.valid(owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
            var next = journal; next.starts[account.id] = nil; try await save(next, c)
            try await readDetail(account.id, c)
            if let login = detail?.login, login.active {
                let value = try await call(CloudProviderLogin.self, "POST", "/v1/provider-accounts/" + account.id + "/logins/" + login.id + "/cancel", key: UUID().uuidString, c)
                guard value.valid(account: account) else { throw CampCloudError.invalidResponse }
                detail = CloudProviderStatus(account: account, login: value)
            }
        }
    }
    func disconnect(_ account: CloudProviderAccount) async {
        await mutate { c in
            guard account.valid(owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
            var next = journal; next.starts[account.id] = nil; try await save(next, c)
            let value = try await call(CloudProviderAccount.self, "POST", "/v1/provider-accounts/" + account.id + "/disconnect", key: UUID().uuidString, c)
            guard value.id == account.id, value.valid(owner: c.credential.actor.userId) else { throw CampCloudError.invalidResponse }
        }
    }
    func submitCode(_ code: String, account: CloudProviderAccount, login: CloudProviderLogin) async {
        let value = code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (8...4096).contains(value.utf8.count), !value.unicodeScalars.contains(where: { CharacterSet.whitespacesAndNewlines.contains($0) || CharacterSet.controlCharacters.contains($0) }) else { error = "Paste the complete authorization code"; return }
        await mutate { c in
            guard account.provider == "Claude", account.valid(owner: c.credential.actor.userId), login.valid(account: account), login.state == "waiting", login.codeSubmitted != true else { throw CampCloudError.invalidResponse }
            let current = try await call(CloudProviderLogin.self, "POST", "/v1/provider-accounts/" + account.id + "/logins/" + login.id + "/code", body: CampWire.body(["code": value]), key: UUID().uuidString, c)
            guard current.valid(account: account) else { throw CampCloudError.invalidResponse }
            detail = CloudProviderStatus(account: account, login: current)
        }
    }
    private func present(_ failure: Error, _ c: Connection) {
        guard connection?.generation == c.generation else { return }
        switch failure {
        case CampCloudError.server(503, "cloud_accounts_unavailable"):
            available = false; error = "Cloud accounts are not enabled on this Camp server"
        case CampCloudError.server(401, _):
            accounts = []; detail = nil; needsSignIn = true; error = "Sign in to Camp again to manage your cloud accounts"
        case CampCloudError.server(404, _): error = "This account is no longer available"
        case CampCloudError.server(409, _): error = "Sign-in status changed. Review the current account state"
        case CampCloudError.server(400, _): error = "Check the account details or authorization code and try again"
        case CampCloudError.invalidResponse, is DecodingError: error = "The cloud account response could not be verified"
        case let value as CampError: error = value.localizedDescription
        default: error = "Cloud accounts are offline. Setup will resume when the connection returns"
        }
    }
}
