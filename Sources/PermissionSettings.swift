import Foundation
import SwiftUI

struct PermissionSettings: Codable, Equatable {
    var profile = ""
    var sandbox = ""
    var approval = ""
    var reviewer = ""
    var claudeMode = ""
    var inherited: Bool { profile.isEmpty && sandbox.isEmpty && approval.isEmpty && reviewer.isEmpty && claudeMode.isEmpty }
    func valid(for provider: String) -> Bool {
        let short = [profile, sandbox, approval, reviewer, claudeMode].allSatisfy { $0.utf8.count <= 160 }
        if provider == "Claude" { return short && profile.isEmpty && sandbox.isEmpty && approval.isEmpty && reviewer.isEmpty && ["", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"].contains(claudeMode) }
        return provider == "Codex" && short && claudeMode.isEmpty && (profile.isEmpty || sandbox.isEmpty) &&
            profile.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || ":_.-".contains($0)) } &&
            ["", "read-only", "workspace-write", "danger-full-access"].contains(sandbox) &&
            ["", "on-request", "never"].contains(approval) && ["", "user", "auto_review"].contains(reviewer) &&
            !(reviewer == "auto_review" && approval == "never")
    }
    var summary: String {
        if inherited { return "Inherit CLI settings" }
        return [profile, sandbox, approval, reviewer, claudeMode].filter { !$0.isEmpty }.map(Self.label).joined(separator: " · ")
    }
    static func label(_ value: String) -> String {
        switch value {
        case "": return "Inherit"
        case "read-only", ":read-only": return "Read only"
        case "workspace-write", ":workspace": return "Workspace access"
        case "danger-full-access", ":danger-full-access": return "Full access"
        case "on-request": return "Ask when needed"
        case "never": return "Never ask"
        case "user", "manual": return "Manual"
        case "auto_review": return "Approve for me"
        case "acceptEdits": return "Accept edits"
        case "dontAsk": return "Don't ask · Deny unapproved actions"
        case "bypassPermissions": return "Bypass permissions"
        case "auto": return "Auto"
        case "plan": return "Plan"
        default: return value
        }
    }
}

struct PermissionCatalog: Codable {
    var provider: String
    var modes: [String]
    var profiles: [String]
    var sandboxes: [String]
    var approvals: [String]
    var reviewers: [String]
    var inherited: String
    var note: String
    func accepts(_ settings: PermissionSettings) -> Bool {
        settings.valid(for: provider) && [(settings.profile, profiles), (settings.sandbox, sandboxes),
            (settings.approval, approvals), (settings.reviewer, reviewers), (settings.claudeMode, modes)]
            .allSatisfy { $0.0.isEmpty || $0.1.contains($0.0) }
    }
}

struct SessionPermissionGrant: Codable, Equatable {
    var revision = UUID().uuidString
    var settings: PermissionSettings
    var context: ExecutionContext
}

@MainActor final class LocalPermissions {
    private let file: URL
    private(set) var grants: [String: SessionPermissionGrant] = [:]
    private(set) var error = ""
    init(directory: URL) {
        file = directory.appendingPathComponent("session-permissions.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        do {
            guard (try file.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0) <= 8 << 20 else { throw CampError.message("Saved permission settings exceed the size limit") }
            let data = try Data(contentsOf: file)
            guard data.count <= 8 << 20 else { throw CampError.message("Saved permission settings exceed the size limit") }
            let values = try JSONDecoder().decode([String: SessionPermissionGrant].self, from: data)
            guard values.count <= 10000, values.values.allSatisfy({ UUID(uuidString: $0.revision) != nil && $0.context.fingerprint.count == 64 && $0.settings.valid(for: $0.context.provider) }) else { throw CampError.message("Saved permission settings are invalid") }
            grants = values
        } catch { self.error = "Permission settings could not be restored: " + error.localizedDescription }
    }
    func save(_ grant: SessionPermissionGrant?, for id: String) throws {
        var next = grants; next[id] = grant
        guard next.count <= 10000 else { throw CampError.message("Too many saved permission settings") }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let data = try JSONEncoder().encode(next)
        guard data.count <= 8 << 20 else { throw CampError.message("Saved permission settings exceed the size limit") }
        try data.write(to: file, options: [.atomic, .completeFileProtectionUnlessOpen])
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        grants = next; error = ""
    }
}

extension CampStore {
    func permissionSettings(_ s: CampSession) -> PermissionSettings {
        sessionPermissions.grants[s.id]?.settings ?? planning.ticket(s.id)?.implementation?.executor.permissions ?? PermissionSettings()
    }
    func permissionRevision(_ s: CampSession) -> String? {
        sessionPermissions.grants[s.id]?.revision ?? planning.ticket(s.id)?.implementation.map { $0.id + ":" + $0.context.fingerprint }
    }
    func permissionCatalog(_ s: CampSession) async throws -> PermissionCatalog {
        let data = try await Bridge.call("permissions", input: JSONEncoder().encode(["session": s]))
        let result = try JSONDecoder().decode(PermissionCatalog.self, from: data)
        guard result.provider == s.provider else { throw CampError.message("The provider changed while reading its settings") }
        return result
    }
    func applyPermissions(_ settings: PermissionSettings, session: CampSession, preview: Bool) async throws {
        guard !session.isCloud, session.provider != "Planning", sessionPermissions.error.isEmpty else { throw CampError.message("Restore the local session before changing permissions") }
        guard settings.valid(for: session.provider) else { throw CampError.message("Invalid provider permission settings") }
        let initialRevision = permissionRevision(session)
        let context = preview ? nil : try await inspectExecution(session, permissions: settings)
        guard permissionRevision(session) == initialRevision,
              sessions.contains(where: { $0.id == session.id && $0.root == session.root && $0.cwd == session.cwd }) else { throw CampError.message("This session changed while saving permissions") }
        let grant = context.map { SessionPermissionGrant(settings: settings, context: $0) }
        try sessionPermissions.save(grant, for: session.id)
        pausePermissionQueue(session.id)
        objectWillChange.send()
    }
}

