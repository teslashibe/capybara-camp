import SwiftUI
import AppKit

struct CampConnectionButton: View {
    @ObservedObject var cloud: CampConnection
    let open: () -> Void
    var body: some View {
        Button(action: open) {
            Label(cloud.signedIn ? (cloud.camps.first { $0.id == cloud.selectedCampId }?.name ?? "Camp") : "Connect a Camp", systemImage: cloud.signedIn ? "person.2" : "network")
                .font(.system(size: 12)).lineLimit(1)
        }.help(cloud.status)
    }
}

struct CampConnectionView: View {
    @ObservedObject var cloud: CampConnection
    @Environment(\.dismiss) private var dismiss
    @State private var projectName = ""
    @State private var inviteEmail = ""
    @State private var inviteRole = "editor"
    @State private var generatedInvitation = ""
    @State private var working = false

    var current: CloudCamp? { cloud.camps.first { $0.id == cloud.selectedCampId } }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack {
                Text("Your Camp").font(.system(size: 23, weight: .semibold))
                Spacer()
                if cloud.busy || working { ProgressView().controlSize(.small) }
                Button { dismiss() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).help("Close").accessibilityLabel("Close Camp settings")
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    if cloud.signedIn { connected } else { CampSignInForm(cloud: cloud) }
                    if !cloud.error.isEmpty {
                        Label(cloud.error, systemImage: "exclamationmark.circle")
                            .font(.system(size: 12)).foregroundStyle(Color.orange).textSelection(.enabled)
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
        }.padding(26).frame(width: 620, height: 640).background(campCream)
    }

