import SwiftUI
import AppKit

struct CampProviderAccountsView: View {
    @ObservedObject var cloud: CampConnection
    @ObservedObject var accounts: CampProviderAccounts
    @State private var provider = "Codex"
    @State private var name = ""
    @State private var authorizationCode = ""
    @State private var disconnecting: CloudProviderAccount?

    init(cloud: CampConnection) { self.cloud = cloud; self.accounts = cloud.providerAccounts }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Cloud accounts").font(.headline)
            Text("Add each Codex or Claude account separately. Sign in through the provider; your teammates never receive its credentials.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            if !cloud.signedIn {
                CampSignInForm(cloud: cloud)
            } else {
                if !accounts.accounts.isEmpty {
                    Picker("Account", selection: Binding(get: { accounts.selectedId }, set: { value in
                        authorizationCode = ""; accounts.select(value); Task { await accounts.refresh() }
                    })) {
                        ForEach(accounts.accounts) { account in Text("\(account.label) · \(account.provider)").tag(account.id) }
                    }.disabled(accounts.working)
                    if let account = accounts.selection { accountDetails(account) }
                    Divider()
                }
                if accounts.needsSignIn {
                    Button("Sign in to Camp again") { Task { await cloud.signOut() } }.disabled(cloud.busy)
                } else if let pending = accounts.pendingAdd {
                    HStack {
                        Text("Adding \(pending.label)…").font(.system(size: 12))
                        Spacer()
                        Button("Retry") { Task { await accounts.retryAdd() } }.disabled(accounts.working)
                    }
                } else {
                    HStack(alignment: .top, spacing: 10) {
                        Picker("Provider", selection: $provider) {
                            HStack { ProviderMark(provider: "Codex"); Text("Codex") }.tag("Codex")
                            HStack { ProviderMark(provider: "Claude"); Text("Claude") }.tag("Claude")
                        }.frame(width: 180)
                        TextField("Account name", text: $name).accessibilityLabel("Cloud account name")
                        Button("Add account") {
                            let label = name.isEmpty ? "My \(provider)" : name
                            Task { await accounts.add(provider: provider, label: label); if accounts.error.isEmpty { name = "" } }
                        }.disabled(accounts.working || !accounts.available)
                    }
                }
                if accounts.working { ProgressView().controlSize(.small).accessibilityLabel("Updating cloud account") }
                if !accounts.error.isEmpty {
                    Label(accounts.error, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange).textSelection(.enabled)
                }
            }
        }.textFieldStyle(.roundedBorder)
            .task(id: cloud.signedIn) {
                guard cloud.signedIn else { return }
                while !Task.isCancelled {
                    await accounts.refresh()
                    do { try await Task.sleep(for: .seconds(2)) } catch { return }
                }
            }
            .onChange(of: accounts.selectedId) { _, _ in authorizationCode = "" }
            .onChange(of: accounts.detail?.login?.id) { _, _ in authorizationCode = "" }
            .confirmationDialog("Disconnect this cloud account?", isPresented: Binding(get: { disconnecting != nil }, set: { if !$0 { disconnecting = nil } }), titleVisibility: .visible) {
                if let account = disconnecting {
                    Button("Disconnect \(account.label)", role: .destructive) { Task { await accounts.disconnect(account) }; disconnecting = nil }
                }
                Button("Cancel", role: .cancel) { disconnecting = nil }
            } message: {
                Text("This stops new work and asks its runner to remove the cloud credentials. Local profiles stay on this Mac.")
            }
    }

    private func accountDetails(_ account: CloudProviderAccount) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                ProviderMark(provider: account.provider, size: 20)
                Text(account.label).font(.system(size: 14, weight: .medium))
                Spacer()
                Label(account.statusLabel, systemImage: account.state == "connected" ? "checkmark.circle" : "cloud").font(.system(size: 11)).foregroundStyle(campMuted)
            }
            if let subject = account.verifiedSubject, !subject.isEmpty {
                Text("Verified identity: \(subject)").font(.system(size: 11)).foregroundStyle(campMuted).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
            }
            if let login = accounts.detail?.login, login.active {
                loginDetails(login, account)
            } else if accounts.preparing {
                Label("Waiting for a cloud runner", systemImage: "clock").font(.system(size: 12))
                Text("Sign-in starts when this server has prepared the account's runner").font(.system(size: 12)).foregroundStyle(campMuted)
                Button("Cancel setup") { Task { await accounts.cancel(account) } }.disabled(accounts.working)
            } else if !["disconnecting", "disconnected"].contains(account.state), let login = accounts.detail?.login, ["failed", "cancelled", "expired"].contains(login.state) {
                Text(login.failureCode == "identity_mismatch" ? "That login belongs to a different account. The existing login was kept." : login.state == "expired" ? "Sign-in expired. Start again when you're ready." : login.state == "cancelled" ? "Sign-in cancelled" : "The provider did not complete sign-in. You can try again.")
                    .font(.system(size: 12)).foregroundStyle(campMuted)
            }
            if account.state == "connected" { Text("Usage has not been reported by this provider").font(.system(size: 11)).foregroundStyle(campMuted) }
            if account.state == "disconnecting" { Text("Waiting for the runner to confirm that its credentials have been removed").font(.system(size: 12)).foregroundStyle(campMuted) }
            HStack {
                if account.state != "disconnecting", accounts.detail?.login?.active != true, !accounts.preparing {
                    Button(["connected", "disconnected"].contains(account.state) ? "Reconnect" : "Sign in") { Task { await accounts.connect(account) } }
                }
                Spacer()
                if !["disconnecting", "disconnected"].contains(account.state) {
                    Button("Disconnect", role: .destructive) { disconnecting = account }
                }
            }.disabled(accounts.working)
        }.padding(14).background(.white.opacity(0.7)).clipShape(RoundedRectangle(cornerRadius: 10))
    }

    private func loginDetails(_ login: CloudProviderLogin, _ account: CloudProviderAccount) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if login.state == "waiting", let value = login.url, ProviderSignInURL.valid(provider: account.provider, value: value, code: login.userCode ?? ""), let url = URL(string: value) {
                Button("Open \(account.provider) sign-in", systemImage: "arrow.up.right") { NSWorkspace.shared.open(url) }
                if let code = login.userCode, !code.isEmpty {
                    HStack {
                        Text(code).monospaced().textSelection(.enabled)
                        Button("Copy code", systemImage: "doc.on.doc") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(code, forType: .string) }
                    }
                }
                if account.provider == "Claude", login.codeSubmitted != true {
                    SecureField("Paste the authorization code", text: $authorizationCode)
                        .accessibilityLabel("Claude authorization code")
                        .onSubmit { submit(account, login) }
                    Button("Continue sign-in") { submit(account, login) }.disabled(authorizationCode.isEmpty || accounts.working)
                } else {
                    Label(login.codeSubmitted == true ? "Finishing sign-in…" : "Complete sign-in in your browser", systemImage: "clock").font(.system(size: 12)).foregroundStyle(campMuted)
                }
            } else {
                Label("Starting sign-in…", systemImage: "clock").font(.system(size: 12)).foregroundStyle(campMuted)
            }
            Button("Cancel sign-in") { authorizationCode = ""; Task { await accounts.cancel(account) } }.disabled(accounts.working)
        }
    }
    private func submit(_ account: CloudProviderAccount, _ login: CloudProviderLogin) {
        guard !authorizationCode.isEmpty, !accounts.working else { return }
        let value = authorizationCode; authorizationCode = ""
        Task { await accounts.submitCode(value, account: account, login: login) }
    }
}
