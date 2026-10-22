import SwiftUI
import AppKit

enum CampSetupStep: Int, Codable, CaseIterable {
    case profiles, camp, project, ready
    var title: String {
        switch self {
        case .profiles: return "Bring your sessions to camp"
        case .camp: return "Connect with your team"
        case .project: return "Make room for your work"
        case .ready: return "Your camp is ready"
        }
    }
    var shortTitle: String {
        switch self { case .profiles: return "Sessions"; case .camp: return "Camp"; case .project: return "Project"; case .ready: return "Ready" }
    }
}

@MainActor final class CampOnboarding: ObservableObject {
    private struct Progress: Codable {
        var version = 1
        var step: CampSetupStep = .profiles
        var completed = false
        var deferred = false
        var projectID: String?
    }
    @Published private(set) var step: CampSetupStep = .profiles
    @Published private(set) var presented = false
    @Published private(set) var error = ""
    @Published private(set) var savingProject = false
    @Published private(set) var projectID = ""
    private var progress = Progress()
    private let file: URL

    init(directory: URL) {
        file = directory.appendingPathComponent("setup.json")
        if FileManager.default.fileExists(atPath: file.path) {
            if let attrs = try? FileManager.default.attributesOfItem(atPath: file.path),
               let size = attrs[.size] as? NSNumber, size.intValue <= 4096,
               let data = try? Data(contentsOf: file), let saved = try? JSONDecoder().decode(Progress.self, from: data), saved.version == 1 {
                progress = saved; step = saved.step; projectID = saved.projectID ?? ""
                presented = !saved.completed && !saved.deferred
            } else {
                // Don't interrupt an existing installation over unreadable setup state.
                error = "Setup progress could not be read. You can open setup again from the app menu."
            }
        } else {
            let existing = ["session-index.json", "board.json", "local-plans.json", "sources.json", "cloud/camp-cache.sqlite"]
                .contains { FileManager.default.fileExists(atPath: directory.appendingPathComponent($0).path) }
            progress.completed = existing
            presented = !existing
            // Persist before the other stores create their files. Quitting on
            // the first screen must not turn a fresh install into an upgrade.
            if !existing { save() }
        }
    }
    func open() {
        if progress.completed { step = .profiles; progress.completed = false }
        progress.deferred = false; presented = true; save()
    }
    func pause() {
        guard presented, !savingProject else { return }
        progress.deferred = true
        save(); presented = false
    }
    func next() {
        guard !savingProject, let value = CampSetupStep(rawValue: step.rawValue + 1) else { return }
        step = value; save()
    }
    func back() {
        guard !savingProject, let value = CampSetupStep(rawValue: step.rawValue - 1) else { return }
        step = value; save()
    }
    func finish() {
        guard step == .ready, !savingProject else { return }
        progress.completed = true; progress.deferred = false
        save(); presented = false
    }
    func selectProject(_ id: String) { projectID = id; save() }
    func createProject(name: String, repositories: String, local: LocalProjects) async -> String? {
        guard !savingProject else { return nil }
        savingProject = true; defer { savingProject = false }
        do {
            let value = CampProject(id: UUID().uuidString, name: name, repositories: repositories.split(whereSeparator: \.isNewline).map(String.init))
            try await local.save(value, expectedRevision: 0)
            selectProject(value.id)
            return value.id
        } catch { self.error = error.localizedDescription; return nil }
    }
    @discardableResult private func save() -> Bool {
        progress.step = step
        progress.projectID = projectID.isEmpty ? nil : projectID
        do {
            try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            try JSONEncoder().encode(progress).write(to: file, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
            error = ""; return true
        } catch { self.error = "Could not save setup progress: " + error.localizedDescription; return false }
    }
}

struct CampOnboardingView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var setup: CampOnboarding
    @ObservedObject var cloud: CampConnection
    @ObservedObject var projects: LocalProjects
    @ObservedObject var notices: CampNotifications
    @State private var connectCamp = false
    private var projectID: String { setup.projectID }
    @State private var projectName = ""
    @State private var repositories = ""
    @State private var showProfileFallback = false
    private var busy: Bool { setup.savingProject || cloud.busy }
    private var canContinue: Bool {
        if busy { return false }
        if setup.step == .camp && connectCamp { return cloud.scope != nil && cloud.status == "Connected to Camp" }
        if setup.step == .project && projectID == "new" { return projects.ready && !projectName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        return true
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(alignment: .top, spacing: 14) {
                Mascot(size: 58)
                VStack(alignment: .leading, spacing: 5) {
                    Text(setup.step.title).font(.system(size: 24, weight: .semibold))
                    Text("Step \(setup.step.rawValue + 1) of \(CampSetupStep.allCases.count)").font(.system(size: 12)).foregroundStyle(campMuted)
                }
                Spacer()
                Button { setup.pause() } label: { Image(systemName: "xmark").frame(width: 32, height: 32) }
                    .buttonStyle(.plain).disabled(busy).help("Finish setup later").accessibilityLabel("Finish setup later")
            }
            HStack(spacing: 8) {
                ForEach(CampSetupStep.allCases, id: \.rawValue) { step in
                    VStack(alignment: .leading, spacing: 6) {
                        Capsule().fill(step.rawValue <= setup.step.rawValue ? campGreen : campInk.opacity(0.1)).frame(height: 3)
                        Text(step.shortTitle).font(.system(size: 11, weight: step == setup.step ? .semibold : .regular)).foregroundStyle(step == setup.step ? campInk : campMuted)
                    }
                }
            }.accessibilityElement(children: .ignore).accessibilityLabel("Setup step \(setup.step.rawValue + 1) of 4, \(setup.step.shortTitle)")
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    switch setup.step {
                    case .profiles: profiles
                    case .camp: camp
                    case .project: project
                    case .ready: ready
                    }
                    if !setup.error.isEmpty { Label(setup.error, systemImage: "exclamationmark.circle").foregroundStyle(.orange).font(.system(size: 12)).textSelection(.enabled) }
                }.frame(maxWidth: .infinity, alignment: .leading).padding(1)
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
            Divider()
            HStack {
                if setup.step != .profiles {
                    Button { setup.back() } label: { Label("Back", systemImage: "chevron.left").frame(minHeight: 24) }.disabled(busy)
                } else { Button("Finish later") { setup.pause() }.disabled(busy) }
                Spacer()
                if setup.step == .camp || setup.step == .project {
                    Button("Skip this step") { setup.next() }.disabled(busy)
                }
                Button(action: advance) {
                    HStack(spacing: 7) {
                        if setup.savingProject { ProgressView().controlSize(.small) }
                        Text(setup.step == .ready ? "Open my camp" : setup.step == .project && projectID == "new" ? "Create project" : "Continue")
                        Image(systemName: setup.step == .ready ? "arrow.up.right" : "arrow.right")
                    }.frame(minHeight: 24)
                }.buttonStyle(.borderedProminent).tint(campGreen).keyboardShortcut(.defaultAction).disabled(!canContinue)
            }
        }.padding(28).frame(width: 650, height: 610).foregroundStyle(campInk).background(campCream)
            .interactiveDismissDisabled(busy)
            .onAppear { connectCamp = cloud.signedIn || cloud.codeRequested }
    }
    private var profiles: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Your Codex and Claude sessions, together on this Mac").font(.system(size: 14))
            if store.importing { HStack { ProgressView().controlSize(.small); Text(store.sources.isEmpty ? "Finding local profiles…" : "Importing sessions…").font(.system(size: 12)) } }
            LocalProfileList(sources: store.sources, loading: store.importing)
            if store.sources.isEmpty && !store.importing {
                Label("No local profiles found yet", systemImage: "folder").font(.system(size: 13, weight: .medium))
            }
            Text("Camp finds your Codex and Claude profiles automatically, including separate profiles in accessible Mac user folders. New sessions appear without refreshing.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            DisclosureGroup("Missing a profile?", isExpanded: $showProfileFallback) {
                VStack(alignment: .leading, spacing: 10) {
                    Text("Profiles in a custom location can be added here. Camp cannot read another Mac user's locked folders.").font(.system(size: 12)).foregroundStyle(campMuted)
                    Button("Add a custom profile folder", systemImage: "folder.badge.plus") { store.addSource() }.disabled(store.importing)
                }.padding(.top, 8)
            }.font(.system(size: 12))
            Text("Sign in through each provider's CLI first. Importing a folder does not verify its current login, and credentials stay with that CLI.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            HStack(spacing: 18) {
                Link("Set up Codex", destination: URL(string: "https://learn.chatgpt.com/docs/cli")!)
                Link("Set up Claude Code", destination: URL(string: "https://code.claude.com/docs/en/setup")!)
            }.font(.system(size: 12)).tint(campGreen)
            let problem = [store.sourceError, store.importError].filter { !$0.isEmpty }.joined(separator: " · ")
            if !problem.isEmpty { Label(problem, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange).textSelection(.enabled) }
        }
    }
    private var camp: some View {
        VStack(alignment: .leading, spacing: 18) {
            Toggle("Connect a shared Camp", isOn: $connectCamp).toggleStyle(.checkbox).disabled(busy)
            if connectCamp {
                if cloud.signedIn {
                    Label(cloud.email, systemImage: "person.crop.circle").font(.system(size: 13, weight: .medium))
                    Text(cloud.status).font(.system(size: 12)).foregroundStyle(campMuted)
                    CampMembershipForm(cloud: cloud)
                } else { CampSignInForm(cloud: cloud) }
                if !cloud.error.isEmpty { Label(cloud.error, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange).textSelection(.enabled) }
            } else {
                Text("Keep working locally, or connect to your team's Camp server to collaborate. You can add this later from Connect a Camp.").font(.system(size: 13)).foregroundStyle(campMuted)
            }
            Text("Only sessions you choose to share become visible to teammates. Shared Camp sign-in is separate from your Codex and Claude accounts; this step does not move execution to a cloud runner.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
        }
    }
    private var project: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Projects group client work across repositories, plans and sessions").font(.system(size: 13))
            Picker("Local project", selection: Binding(get: { projectID }, set: { setup.selectProject($0) })) {
                Text("Start in Inbox").tag("")
                ForEach(projects.projects.filter { !$0.isArchived && !$0.isInbox }) { project in Text(project.name).tag(project.id) }
                Text("Create a project…").tag("new")
            }
            if projectID == "new" { CampProjectFields(name: $projectName, repositories: $repositories) }
            Text("This project stays on your Mac. Assign cards to it from their project menu. Shared projects are available in Camp settings after connecting; linking a repository doesn't grant Git access.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            if !projects.error.isEmpty && projects.error != setup.error { Text(projects.error).font(.system(size: 12)).foregroundStyle(.orange) }
        }.disabled(setup.savingProject)
    }
    private var ready: some View {
        VStack(alignment: .leading, spacing: 20) {
            let count = store.sessions.filter { !$0.isCloud && !$0.isPlan }.count
            Label("\(store.sources.count) local \(store.sources.count == 1 ? "profile" : "profiles") · \(count.formatted()) \(count == 1 ? "session" : "sessions")", systemImage: "rectangle.stack").font(.system(size: 14, weight: .medium))
            Label(cloud.signedIn ? cloud.status : "Working locally on this Mac", systemImage: cloud.signedIn ? "person.2" : "desktopcomputer").font(.system(size: 13))
            Label(projects.byID[projectID]?.name ?? "Inbox", systemImage: "folder").font(.system(size: 13))
            Text("Open a card to continue a chat. Choose Plan for one model, or Plan with comparison for two. New sessions appear automatically.").font(.system(size: 13)).foregroundStyle(campMuted)
            Toggle("Desktop notifications when work finishes", isOn: Binding(get: { notices.preferences.desktop }, set: { value in Task { await notices.enableDesktop(value) } }))
                .toggleStyle(.checkbox).disabled(notices.permissionBusy)
            if !notices.error.isEmpty { Text(notices.error).font(.system(size: 12)).foregroundStyle(.orange) }
            Text("You can revisit these steps from Capybara Camp → Set up Capybara Camp.").font(.system(size: 12)).foregroundStyle(campMuted)
        }
    }
    private func advance() {
        guard canContinue else { return }
        if setup.step == .ready {
            setup.finish()
        } else if setup.step == .project && projectID == "new" {
            Task {
                if await setup.createProject(name: projectName, repositories: repositories, local: projects) != nil { setup.next() }
            }
        } else { setup.next() }
    }
}

struct LocalProfileList: View {
    let sources: [CampSource]
    var loading = false
    var body: some View {
        LazyVStack(spacing: 10) {
            ForEach(sources) { source in
                VStack(alignment: .leading, spacing: 5) {
                    HStack { Text(source.provider + " · " + source.label).fontWeight(.medium); Spacer(); Text(loading && source.count == 0 ? "Reading sessions…" : source.count.formatted()).foregroundStyle(campMuted) }
                    Text(source.root).font(.system(size: 11, design: .monospaced)).textSelection(.enabled).foregroundStyle(campMuted)
                }.font(.system(size: 12)).padding(13).frame(maxWidth: .infinity, alignment: .leading).background(.white.opacity(0.75)).clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}
