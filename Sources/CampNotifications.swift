import Foundation
import SwiftUI
import AppKit
import UserNotifications

struct CampNotice: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var sessionID: String
    var scope: CloudScope?
    var title: String
    var kind: String
    var created: Date
    var read = false
    var planVersion: Int?
    var messageID: String?
    var cloudID: String?
    var cloudPlanID: String?
    var summary: String {
        switch kind { case "plan": return "Plan ready to review"; case "attention": return "Task needs attention"; default: return "Task finished" }
    }
}

struct CampNoticePreferences: Codable, Equatable {
    var desktop = false
    var titles = false
    var sound = false
    var toasts = true
}

private struct NoticeArchive: Codable {
    var version = 1
    var entries: [CampNotice] = []
    var seen: [String] = []
    var preferences = CampNoticePreferences()
}

private actor NoticeWriter {
    let file: URL
    init(file: URL) { self.file = file }
    func save(_ value: NoticeArchive) throws {
        let data = try JSONEncoder().encode(value)
        guard data.count <= 4 << 20 else { throw CampError.message("Notification history is full") }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try data.write(to: file, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
    }
}

enum NoticeAuthorization { case notDetermined, allowed, denied }

@MainActor protocol NoticeDelivery: AnyObject {
    var opened: ((String) -> Void)? { get set }
    var foregrounded: ((String) -> Void)? { get set }
    func authorization() async -> NoticeAuthorization
    func requestPermission() async throws -> Bool
    func send(_ notice: CampNotice, titles: Bool, sound: Bool) async throws
    func remove(_ ids: [String])
    func updateBadge(_ count: Int) async
}
extension NoticeDelivery { func updateBadge(_ count: Int) async {} }

@MainActor final class MacNoticeDelivery: NSObject, NoticeDelivery, UNUserNotificationCenterDelegate {
    static let shared = MacNoticeDelivery()
    var opened: ((String) -> Void)?
    var foregrounded: ((String) -> Void)?
    private var available: Bool { Bundle.main.bundleURL.pathExtension == "app" }
    func install() { if available { UNUserNotificationCenter.current().delegate = self } }
    func authorization() async -> NoticeAuthorization {
        guard available else { return .denied }
        let status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        switch status { case .authorized, .provisional: return .allowed; case .notDetermined: return .notDetermined; default: return .denied }
    }
    func requestPermission() async throws -> Bool {
        guard available else { return false }
        return try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
    }
    func send(_ notice: CampNotice, titles: Bool, sound: Bool) async throws {
        guard available else { throw CampError.message("Desktop notifications require the Mac app") }
        let content = UNMutableNotificationContent()
        content.title = notice.summary
        content.body = titles ? notice.title : "Open Capybara Camp to view the result"
        content.userInfo = ["noticeID": notice.id]
        content.threadIdentifier = PublishMessage.hash(Data(((notice.scope?.key ?? "local") + ":" + notice.sessionID).utf8))
        if sound { content.sound = .default }
        try await UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: notice.id, content: content, trigger: nil))
    }
    func remove(_ ids: [String]) {
        guard available, !ids.isEmpty else { return }
        let center = UNUserNotificationCenter.current()
        center.removePendingNotificationRequests(withIdentifiers: ids)
        center.removeDeliveredNotifications(withIdentifiers: ids)
    }
    func updateBadge(_ count: Int) async {
        guard Bundle.main.bundleURL.pathExtension == "app" else { return }
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        NSApp?.dockTile.badgeLabel = settings.badgeSetting == .enabled && count > 0 ? String(count) : nil
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                           withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        let id = notification.request.content.userInfo["noticeID"] as? String
        completionHandler([])
        Task { @MainActor [weak self] in if let id { self?.foregrounded?(id) } }
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                           withCompletionHandler completionHandler: @escaping () -> Void) {
        let id = response.notification.request.content.userInfo["noticeID"] as? String
        Task { @MainActor [weak self] in if let id { self?.opened?(id) } }
        completionHandler()
    }
}

