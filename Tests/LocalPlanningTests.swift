import Foundation

@main struct LocalPlanningTests {
    @MainActor static func until(_ condition: () -> Bool, line: Int = #line) async throws {
        for _ in 0..<1000 { if condition() { return }; try await Task.sleep(for: .milliseconds(10)) }
        preconditionFailure("Planning timed out at line \(line)")
    }
    static func source(_ provider: String, root: URL) -> CampSession {
        CampSession(id: provider, sessionID: provider, provider: provider, sourceID: provider, sourceLabel: provider,
            root: root.path, title: provider, cwd: root.path, updated: 1, path: "", accountID: "", archived: false, model: provider + "-model")
    }
    static func process(_ code: String, args: [String] = []) -> Process {
        let p = Process(); p.executableURL = URL(fileURLWithPath: "/usr/bin/python3"); p.arguments = ["-u", "-c", code] + args; return p
    }
    static let responder = """
    import json,sys,time,os,select
    r=json.loads(sys.stdin.readline())
    assert r['mode']=='planning'
    assert r['context'][0]['messages'][0]['text']=='Pinned context'
    phase='review' if 'Review the other proposal once' in r['prompt'] else 'combined' if 'Produce one combined plan' in r['prompt'] else 'draft'
    line=json.dumps({'provider':r['session']['provider'],'phase':phase,'model':r['model'],'effort':r['effort'],'prompt':r['prompt']})+'\\n'
    fd=os.open(sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600);os.write(fd,line.encode());os.close(fd)
    print('{"type":"started"}',flush=True)
    end=time.monotonic()+.25
    while time.monotonic()<end:
        if select.select([sys.stdin],[],[],.01)[0]:
            item=json.loads(sys.stdin.readline())
            if item.get('type')=='stop':print('{"type":"cancelled"}',flush=True);sys.exit(0)
    text=r['session']['provider']+' '+phase+' '+r['model']
    print(json.dumps({'type':'text','text':text}),flush=True)
    print('{"type":"usage","inputTokens":123,"outputTokens":45}',flush=True)
    print('{"type":"done"}',flush=True)
    """
    @MainActor static func configured(_ directory: URL, code: String = responder, trace: URL) -> LocalPlanning {
        let engine = LocalPlanning(directory: directory, makeProcess: { process(code, args: [trace.path]) })
        engine.prepare = { _, _ in [SessionContext(id: "context", title: "Context", revision: "1", messages: [Message(id: "m", role: "assistant", text: "Pinned context")])] }
        return engine
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-plan-tests-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let planners = [LocalPlanner(session: source("Codex", root: root), model: "first", tuning: ModelTuning(effort: "high", speed: "fast")),
                        LocalPlanner(session: source("Claude", root: root), model: "second", tuning: ModelTuning(effort: "low", speed: "standard"))]
        let trace = root.appendingPathComponent("trace")
        let engine = configured(root.appendingPathComponent("state"), trace: trace)
        let id = engine.create(planners: planners)!
        engine.edit(id) { $0.brief = "A bounded plan" }
        precondition(!engine.edit(id) { $0.brief = String(repeating: "x", count: 8193) })
        precondition(engine.ticket(id)?.current.brief == "A bounded plan")
        engine.start(id)
        precondition(engine.ticket(id)?.current.state == "draft")
        precondition(!FileManager.default.fileExists(atPath: trace.path), "Strict token limits must fail before launching")
        engine.edit(id) { $0.budgetPolicy = "calls" }
        engine.start(id)
        try await until { engine.ticket(id)?.current.steps.prefix(2).allSatisfy { $0.state == "running" } == true }
        engine.edit(id) { $0.planners[0].model = "must-not-change" }
        engine.pause(id)
        try await until { engine.activeCount == 0 }
        precondition(engine.ticket(id)?.current.completedCount == 2)
        precondition(engine.ticket(id)?.current.state == "paused")
        engine.start(id)
        try await until { engine.ticket(id)?.current.state == "ready" }
        let result = engine.ticket(id)!.current
        precondition(result.steps.count == 5 && result.completedCount == 5)
        precondition(result.planners[0].model == "first")
        precondition(result.steps.allSatisfy { $0.usage.inputTokens == 123 && $0.usage.outputTokens == 45 })
        let records = try String(contentsOf: trace, encoding: .utf8).split(separator: "\n").map { try JSONSerialization.jsonObject(with: Data($0.utf8)) as! [String: String] }
        precondition(records.count == 5)
        precondition(records.filter { $0["phase"] == "draft" }.allSatisfy { !$0["prompt"]!.contains("Codex draft first") })
        precondition(records.filter { $0["phase"] == "review" }.allSatisfy { $0["prompt"]!.contains("Codex draft first") && $0["prompt"]!.contains("Claude draft second") })
        precondition(records.first { $0["phase"] == "combined" }!["prompt"]!.contains("Preserve unresolved disagreements"))
        precondition(Bridge.activeProcesses.isEmpty)
        let restored = configured(root.appendingPathComponent("state"), trace: trace)
        precondition(restored.ticket(id) == engine.ticket(id))
        let permissions = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("state/local-plans.json").path)
        precondition(permissions[.posixPermissions] as? Int == 0o600)

