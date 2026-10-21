import Foundation

@main struct ExecutionApprovalTests {
    @MainActor static func until(_ condition: () -> Bool) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Execution approval test timed out")
    }
    static func process(_ code: String) -> Process {
        let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3"); p.arguments = ["-u", "-c", code]; return p
    }
    @MainActor static func main() async throws {
        let source = CampSession(id: "local", sessionID: UUID().uuidString, provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: "/tmp", title: "Approval fixture", cwd: "/tmp", updated: 1, path: "", accountID: "", archived: false)
        let blocked = LocalConversation(session: source, history: [], prompt: "implement")
        var built = false
        do { try blocked.start(prompt: "implement", model: "", tuning: ModelTuning(), mode: .implementation, makeProcess: { built = true; return process("") }); preconditionFailure("Unapproved implementation started") } catch {}
        precondition(!built)

        let id = UUID().uuidString, providerID = UUID().uuidString
        let run = LocalConversation(session: source, history: [], prompt: "implement")
        var result: ConversationResult?, created: String?
        run.completed = { result = $0 }; run.sessionCreated = { created = $0 }
        try run.start(prompt: "implement", model: "fixture", tuning: ModelTuning(), mode: .implementation, executionApproved: true, newSession: true, makeProcess: {
            process("""
            import json,sys
            request=json.loads(sys.stdin.readline());assert request['mode']=='implementation' and request['executionApproved'] and request['newSession']
            print(json.dumps({'type':'session','sessionID':'\(providerID)'}),flush=True)
            print('{"type":"started"}',flush=True)
            print(json.dumps({'type':'approval','id':'\(id)','kind':'question','title':'Choose','detail':'','canApprove':True,'acceptLabel':'Submit answers','questions':[{'id':'answer','header':'Format','prompt':'Which format?','options':[],'multiple':False,'secret':False,'allowText':True}]}),flush=True)
            answer=json.loads(sys.stdin.readline());assert answer['id']=='\(id)';assert answer['type']=='approval';assert not answer['answers']
            print(json.dumps({'type':'approvalRejected','requestID':'\(id)','message':'Answer the question'}),flush=True)
            answer=json.loads(sys.stdin.readline());assert answer['answers']=={'answer':['line 1\\nline 2']}
            print(json.dumps({'type':'approvalResolved','requestID':'\(id)'}),flush=True)
            print('{"type":"text","text":"Implemented"}',flush=True)
            print('{"type":"done"}',flush=True)
            """)
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
        try cancelled.start(prompt: "cancel", model: "", tuning: ModelTuning(), mode: .implementation, executionApproved: true, makeProcess: {
            process("""
            import json,sys
            sys.stdin.readline()
            print(json.dumps({'type':'approval','id':'\(id)','kind':'command','title':'Run command','detail':'echo test','questions':[],'canApprove':True,'acceptLabel':'Allow once'}),flush=True)
            request=json.loads(sys.stdin.readline());assert request['type']=='stop'
            print('{"type":"cancelled"}',flush=True)
            """)
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
            process("""
            import json,sys
            sys.stdin.readline()
            print(json.dumps({'type':'approval','id':'\(id)','kind':'command','title':'Run command','detail':'echo test','questions':[],'canApprove':True,'acceptLabel':'Allow once'}),flush=True)
            """)
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain" && chat.approvals.isEmpty)
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: implementation approval gate, session identity, question replies, stale requests, rejection recovery and cancellation")
    }
}
