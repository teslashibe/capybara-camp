import SwiftUI

extension CampStore {
    func executionSession(_ session: CampSession) -> CloudSession? {
        if let value = cloudSessions[session.id] { return value }
        guard let binding = sharing.binding(for: session) else { return nil }
        return cloudSessions["camp:" + binding.id] ?? binding.remote
    }
}

struct ExecutionLocationButton: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    var remote: CloudSession? { store.executionSession(session) }
    var location: String { remote?.executionLocation == "cloud" ? "Cloud" : "This Mac" }
    var body: some View {
        if ["Codex", "Claude"].contains(session.provider) {
            Button { store.executionSheet = session } label: {
                Image(systemName: location == "Cloud" ? "cloud" : "desktopcomputer").frame(width: 28, height: 28)
            }.buttonStyle(.plain).foregroundStyle(campMuted)
                .help("Runs on \(location)").accessibilityLabel("Run on · \(location) · \(session.title)")
        }
    }
}

struct RunLocationView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var cloud: CampConnection
    @ObservedObject var execution: CampExecution
    @ObservedObject var accounts: CampProviderAccounts
    @ObservedObject var git: CampGitConnections
    @ObservedObject var sharing: CampSharing
    @ObservedObject var transfers: CampTransfer
    let session: CampSession
    let requestedLocation: String?
    @Environment(\.dismiss) private var dismiss
    @State private var location = "Cloud"
    @State private var accountID = ""
    @State private var projectID = ""
    @State private var repositoryID: Int64 = 0
    @State private var ref = ""
    @State private var implementation = false
    @State private var review = false
    @State private var reviewLocal = false
    @State private var approved = false
    @State private var copying = false
    @State private var localError = ""
    @State private var manageAccounts = false
    @State private var manageGit = false
    @State private var connectCamp = false
    @State private var permissions = PermissionSettings()
    @State private var transferReview = false
    @State private var transferInput: CloudWorkspaceInput?
    init(store: CampStore, session: CampSession, requestedLocation: String? = nil) {
        self.store = store; self.session = session; self.requestedLocation = requestedLocation; cloud = store.cloud; execution = store.cloud.execution
        accounts = store.cloud.providerAccounts; git = store.cloud.gitConnections; sharing = store.sharing
        transfers = store.cloud.transfers
    }
    var remote: CloudSession? { store.executionSession(session) }
    var currentLocation: String { remote?.executionLocation == "cloud" ? "Cloud" : "This Mac" }
    var candidates: [CloudProviderAccount] { accounts.accounts.filter { $0.provider == session.provider && $0.state == "connected" } }
    var project: CloudProject? { cloud.projects.first { $0.id == (remote?.projectId ?? projectID) } }
    var repositories: [CloudGitRepository] {
        git.repositories.filter { repo in repo.read && (project?.repositoryURLs.contains { $0.lowercased() == repo.url.lowercased() } ?? false) }
    }
    var workspace: CloudWorkspace? {
        execution.workspaces.first { $0.sessionId==remote?.id && ["queued", "preparing", "ready", "cancel_requested"].contains($0.state) }
    }
    func current(_ workspace: CloudWorkspace, session: CloudSession) -> Bool {
        workspace.projectId == session.projectId && workspace.sourceRevision == session.revision && workspace.projectRevision == project?.version
    }
    var busy: Bool {
        store.isBusy(session) || cloud.requests.contains { $0.sessionId == remote?.id && !["completed", "failed", "cancelled"].contains($0.state) }
    }
    var historyReady: Bool {
        guard currentLocation != "Cloud", let binding=sharing.binding(for:session) else{return true}
        return ["Shared","Saved to Camp"].contains(binding.state)
    }
    var catalog: PermissionCatalog {
        PermissionCatalog(provider: session.provider, modes: ["manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"], profiles: [],
            sandboxes: ["read-only", "workspace-write", "danger-full-access"], approvals: ["on-request", "never"], reviewers: ["user", "auto_review"], inherited: "Choose explicit cloud permissions", note: "")
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack { Text(transferReview ? "Move to cloud" : "Run on").font(.title2); Spacer(); Button("Done") { dismiss() } }
            Text(session.title).font(.system(size: 14, weight: .medium)).lineLimit(2)
            if !transferReview {
                Text("Currently \(currentLocation)").font(.system(size: 11)).foregroundStyle(campMuted)
                ExecutionLocationPicker(selection: $location)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if busy { Text("Finish or stop the current turn before moving this session").font(.system(size: 12)).foregroundStyle(campMuted) }
                    if transferReview,let remote {
                        TransferReviewView(transfer:transfers,local:session,session:remote,input:transferInput ?? transfers.job?.input,busy:busy,permissions:$permissions,catalog:catalog,accountLabel:accounts.accounts.first(where:{$0.id==(transfers.job?.input.providerAccountId ?? transferInput?.providerAccountId)})?.label ?? "",history:sharing.binding(for:session)?.publishedHistory){transferReview=false}
                    } else if location == "Local" { localForm }
                    else if !cloud.signedIn || cloud.scope == nil {
                        Text("Connect your Camp to run sessions in the cloud").font(.system(size: 12))
                        Button("Connect a Camp") { connectCamp = true }
                    } else if let remote {
                        if remote.ownerUserId != cloud.credential?.actor.userId {
                            Text("The session owner chooses where this runs").font(.system(size: 12))
                        } else if currentLocation != "Cloud" && remote.ownerDeviceId != cloud.credential?.actor.deviceId {
                            Text("Open this session on its source Mac to review and move its files").font(.system(size:12))
                        } else if !historyReady { Text(sharing.binding(for:session)?.state ?? "Copying transcript…").font(.system(size:12)) }
                        else if review, let workspace { reviewForm(workspace, remote: remote) }
                        else { cloudForm(remote) }
                    } else { importForm }
                    if !localError.isEmpty { Text(localError).font(.caption).foregroundStyle(.orange) }
                    if !execution.error.isEmpty { Text(execution.error).font(.caption).foregroundStyle(.orange) }
                    if execution.pending, let pendingSession = cloud.sessions.first(where: { $0.id == execution.pendingSessionID }) ?? (remote?.id == execution.pendingSessionID ? remote : nil) {
                        HStack {
                            Text("Setup saved for \(pendingSession.title)").font(.caption)
                            Spacer()
                            Button("Retry") { Task { await execution.retry(pendingSession) } }.disabled(execution.working)
                        }
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(maxHeight: 480)
        }.padding(24).frame(width: 540).background(campCream).foregroundStyle(campInk).tint(campGreen)
            .onAppear {
                location = requestedLocation ?? (currentLocation == "Cloud" ? "Cloud" : "Local")
                projectID = sharing.binding(for: session)?.projectId ?? cloud.projects.first(where: { !$0.isArchived })?.id ?? ""
                permissions = session.provider == "Codex" ? PermissionSettings(sandbox: "workspace-write", approval: "on-request", reviewer: "user") : PermissionSettings(claudeMode: "manual")
            }
            .task { await accounts.refresh(); await git.refresh(); chooseAccount() }
            .task {
                await transfers.restore()
                if let job=transfers.job,job.session.id==remote?.id,!["complete","cancelled"].contains(job.phase) {transferReview=true;transferInput=job.input}
            }
            .task(id: remote?.id) {
                while !Task.isCancelled, let remote, remote.ownerUserId == cloud.credential?.actor.userId {
                    await execution.refresh(remote)
                    do { try await Task.sleep(for: .seconds(2)) } catch { return }
                }
            }
            .onChange(of: candidates.map(\.id)) { _, _ in chooseAccount() }
            .onChange(of: location) { _, _ in review = false; reviewLocal=false; approved = false; localError = "" }
            .sheet(isPresented: $manageAccounts) { VStack(alignment: .leading) { ScrollView { CampProviderAccountsView(cloud: cloud) }; HStack { Spacer(); Button("Done") { manageAccounts = false } } }.padding(24).frame(width: 600, height: 600).background(campCream) }
            .sheet(isPresented: $manageGit) { VStack(alignment: .leading) { ScrollView { CampGitConnectionsView(cloud: cloud) }; HStack { Spacer(); Button("Done") { manageGit = false } } }.padding(24).frame(width: 600, height: 600).background(campCream) }
            .sheet(isPresented: $connectCamp) { CampConnectionView(cloud: cloud) }
    }
    var localForm: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Uses this Mac and its selected \(session.provider) profile").font(.system(size: 12))
            if currentLocation == "Cloud", let remote {
                Text("Review the cloud history before returning. Implementation files need a separate reviewed transfer.").font(.caption).foregroundStyle(campMuted)
                if execution.target?.requiresTransfer == true {
                    Text("This session has cloud history or project changes to transfer before moving back").font(.caption).foregroundStyle(campMuted)
                } else if reviewLocal {
                    Text("Future prompts will use the original provider session on this Mac").font(.caption)
                    Button("Use This Mac") { Task { await execution.assign(remote, workspace:nil,permissions:PermissionSettings(),approved:false) } }
                        .disabled(busy || execution.working || execution.pending)
                } else { Button("Review move") { reviewLocal=true }
                    .disabled(busy || execution.working || execution.pending || remote.ownerDeviceId != cloud.credential?.actor.deviceId)
                }
            }
        }
    }
    var importForm: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Copy this transcript to your private Camp before choosing a cloud runner. It stays private unless you share it.").font(.system(size: 12))
            Picker("Project", selection: $projectID) { ForEach(cloud.projects.filter { !$0.isArchived }) { Text($0.name).tag($0.id) } }
            if let binding = sharing.binding(for: session), binding.syncToCloud == true { Text(binding.state).font(.caption).foregroundStyle(campMuted) }
            Button(copying ? "Copying…" : "Copy to private Camp") {
                copying = true
                Task {
                    defer { copying = false }
                    do { try await sharing.configure(local: session, projectId: projectID, shared: false, allowRequests: false, syncToCloud: true) }
                    catch { localError = error.localizedDescription }
                }
            }.disabled(copying || busy || projectID.isEmpty)
        }
    }
    @ViewBuilder func cloudForm(_ remote: CloudSession) -> some View {
        if let workspace {
            VStack(alignment: .leading, spacing: 10) {
                Text(workspace.repositoryName).font(.system(size: 13, weight: .medium))
                Text("\(workspace.requestedRef) · \(workspace.baseCommit.prefix(10))").font(.caption).foregroundStyle(campMuted)
                if currentLocation != "Cloud" {
                    Text(workspace.transferId == nil ? "Cancel this preparation to review local files for the move" : "A reviewed move is already prepared for this session").font(.caption).foregroundStyle(campMuted)
                } else if workspace.state == "ready" {
                    Text(current(workspace, session: remote) ? "Workspace ready" : "The project or session changed · Cancel preparation and choose a new workspace").font(.caption)
                    Button("Review move") { review = true; approved = false }.disabled(busy || execution.working || execution.pending || !current(workspace, session: remote))
                } else { Label(workspace.state == "cancel_requested" ? "Stopping preparation" : "Preparing workspace", systemImage: "clock").font(.caption) }
                if workspace.id != execution.target?.workspaceId {
                    Button("Cancel preparation") { Task { await execution.cancel(workspace, session: remote) } }.disabled(execution.working || execution.pending)
                }
            }
        } else {
            Picker("\(session.provider) cloud account", selection: $accountID) {
                Text("Choose an account").tag("")
                ForEach(candidates) { Text($0.label).tag($0.id) }
                if !accountID.isEmpty, !candidates.contains(where: { $0.id == accountID }) { Text("Selected account unavailable").tag(accountID) }
            }
            if !accountID.isEmpty, !candidates.contains(where: { $0.id == accountID }) { Text("Choose a connected account or reconnect the selected account").font(.caption).foregroundStyle(.orange) }
            if let message=candidates.first(where:{$0.id==accountID})?.workloadMessage { Text(message).font(.caption).foregroundStyle(campMuted) }
            HStack {
                Button("Manage accounts") { manageAccounts = true }
                Spacer()
                Button("Manage GitHub") { manageGit = true }
            }.font(.system(size: 11))
            if !git.connections.filter({ $0.state == "connected" }).isEmpty {
                Picker("GitHub account", selection: Binding(get: { git.selectedId }, set: { id in git.select(id); repositoryID = 0; Task { await git.refresh(); await git.loadInstallations() } })) {
                    ForEach(git.connections.filter { $0.state == "connected" }) { Text("@" + $0.login).tag($0.id) }
                }
                Picker("Installation", selection: Binding(get: { git.selectedInstallation }, set: { id in repositoryID = 0; Task { await git.loadRepositories(installation: id) } })) {
                    Text("Choose an installation").tag(Int64(0))
                    ForEach(git.installations) { Text($0.account.login).tag($0.id) }
                }.task(id: git.selectedId) { if !git.selectedId.isEmpty { await git.loadInstallations() } }
                if let next = git.nextInstallationPage { Button("More installations") { Task { await git.loadInstallations(page: next) } }.font(.caption) }
                Picker("Repository", selection: $repositoryID) {
                    Text("Choose a project repository").tag(Int64(0))
                    ForEach(repositories) { Text($0.fullName).tag($0.id) }
                }.onChange(of: repositoryID) { _, id in ref = repositories.first { $0.id == id }?.defaultBranch ?? "" }
                if let next = git.nextRepositoryPage { Button("More repositories") { Task { await git.loadRepositories(installation: git.selectedInstallation, page: next) } }.font(.caption) }
                if !git.repositories.isEmpty && repositories.isEmpty { Text("Link a repository to this project in Projects first").font(.caption).foregroundStyle(campMuted) }
                if currentLocation == "Cloud" {TextField("Branch, tag or commit", text: $ref)}
                else {Text("Uses the current local Git commit").font(.caption).foregroundStyle(campMuted)}
                Toggle("Allow implementation in this workspace", isOn: $implementation)
                Text(currentLocation == "Cloud" ? "Cloud preparation uses this repository revision" : "Review selected local changes and the pinned Git revision before moving").font(.caption).foregroundStyle(campMuted)
            }
            if !git.error.isEmpty { Text(git.error).font(.caption).foregroundStyle(.orange) }
            Button("Review move") {
                if currentLocation == "Cloud" {prepare(remote)}
                else if let choice = workspaceInput(remote) {transferInput=choice;transferReview=true}
            }.disabled(busy || !candidates.contains(where: { $0.id == accountID }) || repositoryID == 0 || execution.working || execution.pending)
        }
    }
    @ViewBuilder func reviewForm(_ workspace: CloudWorkspace, remote: CloudSession) -> some View {
        Text(workspace.repositoryName).font(.headline)
        Text("\(workspace.requestedRef) · \(workspace.baseCommit.prefix(12))").font(.system(size: 11, design: .monospaced))
        Text("Uses \(accounts.accounts.first(where: { $0.id == workspace.providerAccountId })?.label ?? session.provider) and @\(workspace.githubLogin)").font(.caption)
        Text("Transcript revision \(remote.transcriptRevision) will be available to the cloud session").font(.caption).foregroundStyle(campMuted)
        if workspace.purpose == "implementation" {
            PermissionFields(provider: session.provider, catalog: catalog, selection: $permissions)
            Toggle("Allow this cloud session to use project tools with these permissions", isOn: $approved)
        }
        HStack {
            Button { review = false } label: { Image(systemName: "chevron.left"); Text("Back") }
            Spacer()
            Button("Use Cloud") { Task { await execution.assign(remote, workspace: workspace, permissions: permissions, approved: workspace.purpose == "implementation" && approved) } }
                .buttonStyle(.borderedProminent).disabled(busy || execution.working || execution.pending || !current(workspace, session: remote) || execution.target?.location == "cloud" && execution.target?.workspaceId == workspace.id || workspace.purpose == "implementation" && !approved)
        }
        if execution.target?.location == "cloud", execution.target?.workspaceId == workspace.id { Label("Runs in Cloud", systemImage: "checkmark").font(.caption) }
    }
    private func chooseAccount() {
        guard accountID.isEmpty else { return }
        let saved = store.executionDefaults.value(for: cloud.scope).cloudAccounts[session.provider] ?? ""
        accountID = workspace?.providerAccountId ?? (saved.isEmpty ? candidates.first?.id ?? "" : saved)
    }
    private func prepare(_ remote: CloudSession) {
        guard let choice=workspaceInput(remote) else{return}
        Task { await execution.prepare(remote,input:choice) }
    }
    private func workspaceInput(_ remote:CloudSession) -> CloudWorkspaceInput? {
        guard let project, let repo = repositories.first(where: { $0.id == repositoryID }), let account = candidates.first(where: { $0.id == accountID }), let connection = git.selection else { return nil }
        return CloudWorkspaceInput(providerAccountId: account.id, gitConnectionId: connection.id, githubInstallationId: git.selectedInstallation, repositoryId: repo.id, installationPage: git.installationPage, repositoryPage: git.repositoryPage, ref: ref, purpose: implementation ? "implementation" : "planning", expectedSessionRevision: remote.revision, expectedProjectRevision: project.version)
    }
}