@MainActor final class CampNotifications: ObservableObject {
    @Published private(set) var entries: [CampNotice] = []
    @Published private(set) var preferences = CampNoticePreferences()
    @Published private(set) var toast: CampNotice?
    @Published private(set) var toastCount = 0
    @Published private(set) var scope: CloudScope?
    @Published var pendingOpen: String? { didSet { if pendingOpen != nil { openRequested?() } } }
    var openRequested: (() -> Void)?
    var cloudAction: ((CloudScope, [String], String) async throws -> Void)?
    @Published private(set) var error = ""
    @Published private(set) var permissionBusy = false
    private var archive = NoticeArchive()
    private var readable = true
    private let file: URL
    private let writer: NoticeWriter
    private let delivery: any NoticeDelivery
    private let isActive: @MainActor () -> Bool
    private var toastTask: Task<Void, Never>?
    private var deliveryGeneration = UUID()
    private var seen = Set<String>()
    private var toastDuration: Duration
    private var toastPauses: Set<String> = []
    private var revision = 0
    private var pendingWrite: (Int, NoticeArchive)?
    private var writeTask: Task<Void, Never>?
    private var pendingAlerts: [(Int, CampNotice, UUID)] = []
    private var badgeTask: Task<Void, Never>?

    init(directory: URL, delivery: (any NoticeDelivery)? = nil, isActive: @escaping @MainActor () -> Bool = { NSApp?.isActive == true }, toastDuration: Duration = .seconds(6)) {
        file = directory.appendingPathComponent("notifications.json")
        writer = NoticeWriter(file: directory.appendingPathComponent("notifications.json"))
        self.delivery = delivery ?? MacNoticeDelivery.shared; self.isActive = isActive; self.toastDuration = toastDuration
        if FileManager.default.fileExists(atPath: file.path) {
            do {
                let size = (try FileManager.default.attributesOfItem(atPath: file.path)[.size] as? NSNumber)?.intValue ?? Int.max
                guard size <= 4 << 20 else { throw CampError.message("Notification history exceeds its storage limit") }
                archive = try JSONDecoder().decode(NoticeArchive.self, from: Data(contentsOf: file))
                guard archive.version == 1, archive.entries.count <= 1000, archive.seen.count <= 4096,
                      Set(archive.entries.map(\.id)).count == archive.entries.count,
                      archive.entries.allSatisfy({ $0.id.count == 64 && $0.title.utf8.count <= 1024 && $0.sessionID.utf8.count <= 512 && ["finished", "attention", "plan"].contains($0.kind) }) else { throw CampError.message("Notification history is invalid") }
            } catch { archive = NoticeArchive(); readable = false; self.error = "Notification history could not be opened" }
        }
        entries = archive.entries; preferences = archive.preferences; seen = Set(archive.seen)
        self.delivery.opened = { [weak self] id in self?.pendingOpen = id }
        self.delivery.foregrounded = { [weak self] id in
            guard let self, let notice = self.entries.first(where: { $0.id == id && !$0.read }),
                  notice.scope == nil || notice.scope == self.scope else { return }
            self.showToast(notice)
        }
    }
    deinit { toastTask?.cancel(); writeTask?.cancel(); badgeTask?.cancel() }

