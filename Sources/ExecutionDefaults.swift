import Foundation

struct ExecutionPreferences: Codable, Equatable, Sendable {
    var location = "Local"
    var provider = ""
    var localProfiles: [String: String] = [:]
    var cloudAccounts: [String: String] = [:]
    var projectID = ""
    var valid: Bool {
        ["Local", "Cloud"].contains(location) && ["", "Codex", "Claude"].contains(provider) &&
        Set(localProfiles.keys).isSubset(of: ["Codex", "Claude"]) && Set(cloudAccounts.keys).isSubset(of: ["Codex", "Claude"]) &&
        localProfiles.values.allSatisfy { $0.utf8.count <= 512 && !$0.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) } &&
        cloudAccounts.values.allSatisfy { $0.isEmpty || UUID(uuidString: $0) != nil } &&
        (projectID.isEmpty || UUID(uuidString: projectID) != nil)
    }
    var configured: Bool { valid && (location == "Local" || !projectID.isEmpty && cloudAccounts.values.contains(where: { !$0.isEmpty })) }
    func preferredProvider(selected: String?, localAvailable: Set<String>) -> String {
        if !provider.isEmpty { return provider }
        let available = location == "Cloud" ? Set(cloudAccounts.filter { !$0.value.isEmpty }.map(\.key)) : localAvailable
        if let selected, available.contains(selected) { return selected }
        return available.contains("Codex") ? "Codex" : "Claude"
    }
}

@MainActor final class ExecutionDefaults: ObservableObject {
    private struct Saved: Codable {
        var version = 1
        var local = ExecutionPreferences()
        var camps: [String: ExecutionPreferences] = [:]
    }
    @Published private(set) var revision: UInt64 = 0
    @Published private(set) var error = ""
    @Published private(set) var readable = true
    private var saved = Saved()
    private let file: URL
    init(directory: URL) {
        file = directory.appendingPathComponent("execution-defaults.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        do {
            let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
            guard attributes[.type] as? FileAttributeType == .typeRegular,
                  ((attributes[.size] as? NSNumber)?.intValue ?? Int.max) <= 65536 else { throw CampCloudError.invalidResponse }
            let value = try JSONDecoder().decode(Saved.self, from: Data(contentsOf: file))
            guard value.version == 1, value.local.valid, value.camps.count <= 64,
                  value.camps.allSatisfy({ $0.key.utf8.count == 64 && $0.key.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) } && $0.value.valid }) else { throw CampCloudError.invalidResponse }
            saved = value
        } catch { readable = false; self.error = "Saved execution defaults could not be read. Review and save them before starting a new plan" }
    }
    func value(for scope: CloudScope?) -> ExecutionPreferences {
        guard let scope else { return saved.local }
        return saved.camps[scope.key] ?? ExecutionPreferences()
    }
    @discardableResult func save(_ value: ExecutionPreferences, for scope: CloudScope?) -> Bool {
        guard value.valid else { error = "Choose valid execution defaults"; return false }
        var next = saved
        if let scope {
            guard next.camps[scope.key] != nil || next.camps.count < 64 else { error = "Too many saved Camps for execution defaults"; return false }
            next.camps[scope.key] = value
            // Signing out must not silently turn a requested cloud plan into
            // local work. Account choices remain private to their Camp scope.
            next.local.location = value.location
        } else { next.local = value }
        do {
            let data = try JSONEncoder().encode(next)
            guard data.count <= 65536 else { throw CampCloudError.responseTooLarge }
            try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            try data.write(to: file, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
            saved = next; readable = true; error = ""; revision &+= 1; return true
        } catch { self.error = "Execution defaults could not be saved"; return false }
    }
}

struct CloudPlanningSource: Codable, Equatable, Sendable {
    var session: CloudSession
    var accountId: String
    var repositoryName: String
    func valid(scope: CloudScope, projectID: String, accountID: String) -> Bool {
        session.campId == scope.campId && session.ownerUserId == scope.userId && session.projectId == projectID &&
        accountId == accountID && UUID(uuidString: accountId) != nil && UUID(uuidString: session.id) != nil &&
        ["Codex", "Claude"].contains(session.provider) && CloudPlanningExecution(session: session)?.location == "cloud" &&
        repositoryName.utf8.count <= 140 && !repositoryName.isEmpty &&
        !repositoryName.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains)
    }
}

extension CampConnection {
    func planningSources(projectID: String, accountID: String) async throws -> [CloudPlanningSource] {
        guard UUID(uuidString: projectID) != nil, UUID(uuidString: accountID) != nil,
              let scope, let credential, let client else { throw CampCloudError.invalidResponse }
        let values = try await client.get([CloudPlanningSource].self,
            path: "/v1/camps/\(scope.campId)/planning-sources?project_id=\(projectID)&account_id=\(accountID)", token: credential.token)
        guard self.scope == scope, self.credential?.token == credential.token, !Task.isCancelled else { throw CancellationError() }
        guard values.count <= 100, Set(values.map { $0.session.id }).count == values.count,
              values.allSatisfy({ $0.valid(scope: scope, projectID: projectID, accountID: accountID) }) else { throw CampCloudError.invalidResponse }
        return values
    }
}
