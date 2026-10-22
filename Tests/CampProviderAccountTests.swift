import Foundation

actor ProviderAccountFixture: CampAccountTransport {
    let owner: String
    var accounts: [CloudProviderAccount] = []
    var logins: [String: CloudProviderLogin] = [:]
    var receipts: [String: Data] = [:]
    var grantReady = false
    var loseAddReply = true
    var foreignOwner = false
    var created = 0
    var starts = 0
    var reactivations = 0
    var loseReactivationReply = true
    var receivedCode = ""
    var holdList = false
    var holdStart = false
    var heldStart = false
    var held = false
    var release: CheckedContinuation<Void, Never>?
    init(owner: String) { self.owner = owner }
    func prepareRunner() { grantReady = true }
    func counts() -> (Int, Int) { (created, starts) }
    func reactivationCount() -> Int { reactivations }
    func finishErasure(_ id: String) { if let index = accounts.firstIndex(where: { $0.id == id }) { accounts[index].state = "disconnected" } }
    func code() -> String { receivedCode }
    func setForeign(_ value: Bool) { foreignOwner = value }
    func holdNextList() { holdList = true }
    func holdNextStart() { holdStart = true }
    func isStartHeld() -> Bool { heldStart }
    func isHeld() -> Bool { held }
    func resume() { release?.resume(); release = nil }
    func waitForCode(_ id: String) {
        logins[id]?.state = "waiting"
        logins[id]?.url = "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=fixture-login-state&code_challenge=" + String(repeating: "a", count: 43) + "&code_challenge_method=S256"
    }
    func complete(_ id: String) {
        logins[id]?.state = "connected"; logins[id]?.url = nil
        if let index = accounts.firstIndex(where: { $0.id == id }) { accounts[index].state = "connected"; accounts[index].verifiedSubject = "fixture-subject"; accounts[index].revision += 1 }
    }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        let input = try body.map { try JSONSerialization.jsonObject(with: $0) as! [String: Any] } ?? [:]
        if method == "GET" && path == "/v1/provider-accounts" {
            var values = accounts
            if foreignOwner && !values.isEmpty { values[0].ownerUserId = UUID().uuidString }
            if holdList { holdList = false; held = true; await withCheckedContinuation { release = $0 }; held = false }
            return try CampWire.encoder().encode(values)
        }
        if method == "POST" && path == "/v1/provider-accounts" {
            guard let key else { throw CampCloudError.invalidResponse }
            if let data = receipts[key] { return data }
            let account = CloudProviderAccount(id: UUID().uuidString, ownerUserId: owner, provider: input["provider"] as! String,
                label: input["label"] as! String, verifiedSubject: nil, revision: 1, installationId: UUID().uuidString,
                location: "cloud", state: "pending", epoch: 1, credentialExpiresAt: nil)
            accounts.append(account); created += 1
            let data = try CampWire.encoder().encode(account); receipts[key] = data
            if loseAddReply { loseAddReply = false; throw URLError(.networkConnectionLost) }
            return data
        }
        let parts = path.split(separator: "/").map(String.init)
        guard parts.count >= 3, let index = accounts.firstIndex(where: { $0.id == parts[2] }) else { throw CampCloudError.server(404, "not_available") }
        let id = accounts[index].id
        if method == "GET" { return try CampWire.encoder().encode(CloudProviderStatus(account: accounts[index], login: logins[id])) }
        if path.hasSuffix("/reactivate") {
            guard let key else { throw CampCloudError.invalidResponse }
            if let data = receipts[key] { return data }
            guard accounts[index].state == "disconnected" else { throw CampCloudError.server(409, "request_state_changed") }
            accounts[index].state = "pending"; accounts[index].epoch += 1; accounts[index].revision += 1; reactivations += 1
            let data = try CampWire.encoder().encode(accounts[index]); receipts[key] = data
            if loseReactivationReply { loseReactivationReply = false; throw URLError(.networkConnectionLost) }
            return data
        }
        if path.hasSuffix("/logins") {
            guard grantReady else { throw CampCloudError.server(409, "request_state_changed") }
            guard let key else { throw CampCloudError.invalidResponse }
            if let saved = receipts[key] { return saved }
            if holdStart { holdStart = false; heldStart = true; await withCheckedContinuation { release = $0 }; heldStart = false }
            guard logins[id]?.active != true else { throw CampCloudError.server(409, "request_state_changed") }
            let login = CloudProviderLogin(id: UUID().uuidString, installationId: accounts[index].installationId, state: "queued", failureCode: nil, url: nil, userCode: nil, codeSubmitted: nil, codeConsumed: nil, expiresAt: Date().addingTimeInterval(600))
            logins[id] = login; starts += 1
            let data = try CampWire.encoder().encode(login); receipts[key] = data; return data
        }
        if path.hasSuffix("/disconnect") {
            accounts[index].state = "disconnecting"; accounts[index].epoch += 1
            logins[id]?.state = "cancelled"; logins[id]?.url = nil
            return try CampWire.encoder().encode(accounts[index])
        }
        if path.hasSuffix("/cancel") {
            logins[id]?.state = "cancelled"; logins[id]?.url = nil
        } else if path.hasSuffix("/code") {
            guard logins[id]?.codeSubmitted != true else { throw CampCloudError.server(409, "request_state_changed") }
            receivedCode = input["code"] as! String; logins[id]?.codeSubmitted = true
        } else { throw CampCloudError.invalidResponse }
        return try CampWire.encoder().encode(logins[id]!)
    }
}