    var visible: [CampNotice] { entries.filter { ($0.scope == nil || $0.scope == scope) && $0.created > Date().addingTimeInterval(-30 * 24 * 60 * 60) } }
    var unread: Int { visible.filter { !$0.read }.count }
    func setScope(_ value: CloudScope?) {
        guard scope != value else { return }
        let hidden = entries.filter { $0.scope != nil && $0.scope != value }.map(\.id)
        scope = value; deliveryGeneration = UUID(); delivery.remove(hidden)
        if let toast, toast.scope != nil && toast.scope != value { dismissToast() }
        updateBadge()
    }
    private func persist(_ value: NoticeArchive) -> Bool {
        guard readable else { return false }
        var value = value
        let cutoff = Date().addingTimeInterval(-30 * 24 * 60 * 60)
        let expired = value.entries.filter { $0.created < cutoff }.map(\.id)
        value.entries.removeAll { $0.created < cutoff }
        archive = value; entries = value.entries; preferences = value.preferences; seen = Set(value.seen); error = ""
        delivery.remove(expired)
        revision += 1; pendingWrite = (revision, value)
        updateBadge()
        if writeTask == nil {
            writeTask = Task { [weak self] in
                while let self, let next = self.pendingWrite, !Task.isCancelled {
                    self.pendingWrite = nil
                    do {
                        try await self.writer.save(next.1)
                        let alerts = self.pendingAlerts.filter { $0.0 <= next.0 }
                        self.pendingAlerts.removeAll { $0.0 <= next.0 }
                        for (_, notice, generation) in alerts { self.present(notice, generation: generation) }
                    } catch {
                        self.pendingAlerts.removeAll { $0.0 <= next.0 }
                        self.error = "Notification history could not be saved. Desktop delivery was skipped."
                    }
                }
                self?.writeTask = nil
            }
        }
        return true
    }
    func flush() async { while let task = writeTask { await task.value } }
    private func updateBadge() {
        guard badgeTask == nil else { return }
        badgeTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(50))
            guard let self, !Task.isCancelled else { return }
            let count = self.unread
            await self.delivery.updateBadge(count); self.badgeTask = nil
            if count != self.unread { self.updateBadge() }
        }
    }
    private func remembered(_ id: String, in value: inout NoticeArchive) {
        if !value.seen.contains(id) { value.seen.append(id) }
        if value.seen.count > 4096 { value.seen.removeFirst(value.seen.count - 4096) }
    }
    @discardableResult func record(key: String, sessionID: String, title: String, kind: String, scope: CloudScope? = nil,
                                  planVersion: Int? = nil, messageID: String? = nil, notify: Bool = true) -> Bool {
        let id = PublishMessage.hash(Data(key.utf8))
        guard !seen.contains(id), ["finished", "attention", "plan"].contains(kind), sessionID.utf8.count <= 512 else { return false }
        var next = archive
        remembered(id, in: &next)
        let notice = CampNotice(id: id, sessionID: sessionID, scope: scope, title: String(title.prefix(200)), kind: kind, created: Date(), planVersion: planVersion, messageID: messageID)
        next.entries.insert(notice, at: 0)
        let removed = next.entries.dropFirst(1000).map(\.id)
        next.entries = Array(next.entries.prefix(1000))
        guard persist(next) else { return false }
        delivery.remove(removed)
        if notify { pendingAlerts.append((revision, notice, deliveryGeneration)) }
        return true
    }
    private func present(_ notice: CampNotice, generation: UUID) {
        guard generation == deliveryGeneration, notice.scope == nil || notice.scope == self.scope,
              entries.contains(where: { $0.id == notice.id && !$0.read }) else { return }
        if isActive() {
            showToast(notice)
        } else if preferences.desktop {
            let titles = preferences.titles, sound = preferences.sound, id = notice.id
            Task { [weak self] in
                guard let self, await self.delivery.authorization() == .allowed,
                      self.deliveryGeneration == generation, self.preferences.desktop,
                      self.entries.contains(where: { $0.id == id && !$0.read }), notice.scope == nil || notice.scope == self.scope else { return }
                do {
                    try await self.delivery.send(notice, titles: titles, sound: sound)
                    if self.deliveryGeneration != generation || !self.entries.contains(where: { $0.id == id && !$0.read }) { self.delivery.remove([id]) }
                } catch { self.error = "macOS could not deliver this alert. Its result is saved in Notifications." }
            }
        }
    }
    private func showToast(_ notice: CampNotice) {
        guard preferences.toasts, toast?.id != notice.id else { return }
        toastCount = toast.map { notice.created.timeIntervalSince($0.created) < 2 ? toastCount + 1 : 1 } ?? 1
        toast = notice; scheduleToastDismissal()
    }
    func seedPlans(_ plans: [LocalPlan]) {
        var next = archive
        for plan in plans where ["ready", "failed", "needs_review"].contains(plan.current.state) {
            remembered(PublishMessage.hash(Data("plan:\(plan.id):\(plan.current.number)".utf8)), in: &next)
        }
        if next.seen != archive.seen { _ = persist(next) }
    }
    func observePlans(_ plans: [LocalPlan]) {
        for plan in plans where ["ready", "failed", "needs_review"].contains(plan.current.state) {
            record(key: "plan:\(plan.id):\(plan.current.number)", sessionID: plan.id, title: plan.title,
                   kind: plan.current.state == "ready" ? "plan" : "attention", planVersion: plan.current.number)
        }
    }
    func applyCloud(_ values: [CloudNotification], scope: CloudScope, live: Bool) {
        guard self.scope == scope, values.count <= 1000, values.allSatisfy(\.valid) else { return }
        var next = archive, alerts: [CampNotice] = []
        let incoming = values.map { value -> CampNotice in
            let id = PublishMessage.hash(Data(value.key(scope: scope).utf8))
            let notice = CampNotice(id: id, sessionID: value.sessionId.map { "camp:" + $0 } ?? "camp-plan:" + (value.planId ?? ""),
                scope: scope, title: value.title, kind: value.kind, created: value.createdAt, read: value.read,
                planVersion: value.planVersion, cloudID: value.id, cloudPlanID: value.planId)
            if !seen.contains(id) && live && !value.read { alerts.append(notice) }
            remembered(id, in: &next)
            return notice
        }
        let keep = Set(incoming.filter { !$0.read }.map(\.id))
        let removed = next.entries.filter { $0.scope == scope && !keep.contains($0.id) }.map(\.id)
        next.entries.removeAll { $0.scope == scope }
        next.entries += incoming
        next.entries = Array(next.entries.sorted { $0.created > $1.created }.prefix(1000))
        if persist(next) {
            delivery.remove(removed)
            if let toast, removed.contains(toast.id) { dismissToast() }
            for notice in alerts { pendingAlerts.append((revision, notice, deliveryGeneration)) }
        }
    }
    func prune(scope: CloudScope?, allowed: Set<String>) {
        let removed = archive.entries.filter { $0.scope == scope && !allowed.contains($0.sessionID) }.map(\.id)
        var next = archive
        next.entries.removeAll { removed.contains($0.id) }
        guard next.entries != archive.entries else { return }
        if persist(next) { deliveryGeneration = UUID(); delivery.remove(removed); if let toast, removed.contains(toast.id) { dismissToast() } }
    }
    func markRead(_ id: String) async {
        let originalScope = scope
        guard let notice = archive.entries.first(where: { $0.id == id }), !notice.read else { return }
        if let scope = notice.scope, let remote = notice.cloudID {
            do { guard let cloudAction else { return }; try await cloudAction(scope, [remote], "read") }
            catch { self.error = "Could not queue the shared read receipt"; return }
        }
        guard scope == originalScope else { return }
        var next = archive
        guard let index = next.entries.firstIndex(where: { $0.id == id && ($0.scope == nil || $0.scope == scope) }) else { return }
        next.entries[index].read = true
        if persist(next) { delivery.remove([id]); if toast?.id == id { dismissToast() } }
    }
    func markAllRead() async {
        let originalScope = scope
        let ids = Set(visible.filter { !$0.read }.map(\.id)); var next = archive
        let shared = visible.filter { !$0.read }.compactMap(\.cloudID)
        if !shared.isEmpty, let scope {
            do { guard let cloudAction else { return }; try await cloudAction(scope, shared, "read") }
            catch { self.error = "Could not queue the shared read receipts"; return }
            next = archive
        }
        guard scope == originalScope else { return }
        for index in next.entries.indices where ids.contains(next.entries[index].id) { next.entries[index].read = true }
        if persist(next) { delivery.remove(Array(ids)); dismissToast() }
    }
    func clearRead() async {
        let originalScope = scope
        let ids = Set(visible.filter(\.read).map(\.id)); var next = archive
        let shared = visible.filter(\.read).compactMap(\.cloudID)
        if !shared.isEmpty, let scope {
            do { guard let cloudAction else { return }; try await cloudAction(scope, shared, "archive") }
            catch { self.error = "Could not queue the shared inbox change"; return }
            next = archive
        }
        guard scope == originalScope else { return }
        next.entries.removeAll { ids.contains($0.id) }
        if persist(next) { delivery.remove(Array(ids)) }
    }
    func dismissToast() { toastTask?.cancel(); toastTask = nil; toast = nil; toastCount = 0; toastPauses = [] }
    func pauseToast(_ reason: String, paused: Bool) {
        if paused { toastPauses.insert(reason); toastTask?.cancel(); toastTask = nil }
        else { toastPauses.remove(reason); if toastPauses.isEmpty && toast != nil { scheduleToastDismissal() } }
    }
    private func scheduleToastDismissal() {
        toastTask?.cancel(); guard toastPauses.isEmpty else { return }
        toastTask = Task { [weak self, toastDuration] in
            try? await Task.sleep(for: toastDuration)
            guard !Task.isCancelled else { return }; self?.dismissToast()
        }
    }
    func updatePreferences(_ change: (inout CampNoticePreferences) -> Void) {
        var next = archive; change(&next.preferences)
        if persist(next) {
            deliveryGeneration = UUID()
            if !preferences.desktop { delivery.remove(entries.map(\.id)) }
            if !preferences.toasts { dismissToast() }
        }
    }
    func enableDesktop(_ enabled: Bool) async {
        guard !permissionBusy else { return }
        if !enabled { updatePreferences { $0.desktop = false }; return }
        permissionBusy = true; defer { permissionBusy = false }
        do {
            let allowed = try await delivery.requestPermission()
            updatePreferences { $0.desktop = allowed }
            if !allowed { error = "Allow Capybara Camp in System Settings → Notifications to enable desktop alerts" }
        } catch {
            updatePreferences { $0.desktop = false }
            let detail = error as NSError
            self.error = detail.domain == "UNErrorDomain" && detail.code == 1
                ? "Allow Capybara Camp in System Settings → Notifications to enable desktop alerts"
                : "macOS notification permission could not be requested"
            #if CAMP_DEVELOPMENT
            try? FileHandle.standardError.write(contentsOf: Data("Notification permission: \(detail.domain) \(detail.code)\n".utf8))
            #endif
        }
    }
}

