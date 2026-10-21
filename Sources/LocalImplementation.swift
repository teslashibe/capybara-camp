import Foundation

struct ExecutionContext: Codable, Equatable {
    var fingerprint: String
    var cwd: String
    var profile: String
    var provider: String
    var permissionSummary: String
}

struct ImplementationAttempt: Codable, Equatable, Identifiable {
    var id: String
    var prompt: String
    var model: String
    var tuning: ModelTuning
    var state = "starting"
    var error = ""
    var usage = ConversationUsage()
}

struct LocalImplementation: Codable, Equatable, Identifiable {
    var id: String
    var planVersion: Int
    var planDigest: String
    var executor: LocalPlanner
    var context: ExecutionContext
    var providerSessionID: String
    var providerSessionCreated = false
    var state = "approved"
    var initialPrompt: String
    var attempts: [ImplementationAttempt] = []
    var messages: [Message] = []
    var boardStage: String?

    var active: Bool { ["starting", "running"].contains(state) }
    var valid: Bool {
        UUID(uuidString: id) != nil && planVersion > 0 && planDigest.count == 64 && context.fingerprint.count == 64 &&
        ["Codex", "Claude"].contains(executor.session.provider) && context.provider == executor.session.provider &&
        !context.cwd.isEmpty && !context.profile.isEmpty && !providerSessionID.isEmpty && providerSessionID.utf8.count <= 200 &&
        !providerSessionID.contains("/") && !providerSessionID.contains("\\") && executor.model.utf8.count <= 200 &&
        ["approved", "starting", "running", "idle", "complete", "needs_review"].contains(state) &&
        (boardStage == nil || boardStage.flatMap(Stage.init(rawValue:)) != nil) &&
        initialPrompt.utf8.count <= 65536 && attempts.count <= 200 && messages.count <= 2000 &&
        Set(attempts.map(\.id)).count == attempts.count && Set(messages.map(\.id)).count == messages.count &&
        messages.reduce(0, { $0 + $1.text.utf8.count }) <= 8 << 20 &&
        attempts.allSatisfy { UUID(uuidString: $0.id) != nil && $0.prompt.utf8.count <= 65536 && $0.model.utf8.count <= 200 &&
            ["starting", "running", "completed", "cancelled", "failed", "uncertain"].contains($0.state) }
    }
    func session(id: String, title: String, updated: Double) -> CampSession {
        let source = executor.session
        return CampSession(id: id, sessionID: providerSessionID, provider: source.provider, sourceID: source.sourceID,
            sourceLabel: source.sourceLabel, root: context.profile, title: title, cwd: context.cwd, updated: updated,
            path: "", accountID: source.accountID, archived: false, model: executor.model)
    }
}

