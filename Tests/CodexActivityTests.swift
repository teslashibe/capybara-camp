import Foundation

@main struct CodexActivityTests {
    @MainActor static func eventually(_ message: String, _ test: () -> Bool) async throws {
        for _ in 0..<200 {
            if test() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        preconditionFailure(message)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-activity-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let store = CampStore(stateDirectory: root)
        let source = CampSource(id: "codex", provider: "Codex", root: root.path, label: "Fixture", count: 1)
        let claude = CampSource(id: "claude", provider: "Claude", root: root.path, label: "Fixture", count: 1)
        let session = CampSession(id: "codex:one", sessionID: "one", provider: "Codex", sourceID: "codex", sourceLabel: "Fixture", root: root.path, title: "Live fixture", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
        store.sources = [source, claude]; store.sessions = [session]
        precondition(store.stage(session) == .ready)
        func live(_ state: String) -> LiveState { LiveState(id: session.id, sourceID: source.id, state: state, model: "fixture", children: [], owner: "Codex", pid: 123) }
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("running")], sessions: [], errors: [:]))
        precondition(store.stage(session) == .progress && store.boardSessions(.progress).map(\.id) == [session.id])
        store.move(session.id, .done)
        precondition(store.stage(session) == .progress, "A drag cannot override a live turn")
        store.applyActivity(ActivityResult(activity: [], sessions: [], models: [:], errors: [claude.id: "offline"]))
        precondition(store.stage(session) == .progress, "Claude polling must not erase Codex activity")
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("waiting")], sessions: [], errors: [:]))
        precondition(store.stage(session) == .attention)
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("running")], sessions: [], errors: [:]))
        store.applyCodexActivity(CodexActivityUpdate(activity: [], sessions: [], errors: [source.id: "disconnected"]))
        precondition(store.activity[session.id]?.state == "unknown" && store.stage(session) == .attention)
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("running")], sessions: [], errors: [:]))
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("idle")], sessions: [], errors: [:]))
        precondition(store.stage(session) == .attention)
        store.sources = [claude]
        store.applyCodexActivity(CodexActivityUpdate(activity: [live("running")], sessions: [], errors: [:]))
        precondition(store.activity[session.id] == nil, "Removed profiles cannot keep a live card")

        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let script = root.appendingPathComponent("observer.py")
        try """
        import json, sys, time
        sources = json.loads(sys.stdin.readline())
        source = sources[0]
        state = {'id': source['id'] + ':one', 'sourceID': source['id'], 'state':'running', 'model':'fixture', 'children':[], 'owner':'Codex', 'pid':123}
        print(json.dumps({'activity':[state], 'sessions':[], 'errors':{}}), flush=True)
        time.sleep(30)
        """.write(to: script, atomically: true, encoding: .utf8)
        var processes: [Process] = []
        let watcher = CodexActivityWatch(makeProcess: {
            let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3"); p.arguments = [script.path]
            processes.append(p); return p
        })
        var updates: [CodexActivityUpdate] = []
        watcher.changed = { updates.append($0) }
        watcher.observe([source, claude])
        try await eventually("Observer did not stream its snapshot") { updates.last?.activity.first?.id == "codex:one" }
        watcher.observe([CampSource(id: source.id, provider: source.provider, root: source.root, label: source.label, count: 200)])
        precondition(processes.count == 1, "Import counts must not restart the helper")
        watcher.observe([CampSource(id: "second", provider: "Codex", root: root.path, label: "Other", count: 1)])
        try await eventually("Profile change was not observed") { updates.last?.activity.first?.id == "second:one" }
        precondition(processes.count == 2)
        watcher.observe([])
        try await eventually("Stopped observer is still running") { processes.allSatisfy { !$0.isRunning } }
        let stoppedCount = updates.count
        try await Task.sleep(for: .milliseconds(50))
        precondition(updates.count == stoppedCount, "A stopped generation emitted a stale callback")

        try """
        import json, sys
        sys.stdin.readline()
        print(json.dumps({'activity':[], 'sessions':[], 'errors':{}}), flush=True)
        """.write(to: script, atomically: true, encoding: .utf8)
        watcher.observe([source])
        try await eventually("Unexpected EOF did not clear live activity") { updates.last?.errors[source.id] != nil }
        watcher.stop()
        store.stopRuntimeObservation()
        print("PASS: Codex live board transitions, independent providers, source fencing, streamed observer lifecycle and shutdown")
    }
}