extension CampStore {
    func restoreCloudAndNotification() async {
        await cloud.restore(); cloudRestoreFinished = true
        await openPendingNotification()
    }
    func openPendingNotification() async {
        guard !openingNotification, let id = notifications.pendingOpen else { return }
        openingNotification = true; defer { openingNotification = false }
        guard let notice = notifications.entries.first(where: { $0.id == id }) else { notifications.pendingOpen = nil; return }
        if let scope = notice.scope {
            guard scope == cloud.scope else {
                if !cloudRestoreFinished { return }
                notifications.pendingOpen = nil; banner = "Connect to the Camp that owns this notification"; return
            }
            if notice.cloudPlanID != nil && !sharedPlanning.ready { return }
            guard let client = cloud.client, let credential = cloud.credential else { return }
            do {
                if let remoteID = notice.cloudID {
                    let current = try await client.get(CloudNotification.self, path: "/v1/camps/\(scope.campId)/notifications/\(remoteID)", token: credential.token)
                    guard current.valid, current.id == remoteID,
                          PublishMessage.hash(Data(current.key(scope: scope).utf8)) == notice.id,
                          current.planId == notice.cloudPlanID,
                          current.planId != nil || "camp:" + (current.sessionId ?? "") == notice.sessionID,
                          cloud.scope == scope, cloud.credential?.token == credential.token else { throw CampCloudError.invalidResponse }
                    if let planID = current.planId, let version = current.planVersion {
                        let result = try await client.get(CloudPlanningResult.self, path: "/v1/camps/\(scope.campId)/planning/\(planID)?version=\(version)", token: credential.token)
                        guard result.valid, result.id == planID, result.version.version == version, cloud.scope == scope, notifications.pendingOpen == id else { return }
                        try await sharedPlanning.install(result)
                        notificationSheet = false; sharedPlanScope = scope; openSharedPlan(planID, version: version); notificationPlanVersion = version
                        await notifications.markRead(id); notifications.pendingOpen = nil
                        AppDelegate.reopen?(); NSApp?.activate(ignoringOtherApps: true)
                        return
                    }
                }
                let nativeID = String(notice.sessionID.dropFirst(5))
                _ = try await client.get(CloudSession.self, path: "/v1/camps/\(scope.campId)/sessions/\(nativeID)", token: credential.token)
                guard cloud.scope == scope, notifications.pendingOpen == id else { return }
            } catch {
                notifications.pendingOpen = nil
                banner = "This result is unavailable or no longer shared with you"
                return
            }
        }
        guard let session = sessions.first(where: { $0.id == notice.sessionID }) else {
            if notice.scope == nil && !hasImportedSessions { return }
            notifications.pendingOpen = nil; banner = "This session is no longer available"; return
        }
        notificationSheet = false; open(session)
        if notice.kind == "plan", let version = notice.planVersion {
            reviewingPlan = session.id; notificationPlanVersion = version
        }
        notificationMessageID = notice.messageID
        await notifications.markRead(id); notifications.pendingOpen = nil
        AppDelegate.reopen?()
        NSApp?.activate(ignoringOtherApps: true)
    }
}

