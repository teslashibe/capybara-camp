import Foundation

struct LocalPlanImportInput: Codable, Equatable, Sendable {
    struct Version: Codable, Equatable, Sendable {
        var version: Int
        var mode: PlanningMode
        var state: String
        var brief: String
        var budgetPolicy: String
        var mergeSlot: Int
        var planners: [CloudPlannerInput]
        var sources: [CloudSourceRef]
        var localSources: [ImportedPlanningSource]
        var steps: [ImportedPlanningStep]
    }
    struct Attempt: Codable, Equatable, Sendable {
        var id: String
        var prompt: String
        var model: String
        var effort: String
        var speed: String
        var state: String
        var error: String
        var usage: ConversationUsage
        var permissions: PermissionSettings?
        var permissionFingerprint: String?
    }
    struct Implementation: Codable, Equatable, Sendable {
        var sourceId: String
        var planVersion: Int
        var sessionId: String
        var transcriptRevision: Int64
        var messageCount: Int
        var provider: String
        var state: String
        var model: String
        var effort: String
        var speed: String
        var planDigest: String
        var contextFingerprint: String
        var initialPrompt: String
        var permissions: PermissionSettings?
        var permissionSummary: String
        var attempts: [Attempt]
    }
    var sourceId: String
    var title: String
    var projectId: String
    var versions: [Version]
    var implementations: [Implementation]
}

// A complete, pinned transcript read by the publisher. The mapper compares it
// with the local saved implementation before referring to its cloud revision.
struct ImportedTranscriptBinding {
    var session: CloudSession
    var revision: Int64
    var messages: [CloudMessage]
}

struct PreparedLocalPlanImport: Codable, Equatable, Sendable {
    var localFingerprint: String
    var input: LocalPlanImportInput
    var permissions: [String: Int64]
    var body: Data { get throws { try CampWire.encoder().encode(input) } }
    var valid: Bool {
        guard localFingerprint.isImportDigest, input.sourceId.isImportDigest,
              !input.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, input.title.utf8.count <= 2000,
              UUID(uuidString: input.projectId) != nil, (1...100).contains(input.versions.count), input.implementations.count <= 100,
              permissions.allSatisfy({ UUID(uuidString: $0.key) != nil && $0.value > 0 }),
              Set(input.implementations.map(\.sourceId)).count == input.implementations.count else { return false }
        for (index, version) in input.versions.enumerated() {
            guard version.version == index + 1,
                  ["draft", "paused", "ready", "cancelled", "failed", "needs_review"].contains(version.state),
                  version.brief.utf8.count <= 8192, version.state == "draft" || !version.brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
                  ["calls", "strict_tokens"].contains(version.budgetPolicy),
                  version.planners.count == version.mode.plannerCount, (0..<version.mode.plannerCount).contains(version.mergeSlot),
                  version.planners.allSatisfy({ permissions[$0.sessionId] != nil && $0.execution == nil && $0.model.utf8.count <= 200 && $0.effort.utf8.count <= 64 && $0.speed.utf8.count <= 64 }),
                  version.sources.count + version.localSources.count <= 20,
                  Set(version.sources.map(\.sessionId)).count == version.sources.count,
                  version.sources.allSatisfy({ ref in
                      let ids = ref.messageIds ?? []
                      return permissions[ref.sessionId] == ref.permissionRevision && ref.transcriptRevision > 0 &&
                          (1...120).contains(ids.count) && Set(ids).count == ids.count && ids.allSatisfy { !$0.isEmpty && $0.utf8.count <= 256 }
                  }),
                  validPlanningQuotes(version.localSources, sharedCount: version.sources.count),
                  version.steps.isEmpty || version.steps.count == version.mode.turnLimit,
                  version.state != "draft" || version.steps.isEmpty,
                  version.steps.enumerated().allSatisfy({ $0.offset == $0.element.ordinal && $0.element.valid }),
                  version.state != "ready" || version.steps.count == version.mode.turnLimit && version.steps.allSatisfy({ $0.state == "completed" && !$0.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) else { return false }
        }
        for item in input.implementations {
            guard item.sourceId.isImportDigest, (1...input.versions.count).contains(item.planVersion), permissions[item.sessionId] != nil,
                  item.transcriptRevision >= 0, (0...2000).contains(item.messageCount), item.messageCount == 0 || item.transcriptRevision > 0,
                  ["Codex", "Claude"].contains(item.provider), ["approved", "idle", "complete", "needs_review"].contains(item.state),
                  item.model.utf8.count <= 200, item.effort.utf8.count <= 64, item.speed.utf8.count <= 64,
                  item.planDigest.isImportDigest, item.contextFingerprint.isImportDigest, item.initialPrompt.utf8.count <= 65536,
                  item.permissionSummary.utf8.count <= 2048, Self.validPermissions(item.permissions), item.attempts.count <= 200,
                  Set(item.attempts.map(\.id)).count == item.attempts.count,
                  item.attempts.allSatisfy({ attempt in
                      UUID(uuidString: attempt.id) != nil && attempt.prompt.utf8.count <= 65536 && attempt.model.utf8.count <= 200 &&
                      attempt.effort.utf8.count <= 64 && attempt.speed.utf8.count <= 64 && attempt.error.utf8.count <= 65536 &&
                      ["completed", "cancelled", "failed", "uncertain"].contains(attempt.state) &&
                      (attempt.usage.inputTokens ?? 0) >= 0 && (attempt.usage.outputTokens ?? 0) >= 0 &&
                      Self.validPermissions(attempt.permissions) && (attempt.permissionFingerprint == nil || attempt.permissionFingerprint == "" || attempt.permissionFingerprint!.isImportDigest)
                  }) else { return false }
        }
        return (try? body.count).map { $0 <= 16 << 20 } ?? false
    }
    private static func validPermissions(_ value: PermissionSettings?) -> Bool {
        guard let value else { return true }
        return [value.profile, value.sandbox, value.approval, value.reviewer, value.claudeMode].allSatisfy { $0.utf8.count <= 160 }
    }
}

extension String {
    fileprivate var isImportDigest: Bool { range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil }
}

enum LocalPlanImport {
    static func fingerprint(_ plan: LocalPlan) throws -> String { PublishMessage.hash(try CampWire.encoder().encode(plan)) }
    static func plannerHistoryID(_ session: CampSession) -> String {
        HistorySourceSnapshot.localID(key: "planner\n" + session.provider + "\n" + session.sourceID + "\n" + session.sessionID, messages: [])
    }
    static func implementationHistoryID(_ execution: LocalImplementation) -> String {
        HistorySourceSnapshot.localID(key: "implementation\n" + execution.id, messages: execution.messages)
    }
    static func historySession(_ source: CampSession, id: String, title: String, model: String) -> CampSession {
        CampSession(id: id, sessionID: id, provider: source.provider, sourceID: source.sourceID, sourceLabel: source.sourceLabel,
            root: source.root, title: title, cwd: source.cwd, updated: source.updated, path: "", accountID: source.accountID, archived: true, model: model)
    }