extension LocalPlanning {
    func configureExecutor(_ id: String, source: CampSession, model: String, tuning: ModelTuning) {
        guard let original = ticket(id), original.current.state == "ready", original.implementation?.active != true else { return }
        let project = original.current.planners[0].session.cwd
        let selected = CampSession(id: source.id, sessionID: source.sessionID, provider: source.provider, sourceID: source.sourceID,
            sourceLabel: source.sourceLabel, root: source.root, title: source.title, cwd: project, updated: source.updated,
            path: source.path, accountID: source.accountID, archived: source.archived, model: source.model)
        modifyTicket(id) { plan in
            var choices = plan.executorChoices ?? [:]
            if choices[source.id] == nil { choices[source.id] = LocalPlanner(session: selected, model: model, tuning: tuning) }
            else { choices[source.id]?.session = selected }
            plan.executorChoices = choices; plan.selectedExecutor = source.id
        }
    }
    func editExecutor(_ id: String, _ change: (inout LocalPlanner) -> Void) {
        guard let plan = ticket(id), plan.current.state == "ready", plan.implementation?.active != true, let selected = plan.selectedExecutor,
              var value = plan.executorChoices?[selected] else { return }
        change(&value)
        modifyTicket(id) { $0.executorChoices?[selected] = value }
    }
    @discardableResult
    func approveImplementation(_ id: String, expectedVersion: Int, executor: LocalPlanner, context: ExecutionContext, reviewedUnknown: Bool = false) -> Bool {
        guard let plan = ticket(id), plan.current.number == expectedVersion, plan.current.state == "ready",
              plan.current.completedCount == 5, plan.implementation?.active != true,
              plan.selectedExecutor == executor.session.id, plan.executorChoices?[executor.session.id] == executor,
              plan.implementation?.state != "needs_review" || reviewedUnknown,
              context.provider == executor.session.provider, context.fingerprint.count == 64,
              URL(fileURLWithPath: context.cwd).resolvingSymlinksInPath() == URL(fileURLWithPath: executor.session.cwd).resolvingSymlinksInPath(),
              URL(fileURLWithPath: context.profile).resolvingSymlinksInPath() == URL(fileURLWithPath: executor.session.root).resolvingSymlinksInPath() else { return false }
        let digest = (try? CampWire.encoder().encode(plan.current)).map(PublishMessage.hash) ?? ""
        let combined = plan.current.steps.first { $0.phase == "combined" }!.output
        let prompt = "Implement the approved plan, revision \(expectedVersion), in this project. Keep source references and report the changes and validation.\n\nBrief:\n\(plan.current.brief)\n\nApproved plan:\n\(combined)"
        let execution = LocalImplementation(id: UUID().uuidString, planVersion: expectedVersion, planDigest: digest,
            executor: executor, context: context, providerSessionID: UUID().uuidString, initialPrompt: prompt)
        return modifyTicket(id) { $0.implementations = ($0.implementations ?? []) + [execution] }
    }
    @discardableResult
    func beginImplementation(_ id: String, attempt: String, prompt: String, model: String, tuning: ModelTuning) -> Bool {
        guard let plan = ticket(id), let execution = plan.implementation,
              ["approved", "idle", "complete"].contains(execution.state),
              !execution.attempts.contains(where: { $0.id == attempt }),
              execution.planDigest == (try? CampWire.encoder().encode(plan.current)).map(PublishMessage.hash),
              !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return false }
        return modifyImplementation(id, executionID: execution.id) { execution in
            execution.state = "starting"; execution.boardStage = nil
            execution.attempts.append(ImplementationAttempt(id: attempt, prompt: prompt, model: model, tuning: tuning))
            execution.messages.append(Message(id: attempt, role: "user", text: prompt))
        }
    }
    @discardableResult
    func bindImplementationSession(_ id: String, attempt: String, providerID: String) -> Bool {
        guard let plan = ticket(id), let execution = plan.implementation, execution.attempts.last?.id == attempt,
              !execution.providerSessionCreated, !providerID.isEmpty, providerID.utf8.count <= 200 else { return false }
        return modifyImplementation(id, executionID: execution.id) { $0.providerSessionID = providerID; $0.providerSessionCreated = true }
    }
    @discardableResult
    func startedImplementation(_ id: String, attempt: String) -> Bool {
        guard let execution = ticket(id)?.implementation, execution.attempts.last?.id == attempt else { return false }
        return modifyImplementation(id, executionID: execution.id) { value in value.state = "running"; value.attempts[value.attempts.count - 1].state = "running" }
    }
    @discardableResult
    func finishImplementation(_ id: String, executionID: String, attempt: String, messages: [Message], result: ConversationResult) -> Bool {
        guard let execution = ticket(id)?.implementations?.first(where: { $0.id == executionID }),
              execution.attempts.last?.id == attempt, execution.active else { return false }
        return modifyImplementation(id, executionID: executionID) { value in
            guard let index = value.attempts.firstIndex(where: { $0.id == attempt }) else { return }
            value.attempts[index].state = result.state; value.attempts[index].error = result.message; value.attempts[index].usage = result.usage
            var seen = Set(value.messages.map(\.id))
            // Native history uses provider IDs; save only this Camp turn to avoid
            // duplicating earlier messages under their local and provider IDs.
            let turn = messages.firstIndex { $0.id == attempt }.map { messages[$0...] } ?? []
            for message in turn where seen.insert(message.id).inserted { value.messages.append(message) }
            value.state = result.state == "completed" ? "complete" : "needs_review"
        }
    }
    @discardableResult
    func reconcileImplementation(_ id: String, reviewed: Bool) -> Bool {
        guard reviewed, let execution = ticket(id)?.implementation, execution.state == "needs_review", execution.providerSessionCreated else { return false }
        return modifyImplementation(id, executionID: execution.id) { $0.state = "idle" }
    }
    func moveImplementation(_ id: String, stage: Stage) {
        guard let execution = ticket(id)?.implementation, !execution.active else { return }
        modifyImplementation(id, executionID: execution.id) { $0.boardStage = stage.rawValue }
    }
    @discardableResult private func modifyImplementation(_ id: String, executionID: String, _ change: (inout LocalImplementation) -> Void) -> Bool {
        guard ticket(id)?.implementations?.contains(where: { $0.id == executionID }) == true else { return false }
        return modifyTicket(id) { plan in
            guard let index = plan.implementations?.firstIndex(where: { $0.id == executionID }) else { return }
            change(&plan.implementations![index])
        }
    }
}

