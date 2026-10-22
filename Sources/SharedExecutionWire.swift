import Foundation

struct SharedExecutionInput: Codable, Sendable {
    var expectedRevision: Int64
    var expectedVersion: Int
    var executor: CloudPlannerInput
    var expectedSessionRevision: Int64
    var expectedTranscriptRevision: Int64
    var permissions: [String: String]
}
struct SharedExecutionStatus: Codable, Equatable, Identifiable, Sendable {
    var id: String; var state: String; var revision: Int64; var reviewRequired: Bool
    var valid: Bool { UUID(uuidString: id) != nil && revision > 0 && ["pending", "approved", "cancelled", "dispatched"].contains(state) }
    var actionable: Bool { state == "pending" || state == "approved" }
}
struct SharedExecutionApproval: Codable, Equatable, Identifiable, Sendable {
    var id: String; var campId: String; var planId: String; var planVersion: Int; var planRevision: Int64; var planSha256: String
    var creatorUserId: String; var ownerUserId: String; var ownerDeviceId: String; var sessionId: String; var profileId: String
    var sessionRevision: Int64; var transcriptRevision: Int64; var permissionRevision: Int64
    var provider: String; var model: String; var effort: String; var speed: String
    var execution: CloudPlanningExecution; var permissions: [String: String]; var executionContext: String?
    var prompt: String; var state: String; var revision: Int64
    var valid: Bool {
        [id,campId,planId,creatorUserId,ownerUserId,ownerDeviceId,sessionId,profileId].allSatisfy { UUID(uuidString: $0) != nil } &&
        planVersion > 0 && planRevision > 0 && sessionRevision > 0 && transcriptRevision >= 0 && permissionRevision > 0 && revision > 0 &&
        planSha256.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil && execution.valid &&
        !model.isEmpty && model.utf8.count <= 200 && effort.utf8.count <= 64 && speed.utf8.count <= 64 && prompt.utf8.count <= 65536 &&
        ["pending", "approved", "cancelled", "dispatched"].contains(state) && settings?.valid(for: provider) == true &&
        (executionContext == nil || executionContext == "" || executionContext?.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil)
    }
    var settings: PermissionSettings? { PermissionSettings(wireValues: permissions) }
}
extension PermissionSettings {
    init?(wireValues: [String: String]) {
        guard Set(wireValues.keys).isSubset(of: ["profile", "sandbox", "approval", "reviewer", "claudeMode"]) else { return nil }
        self.init(profile: wireValues["profile"] ?? "", sandbox: wireValues["sandbox"] ?? "", approval: wireValues["approval"] ?? "",
            reviewer: wireValues["reviewer"] ?? "", claudeMode: wireValues["claudeMode"] ?? "")
    }
    var wireValues: [String: String] {
        ["profile": profile, "sandbox": sandbox, "approval": approval, "reviewer": reviewer, "claudeMode": claudeMode].filter { !$0.value.isEmpty }
    }
}
extension PermissionCatalog {
    static func requested(_ provider: String, inherited: String) -> PermissionCatalog {
        PermissionCatalog(provider: provider, modes: ["manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"], profiles: [],
            sandboxes: ["read-only", "workspace-write", "danger-full-access"], approvals: ["on-request", "never"], reviewers: ["user", "auto_review"], inherited: inherited, note: "")
    }
}

enum SharedExecutionAction: String { case prepare, approve, cancel, dispatch }
