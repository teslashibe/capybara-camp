import Foundation

@main struct ExecutionDefaultsTests {
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-defaults-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let defaults = ExecutionDefaults(directory: root)
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        var preferences = ExecutionPreferences()
        preferences.location = "Cloud"
        preferences.projectID = UUID().uuidString
        preferences.cloudAccounts = ["Codex": UUID().uuidString, "Claude": UUID().uuidString]
        precondition(preferences.valid && preferences.configured)
        precondition(defaults.save(preferences, for: scope))
        let restored = ExecutionDefaults(directory: root)
        precondition(restored.value(for: scope) == preferences && restored.readable)
        precondition(restored.value(for: nil).location == "Cloud", "Signing out must not silently turn Cloud defaults into Local")
        precondition(restored.value(for: nil).cloudAccounts.isEmpty, "Account IDs must remain in their Camp scope")
        for foreign in [CloudScope(origin: scope.origin, userId: UUID().uuidString, campId: scope.campId),
                        CloudScope(origin: scope.origin, userId: scope.userId, campId: UUID().uuidString),
                        CloudScope(origin: "https://different.example", userId: scope.userId, campId: scope.campId)] {
            precondition(restored.value(for: foreign).cloudAccounts.isEmpty, "Saved accounts crossed the signed-in person, Camp or server")
        }
        let file = root.appendingPathComponent("execution-defaults.json")
        let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
        precondition(attributes[.posixPermissions] as? Int == 0o600)
        var invalid = preferences; invalid.cloudAccounts["Codex"] = "not-an-account"
        precondition(!defaults.save(invalid, for: scope) && defaults.value(for: scope) == preferences)
        precondition(preferences.preferredProvider(selected: "Claude", localAvailable: []) == "Claude")
        preferences.provider = "Codex"
        precondition(preferences.preferredProvider(selected: "Claude", localAvailable: []) == "Codex")

        let store = CampStore(stateDirectory: root.appendingPathComponent("app"))
        func source(_ provider: String, _ profile: String) -> CampSession {
            CampSession(id: provider + profile, sessionID: provider + profile, provider: provider, sourceID: profile, sourceLabel: profile,
                root: root.path, title: provider + profile, cwd: root.path, updated: 1, path: "", accountID: "", archived: false, model: provider + "-model")
        }
        store.sessions = [source("Codex", "first"), source("Codex", "second"), source("Claude", "claude")]
        var local = ExecutionPreferences()
        local.provider = "Codex"; local.localProfiles["Codex"] = "second"
        precondition(store.executionDefaults.save(local, for: nil))
        store.newPlan()
        let plan = store.planning.tickets.first!
        precondition(plan.current.planners.count == 1 && plan.current.planners[0].session.sourceID == "second")
        let count = store.planning.tickets.count
        local.localProfiles["Codex"] = "missing"
        precondition(store.executionDefaults.save(local, for: nil))
        store.newPlan()
        precondition(store.planning.tickets.count == count && store.sourceTab == "Defaults" && store.sourceSheet,
                     "Missing explicitly chosen profile fell back to a different login")
        precondition(!store.executionDefaultsMessage.isEmpty, "Unavailable default must have an explanation inside its settings sheet")
        precondition(store.planning.ticket(plan.id) == plan, "Changing defaults altered an existing plan")
        local.location = "Cloud"; local.projectID = preferences.projectID; local.cloudAccounts = preferences.cloudAccounts
        precondition(store.executionDefaults.save(local, for: nil))
        store.newPlan()
        precondition(store.planning.tickets.count == count && store.sourceSheet, "Signed-out Cloud default launched local work")
        store.newPlan(location: "Local")
        precondition(store.planning.tickets.count == count, "Explicit Local must still honor the selected profile")
        local.localProfiles["Codex"] = "first"
        precondition(store.executionDefaults.save(local, for: nil))
        store.newPlan(location: "Local")
        precondition(store.planning.tickets.count == count + 1, "Explicit Local override did not open a reviewed draft")
        precondition(store.conversations.isEmpty && Bridge.activeProcesses.isEmpty, "Choosing defaults launched provider work")

        var session = CloudSession(id: UUID().uuidString, campId: scope.campId, projectId: preferences.projectID, profileId: UUID().uuidString,
            provider: "Codex", profileLabel: "Fixture", ownerUserId: scope.userId, ownerDeviceId: UUID().uuidString,
            localSessionId: UUID().uuidString, title: "Prepared source", model: "fixture", state: "idle", stage: "Ready",
            shared: false, allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 1, permissionRevision: 1, updatedAt: Date())
        session.executionLocation = "cloud"; session.executionEpoch = 2; session.executionWorkspaceId = UUID().uuidString
        var source = CloudPlanningSource(session: session, accountId: preferences.cloudAccounts["Codex"]!, repositoryName: "fixture/repo")
        precondition(source.valid(scope: scope, projectID: preferences.projectID, accountID: source.accountId))
        source.session.ownerUserId = UUID().uuidString
        precondition(!source.valid(scope: scope, projectID: preferences.projectID, accountID: source.accountId), "Foreign source passed validation")
        source.session = session; source.session.executionLocation = "local"
        precondition(!source.valid(scope: scope, projectID: preferences.projectID, accountID: source.accountId), "Local source was accepted as Cloud")

        try Data("{invalid".utf8).write(to: file)
        let corrupt = ExecutionDefaults(directory: root)
        precondition(!corrupt.readable && !corrupt.error.isEmpty)
        precondition(corrupt.save(ExecutionPreferences(), for: nil) && corrupt.readable, "Explicitly reviewed defaults cannot repair unreadable state")
        let blockedPath = root.appendingPathComponent("not-a-directory")
        try Data().write(to: blockedPath)
        let blocked = ExecutionDefaults(directory: blockedPath)
        precondition(!blocked.save(local, for: nil) && blocked.value(for: nil) == ExecutionPreferences(), "Failed persistence changed active defaults")
        print("PASS: execution defaults preserve account scope, explicit choices, existing plans and reviewed recovery")
    }
}