        // A restart fences old callbacks without erasing the original version.
        let restartID = engine.create(planners: planners)!
        engine.edit(restartID) { $0.brief = "Restart test"; $0.budgetPolicy = "calls" }
        engine.start(restartID)
        try await until { engine.ticket(restartID)?.current.steps.prefix(2).allSatisfy { $0.state == "running" } == true }
        engine.restart(restartID)
        try await until { engine.activeCount == 0 }
        precondition(engine.ticket(restartID)?.versions.count == 2)
        precondition(engine.ticket(restartID)?.current.state == "draft")
        precondition(engine.ticket(restartID)?.current.steps.isEmpty == true)
        precondition(engine.ticket(restartID)?.versions[0].state == "cancelled")
        engine.start(restartID)
        try await until { engine.ticket(restartID)?.current.state == "ready" }
        precondition(engine.ticket(restartID)?.versions[0].state == "cancelled")
        precondition(engine.ticket(restartID)?.versions[1].completedCount == 5)

        // A started process that closes without a terminal event is not retried.
        let unknown = configured(root.appendingPathComponent("unknown"), code: "import sys;sys.stdin.readline();print('{\"type\":\"started\"}',flush=True)", trace: trace)
        let uncertainID = unknown.create(planners: planners)!
        unknown.edit(uncertainID) { $0.brief = "Unknown outcome"; $0.budgetPolicy = "calls" }
        unknown.start(uncertainID)
        try await until { unknown.activeCount == 0 && unknown.ticket(uncertainID)?.current.state == "needs_review" }
        let old = unknown.ticket(uncertainID)
        unknown.start(uncertainID)
        precondition(unknown.ticket(uncertainID) == old && unknown.activeCount == 0)
        unknown.restart(uncertainID)
        precondition(unknown.ticket(uncertainID) == old)
        unknown.restart(uncertainID, reviewedUnknown: true)
        precondition(unknown.ticket(uncertainID)?.current.number == 2)

        // Crash recovery treats reserved and started work conservatively.
        var crashed = engine.ticket(id)!
        crashed.versions[0].state = "running"
        crashed.versions[0].steps[0].state = "starting"
        let crashDirectory = root.appendingPathComponent("crash")
        try FileManager.default.createDirectory(at: crashDirectory, withIntermediateDirectories: true)
        try JSONEncoder().encode([crashed]).write(to: crashDirectory.appendingPathComponent("local-plans.json"))
        let recovery = configured(crashDirectory, trace: trace)
        precondition(recovery.ticket(id)?.current.state == "needs_review")
        precondition(recovery.ticket(id)?.current.steps[0].state == "unknown")

        let store = CampStore(stateDirectory: root.appendingPathComponent("store"))
        store.sessions = planners.map(\.session)
        store.newPlan()
        let created = store.selectedID!
        store.applyImport(ImportResult(sessions: planners.map(\.session), sources: [], errors: []))
        precondition(store.sessions.contains { $0.id == created && $0.provider == "Planning" })
        store.setRecentType("Planning")
        precondition(store.recentSessions.map(\.id) == [created])
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: five isolated planning turns, frozen settings, pause/resume, version fencing, usage, private persistence and unknown-outcome recovery")
    }
}
