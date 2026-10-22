import Foundation

@main struct LocalConversationTests {
    @MainActor static func until(_ condition: () -> Bool, line: Int = #line) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Timed out waiting for a local conversation at line \(line)")
    }
    static func session(_ id: String, _ provider: String, root: String) -> CampSession {
        CampSession(id: id, sessionID: id, provider: provider, sourceID: id, sourceLabel: id, root: root, title: id, cwd: root, updated: 1, path: "", accountID: "", archived: false)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let a = session("one", "Codex", root: root.path), b = session("two", "Claude", root: root.path)
        let c = session("three", "Codex", root: root.path)
        let store = CampStore(stateDirectory: root.appendingPathComponent("state"), conversationProcess: { fixtureProcess("gated-reply", [root.path]) })
        store.sessions = [a,b,c]; store.selectedID = a.id
        store.messages = [Message(id: "history", role: "assistant", text: "Original A history")]
        precondition(store.start(a, prompt: "first", model: "", tuning: ModelTuning()))
        try await until { store.conversations[a.id]?.accepted == true }
        precondition(store.isBusy(a) && !store.isBusy(b))
        // Same provider home is serialized, while separate provider homes run independently.
        precondition(!store.start(c, prompt: "conflict", model: "", tuning: ModelTuning()))
        store.draft = "queued A"; store.enqueueDraft()
        store.open(b)
        precondition(store.selectedID == b.id && store.runningID == nil)
        precondition(store.queuePaused, "A's queue preference must not affect B")
        store.queuedPrompts.append(QueuedPrompt(id: "durable-b", sessionID: b.id, text: "second", model: "", effort: "", speed: "standard"))
        store.saveQueue()
        precondition(store.start(b, prompt: "second", model: "", tuning: ModelTuning(), queuedID: "durable-b"))
        try await until { store.conversations[b.id]?.accepted == true }
        let savedRunning = try JSONDecoder().decode([QueuedPrompt].self, from: Data(contentsOf: root.appendingPathComponent("state/queued-prompts.json")))
        precondition(savedRunning.contains { $0.id == "durable-b" && $0.state == "running" }, "Accepted prompts must remain durable until confirmed completion")
        precondition(store.isBusy(a) && store.isBusy(b))
        store.open(a)
        precondition(store.messages.first?.text == "Original A history")
        precondition(store.messages.last?.text == "first" && !store.queuePaused)
        try Data().write(to: root.appendingPathComponent(b.id))
        try await until { store.conversations[b.id] == nil }
        precondition(store.selectedID == a.id && store.messages.last?.text == "first", "Background completion must not replace the visible transcript")
        store.open(b)
        try Data().write(to: root.appendingPathComponent(a.id))
        try await until { store.conversations[a.id] == nil && store.queued(a).isEmpty }
        precondition(store.selectedID == b.id, "A's background queue must not select its chat")
        precondition(!store.isBusy(a) && !store.isBusy(b))
        precondition(Bridge.activeProcesses.isEmpty)

        let reported = LocalConversation(session: a, history: [], prompt: "settings")
        try reported.start(prompt: "settings", model: "requested-model", tuning: ModelTuning(effort: "low", speed: "standard"), makeProcess: { fixtureProcess("reported-settings") })
        try await until { reported.ready }
        precondition(reported.reportedSettings == ReportedModelSettings(model: "actual-model", effort: "ultra", speed: "ultrafast"))
        reported.stop()
        try await until { Bridge.activeProcesses.isEmpty }

        var result: ConversationResult?
        let metadata = LocalConversation(session: a, history: [], prompt: "metadata")
        metadata.completed = { result = $0 }
        try metadata.start(prompt: "metadata", model: "requested-model", tuning: ModelTuning(effort: "low", speed: "standard"), makeProcess: { fixtureProcess("message-settings") })
        try await until { result != nil }
        let responses = metadata.messages.filter { $0.role == "assistant" }
        precondition(responses.count == 3)
        precondition(responses[0].model == "actual-first" && responses[0].effort == "ultra" && responses[0].speed == "ultrafast")
        precondition(responses[1].model == "actual-second" && responses[1].effort == "high" && responses[1].speed == "fast")
        precondition(responses[2].settingsDescription == nil, "Unknown response settings must not inherit the request or the preceding message")
        let restoredMessage = try JSONDecoder().decode(Message.self, from: JSONEncoder().encode(responses[0]))
        precondition(restoredMessage == responses[0], "Saving implementation history must retain response metadata")
        result = nil
        let cancel = LocalConversation(session: a, history: [], prompt: "cancel")
        cancel.completed = { result = $0 }
        try cancel.start(prompt: "cancel", model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("cancel")
        })
        try await until { cancel.ready }; cancel.stop()
        try await until { result != nil }
        precondition(result?.state == "cancelled" && result?.accepted == true && result?.message == "Stopped")
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let oversized = LocalConversation(session: a, history: [], prompt: "limit")
        oversized.completed = { result = $0 }
        try oversized.start(prompt: "limit", model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("oversized")
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain" && result?.message.contains("size limit") == true)
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let stderr = LocalConversation(session: a, history: [], prompt: "bounded errors")
        stderr.completed = { result = $0 }
        try stderr.start(prompt: "bounded errors", model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("stderr-done")
        })
        try await until { result != nil }
        precondition(result?.state == "completed" && result?.message == "")
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let lateStop = LocalConversation(session: a, history: [], prompt: "late stop")
        lateStop.completed = { result = $0 }
        try lateStop.start(prompt: "late stop", model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("late-stop")
        })
        try await until { lateStop.text == "finished" }; lateStop.stop()
        try await until { result != nil }
        precondition(result?.state == "completed", "An acknowledged completion wins a late Stop action")

        // A provider that stops reading stdin cannot block the UI's Stop action.
        result = nil
        let stalled = LocalConversation(session: a, history: [], prompt: String(repeating: "x", count: 65000))
        stalled.completed = { result = $0 }
        try stalled.start(prompt: String(repeating: "x", count: 65000), model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("stalled")
        })
        try await until { stalled.ready }
        let beforeStop = Date(); stalled.stop()
        precondition(Date().timeIntervalSince(beforeStop) < 0.1)
        try await until { result != nil }
        precondition(result?.state == "uncertain")
        precondition(Bridge.activeProcesses.isEmpty)
        result = nil
        let isolated = LocalConversation(session: a, history: [], prompt: "plan")
        isolated.completed = { result = $0 }
        try isolated.start(prompt: "plan", model: "fixture", tuning: ModelTuning(), mode: .planning, makeProcess: {
            fixtureProcess("isolated-plan")
        })
        try await until { isolated.ready }
        do { try isolated.steer("Change the frozen plan"); preconditionFailure("Planning accepted steering") } catch {}
        try await until { result != nil }
        precondition(result?.state == "completed" && result?.output == "first\n\nsecond")
        precondition(result?.usage.inputTokens == 123 && result?.usage.outputTokens == nil)

        result = nil
        let overflow = LocalConversation(session: a, history: [], prompt: "plan")
        overflow.completed = { result = $0 }
        try overflow.start(prompt: "plan", model: "fixture", tuning: ModelTuning(), mode: .planning, makeProcess: {
            fixtureProcess("overflow-plan")
        })
        try await until { result != nil }
        precondition(result?.state == "failed" && result!.output.utf8.count <= 8192)
        precondition(Bridge.activeProcesses.isEmpty)
        result = nil
        let closedInput = LocalConversation(session: a, history: [], prompt: String(repeating: "x", count: 65000))
        closedInput.completed = { result = $0 }
        try closedInput.start(prompt: String(repeating: "x", count: 65000), model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("close-input")
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain", "A closed input pipe must report an uncertain result without terminating Camp")
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: background navigation, queues, cancellation, isolated planning, usage and bounded process IO")
    }
}