struct CampNotificationButton: View {
    @ObservedObject var notices: CampNotifications
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            Image(systemName: "bell").frame(width: 32, height: 32)
                .overlay(alignment: .topTrailing) {
                    if notices.unread > 0 {
                        Text(notices.unread > 99 ? "99+" : String(notices.unread))
                            .font(.system(size: 10, weight: .semibold)).foregroundStyle(.white)
                            .padding(.horizontal, 4).frame(minWidth: 17, minHeight: 17)
                            .background(campGreen, in: Capsule())
                            .overlay(Capsule().stroke(campCream, lineWidth: 2)).offset(x: 3, y: -3)
                    }
                }
        }.buttonStyle(.plain).help("Notifications").accessibilityLabel("Notifications, \(notices.unread) unread")
    }
}

struct CampNotificationToast: View {
    @ObservedObject var store: CampStore
    @ObservedObject var notices: CampNotifications
    private enum Focus: Hashable { case open, dismiss }
    @FocusState private var focused: Focus?
    var body: some View {
        if let notice = notices.toast {
            HStack(spacing: 10) {
                Text(notices.toastCount > 1 ? "\(notices.toastCount) task updates" : notice.summary + " · " + notice.title)
                    .lineLimit(1).truncationMode(.tail).layoutPriority(1)
                Button {
                    if notices.toastCount > 1 { store.notificationSheet = true; notices.dismissToast() }
                    else { notices.pendingOpen = notice.id }
                } label: {
                    Text(notices.toastCount > 1 ? "View all" : "View").fontWeight(.medium)
                        .padding(.horizontal, 9).frame(minHeight: 28)
                        .background(.white.opacity(0.12), in: RoundedRectangle(cornerRadius: 5))
                }.buttonStyle(.plain).focusable().focused($focused, equals: .open)
                    .accessibilityLabel(notices.toastCount > 1 ? "Open notifications" : "Open result")
                Button { notices.dismissToast() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).focusable().focused($focused, equals: .dismiss).help("Dismiss notification").accessibilityLabel("Dismiss notification")
            }.font(.system(size: 11)).foregroundStyle(.white)
                .padding(.leading, 13).padding(.trailing, 10).padding(.vertical, 9)
                .background(Color(hex: 0x405849), in: RoundedRectangle(cornerRadius: 7))
                .frame(maxWidth: 660).shadow(color: campInk.opacity(0.15), radius: 9, y: 5)
                .onHover { notices.pauseToast("hover", paused: $0) }
                .onChange(of: focused) { _, value in notices.pauseToast("focus", paused: value != nil) }
                .onExitCommand { notices.dismissToast() }
                .onDisappear { notices.pauseToast("hover", paused: false); notices.pauseToast("focus", paused: false) }
        }
    }
}

