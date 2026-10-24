import Foundation

@main struct ExecutionApprovalTests {
    @MainActor static func until(_ condition: () -> Bool) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Execution approval test timed out")
    }
    @MainActor static func main() async throws {
        let source = CampSession(id: "local", sessionID: UUID().uuidString, provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: "/tmp", title: "Approval fixture", cwd: "/tmp", updated: 1, path: "", accountID: "", archived: false)
        let blocked = LocalConversation(session: source, history: [], prompt: "implement")
        var built = false
        do { try blocked.start(prompt: "implement", model: "", tuning: ModelTuning(), mode: .implementation, makeProcess: { built = true; return fixtureProcess("complete") }); preconditionFailure("Unapproved implementation started") } catch {}
        precondition(!built)

        let id = UUID().uuidString, providerID = UUID().uuidString
        let run = LocalConversation(session: source, history: [], prompt: "implement")
        var result: ConversationResult?, created: String?
        run.completed = { result = $0 }; run.sessionCreated = { created = $0 }
        try run.start(prompt: "implement", model: "fixture", tuning: ModelTuning(), mode: .implementation, executionApproved: true, newSession: true, executionContext: String(repeating: "a", count: 64), makeProcess: {
            fixtureProcess("question-approval", [providerID, id])
        })
        try await until { run.approvals.count == 1 }
        precondition(created == providerID && run.providerSessionID == providerID)
        run.answer("unknown", allow: true)
        precondition(run.answering.isEmpty)
        run.answer(id, allow: true)
        try await until { run.notice == "Answer the question" && run.answering.isEmpty }
        precondition(run.approvals.count == 1)
        run.answer(id, allow: true, answers: ["answer": ["line 1\nline 2"]])
        try await until { result != nil }
        precondition(result?.state == "completed" && run.approvals.isEmpty && run.answering.isEmpty)

        result = nil
        let cancelled = LocalConversation(session: source, history: [], prompt: "cancel")
        cancelled.completed = { result = $0 }
        try cancelled.start(prompt: "cancel", model: "", tuning: ModelTuning(), mode: .implementation, executionApproved: true, executionContext: String(repeating: "a", count: 64), makeProcess: {
            fixtureProcess("command-approval", [id])
        })
        try await until { cancelled.approvals.count == 1 }
        cancelled.stop(); cancelled.answer(id, allow: true)
        precondition(cancelled.approvals.isEmpty && cancelled.answering.isEmpty)
        try await until { result != nil }
        precondition(result?.state == "cancelled")

        result = nil
        let chat = LocalConversation(session: source, history: [], prompt: "read only")
        chat.completed = { result = $0 }
        try chat.start(prompt: "read only", model: "", tuning: ModelTuning(), makeProcess: {
            fixtureProcess("unexpected-approval", [id])
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain" && chat.approvals.isEmpty)
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: implementation approval gate, session identity, question replies, stale requests, rejection recovery and cancellation")
    }
}