@main struct CampProviderAccountTests {
    @MainActor static func main() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("camp-account-tests-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let cache = try CampCache(directory: directory)
        let credential = CloudCredential(origin: "https://fixture.example", actor: CloudActor(userId: UUID().uuidString, deviceId: UUID().uuidString, email: "owner@example.com", name: "Owner"), token: "fixture-device-token", expiresAt: Date().addingTimeInterval(3600))
        let transport = ProviderAccountFixture(owner: credential.actor.userId)
        let accounts = CampProviderAccounts(cache: cache)
        accounts.attach(transport: transport, credential: credential)
        await accounts.add(provider: "Claude", label: "My Claude")
        precondition(accounts.accounts.count == 1 && accounts.pendingAdd == nil && accounts.preparing, "Lost creation response should recover using the same key")
        let initialCounts = await transport.counts()
        precondition(initialCounts.0 == 1, "A lost response must not create another account")
        let id = accounts.selectedId
        let restored = CampProviderAccounts(cache: cache)
        restored.attach(transport: transport, credential: credential)
        await transport.prepareRunner()
        await restored.refresh()
        precondition(restored.detail?.login?.state == "queued" && !restored.preparing, "Pending setup must continue after relaunch")
        await restored.refresh()
        let refreshedCounts = await transport.counts()
        precondition(refreshedCounts.1 == 1, "Refreshing must not restart sign-in")
        await transport.waitForCode(id)
        await restored.refresh()
        guard let account = restored.selection, let login = restored.detail?.login else { preconditionFailure("Missing account") }
        precondition(login.state == "waiting" && login.valid(account: account))
        await restored.submitCode("fixture-return-code#state", account: account, login: login)
        let receivedCode = await transport.code()
        precondition(receivedCode == "fixture-return-code#state")
        precondition(restored.detail?.login?.codeSubmitted == true)
        let persisted = try await cache.metadata("provider-setup:" + credential.origin + ":" + credential.actor.userId)
        let saved = String(data: persisted ?? Data(), encoding: .utf8) ?? ""
        precondition(!saved.contains("fixture-return-code") && !saved.contains("fixture-device-token"), "Setup journal must not persist credentials or return codes")
        await transport.complete(id)
        await restored.refresh()
        precondition(restored.selection?.state == "connected")
        await restored.connect(restored.selection!)
        let reconnectCounts = await transport.counts()
        precondition(reconnectCounts.1 == 2, "Explicit reconnect starts one new attempt")
        await restored.cancel(restored.selection!)
        precondition(restored.detail?.login?.state == "cancelled")
        precondition(restored.selection?.state == "connected", "Cancelling reconnect must retain the connected account")
        await restored.disconnect(restored.selection!)
        precondition(restored.selection?.state == "disconnecting")
        await transport.finishErasure(id); await restored.refresh()
        let erased = restored.selection!
        await restored.connect(erased)
        await restored.refresh()
        let reactivations = await transport.reactivationCount()
        precondition(reactivations == 1 && restored.accounts.count == 1 && restored.selection?.id == id && restored.selection?.epoch == erased.epoch + 1,
                     "Lost reactivation response must preserve identity and increment epoch once")
        precondition(restored.detail?.login?.state == "queued")

        await transport.setForeign(true)
        await restored.refresh()
        precondition(!restored.error.isEmpty && restored.accounts.allSatisfy { $0.ownerUserId == credential.actor.userId }, "Reject foreign account responses")
        await transport.setForeign(false)
        await transport.holdNextList()
        let late = Task { await restored.refresh() }
        for _ in 0..<100 {
            if await transport.isHeld() { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        let held = await transport.isHeld()
        precondition(held)
        restored.attach(transport: nil, credential: nil)
        await transport.resume(); await late.value
        precondition(restored.accounts.isEmpty && restored.detail == nil, "A signed-out response must not restore account data")

        let raceCache = try CampCache(directory: directory.appendingPathComponent("cancel-race"))
        let racing = CampProviderAccounts(cache: raceCache)
        let raceTransport = ProviderAccountFixture(owner: credential.actor.userId)
        racing.attach(transport: raceTransport, credential: credential)
        await racing.add(provider: "Codex", label: "Cancellation test")
        let raceAccount = racing.selection!
        await raceTransport.prepareRunner(); await raceTransport.holdNextStart()
        let starting = Task { await racing.refresh() }
        for _ in 0..<100 {
            if await raceTransport.isStartHeld() { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        let cancelling = Task { await racing.cancel(raceAccount) }
        for _ in 0..<100 where !racing.working { try await Task.sleep(for: .milliseconds(10)) }
        precondition(racing.working)
        await raceTransport.resume(); await starting.value; await cancelling.value
        precondition(racing.detail?.login?.state == "cancelled" && !racing.preparing,
                     "Cancel must fence a start whose response arrives late")

        let codeURL = "https://auth.openai.com/codex/device"
        precondition(ProviderSignInURL.valid(provider: "Codex", value: codeURL, code: "TEST-1234"))
        for bad in [codeURL + "?redirect=evil", "https://auth.openai.com.evil.test/codex/device", "https://user@auth.openai.com/codex/device", "http://auth.openai.com/codex/device", "https://auth.openai.com/codex/%64evice"] {
            precondition(!ProviderSignInURL.valid(provider: "Codex", value: bad, code: "TEST-1234"))
        }
        precondition(!ProviderSignInURL.valid(provider: "Codex", value: codeURL, code: "INVALID\nCODE"))
        let unsafe = login.url!.replacingOccurrences(of: "9d1c250a-e61b-44d9-88ed-5944d1962f5e", with: UUID().uuidString)
        precondition(!ProviderSignInURL.valid(provider: "Claude", value: unsafe, code: ""))
        print("PASS: cloud account ownership, setup recovery, single login, code relay, cancellation, stale responses and provider links")
    }
}
