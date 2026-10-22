import Foundation

struct PlanningQuoteLink {
    let planID: String
    let version: Int
    let sourceID: String
    var id: String { "plan-source:\(planID):\(version):\(sourceID)" }
    init(planID: String, version: Int, sourceID: String) { self.planID = planID; self.version = version; self.sourceID = sourceID }
    init?(id: String) {
        let parts = id.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 4, parts[0] == "plan-source", UUID(uuidString: String(parts[1])) != nil,
              let version = Int(parts[2]), version > 0, parts[3].range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil else { return nil }
        self.init(planID: String(parts[1]), version: version, sourceID: String(parts[3]))
    }
}

struct ImportedPlanningSource: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var title: String
    var revision: String
    var messages: [Message]

    var valid: Bool {
        [id, revision].allSatisfy { $0.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil } &&
        title.utf8.count <= 500 && (1...120).contains(messages.count) && Set(messages.map(\.id)).count == messages.count &&
        messages.allSatisfy { !$0.id.isEmpty && $0.id.utf8.count <= 256 && ["user", "assistant", "system", "tool"].contains($0.role) &&
            $0.text.utf8.count <= 48 << 10 && ($0.model?.utf8.count ?? 0) <= 200 && ($0.effort?.utf8.count ?? 0) <= 64 && ($0.speed?.utf8.count ?? 0) <= 64 }
    }
}

struct ImportedPlanningStep: Codable, Equatable, Sendable {
    var ordinal: Int
    var state: String
    var output: String
    var error: String?
    var usage: ConversationUsage?

    var valid: Bool {
        ["pending", "completed", "cancelled", "failed", "unknown"].contains(state) && output.utf8.count <= 8192 &&
        (error?.utf8.count ?? 0) <= 8192 && (usage?.inputTokens ?? 0) >= 0 && (usage?.outputTokens ?? 0) >= 0
    }
    var displayedState: String { state == "unknown" ? "outcome_unknown" : state }
}

struct ImportedPlanningSnapshot: Codable, Equatable, Sendable {
    var state: String
    var localSources: [ImportedPlanningSource]
    var steps: [ImportedPlanningStep]

    func valid(for version: CloudPlanningResult.Version) -> Bool {
        ["draft", "paused", "ready", "cancelled", "failed", "needs_review"].contains(state) &&
        ["ready", "failed", "cancelled", "needs_review"].contains(version.state) &&
        (version.state != "ready" || state == "ready") &&
        localSources.count + version.sources.count <= 20 && localSources.allSatisfy(\.valid) &&
        Set(localSources.map(\.id)).count == localSources.count &&
        localSources.reduce(0, { $0 + $1.messages.reduce(0, { $0 + $1.text.utf8.count }) }) <= 256 << 10 &&
        (steps.isEmpty || steps.count == version.workflow.turnLimit) &&
        (state != "draft" || steps.isEmpty) &&
        (state != "ready" || steps.count == version.workflow.turnLimit && steps.allSatisfy { $0.state == "completed" && !$0.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) &&
        steps.enumerated().allSatisfy { $0.offset == $0.element.ordinal && $0.element.valid } &&
        version.planners.allSatisfy { $0.execution == nil } && steps.count == version.steps.count &&
        zip(steps, version.steps).allSatisfy { saved, displayed in
            saved.ordinal == displayed.ordinal && saved.output == displayed.output && saved.displayedState == displayed.state && saved.usage == displayed.usage
        }
    }
}

func validPlanningQuotes(_ sources: [ImportedPlanningSource], sharedCount: Int) -> Bool {
    sources.count + sharedCount <= 20 && sources.allSatisfy(\.valid) && Set(sources.map(\.id)).count == sources.count &&
        sources.reduce(0, { $0 + $1.messages.reduce(0, { $0 + $1.text.utf8.count }) }) <= 256 << 10
}

func inspectImportedPlanningSource(_ plan: CloudPlanningResult, sourceID: String, scope: CloudScope, permissions: [String: Int64]) throws -> ContextInspection {
    guard plan.valid, plan.campId == scope.campId,
          let source = plan.version.allQuotedSources.first(where: { $0.id == sourceID }),
          plan.referencedSessions.allSatisfy({ permissions["camp:" + $0] != nil }) else { throw CampCloudError.invalidResponse }
    return ContextInspection(sourceID: "plan-source:" + source.id, title: source.title.isEmpty ? "Imported source" : source.title,
        messages: source.messages, selectedMessage: source.messages.first?.id ?? "", scope: scope.key,
        planID: plan.id, sourcePermissions: permissions)
}

extension CampStore {
    func openQuotedPlanningLink(id: String, revision: String, message: String) {
        guard let link = PlanningQuoteLink(id: id), let scope = cloud.scope else { banner = "This quoted planning source is unavailable"; return }
        Task {
            do {
                let plan = try await sharedPlanning.readDetail(id: link.planID, version: link.version)
                guard cloud.scope == scope, let source = plan.version.allQuotedSources.first(where: { $0.id == link.sourceID }), source.revision == revision,
                      source.messages.contains(where: { $0.id == message }) else { throw CampCloudError.invalidResponse }
                let permissions = try plan.referencedSessions.reduce(into: [String: Int64]()) { values, id in
                    guard let value = cloudSessions["camp:" + id] else { throw CampCloudError.invalidResponse }
                    values["camp:" + id] = value.permissionRevision
                }
                var inspection = try inspectImportedPlanningSource(plan, sourceID: link.sourceID, scope: scope, permissions: permissions)
                inspection.selectedMessage = message
                contextInspection = inspection
            } catch is CancellationError {} catch { if cloud.scope == scope { banner = error.localizedDescription } }
        }
    }
    func inspectPlanningSource(_ plan: CloudPlanningResult, sourceID: String, imported: Bool) async throws -> ContextInspection {
        guard let scope = cloud.scope else { throw CampCloudError.cacheUnavailable }
        let permissions = try plan.referencedSessions.reduce(into: [String: Int64]()) { result, id in
            guard let source = cloudSessions["camp:" + id], source.campId == scope.campId else { throw CampCloudError.invalidResponse }
            result["camp:" + id] = source.permissionRevision
        }
        let verified = try await sharedPlanning.verifiedDetail(plan)
        let inspection: ContextInspection
        if imported {
            inspection = try inspectImportedPlanningSource(verified, sourceID: sourceID, scope: scope, permissions: permissions)
        } else {
            guard let ref = verified.version.sources.first(where: { $0.sessionId == sourceID }),
                  let session = cloudSessions["camp:" + sourceID] else { throw CampCloudError.invalidResponse }
            let page = try await cloud.context(session: session, source: ref)
            inspection = ContextInspection(sourceID: "camp:" + sourceID, title: session.title,
                messages: page.messages.map(\.transcriptMessage), selectedMessage: page.messages.first?.id ?? "", scope: scope.key,
                planID: plan.id, sourcePermissions: permissions)
        }
        try Task.checkCancellation()
        guard cloud.scope == scope, verified.referencedSessions == plan.referencedSessions,
              permissions.allSatisfy({ cloudSessions[$0.key]?.permissionRevision == $0.value }) else { throw CancellationError() }
        return inspection
    }
}
