import Foundation

private struct NoPromotionWork: CampPublishingTransport {
    func perform(_ operation: CampPublication) async throws -> Data { preconditionFailure("A saved board link started network work") }
}

@main struct CampStateTests {
    @MainActor static func main() async throws {
        do { _ = try Bridge.process("unsupported-fixture-action"); preconditionFailure("Unsupported helper action accepted") }
        catch { precondition(error.localizedDescription.contains("Unsupported local session action")) }
        // Test executables have no app bundle. Missing bundled helpers must
        // fail explicitly without probing for an alternate language runtime.
        do { _ = try Bridge.process("conversation"); preconditionFailure("Missing bundled helper accepted") }
        catch { precondition(error.localizedDescription.contains("bundled Go session helper is missing")) }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let store = CampStore(stateDirectory: root)
        let a = CampSession(id: "codex-a", sessionID: "a", provider: "Codex", sourceID: "source-a", sourceLabel: "Personal", root: "/tmp", title: "Fix search", cwd: "/tmp/Apple", updated: 1, path: "", accountID: "", archived: false)
        let b = CampSession(id: "claude-b", sessionID: "b", provider: "Claude", sourceID: "source-b", sourceLabel: "Work", root: "/tmp", title: "Write tests", cwd: "/tmp/Banana", updated: 2, path: "", accountID: "", archived: true)
        store.sessions = [a,b]
        precondition(store.sessionCount("all") == 2 && store.sessionCount("Codex") == 1 && store.sessionCount("Claude") == 1)
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
        store.search = ""
        precondition(store.boardSessions(.ready).map(\.id) == [a.id])
        precondition(store.boardSessions(.done).map(\.id) == [b.id])
        store.move(a.id, .done)
        let restored = CampStore(stateDirectory: root)
        precondition(restored.stages[a.id] == .done)
        precondition(store.boardSessions(.ready).isEmpty && store.boardSessions(.done).count == 2)
        let attributes = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("board.json").path)
        precondition(attributes[.posixPermissions] as? Int == 0o600)
        store.move("untrusted-drop", .attention)
        precondition(store.stages["untrusted-drop"] == nil)
        store.conversations[a.id] = LocalConversation(session: a, history: [], prompt: "fixture")
        precondition(store.sessionCount("active") == 1)
        precondition(store.stage(a) == .progress)
        store.move(a.id, .ready)
        precondition(store.stages[a.id] == .done)
        store.conversations.removeValue(forKey: a.id)
        let external = LiveState(id: b.id, sourceID: b.sourceID, state: "running", model: "opus", children: [], owner: "Claude Code", pid: 123)
        store.applyActivity(ActivityResult(activity: [external], sessions: [], models: [:], errors: [:]))
        precondition(store.stage(b) == .progress)
        var actual = external; actual.effort = "max"; actual.speed = "fast"
        store.selectModel(b, "next-model")
        store.modelTuning[b.id] = ["next-model": ModelTuning(effort: "low", speed: "standard")]
        store.applyActivity(ActivityResult(activity: [actual], sessions: [], models: [:], errors: [:]))
        precondition(store.reportedSettings(b) == ReportedModelSettings(model: "opus", effort: "max", speed: "fast"))
        precondition(store.tuning(b).effort == "low", "Observing a run must not overwrite the next reply's settings")
        let reportedMenu = ModelSettingsMenu(provider: "Claude", title: b.title, sessionModel: b.model, modelLabel: "next-model", models: [], caps: ModelOption(id: "next-model", label: "Next"), choice: store.tuning(b), locked: false, reported: store.reportedSettings(b), selectModel: { _ in }, tune: { _, _ in })
        precondition(reportedMenu.displayedModel == "opus" && reportedMenu.effortTitle == "Max")
        store.applyActivity(ActivityResult(activity: [external], sessions: [], models: [:], errors: [:]))
        precondition(store.reportedSettings(b)?.effort == nil, "Missing runtime metadata must stay unknown")
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
        for background in [false, true] {
            let rejected = CampStore(stateDirectory: root.appendingPathComponent("rejected-\(background)"), conversationProcess: {
                fixtureProcess("rejected")
            })
            rejected.sessions = [a, b]; rejected.selectedID = a.id; rejected.view = "Chat"
            rejected.draft = "Preserve this rejected prompt"
            precondition(rejected.start(a, prompt: rejected.draft, model: "", tuning: ModelTuning(), notifyCompletion: false))
            rejected.draft = ""
            if background { rejected.selectedID = b.id; rejected.draft = "Another session's draft" }
            for _ in 0..<200 {
                if rejected.conversations[a.id] == nil { break }
                try await Task.sleep(for: .milliseconds(10))
            }
            precondition(rejected.conversations[a.id] == nil, "A rejected turn must finish")
            if background {
                precondition(rejected.draft == "Another session's draft", "A background failure must not overwrite the visible draft")
                rejected.open(a)
                precondition(rejected.draft == "Preserve this rejected prompt", "Recover the rejected prompt when returning to its original session")
            } else {
                precondition(rejected.draft == "Preserve this rejected prompt", "A rejection before acceptance must not discard the prompt")
            }
        }
        let visible = CloudSession(id: UUID().uuidString, campId: UUID().uuidString, projectId: UUID().uuidString, profileId: UUID().uuidString,
            provider: "Codex", profileLabel: "Fixture", ownerUserId: UUID().uuidString, ownerDeviceId: UUID().uuidString,
            localSessionId: UUID().uuidString, title: "Live session", model: "fixture", state: "idle", stage: "Ready", shared: false,
            allowRequests: false, externalOwner: false, revision: 1, transcriptRevision: 0, permissionRevision: 1, updatedAt: Date(),
            executionLocation: "local", executionEpoch: 1)
        var history = visible; history.id = UUID().uuidString; history.title = "Saved implementation"; history.historyOnly = true
        let historyCopy = try CampWire.decoder().decode(CloudSession.self, from: CampWire.encoder().encode(history))
        precondition(historyCopy.isHistoryOnly && CloudPlanningExecution(session: historyCopy) == nil)
        precondition(CloudPlanningExecution(session: visible)?.location == "local")
        store.applyCloud([visible, historyCopy], projects: [])
        precondition(store.sessions.contains { $0.id == "camp:" + visible.id })
        precondition(!store.sessions.contains { $0.id == "camp:" + history.id }, "Archived source became an ordinary board or recent-session card")
        precondition(store.cloudSessions["camp:" + history.id]?.isHistoryOnly == true, "Archived history lost its access/provenance record")
        let localPlanID = store.planning.create(planners: [.init(session: a, model: "fixture-model", tuning: .init())], mode: .single)!
        let localPlan = store.planning.ticket(localPlanID)!
        precondition(store.sessions.contains { $0.id == localPlanID })
        let linkScope = CloudScope(origin: "https://camp.example", userId: visible.ownerUserId, campId: visible.campId)
        let linkActor = CloudActor(userId: visible.ownerUserId, deviceId: visible.ownerDeviceId, email: "fixture@example.com", name: "Fixture")
        let board = CampStore(stateDirectory: root.appendingPathComponent("returned-board"))
        var linked = visible; linked.localSessionId = a.sessionID
        let share = LocalShare(id: linked.id, local: a, projectId: linked.projectId, shared: false, allowRequests: false,
            profile: CloudProfile(id: linked.profileId, ownerUserId: linkActor.userId, ownerDeviceId: linkActor.deviceId,
                provider: a.provider, localId: "fixture", label: "Fixture"), remote: linked, syncToCloud: true)
        try await board.cloud.cache!.saveShare(share, scope: linkScope, expectedEpoch: 0)
        board.sessions = [a]
        board.sharing.attach(scope: linkScope, actor: linkActor, transport: nil)
        for _ in 0..<100 where !board.sharing.ready { try await Task.sleep(for: .milliseconds(10)) }
        precondition(board.sharing.ready)
        board.applyCloud([linked], projects: [])
        precondition(board.visibleSessions.map(\.id) == [a.id], "A private local copy must keep one original card")
        linked.executionLocation = "cloud"
        board.applyCloud([linked], projects: [])
        precondition(board.visibleSessions.map(\.id) == ["camp:" + linked.id], "The cloud ticket must replace the source card")
        board.selectedID = "camp:" + linked.id
        linked.executionLocation = "local"; linked.executionEpoch = 3
        linked.profileId = UUID().uuidString; linked.localSessionId = UUID().uuidString
        board.applyCloud([linked], projects: [])
        precondition(board.visibleSessions.map(\.id) == ["camp:" + linked.id], "Returning must not resurrect the old source card")
        precondition(board.selectedID == "camp:" + linked.id, "Returning switched to stale source history")
        precondition(board.shareSource(board.selected!) == nil, "Returned history resolved to the original provider conversation")
        precondition(board.sessions.contains(a), "Hiding the old source must preserve it locally")
        precondition(board.executionLocationLabel(board.selected!) == "Owner’s Mac", "A foreign execution device was labelled This Mac")
        board.applyCloud([], projects: [])
        precondition(board.visibleSessions.map(\.id) == [a.id], "Disconnect must restore the retained local source")
        board.sharing.attach(scope: nil, actor: nil, transport: nil)
        let savedLink = PlanPromotionLink(planId: UUID().uuidString, localUpdated: localPlan.updated, fingerprint: try LocalPlanImport.fingerprint(localPlan))
        try await store.cloud.cache!.savePlanPromotion(JSONEncoder().encode(PlanPromotionState(links: [localPlanID: savedLink])), scope: linkScope, expectedEpoch: 0)
        store.planPromotion.attach(scope: linkScope, actor: linkActor, transport: NoPromotionWork(), sessions: [])
        for _ in 0..<100 where !store.planPromotion.ready { try await Task.sleep(for: .milliseconds(10)) }
        precondition(store.planPromotion.ready && !store.sessions.contains { $0.id == localPlanID }, "A saved local snapshot stayed duplicated on the board")
        precondition(store.planning.ticket(localPlanID) == localPlan, "Linking deleted the local recovery copy")
        store.planning.edit(localPlanID) { $0.brief = "New local work after the copy" }
        precondition(store.sessions.contains { $0.id == localPlanID }, "Newer local work disappeared behind an old saved copy")
        store.planPromotion.attach(scope: nil, actor: nil, transport: nil, sessions: [])
        precondition(store.sessions.contains { $0.id == localPlanID })
        print("PASS: native state, transcript notifications, queues, rejected prompt recovery and archived history visibility")
    }
}