struct CampNotificationsView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var notices: CampNotifications
    @State private var showRead = true
    @State private var count = 50
    @State private var settings = false
    @FocusState private var closeFocused: Bool
    private var values: [CampNotice] { notices.visible.filter { showRead || !$0.read } }
    var body: some View {
        let shown = Array(values.prefix(count))
        let ids = Set(shown.map(\.sessionID))
        let providers = Dictionary(store.sessions.lazy.filter { ids.contains($0.id) }.map { ($0.id, $0.provider) }, uniquingKeysWith: { first, _ in first })
        VStack(alignment: .leading, spacing: 15) {
            HStack {
                Text("Notifications").font(.system(size: 20, weight: .medium))
                Spacer()
                Button { store.notificationSheet = false } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).help("Close notifications").accessibilityLabel("Close notifications")
                    .keyboardShortcut(.cancelAction).focusable().focused($closeFocused)
                    .onKeyPress(keys: [.space, .return]) { _ in store.notificationSheet = false; return .handled }
            }
            HStack(spacing: 6) {
                CampSegmentedControl(options: [(true, "All"), (false, notices.unread > 0 ? "Unread · \(notices.unread)" : "Unread")], selection: $showRead).frame(width: 142)
                Spacer()
                Button { Task { await notices.markAllRead() } } label: { Image(systemName: "checkmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).focusable().help("Mark all as read").accessibilityLabel("Mark all as read").disabled(notices.unread == 0)
                    .onKeyPress(keys: [.space, .return]) { _ in
                        guard notices.unread > 0 else { return .ignored }
                        Task { await notices.markAllRead() }; return .handled
                    }
                Button { settings = true } label: { Image(systemName: "gearshape").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).focusable().help("Notification settings").accessibilityLabel("Notification settings")
                    .onKeyPress(keys: [.space, .return]) { _ in settings = true; return .handled }
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if values.isEmpty {
                        VStack(spacing: 12) {
                            Image(systemName: "bell").font(.system(size: 26))
                            Text(showRead ? "No notifications yet" : "You’re all caught up").font(.system(size: 15, weight: .medium)).foregroundStyle(campInk)
                            Text("Finished tasks and plans appear here\nSelect one to pick up where you left off").font(.system(size: 12)).multilineTextAlignment(.center)
                        }.foregroundStyle(campMuted).frame(maxWidth: .infinity, minHeight: 280)
                    }
                    ForEach(shown) { notice in notificationRow(notice, provider: providers[notice.sessionID]) }
                    if values.count > count { Button("Show 50 more") { count += 50 }.padding(12) }
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
            Divider()
            HStack {
                Text(notices.unread > 0 ? "\(notices.unread) unread" : "All caught up").font(.system(size: 11)).foregroundStyle(campMuted)
                Spacer()
                Button("Clear read") { Task { await notices.clearRead() } }.disabled(!notices.visible.contains(where: \.read))
            }
        }.padding(19).frame(maxHeight: .infinity).background(campCream, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).stroke(campInk.opacity(0.08)))
            .shadow(color: campInk.opacity(0.15), radius: 20, y: 8)
            .onAppear { closeFocused = true; notices.dismissToast() }
            .onReceive(notices.$toast) { if $0 != nil { notices.dismissToast() } }
            .onExitCommand { store.notificationSheet = false }
            .sheet(isPresented: $settings) { CampNotificationSettings(notices: notices) { settings = false }.campWindowBehavior() }
    }
    private func notificationRow(_ notice: CampNotice, provider: String?) -> some View {
        return Button { notices.pendingOpen = notice.id } label: {
            HStack(alignment: .top, spacing: 10) {
                Group {
                    if notice.kind == "attention" { Image(systemName: "exclamationmark.circle") }
                    else if notice.kind == "plan" { Image(systemName: "lightbulb") }
                    else if let provider { ProviderMark(provider: provider, size: 14) }
                    else { Image(systemName: "terminal") }
                }.foregroundStyle(campGreen).frame(width: 29, height: 29)
                    .background(Color(hex: 0xE3E9DC), in: RoundedRectangle(cornerRadius: 7))
                VStack(alignment: .leading, spacing: 5) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(notice.summary).font(.system(size: 12, weight: .semibold))
                        Spacer(minLength: 4)
                        Text(notice.created.formatted(date: Calendar.current.isDateInToday(notice.created) ? .omitted : .abbreviated, time: .shortened))
                            .font(.system(size: 10)).foregroundStyle(campMuted).fixedSize()
                    }
                    Text(notice.title).font(.system(size: 12)).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                    Text(detail(notice, provider: provider)).font(.system(size: 10)).foregroundStyle(campMuted)
                }
                Circle().fill(notice.read ? .clear : campGreen).frame(width: 6, height: 6).padding(.top, 8)
            }.padding(.horizontal, 8).padding(.vertical, 13).frame(maxWidth: .infinity, minHeight: 95, alignment: .topLeading)
                .background(notice.read ? Color.clear : Color(hex: 0xEDF0E7), in: RoundedRectangle(cornerRadius: 7))
                .overlay(alignment: .bottom) { Rectangle().fill(campInk.opacity(0.08)).frame(height: 1) }
                .contentShape(Rectangle())
        }.buttonStyle(.plain).focusable().accessibilityValue(notice.read ? "Read" : "Unread")
            .accessibilityIdentifier("camp-notification-\(notice.id)")
            .onKeyPress(keys: [.space, .return]) { _ in notices.pendingOpen = notice.id; return .handled }
            .contextMenu { Button("Mark read") { Task { await notices.markRead(notice.id) } }.disabled(notice.read) }
    }
    private func detail(_ notice: CampNotice, provider: String?) -> String {
        switch notice.kind {
        case "attention": return "Open the session to review"
        case "plan": return "Planning · Ready for your review"
        default: return provider.map { $0 + " · Response ready" } ?? "Response ready"
        }
    }
}

