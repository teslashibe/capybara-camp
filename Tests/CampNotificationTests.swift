import Foundation

@MainActor final class FakeNoticeDelivery: NoticeDelivery {
    var opened: ((String) -> Void)?
    var foregrounded: ((String) -> Void)?
    var permission = true
    var permissionRequests = 0
    var sent: [(CampNotice, Bool, Bool)] = []
    var removed: [String] = []
    func authorization() async -> NoticeAuthorization { permission ? .allowed : .denied }
    func requestPermission() async throws -> Bool { permissionRequests += 1; return permission }
    func send(_ notice: CampNotice, titles: Bool, sound: Bool) async throws { sent.append((notice, titles, sound)) }
    func remove(_ ids: [String]) { removed += ids }
}

@main struct CampNotificationTests {
    @MainActor static func settle() async { for _ in 0..<20 { await Task.yield() } }
    static func scope(_ suffix: String) -> CloudScope {
        CloudScope(origin: "https://camp.example", userId: "person-" + suffix, campId: "shared-camp")
    }
    static func notification(_ title: String) -> CloudNotification {
        CloudNotification(id: UUID().uuidString, kind: "finished", title: title, sessionId: UUID().uuidString, requestId: UUID().uuidString,
            revision: 1, read: false, createdAt: Date())
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-notices-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let delivery = FakeNoticeDelivery()
        let directory = root.appendingPathComponent("main")
        let notices = CampNotifications(directory: directory, delivery: delivery, isActive: { true }, toastDuration: .milliseconds(20))
        precondition(delivery.permissionRequests == 0 && !notices.preferences.desktop)
        precondition(notices.record(key: "turn-a", sessionID: "local", title: "Private task", kind: "finished"))
        await notices.flush()
        precondition(notices.toast?.title == "Private task" && notices.unread == 1)
        precondition(!notices.record(key: "turn-a", sessionID: "local", title: "Replay", kind: "finished"))
        precondition(delivery.sent.isEmpty)
        try await Task.sleep(for: .milliseconds(40))
        precondition(notices.toast == nil)
        notices.record(key: "toast-a", sessionID: "local", title: "First", kind: "finished")
        notices.record(key: "toast-b", sessionID: "local", title: "Second", kind: "finished")
        await notices.flush()
        precondition(notices.toastCount == 2)
        notices.pauseToast("focus", paused: true)
        try await Task.sleep(for: .milliseconds(40))
        precondition(notices.toast != nil)
        notices.pauseToast("focus", paused: false)
        try await Task.sleep(for: .milliseconds(40))
        precondition(notices.toast == nil)
        await notices.markAllRead(); await notices.clearRead(); await notices.flush()
        notices.record(key: "unread-test", sessionID: "local", title: "Read this", kind: "finished")
        await notices.flush()
        let id = notices.entries[0].id
        await notices.markRead(id); precondition(notices.unread == 0)
        await notices.clearRead(); precondition(notices.entries.isEmpty)
        await notices.flush()
        precondition(!notices.record(key: "turn-a", sessionID: "local", title: "Deleted replay", kind: "finished"))

        let background = CampNotifications(directory: directory, delivery: delivery, isActive: { false })
        await background.enableDesktop(true)
        precondition(delivery.permissionRequests == 1 && background.preferences.desktop)
        background.record(key: "turn-b", sessionID: "local", title: "Sensitive title", kind: "finished")
        await background.flush()
        await settle()
        precondition(delivery.sent.count == 1 && delivery.sent[0].1 == false && delivery.sent[0].2 == false)
        precondition(background.toast == nil)
        delivery.foregrounded?(background.entries[0].id)
        precondition(background.toast?.id == background.entries[0].id, "Returning to the foreground while macOS delivers an alert must show an in-app toast")
        background.dismissToast()
        let restored = CampNotifications(directory: directory, delivery: delivery, isActive: { false })
        precondition(!restored.record(key: "turn-b", sessionID: "local", title: "Replay", kind: "finished"))
        precondition(restored.unread == 1 && restored.preferences.desktop)
        let permissions = try FileManager.default.attributesOfItem(atPath: directory.appendingPathComponent("notifications.json").path)[.posixPermissions] as? NSNumber
        precondition(permissions?.intValue == 0o600)

        let a = scope("a"), b = scope("b")
        restored.setScope(a)
        restored.record(key: "shared-a", sessionID: "camp:shared-session", title: "A private title", kind: "plan", scope: a)
        await settle()
        restored.setScope(b)
        precondition(restored.visible.allSatisfy { $0.scope == nil || $0.scope == b })
        precondition(delivery.removed.contains(PublishMessage.hash(Data("shared-a".utf8))))
        restored.setScope(a)
        restored.prune(scope: a, allowed: [])
        await restored.flush()
        precondition(restored.entries.allSatisfy { $0.scope != a })

        let cloudDirectory = root.appendingPathComponent("cloud")
        let cloud = CampNotifications(directory: cloudDirectory, delivery: delivery, isActive: { false })
        cloud.setScope(a); await cloud.enableDesktop(true)
        let sentBefore = delivery.sent.count
        let historical = notification("Historical result"), fresh = notification("New result")
        cloud.applyCloud([historical], scope: a, live: false)
        await cloud.flush()
        precondition(cloud.entries.count == 1 && delivery.sent.count == sentBefore, "Initial history is visible without replaying alerts")
        let reconnected = CampNotifications(directory: cloudDirectory, delivery: delivery, isActive: { false })
        reconnected.setScope(a)
        reconnected.applyCloud([historical, fresh], scope: a, live: false)
        await reconnected.flush()
        reconnected.applyCloud([historical, fresh], scope: a, live: true)
        await reconnected.flush()
        await settle()
        precondition(reconnected.entries.count == 2 && delivery.sent.count == sentBefore, "Reconnect restores inbox history without replaying desktop alerts")
        let saved = try Data(contentsOf: cloudDirectory.appendingPathComponent("notifications.json"))
        precondition(!String(data: saved, encoding: .utf8)!.contains("private prompt"))
        var uncertain = notification("Unconfirmed result"); uncertain.kind = "attention"
        reconnected.applyCloud([historical, fresh, uncertain], scope: a, live: true)
        await reconnected.flush()
        await settle(); let beforeReconcile = delivery.sent.count
        uncertain.kind = "finished"; uncertain.revision = 2
        reconnected.applyCloud([historical, fresh, uncertain], scope: a, live: true)
        await reconnected.flush()
        await settle()
        precondition(reconnected.entries.first?.kind == "finished" && delivery.sent.count == beforeReconcile)
        let fast = notification("Fast task")
        reconnected.applyCloud([historical, fresh, uncertain, fast], scope: a, live: true)
        await reconnected.flush()
        await settle()
        precondition(reconnected.entries.first?.title == "Fast task", "A coalesced live completion must not require a visible started event")
        var actions: [CloudNotificationAction] = []
        reconnected.cloudAction = { _, ids, action in actions.append(CloudNotificationAction(ids: ids, action: action)) }
        let localID = PublishMessage.hash(Data(fast.key(scope: a).utf8))
        await reconnected.markRead(localID)
        precondition(actions.count == 1 && actions[0].ids == [fast.id] && actions[0].action == "read")
        var remotelyRead = fresh; remotelyRead.read = true; remotelyRead.revision = 2
        reconnected.applyCloud([historical, remotelyRead, uncertain], scope: a, live: true)
        precondition(reconnected.entries.first(where: { $0.cloudID == fresh.id })?.read == true, "Read receipts from another Mac are authoritative")
        precondition(reconnected.entries.allSatisfy { $0.cloudID != fast.id }, "An archived server entry must disappear locally")
        let unreadBeforeSwitch = reconnected.unread
        var finishReceipt: CheckedContinuation<Void, Never>?
        reconnected.cloudAction = { _, _, _ in await withCheckedContinuation { finishReceipt = $0 } }
        let pendingReceipt = Task { await reconnected.markAllRead() }
        await settle()
        precondition(finishReceipt != nil)
        reconnected.setScope(b); finishReceipt?.resume(); await pendingReceipt.value
        reconnected.setScope(a)
        precondition(reconnected.unread == unreadBeforeSwitch, "A delayed action cannot alter the inbox after switching Camp identity")
        reconnected.cloudAction = { _, _, _ in throw CampCloudError.cacheUnavailable }
        await reconnected.markRead(PublishMessage.hash(Data(historical.key(scope: a).utf8)))
        precondition(reconnected.unread == unreadBeforeSwitch && !reconnected.error.isEmpty, "Failed durable enqueue must preserve unread state")

        let raceDelivery = FakeNoticeDelivery()
        let race = CampNotifications(directory: root.appendingPathComponent("race"), delivery: raceDelivery, isActive: { false })
        await race.enableDesktop(true); race.setScope(a)
        race.record(key: "race", sessionID: "camp:shared-session", title: "Scope changed", kind: "finished", scope: a)
        race.setScope(b); await settle()
        precondition(raceDelivery.sent.isEmpty, "Switching identity must fence a pending desktop alert")
        raceDelivery.permission = false
        await race.enableDesktop(true)
        precondition(!race.preferences.desktop && !race.error.isEmpty)

        let bounded = CampNotifications(directory: root.appendingPathComponent("bounded"), delivery: delivery, isActive: { false })
        for index in 0..<1020 { bounded.record(key: "bounded-\(index)", sessionID: "local", title: "Entry", kind: "finished") }
        await bounded.flush()
        precondition(bounded.entries.count == 1000)
        precondition(!bounded.record(key: "bounded-0", sessionID: "local", title: "Replay after eviction", kind: "finished"))
        let broken = root.appendingPathComponent("broken")
        try FileManager.default.createDirectory(at: broken, withIntermediateDirectories: true)
        let bytes = Data("invalid history".utf8); try bytes.write(to: broken.appendingPathComponent("notifications.json"))
        let invalid = CampNotifications(directory: broken, delivery: delivery, isActive: { false })
        precondition(!invalid.record(key: "invalid", sessionID: "local", title: "Must not write", kind: "finished"))
        let preserved = try Data(contentsOf: broken.appendingPathComponent("notifications.json"))
        precondition(preserved == bytes)

        let state = root.appendingPathComponent("store")
        let store = CampStore(stateDirectory: state, conversationProcess: {
            let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
            process.arguments = ["-u", "-c", "import sys,json;json.loads(sys.stdin.readline());print('{\"type\":\"started\"}',flush=True);print('{\"type\":\"text\",\"text\":\"Complete\"}',flush=True);print('{\"type\":\"done\"}',flush=True)"]
            return process
        })
        let session = CampSession(id: "notification-session", sessionID: "fixture", provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Native completion", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
        store.sessions = [session]
        precondition(store.start(session, prompt: "Synthetic check", model: "", tuning: ModelTuning()))
        for _ in 0..<200 { if !store.notifications.entries.isEmpty { break }; try await Task.sleep(for: .milliseconds(10)) }
        precondition(store.notifications.entries.count == 1 && store.notifications.entries[0].sessionID == session.id)
        store.notifications.pendingOpen = store.notifications.entries[0].id
        await settle()
        precondition(store.selectedID == session.id && store.notifications.unread == 0)
        await store.notifications.flush(); await race.flush()
        precondition(Bridge.activeProcesses.isEmpty)
        print("PASS: notification persistence, replay dedupe, cloud scope/reconnect, private desktop defaults, permission denial, bounded history and native completion routing")
    }
}
