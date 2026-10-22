import Foundation

actor GitConnectionFixture: CampAccountTransport {
    let owner: String
    var connection: CloudGitConnection?
    var login: CloudGitLogin?
    var receipts: [String: Data] = [:]
    var begins = 0
    var loseReply = true
    var holdRead = false
    var held = false
    var continuation: CheckedContinuation<Void, Never>?
    init(owner: String) { self.owner = owner }
    func count() -> Int { begins }
    func complete() { connection?.state = "connected"; connection?.githubUserId = 123; connection?.login = "fixture-user"; login?.state = "connected"; login?.url = nil }
    func hold() { holdRead = true }
    func isHeld() -> Bool { held }
    func resume() { continuation?.resume(); continuation = nil }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        if method == "POST" && path == "/v1/git/logins" {
            guard let key else { throw CampCloudError.invalidResponse }
            if let value = receipts[key] { return value }
            if connection == nil { connection = CloudGitConnection(id: UUID().uuidString, ownerUserId: owner, githubUserId: nil, login: "", state: "pending", revision: 1, epoch: 1, accessExpiresAt: nil) }
            begins += 1
            let query = [URLQueryItem(name: "client_id", value: "fixture"), URLQueryItem(name: "redirect_uri", value: "https://fixture.example/v1/git/github/callback"), URLQueryItem(name: "state", value: String(repeating: "s", count: 43)), URLQueryItem(name: "code_challenge", value: String(repeating: "c", count: 43)), URLQueryItem(name: "code_challenge_method", value: "S256"), URLQueryItem(name: "prompt", value: "select_account"), URLQueryItem(name: "scope", value: "offline_access")]
            var url = URLComponents(string: "https://github.com/login/oauth/authorize")!; url.queryItems = query
            login = CloudGitLogin(id: UUID().uuidString, connectionId: connection!.id, state: "waiting", failureCode: nil, expiresAt: Date().addingTimeInterval(600), url: url.string)
            let data = try CampWire.encoder().encode(login!); receipts[key] = data
            if loseReply { loseReply = false; throw URLError(.networkConnectionLost) }
            return data
        }
        if path == "/v1/git/connections" {
            let value = CloudGitOverview(connections: connection.map { [$0] } ?? [], installUrl: "https://github.com/apps/fixture/installations/new")
            if holdRead { holdRead = false; held = true; await withCheckedContinuation { continuation = $0 }; held = false }
            return try CampWire.encoder().encode(value)
        }
        guard let account = connection else { throw CampCloudError.invalidResponse }
        if path.hasSuffix("/cancel") { login?.state = "cancelled"; login?.url = nil; return try CampWire.encoder().encode(login!) }
        if path.hasSuffix("/disconnect") { connection?.state = "disconnected"; connection?.epoch += 1; return try CampWire.encoder().encode(connection!) }
        return try CampWire.encoder().encode(CloudGitStatus(connection: account, login: login))
    }
}

@main struct CampGitConnectionTests {
    @MainActor static func main() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("camp-git-tests-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let cache = try CampCache(directory: directory)
        let credential = CloudCredential(origin: "https://fixture.example", actor: CloudActor(userId: UUID().uuidString, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture"), token: "fixture-secret-token", expiresAt: Date().addingTimeInterval(3600))
        let fixture = GitConnectionFixture(owner: credential.actor.userId)
        let git = CampGitConnections(cache: cache); git.attach(transport: fixture, credential: credential)
        await git.begin()
        precondition(git.pending && !git.error.isEmpty, "A lost response must preserve the start key")
        let restored = CampGitConnections(cache: cache); restored.attach(transport: fixture, credential: credential)
        await restored.refresh()
        precondition(!restored.pending && restored.connections.count == 1 && restored.authorizationURL?.host == "github.com")
        let count = await fixture.count(); precondition(count == 1, "Relaunch must reuse the pending OAuth attempt")
        await restored.cancel(); precondition(restored.detail?.login?.state == "cancelled" && restored.authorizationURL == nil)
        await restored.begin(reconnect: true); await fixture.complete(); await restored.refresh()
        precondition(restored.selection?.state == "connected")
        let saved = try await cache.metadata("git-setup:" + credential.origin + ":" + credential.actor.userId)
        let text = String(data: saved ?? Data(), encoding: .utf8) ?? ""
        precondition(!text.contains("github.com") && !text.contains("fixture-secret-token") && !text.contains(String(repeating: "s", count: 43)), "Journal must exclude authorization URLs and tokens")
        await fixture.hold()
        let refresh = Task { await restored.refresh() }
        for _ in 0..<100 where !(await fixture.isHeld()) { try await Task.sleep(for: .milliseconds(5)) }
        restored.attach(transport: nil, credential: nil); await fixture.resume(); await refresh.value
        precondition(restored.connections.isEmpty && restored.detail == nil, "A late response after sign-out must not restore Git identity")
        precondition(!GitHubURL.authorization("https://github.com.attacker.example/login/oauth/authorize", origin: credential.origin))
        precondition(!GitHubURL.installation("https://github.com/apps/../installations/new"))
        print("PASS: GitHub setup recovery, owner state, cancellation, sign-out races and private persistence")
    }
}