private struct CampNotificationSettings: View {
    @ObservedObject var notices: CampNotifications
    let close: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack {
                Text("Notification settings").font(.system(size: 20, weight: .medium))
                Spacer()
                Button(action: close) { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).help("Close notification settings").accessibilityLabel("Close notification settings").keyboardShortcut(.cancelAction)
            }
            VStack(spacing: 5) {
                preference("In-app toasts", detail: "A quiet banner when work finishes while Camp is open", binding: Binding(get: { notices.preferences.toasts }, set: { value in notices.updatePreferences { $0.toasts = value } }))
                preference("Desktop notifications", detail: "Show a macOS banner when Camp is in the background", binding: Binding(get: { notices.preferences.desktop }, set: { value in Task { await notices.enableDesktop(value) } })).disabled(notices.permissionBusy)
                preference("Task names in desktop previews", detail: "Off keeps task titles private outside Camp", binding: Binding(get: { notices.preferences.titles }, set: { value in notices.updatePreferences { $0.titles = value } })).disabled(!notices.preferences.desktop)
                preference("Play a sound", detail: "Use the sound allowed by your macOS notification settings", binding: Binding(get: { notices.preferences.sound }, set: { value in notices.updatePreferences { $0.sound = value } })).disabled(!notices.preferences.desktop)
            }
            Text("Every completion stays in Notifications, even with banners turned off").font(.system(size: 11)).foregroundStyle(campMuted)
            if !notices.error.isEmpty {
                Text(notices.error).font(.system(size: 11)).foregroundStyle(.orange)
                Button("Open System Settings") { NSWorkspace.shared.open(URL(fileURLWithPath: "/System/Applications/System Settings.app")) }
            }
            HStack { Button("Back", action: close); Spacer() }
        }.padding(24).frame(width: 530).background(campCream).foregroundStyle(campInk).tint(campGreen)
            .onExitCommand(perform: close)
    }
    private func preference(_ title: String, detail: String, binding: Binding<Bool>) -> some View {
        HStack(spacing: 20) {
            VStack(alignment: .leading, spacing: 5) {
                Text(title).font(.system(size: 13, weight: .medium))
                Text(detail).font(.system(size: 11)).foregroundStyle(campMuted)
            }
            Spacer(minLength: 0)
            Toggle(title, isOn: binding).labelsHidden().toggleStyle(.checkbox).accessibilityLabel(title)
        }.padding(.vertical, 12).frame(maxWidth: .infinity, alignment: .leading)
            .overlay(alignment: .bottom) { Rectangle().fill(campInk.opacity(0.08)).frame(height: 1) }
    }
}
