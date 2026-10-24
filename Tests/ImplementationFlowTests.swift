import Foundation

@main struct ImplementationFlowTests {
    @MainActor static func until(_ condition: () -> Bool) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Implementation flow timed out")
    }
    static func source(_ provider: String, root: URL, cwd: URL) -> CampSession {
        CampSession(id: provider, sessionID: UUID().uuidString, provider: provider, sourceID: provider + "-profile", sourceLabel: provider,
            root: root.path, title: provider, cwd: cwd.path, updated: 1, path: "", accountID: "", archived: false, model: provider + "-planning")
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-implementation-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let project = root.appendingPathComponent("project"), other = root.appendingPathComponent("other-project")
        try FileManager.default.createDirectory(at: project, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: other, withIntermediateDirectories: true)
        let a = source("Codex", root: root, cwd: project), b = source("Claude", root: root, cwd: other)
        let steps = (0..<5).map { LocalPlanStep(ordinal: $0, slot: $0 == 4 ? 0 : $0 % 2, phase: $0 < 2 ? "draft" : $0 < 4 ? "review" : "combined", state: "completed", output: "Approved step \($0)") }
        let version = LocalPlanVersion(number: 1, brief: "A small implementation", planners: [LocalPlanner(session: a, model: a.model, tuning: ModelTuning()), LocalPlanner(session: b, model: b.model, tuning: ModelTuning())], budgetPolicy: "calls", state: "ready", steps: steps)
        let id = "plan:" + UUID().uuidString
        let plan = LocalPlan(id: id, title: "Implementation ticket", updated: 2, versions: [version])
        let state = root.appendingPathComponent("state"); try FileManager.default.createDirectory(at: state, withIntermediateDirectories: true)
        try JSONEncoder().encode([plan]).write(to: state.appendingPathComponent("local-plans.json"))
        await LocalSessionIndex.shared.save(ImportResult(sessions: [a,b], sources: [], errors: []), directory: state)
        let trace = root.appendingPathComponent("requests")
        let store = CampStore(stateDirectory: state, conversationProcess: {
            fixtureProcess("implementation", [trace.path])
        })
        store.implementationTranscriptReader = { _, _ in Transcript(messages: [], total: 0) }
        store.modelCatalogs[b.sourceID] = [ModelOption(id: "third-model", label: "Third model", efforts: ["high"], speeds: ["standard"])]
        store.executionContextReader = { source in
            precondition(source.cwd == project.path, "Choosing an account must retain the plan's project")
            return ExecutionContext(fingerprint: String(repeating: "a", count: 64), cwd: source.cwd, profile: source.root, provider: source.provider, permissionSummary: "Fixture")
        }
        store.planning.configureExecutor(id, source: b, model: "third-model", tuning: ModelTuning(effort: "high"))
        let executor = store.planning.ticket(id)!.executorChoices![b.id]!
        store.planning.editExecutor(id) { $0.tuning.effort = "low" }
        let context = try await store.inspectExecution(executor.session)
        precondition(!store.planning.approveImplementation(id, expectedVersion: 1, executor: executor, context: context), "Changed executor settings must invalidate approval")
        store.planning.editExecutor(id) { $0.tuning.effort = "high" }
        await store.approveAndImplement(id, expectedVersion: 1, executor: executor, reviewedUnknown: false)
        try await until { store.conversations[id]?.accepted == true }
        precondition(store.selectedID == id && store.selected?.provider == "Claude")
        precondition(store.planning.ticket(id)?.current == version)
        store.planning.restart(id)
        precondition(store.planning.ticket(id)?.current.number == 1, "Running implementation must fence plan restart")
        store.draft = "Queued follow-up"; store.enqueueDraft()
        try await until { store.conversations[id] == nil && store.planning.ticket(id)?.implementation?.attempts.count == 2 && store.queuedPrompts.isEmpty }
        let execution = store.planning.ticket(id)!.implementation!
        precondition(execution.state == "complete" && execution.executor.model == "third-model")
        precondition(execution.attempts.allSatisfy { $0.model == "third-model" && $0.tuning.effort == "high" && $0.state == "completed" })
        precondition(execution.messages.count == 4 && Set(execution.messages.map(\.id)).count == 4)
        precondition(!store.planning.beginImplementation(id, attempt: execution.attempts[0].id, prompt: "Duplicate", model: "third-model", tuning: ModelTuning()))
        precondition(!store.planning.finishImplementation(id, executionID: execution.id, attempt: execution.attempts[0].id, messages: [], result: ConversationResult(state: "completed", message: "", accepted: true)))
        let requests = try String(contentsOf: trace, encoding: .utf8).split(separator: "\n").map { try JSONSerialization.jsonObject(with: Data($0.utf8)) as! [String: Any] }
        precondition(requests.count == 2 && requests[0]["newSession"] as? Bool == true && requests[1]["newSession"] as? Bool == false)
        precondition((requests[0]["session"] as! [String: Any])["sessionID"] as? String == (requests[1]["session"] as! [String: Any])["sessionID"] as? String)

        let mirror = CampSession(id: b.sourceID + ":" + execution.providerSessionID, sessionID: execution.providerSessionID, provider: b.provider, sourceID: b.sourceID, sourceLabel: b.sourceLabel, root: root.path, title: "Native mirror", cwd: project.path, updated: 3, path: "", accountID: "", archived: false)
        store.applyImport(ImportResult(sessions: [a,b,mirror], sources: [], errors: []))
        precondition(store.visibleSessions.contains { $0.id == id } && !store.visibleSessions.contains { $0.id == mirror.id })
        precondition(store.planningProfiles("Claude").count == 1)
        let external = execution.messages + [Message(id: "terminal-user", role: "user", text: "Continued in terminal"), Message(id: "terminal-assistant", role: "assistant", text: "Streaming from terminal")]
        store.implementationTranscriptReader = { source, _ in
            precondition(source.sessionID == mirror.sessionID && source.root == mirror.root, "Read the physical provider session while retaining the logical card")
            return Transcript(messages: external, total: external.count)
        }
        store.loadHistory(silent: true)
        try await until { store.messages == external }
        precondition(store.messages.filter { $0.id == "terminal-assistant" }.count == 1)
        store.implementationTranscriptReader = { _, _ in Transcript(messages: [], total: 0) }
        store.draft = "Continue after terminal work"; store.send()
        try await until { store.conversations[id] == nil && store.planning.ticket(id)?.implementation?.attempts.count == 3 }
        let latestExecution = store.planning.ticket(id)!.implementation!
        precondition(latestExecution.messages.count == 6, "Provider history must not duplicate prior Camp messages in the audit history")
        store.activity[mirror.id] = LiveState(id: mirror.id, sourceID: b.sourceID, state: "running", model: "third-model", children: [], owner: "Claude", pid: 123)
        precondition(store.isBusy(store.selected!))
        precondition(!store.start(store.selected!, prompt: "Do not take over", model: "third-model", tuning: ModelTuning()))
        store.activity = [:]
        store.move(id, .done)
        precondition(store.stage(store.selected!) == .done)

        let restored = CampStore(stateDirectory: state)
        precondition(restored.planning.ticket(id)?.implementation?.messages == latestExecution.messages)
        precondition(restored.planning.ticket(id)?.current == version)
        precondition(restored.planning.ticket(id)?.stage == .done)
        restored.implementationTranscriptReader = { _, _ in Transcript(messages: [], total: 0) }
        restored.planning.modifyTicket(id) { value in
            value.implementations![0].messages = (0..<130).map { Message(id: "history-\($0)", role: "assistant", text: "Saved output \($0)") }
        }
        restored.open(restored.planning.ticket(id)!.session)
        precondition(restored.messages.count == 120)
        restored.loadHistory(more: true)
        precondition(restored.messages.count == 130, "Saved history pagination works before a native transcript is available")
        var crashed = restored.planning.ticket(id)!
        crashed.implementations![0].state = "running"
        crashed.implementations![0].attempts[1].state = "running"
        let crash = root.appendingPathComponent("crash"); try FileManager.default.createDirectory(at: crash, withIntermediateDirectories: true)
        try JSONEncoder().encode([crashed]).write(to: crash.appendingPathComponent("local-plans.json"))
        let recovered = LocalPlanning(directory: crash)
        precondition(recovered.ticket(id)?.implementation?.state == "needs_review")
        precondition(!recovered.reconcileImplementation(id, reviewed: false))
        precondition(recovered.reconcileImplementation(id, reviewed: true))
        recovered.restart(id)
        precondition(recovered.ticket(id)?.current.number == 2 && recovered.ticket(id)?.implementation == nil)
        precondition(recovered.ticket(id)?.implementations?.count == 1)
        var invalid = crashed
        invalid.implementations![0].attempts.append(invalid.implementations![0].attempts[0])
        let bad = root.appendingPathComponent("invalid"); try FileManager.default.createDirectory(at: bad, withIntermediateDirectories: true)
        try JSONEncoder().encode([invalid]).write(to: bad.appendingPathComponent("local-plans.json"))
        precondition(LocalPlanning(directory: bad).tickets.isEmpty, "Duplicate durable attempt IDs must fail closed")
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: same-ticket implementation, executor approval fencing, terminal updates, queued continuation, mirror suppression and restart recovery")
    }
}