extension CampStore {
    func inspectExecution(_ source: CampSession) async throws -> ExecutionContext {
        if let executionContextReader { return try await executionContextReader(source) }
        let data = try await Bridge.call("execution-context", input: JSONSerialization.data(withJSONObject: ["session": object(source)]))
        return try JSONDecoder().decode(ExecutionContext.self, from: data)
    }
    func implementationReferences(_ session: CampSession, adding references: [SessionReference]) throws -> [SessionReference] {
        var result = planning.ticket(session.id)?.current.sources ?? []
        for reference in references {
            if let existing = result.first(where: { $0.id == reference.id }) {
                guard existing == reference else { throw CampError.message("Plan context is pinned. Start a new planning revision to change that source.") }
            } else { result.append(reference) }
        }
        guard result.count <= 20 else { throw CampError.message("Attach up to 20 sessions") }
        return result
    }
    func approveAndImplement(_ id: String, expectedVersion: Int, executor: LocalPlanner, reviewedUnknown: Bool) async {
        guard let plan = planning.ticket(id), plan.current.number == expectedVersion,
              sessions.contains(where: { !$0.isPlan && $0.id == executor.session.id && $0.root == executor.session.root }) else {
            banner = "The selected plan or executor changed. Review the settings again."; return
        }
        do {
            let context = try await inspectExecution(executor.session)
            let contexts = try await resolveContexts(plan.current.sources, destination: executor.session)
            try mentions.record(plan.current.sources)
            guard planning.approveImplementation(id, expectedVersion: expectedVersion, executor: executor, context: context, reviewedUnknown: reviewedUnknown),
                  let approved = planning.ticket(id), let execution = approved.implementation else {
                banner = "The plan changed or its implementation needs review"; return
            }
            reviewingPlan = nil; open(approved.session)
            selectModel(approved.session, executor.model)
            tune(approved.session, effort: executor.tuning.effort, speed: executor.tuning.speed)
            _ = start(approved.session, prompt: execution.initialPrompt, model: executor.model, tuning: executor.tuning, contexts: contexts, references: plan.current.sources)
        } catch { banner = error.localizedDescription }
    }
    func startApprovedImplementation(_ id: String) {
        guard let plan = planning.ticket(id), let execution = plan.implementation, execution.attempts.isEmpty else { return }
        Task {
            do {
                let contexts = try await resolveContexts(plan.current.sources, destination: plan.session)
                _ = start(plan.session, prompt: execution.initialPrompt, model: execution.executor.model, tuning: execution.executor.tuning, contexts: contexts, references: plan.current.sources)
            } catch { banner = error.localizedDescription }
        }
    }
    func executionSource(_ session: CampSession) -> CampSession? {
        if let plan = planning.ticket(session.id), let execution = plan.implementation, execution.providerSessionCreated {
            return sessions.first { !$0.isPlan && $0.provider == session.provider && ($0.root == execution.context.profile || $0.root == execution.executor.session.root) &&
                $0.sessionID == execution.providerSessionID } ?? plan.session
        }
        return shareSource(session)
    }
}