    // Bindings are supplied only after the existing session publisher has saved
    // them. No profile roots, account credentials or execution paths enter input.
    static func prepare(_ plan: LocalPlan, scope: CloudScope, projectID: String,
                        planners: [String: LocalShare], implementations: [String: ImportedTranscriptBinding],
                        visible: [String: CloudSession]) throws -> PreparedLocalPlanImport {
        guard plan.id.hasPrefix("plan:"), UUID(uuidString: String(plan.id.dropFirst(5))) != nil,
              !plan.versions.isEmpty, plan.versions.count <= 100, (plan.implementations?.count ?? 0) <= 100 else {
            throw CampError.message("The saved planning history is invalid")
        }
        var permissions: [String: Int64] = [:]
        func authorize(_ session: CloudSession, provider: String? = nil, owned: Bool) throws {
            guard session.campId == scope.campId, !owned || session.ownerUserId == scope.userId,
                  provider == nil || session.provider == provider, UUID(uuidString: session.id) != nil,
                  session.permissionRevision > 0, let current = visible[session.id],
                  current.campId == session.campId, current.profileId == session.profileId,
                  current.ownerUserId == session.ownerUserId, current.provider == session.provider,
                  current.permissionRevision == session.permissionRevision else {
                throw CampError.message("A source is no longer available in this Camp")
            }
            permissions[session.id] = session.permissionRevision
        }
        var versions: [LocalPlanImportInput.Version] = []
        for version in plan.versions {
            guard version.workflow.accepts(version.planners.map { $0.session.provider }) else { throw CampError.message("The planning providers are invalid") }
            var mapped: [CloudPlannerInput] = []
            for planner in version.planners {
                guard let binding = planners[planner.session.id], let remote = binding.remote,
                      binding.id == remote.id, binding.local.sourceID == planner.session.sourceID,
                      binding.local.provider == planner.session.provider else { throw CampError.message("Save each planner's source session to this Camp before importing") }
                if remote.isHistoryOnly {
                    let expected = plannerHistoryID(planner.session), empty = HistorySourceSnapshot(messages: [])
                    guard binding.local.sessionID == expected, remote.localSessionId == expected,
                          remote.id == HistorySourceSnapshot.cloudID(localID: expected, scope: scope), binding.historySnapshot == empty,
                          binding.publishedHistory?.mainRevision == empty.revision,
                          binding.publishedHistory?.transcriptRevision == remote.transcriptRevision else { throw CampCloudError.invalidResponse }
                } else {
                    guard let profile = binding.profile, binding.local.sessionID == planner.session.sessionID,
                          profile.id == remote.profileId, profile.provider == planner.session.provider,
                          profile.ownerUserId == scope.userId, profile.localId == PublishMessage.hash(Data(planner.session.sourceID.utf8)),
                          remote.localSessionId == planner.session.sessionID else { throw CampCloudError.invalidResponse }
                }
                try authorize(remote, provider: planner.session.provider, owned: true)
                mapped.append(CloudPlannerInput(sessionId: remote.id, model: planner.model, effort: planner.tuning.effort, speed: planner.tuning.speed))
            }
            var refs: [CloudSourceRef] = [], quotes: [ImportedPlanningSource] = []
            for reference in version.sources {
                if let cloud = reference.cloud {
                    guard reference.scope == scope.key, reference.id == "camp:" + cloud.sessionId,
                          let current = visible[cloud.sessionId], current.permissionRevision == cloud.permissionRevision,
                          current.transcriptRevision >= cloud.transcriptRevision else { throw CampError.message("A tagged Camp source changed access or belongs to another Camp") }
                    try authorize(current, owned: false); refs.append(cloud)
                } else {
                    guard let messages = reference.messages, let digest = reference.digest,
                          digest == PublishMessage.hash(try CampWire.encoder().encode(messages.map(\.contextMessage))) else {
                        throw CampError.message("A tagged local source has no complete saved snapshot")
                    }
                    quotes.append(ImportedPlanningSource(id: PublishMessage.hash(Data(reference.id.utf8)), title: reference.title ?? "", revision: digest, messages: messages))
                }
            }
            versions.append(.init(version: version.number, mode: version.workflow, state: version.state, brief: version.brief,
                budgetPolicy: version.budgetPolicy, mergeSlot: version.mergeSlot, planners: mapped, sources: refs, localSources: quotes,
                steps: version.steps.map { .init(ordinal: $0.ordinal, state: $0.state, output: $0.output, error: $0.error.isEmpty ? nil : $0.error, usage: $0.usage) }))
        }
        var history: [LocalPlanImportInput.Implementation] = []
        for item in plan.implementations ?? [] {
            guard item.valid, !item.active, let binding = implementations[item.id], binding.revision >= 0,
                  binding.revision <= binding.session.transcriptRevision,
                  binding.session.localSessionId == (binding.session.isHistoryOnly ? implementationHistoryID(item) : item.providerSessionID),
                  !binding.session.isHistoryOnly || binding.session.id == HistorySourceSnapshot.cloudID(localID: implementationHistoryID(item), scope: scope),
                  item.messages.count == binding.messages.count,
                  Set(binding.messages.map(\.id)).count == binding.messages.count,
                  binding.messages.enumerated().allSatisfy({ index, cloud in
                      let local = item.messages[index]
                      return cloud.ordinal == Int64(index) && !cloud.removed && cloud.revision > 0 && cloud.revision <= binding.revision && cloud.id == local.id &&
                          cloud.role == local.role && cloud.text == local.text && (cloud.model ?? "") == (local.model ?? "") && (cloud.effort ?? "") == (local.effort ?? "") && (cloud.speed ?? "") == (local.speed ?? "")
                  }) else { throw CampError.message("Save the complete, stopped implementation transcript to this Camp before importing") }
            try authorize(binding.session, provider: item.executor.session.provider, owned: true)
            history.append(.init(sourceId: PublishMessage.hash(Data(item.id.utf8)), planVersion: item.planVersion,
                sessionId: binding.session.id, transcriptRevision: binding.revision, messageCount: item.messages.count,
                provider: item.executor.session.provider, state: item.state, model: item.executor.model,
                effort: item.executor.tuning.effort, speed: item.executor.tuning.speed,
                planDigest: item.planDigest, contextFingerprint: item.context.fingerprint, initialPrompt: item.initialPrompt,
                permissions: item.executor.permissions, permissionSummary: item.context.permissionSummary,
                attempts: item.attempts.map { .init(id: $0.id, prompt: $0.prompt, model: $0.model, effort: $0.tuning.effort, speed: $0.tuning.speed,
                    state: $0.state, error: $0.error, usage: $0.usage, permissions: $0.permissions, permissionFingerprint: $0.permissionFingerprint) }))
        }
        let prepared = PreparedLocalPlanImport(localFingerprint: try fingerprint(plan),
            input: .init(sourceId: PublishMessage.hash(Data(plan.id.utf8)), title: plan.title, projectId: projectID, versions: versions, implementations: history), permissions: permissions)
        guard prepared.valid else { throw CampError.message("This history exceeds the import limits or contains unfinished work. Nothing has been imported") }
        return prepared
    }
}
