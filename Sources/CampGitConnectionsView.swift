import SwiftUI
import AppKit

struct CampGitConnectionsView: View {
    @ObservedObject var cloud: CampConnection
    @ObservedObject var git: CampGitConnections
    @State private var confirmDisconnect = false
    init(cloud: CampConnection) { self.cloud = cloud; self.git = cloud.gitConnections }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("GitHub").font(.headline)
            Text("Connect your own GitHub account and choose the repositories this Camp can use. Your teammate connects theirs separately.")
                .font(.system(size: 12)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
            if !cloud.signedIn { CampSignInForm(cloud: cloud) }
            else {
                if !git.connections.isEmpty {
                    Picker("GitHub account", selection: Binding(get: { git.selectedId }, set: { git.select($0); Task { await git.refresh() } })) {
                        ForEach(git.connections) { Text($0.login.isEmpty ? "GitHub account" : "@" + $0.login).tag($0.id) }
                    }.disabled(git.working || git.pending)
                    if let connection = git.selection {
                        HStack {
                            Label(connection.login.isEmpty ? "GitHub account" : "@" + connection.login, systemImage: "chevron.left.forwardslash.chevron.right").font(.system(size: 14, weight: .medium))
                            Spacer()
                            Text(connection.statusLabel).font(.system(size: 11)).foregroundStyle(campMuted)
                        }
                        if let login = git.detail?.login, login.active {
                            Label(login.state == "waiting" ? "Finish signing in on GitHub" : "Verifying your GitHub account", systemImage: "clock").font(.system(size: 12))
                            HStack {
                                if let url = git.authorizationURL { Button("Continue on GitHub") { NSWorkspace.shared.open(url) } }
                                Button("Cancel") { Task { await git.cancel() } }
                            }.disabled(git.working)
                        } else {
                            if let login = git.detail?.login, ["failed", "expired", "cancelled"].contains(login.state) {
                                Text(login.failureCode == "identity_mismatch" ? "That sign-in belongs to a different GitHub account" : login.state == "expired" ? "Sign-in expired" : login.state == "cancelled" ? "Sign-in cancelled" : "GitHub sign-in could not finish")
                                    .font(.system(size: 12)).foregroundStyle(campMuted)
                            }
                            HStack {
                                Button(connection.state == "connected" ? "Reconnect" : "Sign in") { connect(reconnect: true) }
                                if connection.state != "disconnected" { Button("Disconnect", role: .destructive) { confirmDisconnect = true } }
                            }.disabled(git.working)
                        }
                        if connection.state == "connected" {
                            Divider()
                            HStack {
                                Text("Repository access").font(.system(size: 13, weight: .medium))
                                Spacer()
                                Button("Show repositories") { Task { await git.loadInstallations() } }.disabled(git.working)
                            }
                            if let url = URL(string: git.installURL), GitHubURL.installation(git.installURL) {
                                Button("Choose repositories on GitHub", systemImage: "arrow.up.right") { NSWorkspace.shared.open(url) }
                            }
                            ForEach(git.installations) { installation in
                                Button {
                                    Task { await git.loadRepositories(installation: installation.id) }
                                } label: {
                                    HStack { Image(systemName: "folder"); Text(installation.account.login); Spacer(); if installation.suspended { Text("Suspended") } else { Image(systemName: "chevron.right") } }
                                        .font(.system(size: 12)).frame(minHeight: 28).contentShape(Rectangle())
                                }.buttonStyle(.plain).disabled(git.working || installation.suspended)
                            }
                            if let page = git.nextInstallationPage { Button("More installations") { Task { await git.loadInstallations(page: page) } }.disabled(git.working) }
                            ForEach(git.repositories) { repository in
                                HStack(spacing: 10) {
                                    Image(systemName: repository.private ? "lock" : "folder")
                                    VStack(alignment: .leading, spacing: 3) { Text(repository.fullName); Text(repository.write ? "Read and write" : repository.read ? "Read only" : "No content access").font(.system(size: 10)).foregroundStyle(campMuted) }
                                    Spacer()
                                    Button { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(repository.url, forType: .string) } label: { Image(systemName: "doc.on.doc").frame(width: 28, height: 28) }
                                        .buttonStyle(.plain).help("Copy repository URL").accessibilityLabel("Copy \(repository.fullName) URL")
                                }.font(.system(size: 12)).padding(.vertical, 3)
                            }
                            if let page = git.nextRepositoryPage { Button("More repositories") { Task { await git.loadRepositories(installation: git.selectedInstallation, page: page) } }.disabled(git.working) }
                        }
                    }
                    Divider()
                }
                if git.needsSignIn { Button("Sign in to Camp again") { Task { await cloud.signOut() } } }
                else if git.pending { Button("Retry GitHub setup") { Task { await git.begin() } }.disabled(git.working) }
                else { Button("Connect GitHub", systemImage: "plus") { connect() }.disabled(git.working || !git.available) }
                if git.working { ProgressView().controlSize(.small).accessibilityLabel("Updating GitHub connection") }
                if !git.error.isEmpty { Label(git.error, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true) }
            }
        }.textFieldStyle(.roundedBorder)
            .task(id: cloud.signedIn) {
                guard cloud.signedIn else { return }
                while !Task.isCancelled { await git.refresh(); do { try await Task.sleep(for: .seconds(3)) } catch { return } }
            }
            .confirmationDialog("Disconnect GitHub?", isPresented: $confirmDisconnect, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) { Task { await git.disconnect() } }
                Button("Cancel", role: .cancel) {}
            } message: { Text("This removes this Camp server's authorization to use the account") }
    }
    private func connect(reconnect: Bool = false) {
        Task { await git.begin(reconnect: reconnect); if git.error.isEmpty, let url = git.authorizationURL { NSWorkspace.shared.open(url) } }
    }
}