struct PermissionFields: View {
    let provider: String
    let catalog: PermissionCatalog
    @Binding var selection: PermissionSettings
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Inherit · \(catalog.inherited)").font(.caption).foregroundStyle(campMuted).textSelection(.enabled)
            if provider == "Codex" {
                Picker("Permission profile", selection: Binding(get: { selection.profile }, set: { selection.profile = $0; if !$0.isEmpty { selection.sandbox = "" } })) {
                    Text("Inherit").tag("")
                    ForEach(catalog.profiles, id: \.self) { Text(PermissionSettings.label($0)).tag($0) }
                }
                Picker("Sandbox", selection: Binding(get: { selection.sandbox }, set: { selection.sandbox = $0; if !$0.isEmpty { selection.profile = "" } })) {
                    Text("Inherit").tag("")
                    ForEach(catalog.sandboxes, id: \.self) { Text(PermissionSettings.label($0)).tag($0) }
                }
                Picker("Approvals", selection: $selection.approval) {
                    Text("Inherit").tag("")
                    ForEach(catalog.approvals, id: \.self) { Text(PermissionSettings.label($0)).tag($0) }
                }
                Picker("Reviewer", selection: $selection.reviewer) {
                    Text("Inherit").tag("")
                    ForEach(catalog.reviewers, id: \.self) { Text(PermissionSettings.label($0)).tag($0) }
                }
            } else {
                Picker("Permission mode", selection: $selection.claudeMode) {
                    Text("Inherit").tag("")
                    ForEach(catalog.modes, id: \.self) { Text(PermissionSettings.label($0)).tag($0) }
                }
            }
            if !catalog.accepts(selection) {
                Text("Unavailable selection · \(selection.summary)").font(.caption).foregroundStyle(.red)
            }
            Text(catalog.note).font(.caption).foregroundStyle(campMuted)
        }
    }
}

struct SessionPermissionsView: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    @State private var selection = PermissionSettings()
    @State private var preview = true
    @State private var catalog: PermissionCatalog?
    @State private var busy = false
    @State private var error = ""
    var implementation: Bool { store.planning.ticket(session.id)?.implementation != nil }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack { Text("Permissions").font(.system(size: 20, weight: .medium)); Spacer(); Button { store.permissionSheet = nil } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Close permissions").accessibilityLabel("Close permissions").keyboardShortcut(.cancelAction) }
            Text(session.title).font(.headline).lineLimit(2)
            if !implementation { Toggle("Chat preview only", isOn: $preview).toggleStyle(.switch) }
            if let catalog { PermissionFields(provider: session.provider, catalog: catalog, selection: $selection).disabled(preview) }
            else if error.isEmpty { ProgressView().controlSize(.small) }
            if let run = store.conversation(session) {
                Text("Current turn · \(run.effectivePermissions.isEmpty ? "Starting" : run.effectivePermissions)").font(.caption).foregroundStyle(campMuted)
                Text("Your change applies to the next Camp turn").font(.caption).foregroundStyle(campMuted)
            }
            Text(preview ? "Chat preview keeps project tools disabled" : "Project tools will use these permissions in this profile and working folder. Required approvals and questions appear in chat.").font(.system(size: 12)).foregroundStyle(campMuted)
            if !error.isEmpty { Text(error).font(.caption).foregroundStyle(.red).textSelection(.enabled) }
            HStack {
                Spacer()
                Button("Cancel") { store.permissionSheet = nil }
                Button(preview ? "Use chat preview" : "Apply to next turn") {
                    busy = true
                    Task {
                        do { try await store.applyPermissions(selection, session: session, preview: preview); store.permissionSheet = nil }
                        catch { self.error = error.localizedDescription }
                        busy = false
                    }
                }.buttonStyle(.borderedProminent).tint(campGreen).disabled(busy || (!preview && !(catalog?.accepts(selection) ?? false)))
            }
        }.padding(24).frame(width: 480).background(campCream).foregroundStyle(campInk).tint(campGreen).disabled(busy)
        .task {
            selection = store.permissionSettings(session)
            preview = !implementation && store.sessionPermissions.grants[session.id] == nil
            do { catalog = try await store.permissionCatalog(session) } catch { self.error = error.localizedDescription }
        }
    }
}

struct ImplementationPermissions: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: LocalPlanning
    let id: String
    let executor: LocalPlanner
    @State private var catalog: PermissionCatalog?
    @State private var error = ""
    var body: some View {
        DisclosureGroup("Permissions · \((executor.permissions ?? PermissionSettings()).summary)") {
            if let catalog {
                PermissionFields(provider: executor.session.provider, catalog: catalog,
                    selection: Binding(get: { executor.permissions ?? PermissionSettings() }, set: { value in planning.editExecutor(id) { $0.permissions = value } }))
            } else if error.isEmpty { ProgressView().controlSize(.small) }
            else { Text(error).font(.caption).foregroundStyle(campMuted) }
        }.font(.system(size: 12)).task(id: executor.session.sourceID) {
            catalog = nil; error = ""
            do {
                let value = try await store.permissionCatalog(executor.session)
                guard !Task.isCancelled else { return }
                catalog = value
            } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
        }
    }
}