    var connected: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 5) {
                Text(cloud.email).font(.system(size: 13, weight: .medium))
                Text(cloud.status).font(.system(size: 12)).foregroundStyle(campMuted)
                Text(cloud.server).font(.system(size: 11)).foregroundStyle(campMuted).textSelection(.enabled)
            }
            CampWorkerStatusView(worker: cloud.worker)
            CampMembershipForm(cloud: cloud)
            if let current {
                Divider()
                Text("Projects").font(.headline)
                ForEach(cloud.projects) { project in Label(project.name, systemImage: "folder").font(.system(size: 12)) }
                if current.role != "viewer" {
                    HStack {
                        TextField("New project name", text: $projectName)
                        Button("Add project") { let name = projectName; run { await cloud.createProject(name: name); projectName = "" } }
                            .disabled(working || projectName.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
                Divider()
                Text("People").font(.headline)
                ForEach(cloud.members) { member in
                    HStack { Label(member.name, systemImage: "person.crop.circle"); Spacer(); Text(member.role.capitalized).foregroundStyle(campMuted) }.font(.system(size: 12))
                }
                if current.role == "owner" {
                    HStack {
                        TextField("Invite by email", text: $inviteEmail).textContentType(.emailAddress)
                        Picker("Role", selection: $inviteRole) { Text("Editor").tag("editor"); Text("Viewer").tag("viewer") }.labelsHidden().frame(width: 100)
                        Button("Invite") { run { generatedInvitation = await cloud.invite(email: inviteEmail, role: inviteRole) ?? "" } }
                            .disabled(working || inviteEmail.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                    if !generatedInvitation.isEmpty {
                        HStack {
                            Text("Invitation ready for \(inviteEmail)").font(.system(size: 12))
                            Spacer()
                            Button("Copy invitation") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(generatedInvitation, forType: .string) }
                        }
                        Text("Send the token privately. Your teammate signs in with the invited email, then chooses Join Camp.")
                            .font(.system(size: 11)).foregroundStyle(campMuted)
                    }
                }
            }
            Divider()
            Button("Sign out of Camp", role: .destructive) { run { await cloud.signOut() } }.disabled(working)
        }.textFieldStyle(.roundedBorder)
    }
    private func run(_ operation: @escaping @MainActor () async -> Void) {
        guard !working else { return }; working = true
        Task { await operation(); working = false }
    }
}

// The same controls power first-run setup and Camp settings. Neither form
// grants access to provider accounts or publishes any local sessions.
struct CampSignInForm: View {
    @ObservedObject var cloud: CampConnection
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Connect to your shared Camp").font(.headline)
            Text("Sign in to your team's server to share selected sessions. Your local Codex and Claude logins stay on this Mac.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            TextField("Camp server", text: $cloud.server).textContentType(.URL)
                .accessibilityLabel("Camp server address").disabled(cloud.codeRequested || cloud.busy)
            TextField("Email address", text: $cloud.email).textContentType(.emailAddress)
                .accessibilityLabel("Camp email address").disabled(cloud.codeRequested || cloud.busy)
            if cloud.codeRequested {
                SecureField("Email code", text: $cloud.code).accessibilityLabel("Email verification code").disabled(cloud.busy)
                HStack {
                    Button("Use a different address") { cloud.codeRequested = false; cloud.code = ""; cloud.error = "" }.disabled(cloud.busy)
                    Spacer()
                    Button("Sign in") { Task { await cloud.verifyCode() } }
                        .buttonStyle(.borderedProminent).tint(campGreen).disabled(cloud.busy || cloud.code.isEmpty)
                }
            } else {
                Button("Send sign-in code") { Task { await cloud.sendCode() } }
                    .buttonStyle(.borderedProminent).tint(campGreen).disabled(cloud.busy || cloud.email.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }.textFieldStyle(.roundedBorder)
    }
}

struct CampMembershipForm: View {
    @ObservedObject var cloud: CampConnection
    @State private var campName = ""
    @State private var invitation = ""
    @State private var working = false
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if !cloud.camps.isEmpty {
                Picker("Camp", selection: Binding(get: { cloud.selectedCampId }, set: { id in run { await cloud.selectCamp(id) } })) {
                    ForEach(cloud.camps) { camp in Text(camp.name).tag(camp.id) }
                }
            }
            HStack {
                TextField("New Camp name", text: $campName)
                Button("Create Camp") { let name = campName; run { await cloud.createCamp(name: name); if cloud.error.isEmpty { campName = "" } } }
                    .disabled(campName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            HStack {
                SecureField("Invitation token", text: $invitation)
                Button("Join Camp") { let token = invitation; run { await cloud.join(token: token); if cloud.error.isEmpty { invitation = "" } } }
                    .disabled(invitation.isEmpty)
            }
            if working { ProgressView().controlSize(.small) }
        }.textFieldStyle(.roundedBorder).disabled(working || cloud.busy)
    }
    private func run(_ operation: @escaping @MainActor () async -> Void) {
        guard !working else { return }; working = true; cloud.error = ""
        Task { await operation(); working = false }
    }
}

struct CloudRequestQueueView: View {
    @ObservedObject var cloud: CampConnection
    let session: CloudSession
    var items: [CloudRequest] { cloud.requests.filter { $0.sessionId == session.id && !["completed", "failed", "cancelled"].contains($0.state) } }
    var pending: [CloudOutboxItem] { cloud.pending.filter { $0.path.contains("/sessions/\(session.id)/") } }
    var body: some View {
        CampWorkerStatusView(worker: cloud.worker, sessionID: session.id, showStatus: false)
        if !items.isEmpty || !pending.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(items) { request in
                    HStack(alignment: .top) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(request.prompt).lineLimit(2)
                            Text(stateName(request.state)).foregroundStyle(campMuted)
                        }
                        Spacer()
                        if request.state == "pending_approval" && request.ownerUserId == cloud.credential?.actor.userId {
                            Button("Approve") { Task { await cloud.queueAction(request: request, action: "approve") } }
                        }
                        if request.state != "outcome_unknown" {
                            Button { Task { await cloud.queueAction(request: request, action: "cancel") } } label: { Image(systemName: "xmark").frame(width: 24, height: 24) }
                                .help("Cancel request").accessibilityLabel("Cancel request")
                        }
                    }
                }
                ForEach(pending) { item in
                    Label(item.state == "pending" || item.state == "sending" ? "Saved on this Mac · Waiting to sync" : "Needs review · \(item.error)", systemImage: item.state == "pending" ? "clock" : "exclamationmark.circle")
                        .foregroundStyle(campMuted)
                }
            }.font(.system(size: 11)).padding(12).background(campInk.opacity(0.035)).clipShape(RoundedRectangle(cornerRadius: 9))
        }
    }
    private func stateName(_ state: String) -> String {
        switch state {
        case "pending_approval": return "Waiting for the session owner's approval"
        case "queued": return "Queued for the owner's Mac"
        case "claimed": return "Starting on the owner's Mac"
        case "started": return "Running on the owner's Mac"
        case "cancel_requested": return "Stopping on the owner's Mac"
        case "outcome_unknown": return "Delivery unconfirmed · The owner must review the transcript"
        default: return state.replacingOccurrences(of: "_", with: " ").capitalized
        }
    }
}

struct CampWorkerStatusView: View {
    @ObservedObject var worker: CampLocalWorker
    var sessionID: String? = nil
    var showStatus = true
    @State private var reviewing: CloudAttempt?
    var items: [CloudAttempt] { worker.issues.filter { sessionID == nil || $0.request.sessionId == sessionID } }
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if showStatus { Label(worker.status, systemImage: "desktopcomputer").font(.system(size: 11)).foregroundStyle(campMuted) }
            ForEach(items) { attempt in
                HStack {
                    Label("Unconfirmed local execution", systemImage: "exclamationmark.circle").font(.system(size: 11))
                    Spacer()
                    Button("Review result") { reviewing = attempt }.font(.system(size: 11))
                }
            }
        }
        .sheet(item: $reviewing) { attempt in
            VStack(alignment: .leading, spacing: 18) {
                Text("Review the interrupted request").font(.headline)
                Text(attempt.request.prompt).lineLimit(5).font(.system(size: 12))
                Text("Check the original session transcript before recording the outcome. Confirming a result will not send the prompt again.")
                    .font(.system(size: 12)).foregroundStyle(campMuted)
                HStack {
                    Button("Back") { reviewing = nil }
                    Spacer()
                    ForEach(["completed", "failed", "cancelled"], id: \.self) { outcome in
                        Button("Mark " + outcome) { Task { await worker.confirm(attempt, outcome: outcome); reviewing = nil } }
                    }
                }
            }.padding(24).frame(width: 580)
        }
    }
}
