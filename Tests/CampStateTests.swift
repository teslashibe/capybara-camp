import Foundation

@main struct CampStateTests {
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let store = CampStore(stateDirectory: root)
        let a = CampSession(id: "codex-a", sessionID: "a", provider: "Codex", sourceID: "source-a", sourceLabel: "Personal", root: "/tmp", title: "Fix search", cwd: "/tmp/Apple", updated: 1, path: "", accountID: "", archived: false)
        let b = CampSession(id: "claude-b", sessionID: "b", provider: "Claude", sourceID: "source-b", sourceLabel: "Work", root: "/tmp", title: "Write tests", cwd: "/tmp/Banana", updated: 2, path: "", accountID: "", archived: true)
        store.sessions = [a,b]
        precondition(store.filtered.count == 2)
        store.sourceFilter = "Claude"
        precondition(store.filtered.map(\.id) == [b.id])
        store.sourceFilter = "source-a"
        precondition(store.filtered.map(\.id) == [a.id])
        store.sourceFilter = "all"; store.search = "APPLE"
        precondition(store.filtered.map(\.id) == [a.id])
        store.search = "work"
        precondition(store.filtered.map(\.id) == [b.id])
        precondition(store.stage(a) == .ready && store.stage(b) == .done)
        store.move(a.id, .done)
        let restored = CampStore(stateDirectory: root)
        precondition(restored.stages[a.id] == .done)
        let attributes = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("board.json").path)
        precondition(attributes[.posixPermissions] as? Int == 0o600)
        store.move("untrusted-drop", .attention)
        precondition(store.stages["untrusted-drop"] == nil)
        store.conversations[a.id] = LocalConversation(session: a, history: [], prompt: "fixture")
        precondition(store.stage(a) == .progress)
        store.move(a.id, .ready)
        precondition(store.stages[a.id] == .done)
        store.conversations.removeValue(forKey: a.id)
        let external = LiveState(id: b.id, sourceID: b.sourceID, state: "running", model: "opus", children: [], owner: "Claude Code", pid: 123)
        store.applyActivity(ActivityResult(activity: [external], sessions: [], models: [:], errors: [:]))
        precondition(store.stage(b) == .progress)
        store.move(b.id, .done)
        precondition(store.stage(b) == .progress)
        store.sourceFilter = "active"; store.search = ""
        precondition(store.filtered.map(\.id) == [b.id])
        store.selectModel(a, "gpt-6-luna")
        precondition(CampStore(stateDirectory: root).modelChoices[a.id] == "gpt-6-luna")
        store.applyActivity(ActivityResult(activity: [], sessions: [], models: [:], errors: [b.sourceID: "offline"]))
        precondition(store.activity[b.id]?.state == "unknown" && store.stage(b) == .attention)
        store.applyActivity(ActivityResult(activity: [external], sessions: [], models: [:], errors: [:]))
        let idle = LiveState(id: b.id, sourceID: b.sourceID, state: "idle", model: "opus", children: [], owner: "Claude Code", pid: 123)
        store.applyActivity(ActivityResult(activity: [idle], sessions: [], models: [:], errors: [:]))
        precondition(store.stage(b) == .attention)
        store.modelCatalogs[a.sourceID] = [
            ModelOption(id: "sol", label: "Sol", efforts: ["low", "ultra"], speeds: ["standard", "fast", "ultrafast"]),
            ModelOption(id: "luna", label: "Luna", efforts: ["low", "max"], speeds: ["standard", "fast"])]
        store.selectModel(a, "sol"); store.tune(a, effort: "ultra", speed: "ultrafast")
        precondition(store.tuning(a) == ModelTuning(effort: "ultra", speed: "ultrafast"))
        store.selectModel(a, "luna")
        precondition(store.tuning(a).speed == "standard")
        store.tune(a, effort: "max", speed: "fast")
        store.selectModel(a, "sol")
        precondition(store.tuning(a).speed == "ultrafast")
        let restarted = CampStore(stateDirectory: root)
        precondition(restarted.tuning(a) == store.tuning(a))
        store.selectModel(a, "luna"); store.tune(a, speed: "ultrafast")
        precondition(store.tuning(a).speed == "fast")
        let tuningPermissions = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("model-tuning.json").path)
        precondition(tuningPermissions[.posixPermissions] as? Int == 0o600)
        store.selectedID = a.id; store.conversations[a.id] = LocalConversation(session: a, history: [], prompt: "fixture")
        store.draft = "First queued prompt"; store.enqueueDraft()
        store.draft = "Second queued prompt"; store.enqueueDraft()
        precondition(store.queued(a).map(\.text) == ["First queued prompt", "Second queued prompt"])
        precondition(!store.queuePaused && store.draft.isEmpty)
        let queued = store.queued(a)[0]
        precondition(queued.model == "luna" && queued.speed == "fast")
        let savedQueue = CampStore(stateDirectory: root)
        precondition(savedQueue.queuePaused && savedQueue.queued(a).count == 2)
        store.editQueued(queued)
        precondition(store.draft == queued.text && store.queued(a).count == 1)
        store.removeQueued(store.queued(a)[0])
        precondition(store.queued(a).isEmpty)
        store.draft = "Uncertain delivery"; store.enqueueDraft()
        store.queuedPrompts[0].state = "sending"; store.saveQueue()
        let recovered = CampStore(stateDirectory: root)
        precondition(recovered.queued(a)[0].state == "needsReview" && recovered.queuePaused)
        store.queuedPrompts[0].state = "running"; store.saveQueue()
        precondition(store.queued(a).isEmpty)
        precondition(CampStore(stateDirectory: root).queued(a)[0].state == "needsReview")
        let queuePermissions = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("queued-prompts.json").path)
        precondition(queuePermissions[.posixPermissions] as? Int == 0o600)
        let sync = CampStore(stateDirectory: root.appendingPathComponent("sync"))
        sync.sessions = [a, b]; sync.selectedID = a.id; sync.draft = "Keep my unsent words"
        sync.search = "apple"; sync.sourceFilter = "Codex"; sync.view = "Chat"
        sync.banner = "Keep provider feedback"; sync.conversations[a.id] = LocalConversation(session: a, history: [], prompt: "fixture")
        var renamed = b; renamed.title = "Automatically discovered update"
        sync.applyImport(ImportResult(sessions: [renamed], sources: [], errors: []))
        precondition(sync.selectedID == a.id && sync.sessions.contains(a))
        precondition(sync.draft == "Keep my unsent words" && sync.search == "apple" && sync.sourceFilter == "Codex" && sync.view == "Chat")
        precondition(sync.banner == "Keep provider feedback" && sync.sessions.first { $0.id == b.id }?.title == renamed.title)
        sync.applyHistory(Transcript(messages: [Message(id: "stale", role: "assistant", text: "old read")], total: 1), sessionID: a.id)
        precondition(sync.messages.isEmpty)
        sync.conversations.removeValue(forKey: a.id)
        sync.applyHistory(Transcript(messages: [Message(id: "new", role: "assistant", text: "Automatically refreshed reply")], total: 1), sessionID: a.id)
        precondition(sync.messages.last?.text == "Automatically refreshed reply" && sync.historyTotal == 1)
        sync.applyHistory(Transcript(messages: [], total: 0), sessionID: b.id)
        precondition(sync.messages.count == 1 && sync.draft == "Keep my unsent words")
        sync.applyImport(ImportResult(sessions: [], sources: [], errors: ["Profile unavailable"], failedRoots: ["/tmp"]))
        precondition(sync.sessions.count == 2 && sync.importError == "Profile unavailable")
        sync.selectedID = nil
        sync.applyImport(ImportResult(sessions: [b], sources: [], errors: []))
        precondition(sync.sessions == [b] && sync.selectedID == b.id && sync.importError.isEmpty)
        let log = root.appendingPathComponent("live.jsonl")
        try Data("first\n".utf8).write(to: log)
        let watcher = TranscriptWatch()
        var notifications = 0
        watcher.observe([root.path, log.path]) { notifications += 1 }
        let handle = try FileHandle(forWritingTo: log); try handle.seekToEnd()
        try handle.write(contentsOf: Data("append\n".utf8)); try handle.close()
        for _ in 0..<50 { if notifications > 0 { break }; try await Task.sleep(for: .milliseconds(10)) }
        precondition(notifications > 0, "A transcript append must deliver a filesystem notification within 500 ms")
        notifications = 0
        try Data("replacement\n".utf8).write(to: log, options: .atomic)
        for _ in 0..<50 { if notifications > 0 { break }; try await Task.sleep(for: .milliseconds(10)) }
        precondition(notifications > 0, "Atomic replacement must reattach the watcher")
        notifications = 0
        let replacement = try FileHandle(forWritingTo: log); try replacement.seekToEnd()
        try replacement.write(contentsOf: Data("after replacement\n".utf8)); try replacement.close()
        for _ in 0..<50 { if notifications > 0 { break }; try await Task.sleep(for: .milliseconds(10)) }
        precondition(notifications > 0, "The replacement file must stay live")
        watcher.stop()
        print("PASS: 40 native checks including immediate append/replacement notifications, sync preservation and queues")
    }
}
