import Foundation

@main struct PermissionSettingsTests {
    @MainActor static func until(_ condition: () -> Bool) async throws {
        for _ in 0..<500 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Permission settings test timed out")
    }
    @MainActor static func main() async throws {
        precondition(!PermissionSettings(approval: "untrusted").valid(for: "Codex"))
        precondition(!PermissionSettings(sandbox: "workspace-write", claudeMode: "auto").valid(for: "Codex"))
        precondition(!PermissionSettings(approval: "never", reviewer: "auto_review").valid(for: "Codex"))
        precondition(PermissionSettings(claudeMode: "dontAsk").valid(for: "Claude"))
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = CampSession(id: "fixture:one", sessionID: UUID().uuidString, provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Permission fixture", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
        await LocalSessionIndex.shared.save(ImportResult(sessions: [source], sources: [], errors: []), directory: root)
        let requestID = UUID().uuidString, trace = root.appendingPathComponent("request.json")
        let store = CampStore(stateDirectory: root, conversationProcess: {
            let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
            p.arguments = ["-u", "-c", """
            import json,sys
            r=json.loads(sys.stdin.readline())
            with open(sys.argv[1],'w') as f:json.dump(r,f)
            assert r['mode']=='implementation' and r['permissions']['sandbox']=='read-only'
            print(json.dumps({'type':'permissions','text':'read-only · on-request · user'}),flush=True)
            print('{"type":"started"}',flush=True)
            print(json.dumps({'type':'approval','id':sys.argv[2],'kind':'command','title':'Approve command','detail':'echo test','questions':[],'canApprove':True,'acceptLabel':'Allow','decisions':[{'id':'acceptForSession','label':'Allow for this session','allows':True},{'id':'cancel','label':'Decline and stop','allows':False}]}),flush=True)
            response=json.loads(sys.stdin.readline())
            assert response['decision']=='acceptForSession' and response['allow']
            print(json.dumps({'type':'approvalResolved','requestID':sys.argv[2]}),flush=True)
            print('{"type":"text","text":"Done"}',flush=True)
            print('{"type":"done"}',flush=True)
            """, trace.path, requestID]
            return p
        })
        store.executionContextReader = { session in ExecutionContext(fingerprint: String(repeating: "a", count: 64), cwd: session.cwd, profile: session.root, provider: session.provider, permissionSummary: "Fixture") }
        let initial = PermissionSettings(sandbox: "read-only", approval: "on-request", reviewer: "user")
        try await store.applyPermissions(initial, session: source, preview: false)
        precondition(store.sessionPermissions.grants[source.id] != nil)
        precondition(store.start(source, prompt: "Test", model: "", tuning: ModelTuning()))
        try await until { store.conversations[source.id]?.approvals.count == 1 }
        let run = store.conversations[source.id]!
        precondition(store.stage(source) == .attention && store.isBusy(source))
        precondition(run.effectivePermissions == "read-only · on-request · user")
        store.selectedID = source.id; store.draft = "Queued under original permissions"; store.enqueueDraft()
        let queuedRevision = store.queuedPrompts.first!.permissionRevision
        let next = PermissionSettings(sandbox: "workspace-write", approval: "on-request", reviewer: "user")
        try await store.applyPermissions(next, session: source, preview: false)
        precondition(store.permissionRevision(source) != queuedRevision)
        precondition(run.effectivePermissions == "read-only · on-request · user", "Changing saved settings must not pretend the active turn changed")
        run.answer(requestID, allow: true, decision: "unoffered")
        precondition(run.answering.isEmpty)
        run.answer(requestID, allow: true, decision: "acceptForSession")
        try await until { store.conversations.isEmpty }
        store.resumeQueue()
        precondition(store.conversations.isEmpty && store.queuedPrompts.count == 1 && store.banner.contains("Permissions changed"))
        let restored = CampStore(stateDirectory: root)
        precondition(restored.permissionSettings(source) == next)
        let attrs = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("session-permissions.json").path)
        precondition((attrs[.posixPermissions] as? NSNumber)?.intValue == 0o600)
        try await restored.applyPermissions(PermissionSettings(), session: source, preview: true)
        precondition(restored.sessionPermissions.grants[source.id] == nil)
        let request = try JSONSerialization.jsonObject(with: Data(contentsOf: trace)) as! [String: Any]
        precondition(request["executionApproved"] as? Bool == true && request["newSession"] as? Bool == false)
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: persistent permission choices, active-turn boundaries, Needs you, offered decisions and stale queues")
    }
}
