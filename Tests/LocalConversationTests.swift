import Foundation

@main struct LocalConversationTests {
    @MainActor static func until(_ condition: () -> Bool, line: Int = #line) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Timed out waiting for a local conversation at line \(line)")
    }
    static func process(_ code: String, _ args: [String] = []) -> Process {
        let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
        p.arguments = ["-u", "-c", code] + args; return p
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
        let code = """
        import json,sys,time,os,select
        r=json.loads(sys.stdin.readline())
        name=r['session']['id']
        print(json.dumps({'type':'started'}),flush=True)
        while not os.path.exists(sys.argv[1]+'/'+name):
            ready,_,_=select.select([sys.stdin],[],[],.01)
            if ready and not sys.stdin.readline():sys.exit(0)
        print(json.dumps({'type':'text','text':'reply '+r['prompt']}),flush=True)
        print(json.dumps({'type':'done'}),flush=True)
        """
        let store = CampStore(stateDirectory: root.appendingPathComponent("state"), conversationProcess: { process(code, [root.path]) })
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

        var result: ConversationResult?
        let cancel = LocalConversation(session: a, history: [], prompt: "cancel")
        cancel.completed = { result = $0 }
        try cancel.start(prompt: "cancel", model: "", tuning: ModelTuning(), makeProcess: {
            process("""
            import json,sys
            json.loads(sys.stdin.readline())
            print('{"type":"started"}',flush=True)
            for line in sys.stdin:
                if json.loads(line).get('type')=='stop':
                    print('{"type":"cancelled","text":"Stopped"}',flush=True)
                    break
            """)
        })
        try await until { cancel.ready }; cancel.stop()
        try await until { result != nil }
        precondition(result?.state == "cancelled" && result?.accepted == true)
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let oversized = LocalConversation(session: a, history: [], prompt: "limit")
        oversized.completed = { result = $0 }
        try oversized.start(prompt: "limit", model: "", tuning: ModelTuning(), makeProcess: {
            process("import sys;sys.stdin.readline();print('x'*(5*1024*1024),flush=True)")
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain" && result?.message.contains("size limit") == true)
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let stderr = LocalConversation(session: a, history: [], prompt: "bounded errors")
        stderr.completed = { result = $0 }
        try stderr.start(prompt: "bounded errors", model: "", tuning: ModelTuning(), makeProcess: {
            process("import sys;sys.stdin.readline();sys.stderr.write('x'*(1024*1024));sys.stderr.flush();print('{\"type\":\"done\"}',flush=True)")
        })
        try await until { result != nil }
        precondition(result?.state == "completed" && result?.message == "")
        precondition(Bridge.activeProcesses.isEmpty)

        result = nil
        let lateStop = LocalConversation(session: a, history: [], prompt: "late stop")
        lateStop.completed = { result = $0 }
        try lateStop.start(prompt: "late stop", model: "", tuning: ModelTuning(), makeProcess: {
            process("import sys;sys.stdin.readline();print('{\"type\":\"started\"}',flush=True);print('{\"type\":\"text\",\"text\":\"finished\"}',flush=True);print('{\"type\":\"done\"}',flush=True);sys.stdin.readline()")
        })
        try await until { lateStop.text == "finished" }; lateStop.stop()
        try await until { result != nil }
        precondition(result?.state == "completed", "An acknowledged completion wins a late Stop action")

        // A provider that stops reading stdin cannot block the UI's Stop action.
        result = nil
        let stalled = LocalConversation(session: a, history: [], prompt: String(repeating: "x", count: 65000))
        stalled.completed = { result = $0 }
        try stalled.start(prompt: String(repeating: "x", count: 65000), model: "", tuning: ModelTuning(), makeProcess: {
            process("import time;print('{\"type\":\"started\"}',flush=True);time.sleep(30)")
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
            process("""
            import json,sys,time
            request=json.loads(sys.stdin.readline())
            assert request['mode']=='planning'
            print('{"type":"started"}',flush=True)
            time.sleep(.15)
            print('{"type":"text","text":"first"}',flush=True)
            print('{"type":"boundary"}',flush=True)
            print('{"type":"text","text":"second"}',flush=True)
            print('{"type":"usage","inputTokens":123,"outputTokens":true}',flush=True)
            print('{"type":"done"}',flush=True)
            """)
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
            process("""
            import json,sys
            sys.stdin.readline()
            for event in [{'type':'started'},{'type':'text','text':'a'*8000},{'type':'boundary'},{'type':'text','text':'b'*300},{'type':'done'}]:
                print(json.dumps(event),flush=True)
            """)
        })
        try await until { result != nil }
        precondition(result?.state == "failed" && result!.output.utf8.count <= 8192)
        precondition(Bridge.activeProcesses.isEmpty)
        result = nil
        let closedInput = LocalConversation(session: a, history: [], prompt: String(repeating: "x", count: 65000))
        closedInput.completed = { result = $0 }
        try closedInput.start(prompt: String(repeating: "x", count: 65000), model: "", tuning: ModelTuning(), makeProcess: {
            process("import sys;sys.stdin.close()")
        })
        try await until { result != nil }
        precondition(result?.state == "uncertain", "A closed input pipe must report an uncertain result without terminating Camp")
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: background navigation, queues, cancellation, isolated planning, usage and bounded process IO")
    }
}
