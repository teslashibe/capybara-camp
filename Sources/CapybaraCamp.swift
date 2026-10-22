import SwiftUI
import AppKit
import Darwin

struct CampSession: Codable, Identifiable, Equatable, Sendable {
    let id: String
    let sessionID: String
    let provider: String
    let sourceID: String
    let sourceLabel: String
    let root: String
    var title: String
    let cwd: String
    let updated: Double
    let path: String
    let accountID: String
    let archived: Bool
    var model: String = ""
    var effort: String? = nil
    var speed: String? = nil
    var project: String { URL(fileURLWithPath: cwd).lastPathComponent }
    var isSharedPlan: Bool { id.hasPrefix("camp-plan:") || id.hasPrefix("camp-plan-draft:") }
    var isCloud: Bool { (id.hasPrefix("camp:") || isSharedPlan) && root.isEmpty }
    var isPlan: Bool { id.hasPrefix("plan:") || isSharedPlan }
}
struct CampSource: Codable, Identifiable, Equatable {
    let id: String
    let provider: String
    let root: String
    let label: String
    let count: Int
}
struct ImportResult: Codable { let sessions: [CampSession]; let sources: [CampSource]; let errors: [String]; var failedRoots: [String]? = nil }
struct ChildSession: Codable, Identifiable, Equatable {
    let id: String
    let sessionID: String
    let name: String
    let phase: String
    let workflow: String
    let status: String
    let model: String
    let path: String
    let updated: Double
}
struct LiveState: Codable, Equatable {
    let id: String
    let sourceID: String
    var state: String
    let model: String
    let children: [ChildSession]
    let owner: String
    let pid: Int
    var effort: String? = nil
    var speed: String? = nil
}
struct ReportedModelSettings: Equatable {
    var model: String? = nil
    var effort: String? = nil
    var speed: String? = nil
}
struct ModelOption: Codable, Identifiable, Equatable {
    let id: String
    let label: String
    var efforts: [String] = []
    var speeds: [String] = ["standard"]
    var defaultEffort: String = ""
    var showInPicker = true
}
struct ModelTuning: Codable, Equatable {
    var effort = ""
    var speed = "standard"
}
struct ActivityResult: Codable {
    let activity: [LiveState]
    let sessions: [CampSession]
    let models: [String: [ModelOption]]
    let errors: [String: String]
}
struct ChildrenResult: Codable { let children: [ChildSession] }
struct Message: Codable, Identifiable, Equatable, Sendable {
    let id: String
    let role: String
    let text: String
    var model: String? = nil
    var effort: String? = nil
    var speed: String? = nil
    // Context provenance covers exactly the text supplied to the model.
    var contextMessage: Message { Message(id: id, role: role, text: text) }
    var settingsDescription: String? {
        guard role == "assistant" else { return nil }
        var parts: [String] = []
        if let model, !model.isEmpty, model.utf8.count <= 200, !model.contains(where: { $0.isNewline || $0.asciiValue == 0 }) { parts.append(model) }
        if let effort, ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"].contains(effort) { parts.append(ModelMenu.effortName(effort) + " effort") }
        if let speed, ["standard", "fast", "ultrafast"].contains(speed) { parts.append(ModelMenu.speedName(speed)) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
struct QueuedPrompt: Codable, Identifiable {
    let id: String
    let sessionID: String
    let text: String
    let model: String
    let effort: String
    let speed: String
    var state = "waiting"
    var sources: [SessionReference]? = nil
    var permissionRevision: String? = nil
}
struct Transcript: Codable { let messages: [Message]; let total: Int }
enum Stage: String, CaseIterable, Codable {
    case ready = "Ready", progress = "In progress", attention = "Needs you", done = "Done"
    var color: Color {
        switch self { case .ready: return Color(hex: 0x91856F); case .progress: return Color(hex: 0x62816B); case .attention: return Color(hex: 0xC18549); case .done: return Color(hex: 0x77969C) }
    }
}
private struct SessionStateSnapshot {
    let running: Set<String>
    let waiting: Set<String>
    let activity: [String: LiveState]
    let remote: [String: CloudSession]
    let requests: Set<String>
    let stages: [String: Stage]
    func busy(_ session: CampSession) -> Bool {
        if running.contains(session.id) { return true }
        if let shared = remote[session.id] { return shared.state == "running" || requests.contains(shared.id) }
        return activity[session.id]?.state == "running"
    }
    func stage(_ session: CampSession) -> Stage {
        if waiting.contains(session.id) || remote[session.id]?.state == "waiting" { return .attention }
        if busy(session) { return .progress }
        if let live = activity[session.id], ["waiting", "unknown"].contains(live.state) { return .attention }
        return stages[session.id] ?? (session.archived ? .done : .ready)
    }
}
enum CampError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case .message(let value) = self { return value }; return nil }
}
let campCream = Color(hex: 0xF7F5EE)
let campInk = Color(hex: 0x30392F)
let campGreen = Color(hex: 0x4B6753)
let campMuted = Color(hex: 0x85887D)
extension Color {
    init(hex: UInt32) { self.init(red: Double((hex >> 16) & 255)/255, green: Double((hex >> 8) & 255)/255, blue: Double(hex & 255)/255) }
}

enum Bridge {
    static var activeProcesses: [UUID: Process] = [:]
    static let support: URL = {
        if let path = ProcessInfo.processInfo.environment["CAPY_SUPPORT"], path.hasPrefix("/") { return URL(fileURLWithPath: path) }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Capybara Camp")
    }()
    static func process(_ action: String) throws -> Process {
        guard ["discover", "scan", "transcript", "export-page", "add-source", "children", "activity", "permissions", "execution-context", "codex-activity-watch", "conversation"].contains(action) else {
            throw CampError.message("Unsupported local session action")
        }
        let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local")
        guard FileManager.default.isExecutableFile(atPath: helper.path) else { throw CampError.message("The bundled Go session helper is missing. Rebuild the app.") }
        let p = Process()
        p.executableURL = helper; p.arguments = [action]
        return p
    }
    static func call(_ action: String, input: Data = Data()) async throws -> Data {
        try await Task.detached(priority: .userInitiated) {
            let p = try process(action), out = Pipe(), err = Pipe(), stdin = Pipe()
            p.standardOutput = out; p.standardError = err; p.standardInput = stdin
            try p.run()
            let errors = Task.detached { err.fileHandleForReading.readDataToEndOfFile() }
            stdin.fileHandleForWriting.write(input); try? stdin.fileHandleForWriting.close()
            let data = out.fileHandleForReading.readDataToEndOfFile()
            p.waitUntilExit()
            let errorData = await errors.value
            guard p.terminationStatus == 0 else {
                let raw = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
                throw CampError.message(raw?["message"] as? String ?? String(data: errorData.suffix(4000), encoding: .utf8) ?? "Could not read the local session store")
            }
            return data
        }.value
    }
}

// Watch only the open transcript and its database/journal locations. Parent
// directories keep the watch alive when a CLI replaces a file or rotates a WAL.
final class TranscriptWatch {
    private var paths: [String] = []
    private var streams: [DispatchSourceFileSystemObject] = []
    private var delivery: DispatchWorkItem?
    private var changed: (() -> Void)?

    func observe(_ paths: [String], changed: @escaping () -> Void) {
        guard self.paths != paths else { return }
        stop()
        self.paths = paths; self.changed = changed
        install()
    }
    private func install() {
        streams.forEach { $0.cancel() }; streams = []
        for path in paths {
            let descriptor = Darwin.open(path, O_EVTONLY)
            guard descriptor >= 0 else { continue }
            let stream = DispatchSource.makeFileSystemObjectSource(fileDescriptor: descriptor,
                eventMask: [.write, .extend, .rename, .delete, .attrib, .revoke], queue: .main)
            stream.setEventHandler { [weak self] in self?.schedule() }
            stream.setCancelHandler { Darwin.close(descriptor) }
            streams.append(stream); stream.resume()
        }
    }
    private func schedule() {
        guard delivery == nil else { return }
        let item = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.delivery = nil
            self.install()
            self.changed?()
        }
        delivery = item
        // Coalesce one burst of filesystem writes, without a polling interval.
        DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(30), execute: item)
    }
    func stop() {
        delivery?.cancel(); delivery = nil
        streams.forEach { $0.cancel() }; streams = []
        paths = []; changed = nil
    }
    deinit { stop() }
}

@MainActor final class CampStore: ObservableObject {
    let onboarding: CampOnboarding
    let cloud: CampConnection
    let sharing: CampSharing
    let mentions: SessionMentions
    let planning: LocalPlanning
    let notifications: CampNotifications
    let sharedPlanning: CampPlanning
    @Published var notificationSheet = false
    @Published var notificationPlanVersion: Int?
    @Published var notificationMessageID: String?
    @Published var sharedPlanResult: CloudPlanningResult?
    var sharedPlanScope: CloudScope?
    var openingNotification = false
    var cloudRestoreFinished = false
    var hasImportedSessions = false
    private var externalRuns: [String: String] = [:]
    private var backgroundPoll: Task<Void, Never>?
    @Published var shareSheet: CampSession?
    @Published var executionSheet: CampSession?
    @Published var contextInspection: ContextInspection?
    @Published var reviewingPlan: String?
    @Published var planNavigation: PlanNavigationRequest?
    @Published var selectedChildID: String?
    var selectedChild: ChildSession? { children.first { $0.id == selectedChildID } }
    var implementationTranscriptReader: ((CampSession, Int) async throws -> Transcript)?
    @Published var cloudSessions: [String: CloudSession] = [:] { didSet { filterRevision += 1 } }
    @Published var recentType = "All types"
    @Published var remoteHasMore = false
    private var cloudHistory: [String: [CloudMessage]] = [:]
    private var cloudHistoryBefore: [String: Int64] = [:]
    private var cloudHistoryRevision: [String: Int64] = [:]
    @Published var sessions: [CampSession] = [] { didSet { filterRevision += 1 } }
    @Published var sources: [CampSource] = []
    @Published var stages: [String: Stage] = [:] { didSet { filterRevision += 1 } }
    @Published var selectedID: String?
    @Published var sourceFilter = "all"
    @Published var search = ""
    @Published var view = "Board"
    @Published var importing = false
    @Published var loading = false
    @Published var messages: [Message] = []
    @Published var historyTotal = 0
    @Published var draft = ""
    @Published var preparingPrompts: Set<String> = []
    @Published var conversations: [String: LocalConversation] = [:] { didSet { filterRevision += 1 } }
    var liveText: String {
        if selected?.isCloud == true { return cloud.live.messages.last?.text ?? "" }
        return selected.flatMap { conversation($0)?.text } ?? ""
    }
    var runningID: String? { selected.flatMap { conversation($0) == nil ? nil : $0.id } }
    @Published var queuedPrompts: [QueuedPrompt] = []
    @Published private var enabledQueues: Set<String> = []
    var queuePaused: Bool {
        get { selectedID.map { !enabledQueues.contains($0) } ?? true }
        set { guard let selectedID else { return }; pauseQueue(selectedID, newValue) }
    }
    func controlsCloudTurn(_ session: CampSession) -> Bool {
        guard let remote = cloudSessions[session.id], remote.executionLocation == "cloud",
              remote.ownerUserId == cloud.credential?.actor.userId, cloud.live.sessionID == remote.id else { return false }
        return cloud.live.head?.running == true
    }
    func chatRunning(_ session: CampSession) -> Bool { conversation(session) != nil || controlsCloudTurn(session) }
    var readyToSteer: Bool {
        guard let selected else { return false }
        if controlsCloudTurn(selected), let head = cloud.live.head {
            return head.state == "started" && cloud.requests.first(where:{$0.id == head.id})?.isPlanning != true
        }
        return conversation(selected)?.ready ?? false
    }
    var steering: Bool {
        guard let selected else { return false }
        if controlsCloudTurn(selected), let head = cloud.live.head { return cloud.pendingControl(requestID:head.id,kind:"steer") }
        return conversation(selected)?.steering ?? false
    }
    @Published var banner = ""
    @Published var sourceSheet = false
    @Published var sourceError = ""
    @Published var projectSheet = false
    @Published var permissionSheet: CampSession?
    let sessionPermissions: LocalPermissions
    @Published var projectFilter = "all" { didSet { filterRevision += 1 } }
    let projects: LocalProjects
    private(set) var cloudProjectIndex: [String:CampProject] = [:]
    @Published var activity: [String: LiveState] = [:] { didSet { filterRevision += 1 } }
    @Published var modelCatalogs: [String: [ModelOption]] = [:]
    @Published var modelChoices: [String: String] = [:]
    @Published var modelTuning: [String: [String: ModelTuning]] = [:]
    @Published var activityError = ""
    @Published var importError = ""
    @Published var subagentsParent: CampSession?
    @Published var children: [ChildSession] = []
    @Published var childrenLoading = false
    private var observing = false
    private let codexWatch = CodexActivityWatch()
    private var polledActivity = ActivityResult(activity: [], sessions: [], models: [:], errors: [:])
    private var codexActivity = CodexActivityUpdate(activity: [], sessions: [], errors: [:])
    private var scanning = false
    private var refreshPending = false
    private var nextImportCheck = Date.distantPast
    private var historyRequestID: String?
    private let transcriptWatch = TranscriptWatch()
    private var historyDirty = false
    private var historyLimit = 120
    private var readGeneration = UUID()
    private var drafts: [String: String] = [:]
    private var filterRevision = 0
    private var cachedFilterRevision = -1
    private var cachedFilterSearch = ""
    private var cachedFilterSource = ""
    private var cachedFiltered: [CampSession] = []
    private var cachedVisibleRevision = -1
    private var cachedVisible: [CampSession] = []
    private var cachedCountRevision = -1
    private var cachedCounts: [String: Int] = [:]
    private var sharingSelection: Set<String> = []
    private var activeRequestSessions: Set<String> = []
    private var cachedBoardRevision = -1
    private var cachedBoardSearch = ""
    private var cachedBoardSource = ""
    private var cachedBoard: [Stage: [CampSession]] = [:]
    private var cachedRecentRevision = -1
    private var cachedRecentType = ""
    private var cachedRecent: [CampSession] = []
    private let stateDirectory: URL
    private let conversationProcess: () throws -> Process
    private let decoder = JSONDecoder()
    var executionContextReader: ((CampSession) async throws -> ExecutionContext)?
    var selected: CampSession? { sessions.first { $0.id == selectedID } }
    var visibleSessions: [CampSession] {
        if cachedVisibleRevision == filterRevision { return cachedVisible }
        let locals = Set(sharing.shares.filter { ($0.shared || $0.syncToCloud == true && cloudSessions["camp:" + $0.id]?.executionLocation == "cloud") && cloudSessions["camp:" + $0.id] != nil }.map { $0.local.id })
        let localIDs = Set(sessions.filter { !$0.isCloud }.map(\.id))
        let privateMirrors = Set(sharing.shares.filter { !$0.shared && !($0.syncToCloud == true && cloudSessions["camp:" + $0.id]?.executionLocation == "cloud") && localIDs.contains($0.local.id) }.map { "camp:" + $0.id })
        let implementations = Set(planning.tickets.compactMap(\.implementation).filter(\.providerSessionCreated).map { $0.executor.session.provider + "\0" + $0.context.profile + "\0" + $0.providerSessionID })
        cachedVisible = sessions.filter { s in
            !locals.contains(s.id) && !privateMirrors.contains(s.id) && (s.isPlan || !implementations.contains(s.provider + "\0" + s.root + "\0" + s.sessionID))
        }
        cachedVisibleRevision = filterRevision; return cachedVisible
    }
    func sessionCount(_ kind: String) -> Int {
        if cachedCountRevision != filterRevision {
            let state = stateSnapshot(), visible = visibleSessions
            var counts = ["all": visible.count, "active": 0, "Codex": 0, "Claude": 0]
            for session in visible {
                counts[session.provider, default: 0] += 1
                if state.busy(session) { counts["active", default: 0] += 1 }
            }
            cachedCounts = counts; cachedCountRevision = filterRevision
        }
        return cachedCounts[kind, default: 0]
    }
    var filtered: [CampSession] {
        if cachedFilterRevision == filterRevision && cachedFilterSearch == search && cachedFilterSource == sourceFilter { return cachedFiltered }
        let state = stateSnapshot()
        cachedFiltered = visibleSessions.filter { s in
            (sourceFilter == "all" || (sourceFilter == "active" && state.busy(s)) || s.sourceID == sourceFilter || s.provider == sourceFilter) &&
            projectMatches(s) && (search.isEmpty || (s.title + " " + s.cwd + " " + s.sourceLabel + " " + projectLabel(s)).localizedCaseInsensitiveContains(search))
        }
        cachedFilterRevision = filterRevision; cachedFilterSearch = search; cachedFilterSource = sourceFilter
        return cachedFiltered
    }
    private func stateSnapshot() -> SessionStateSnapshot {
        var running = Set(conversations.keys)
        var waiting = Set(conversations.filter { !$0.value.approvals.isEmpty }.keys)
        running.formUnion(planning.tickets.filter { $0.stage == .progress }.map(\.id))
        running.formUnion(sharedPlanning.tickets.filter { $0.stage == .progress }.map { "camp-plan:" + $0.id })
        var observed = activity
        for plan in planning.tickets { if let state = activityFor(plan.session) { observed[plan.id] = state } }
        for share in sharing.shares where running.contains(share.local.id) { running.insert("camp:" + share.id) }
        for share in sharing.shares where waiting.contains(share.local.id) { waiting.insert("camp:" + share.id) }
        return SessionStateSnapshot(running: running, waiting: waiting, activity: observed, remote: cloudSessions,
            requests: Set(cloud.requests.filter { !$0.isPlanning && ["claimed", "started", "cancel_requested"].contains($0.state) }.map(\.sessionId)), stages: stages)
    }
    func boardSessions(_ stage: Stage) -> [CampSession] {
        if cachedBoardRevision != filterRevision || cachedBoardSearch != search || cachedBoardSource != sourceFilter {
            let state = stateSnapshot()
            var grouped: [Stage: [CampSession]] = [:]
            for session in filtered { grouped[state.stage(session), default: []].append(session) }
            cachedBoard = grouped; cachedBoardRevision = filterRevision; cachedBoardSearch = search; cachedBoardSource = sourceFilter
        }
        return cachedBoard[stage] ?? []
    }
    init(stateDirectory: URL = Bridge.support, conversationProcess: @escaping () throws -> Process = { try Bridge.process("conversation") }) {
        onboarding = CampOnboarding(directory: stateDirectory)
        self.stateDirectory = stateDirectory
        self.mentions = SessionMentions(directory: stateDirectory)
        self.planning = LocalPlanning(directory: stateDirectory, makeProcess: conversationProcess)
        self.notifications = CampNotifications(directory: stateDirectory)
        self.sessionPermissions = LocalPermissions(directory: stateDirectory)
        self.conversationProcess = conversationProcess
        #if CAMP_DEVELOPMENT
        cloud = CampConnection(directory: stateDirectory.appendingPathComponent("cloud"), vault: ProcessInfo.processInfo.environment["CAMP_DEV_EPHEMERAL_CREDENTIALS"] == "1" ? MemoryCampCredentials() : CampKeychain(), allowLoopback: true)
        #elseif CAMP_TESTING
        cloud = CampConnection(directory: stateDirectory.appendingPathComponent("cloud"), vault: MemoryCampCredentials(), allowLoopback: true)
        #else
        cloud = CampConnection(directory: stateDirectory.appendingPathComponent("cloud"))
        #endif
        projects = LocalProjects(cache: cloud.cache)
        sharing = CampSharing(cache: cloud.cache)
        sharedPlanning = CampPlanning(cache: cloud.cache)
        projects.changed = { [weak self] in self?.projectsChanged() }
        sharedPlanning.changed = { [weak self] in self?.syncSharedPlans() }
        sharedPlanning.created = { [weak self] draft, id in
            guard let self, self.selectedID == "camp-plan-draft:" + draft else { return }
            self.openSharedPlan(id)
        }
        notifications.seedPlans(planning.tickets)
        notifications.openRequested = { [weak self] in Task { await self?.openPendingNotification() } }
        try? FileManager.default.createDirectory(at: stateDirectory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        if let index = LocalSessionIndex.read(directory: stateDirectory) {
            sessions = index.sessions; sources = index.sources; selectedID = sessions.first?.id
        }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("model-tuning.json")), let saved = try? decoder.decode([String: [String: ModelTuning]].self, from: data) { modelTuning = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("models.json")), let saved = try? decoder.decode([String: String].self, from: data) { modelChoices = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("board.json")), let saved = try? decoder.decode([String: Stage].self, from: data) { stages = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("recent-filter.json")), let saved = try? decoder.decode(String.self, from: data), ["All types", "Planning", "Codex", "Claude"].contains(saved) { recentType = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("queued-prompts.json")), let saved = try? decoder.decode([QueuedPrompt].self, from: data) {
            queuedPrompts = saved.map { item in var item = item; if ["sending", "running"].contains(item.state) { item.state = "needsReview" }; return item }
            saveQueue()
        }
        sharing.localSession = { [weak self] id in self?.sessions.first { $0.id == id && !$0.isCloud } }
        sharing.localStatus = { [weak self] local in
            guard let self else { return LocalShareStatus(model: local.model, state: "unknown", stage: "Ready", externalOwner: false) }
            let live = self.activity[local.id]
            let run = self.conversations[local.id]
            return LocalShareStatus(model: self.effectiveModel(local), state: run != nil ? (run!.approvals.isEmpty ? "running" : "waiting") : live?.state ?? "idle", stage: self.stage(local).rawValue, externalOwner: live != nil)
        }
        sharing.changed = { [weak self] in
            guard let self else { return }
            let selection = Set(self.sharing.shares.map { $0.id + ":" + $0.local.id + ":" + String($0.shared) })
            if self.sharingSelection != selection {
                self.sharingSelection = selection; self.filterRevision += 1; self.objectWillChange.send()
                self.cloud.worker.scan()
            }
        }
        sharing.published = { [weak cloud] value, scope, epoch in await cloud?.acceptPublishedSession(value, scope: scope, epoch: epoch) }
        cloud.worker.nativeSession = { [weak sharing] remote in sharing?.native(for: "camp:" + remote.id) }
        cloud.worker.available = { [weak self] local in
            guard let self else { return false }
            return self.activity[local.id] == nil && self.conversations.count + self.planning.activeCount + self.cloud.worker.activePlanningCount < 4 && !self.planning.uses(local.provider) && !self.conversations.values.contains { $0.session.provider == local.provider && $0.session.root == local.root }
        }
        cloud.worker.execute = { [weak self] local, request, prompt, history, contexts, completed in
            guard let self else { return nil }
            if request.isPlanning {
                let run = LocalConversation(session: local, history: [], prompt: prompt)
                run.completed = completed
                do {
                    try run.start(prompt: prompt, model: request.model, tuning: ModelTuning(effort: request.effort, speed: request.speed), contexts: contexts, mode: .planning, makeProcess: self.conversationProcess)
                    return run
                } catch { return nil }
            }
            let references = request.sources.map { SessionReference(id: "camp:" + $0.sessionId, scope: self.cloud.scope?.key, cloud: $0) }
            guard self.start(local, prompt: prompt, model: request.model, tuning: ModelTuning(effort: request.effort, speed: request.speed), history: history, contexts: contexts, references: references, notifyCompletion: false, completion: completed) else { return nil }
            return self.conversations[local.id]
        }
        cloud.worker.requestUpdated = { [weak cloud] value in Task { await cloud?.acceptWorkerRequest(value) } }
        codexWatch.changed = { [weak self] value in
            self?.applyCodexActivity(value)
        }
        cloud.requestChanged = { [weak self] value in
            self?.cloud.worker.changed(value)
            if value.isPlanning { self?.sharedPlanning.refresh() }
        }
        cloud.notificationsChanged = { [weak self] values, scope, live in self?.notifications.applyCloud(values, scope: scope, live: live) }
        cloud.planningChanged = { [weak self] id in
            self?.cloud.worker.scan()
            self?.sharedPlanning.refresh()
        }
        notifications.cloudAction = { [weak cloud] scope, ids, action in
            guard let cloud else { throw CampCloudError.cacheUnavailable }
            try await cloud.queueNotificationAction(scope: scope, ids: ids, action: action)
        }
        cloud.sessionsChanged = { [weak self] sessions, projects, scope in
            guard let self else { return }
            if scope == nil, let previous = self.notifications.scope { self.notifications.prune(scope: previous, allowed: []) }
            self.notifications.setScope(scope)
            self.applyCloud(sessions, projects: projects)
            if let result = self.sharedPlanResult,
               self.sharedPlanScope != scope || !result.referencedSessions.isSubset(of: Set(sessions.map(\.id))) {
                self.sharedPlanResult = nil; self.view = "Board"
            }
            if let scope {
                let plans = self.notifications.entries.filter { $0.scope == scope && $0.cloudPlanID != nil }.map(\.sessionID)
                self.notifications.prune(scope: scope, allowed: Set(sessions.map { "camp:" + $0.id } + plans))
            }
            if let scope, let actor = self.cloud.credential?.actor, let client = self.cloud.client, let token = self.cloud.credential?.token {
                let transport = AuthenticatedCampPublisher(client: client, token: token)
                self.sharing.attach(scope: scope, actor: actor, transport: transport)
                self.cloud.worker.attach(scope: scope, actor: actor, transport: transport, sessions: sessions)
                self.sharedPlanning.attach(scope: scope, actor: actor, transport: transport, sessions: sessions)
            } else {
                self.sharing.attach(scope: nil, actor: nil, transport: nil)
                self.cloud.worker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
                self.sharedPlanning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
            }
        }
        cloud.transcriptChanged = { [weak self] id in
            guard let self, self.selectedID == "camp:" + id else { return }
            self.loadHistory(silent: true)
        }
        cloud.execution.changed = { [weak self] id in Task {
            guard let self else{return}
            await self.cloud.refreshSession(id)
            guard self.view == "Chat",let local=self.sharing.shares.first(where:{$0.id==id})?.local,
                  self.selectedID==local.id || self.selectedID=="camp:"+id else{return}
            if let remote=self.sessions.first(where:{$0.id=="camp:"+id}),self.cloudSessions[remote.id]?.executionLocation=="cloud" {self.open(remote)}
        } }
        cloud.live.changed = { [weak self] in
            guard let self, self.selectedID == self.cloud.live.sessionID.map({ "camp:" + $0 }) else { return }
            self.objectWillChange.send()
        }
        cloud.controlsChanged = { [weak self] in self?.objectWillChange.send() }
        planning.available = { [weak self] planner in
            guard let self else { return false }
            return !self.cloud.worker.usesPlanningProvider(planner.session.provider) && self.conversations.count + self.planning.activeCount + self.cloud.worker.activePlanningCount < 4 &&
                !self.conversations.values.contains { $0.session.provider == planner.session.provider && $0.session.root == planner.session.root }
        }
        planning.prepare = { [weak self] planner, references in
            guard let self, let current = self.sessions.first(where: { $0.id == planner.session.id }),
                  current.root == planner.session.root, current.sourceID == planner.session.sourceID else {
                throw CampError.message("This planner profile is no longer imported")
            }
            let caps = self.availableModels(current).first { $0.id == planner.model }
            if let caps {
                guard (planner.tuning.effort.isEmpty || caps.efforts.contains(planner.tuning.effort)), caps.speeds.contains(planner.tuning.speed) else {
                    throw CampError.message("The selected planner settings are no longer supported")
                }
            } else if !planner.tuning.effort.isEmpty || planner.tuning.speed != "standard" {
                throw CampError.message("Refresh the planner model's available settings before starting")
            }
            try self.mentions.record(references)
            return try await self.resolveContexts(references, destination: current)
        }
        planning.changed = { [weak self] structural in
            guard let self else { return }
            if structural { self.syncPlans() }
            if structural { self.notifications.observePlans(self.planning.tickets) }
        }
        syncPlans()
    }
    private func syncPlans() {
        guard !planning.tickets.isEmpty else { return }
        let plans = planning.tickets.map(\.session)
        sessions = (plans + sessions.filter { !$0.isPlan || $0.isSharedPlan }).sorted { $0.updated > $1.updated }
        for plan in planning.tickets { stages[plan.id] = plan.stage }
    }
    func projectsChanged() { filterRevision += 1; objectWillChange.send() }
    var recentSessions: [CampSession] {
        if cachedRecentRevision == filterRevision && cachedRecentType == recentType { return cachedRecent }
        let type = recentType
        cachedRecent = Array(visibleSessions.lazy.filter { type == "All types" || $0.provider == type }.sorted { $0.updated > $1.updated }.prefix(10))
        cachedRecentRevision = filterRevision; cachedRecentType = recentType
        return cachedRecent
    }
    func setRecentType(_ value: String) {
        guard ["All types", "Planning", "Codex", "Claude"].contains(value) else { return }
        recentType = value
        if let data = try? JSONEncoder().encode(value) { try? data.write(to: stateDirectory.appendingPathComponent("recent-filter.json"), options: .atomic) }
    }
    func applyCloud(_ values: [CloudSession], projects: [CloudProject]) {
        let newProjects=Dictionary(uniqueKeysWithValues:projects.map { ($0.id,$0) })
        if cloudProjectIndex != newProjects { cloudProjectIndex=newProjects; projectsChanged() }
        let previous = cloudSessions
        let next = Dictionary(uniqueKeysWithValues: values.map { ("camp:" + $0.id, $0) })
        if cloudSessions != next { cloudSessions = next }
        let active = Set(cloud.requests.filter { !$0.isPlanning && ["claimed", "started", "cancel_requested"].contains($0.state) }.map(\.sessionId))
        if activeRequestSessions != active { activeRequestSessions = active; filterRevision += 1; objectWillChange.send() }
        let names = Dictionary(uniqueKeysWithValues: projects.map { ($0.id, $0.name) })
        let imported = values.map { value in
            CampSession(id: "camp:" + value.id, sessionID: value.localSessionId, provider: value.provider, sourceID: "camp-profile:" + value.profileId, sourceLabel: value.profileLabel, root: "", title: value.title, cwd: names[value.projectId] ?? "Shared project", updated: value.updatedAt.timeIntervalSince1970, path: "", accountID: value.ownerUserId, archived: value.stage == "Done", model: value.model)
        }
        for (id, value) in cloudSessions where previous[id]?.stage != value.stage || value.revision > (previous[id]?.revision ?? 0) { stages[id] = Stage(rawValue: value.stage) ?? .ready }
        let updatedSessions = (sessions.filter { !$0.isCloud || $0.isSharedPlan } + imported).sorted { $0.updated > $1.updated }
        if sessions != updatedSessions { sessions = updatedSessions }
        for run in conversations.values where run.references.contains(where: { reference in
            guard let source = reference.cloud else { return false }
            return reference.scope != cloud.scope?.key || next[reference.id]?.permissionRevision != source.permissionRevision
        }) { run.stop() }
        checkPlanningSources()
        if let selectedID, let binding = sharing.shares.first(where: { "camp:" + $0.id == selectedID && !$0.shared }),
           let remote = cloudSessions[selectedID], !remote.shared, remote.executionLocation != "cloud", sessions.contains(where: { $0.id == binding.local.id }) {
            self.selectedID = binding.local.id; readGeneration = UUID()
            if let run = conversations[binding.local.id] { messages = run.messages; banner = run.notice }
            else { messages = []; loadHistory() }
        }
        if let selectedID, selectedID.hasPrefix("camp:"), cloudSessions[selectedID] == nil {
            self.selectedID = nil; messages = []; cloudHistory = [:]; readGeneration = UUID()
        }
    }
    func isBusy(_ s: CampSession) -> Bool {
        stateSnapshot().busy(s)
    }
    func activityFor(_ s: CampSession) -> LiveState? {
        if let direct = activity[s.id] { return direct }
        guard s.isPlan, let execution = planning.ticket(s.id)?.implementation, execution.providerSessionCreated else { return nil }
        return activity[execution.executor.session.sourceID + ":" + execution.providerSessionID]
    }
    func stage(_ session: CampSession) -> Stage {
        if stateSnapshot().waiting.contains(session.id) || cloudSessions[session.id]?.state == "waiting" { return .attention }
        if stateSnapshot().busy(session) { return .progress }
        if let plan = planning.ticket(session.id) { return plan.stage }
        return stateSnapshot().stage(session)
    }
    func modelLabel(_ s: CampSession) -> String { modelChoices[s.id] ?? (s.model.isEmpty ? "Session default" : s.model) }
    func reportedSettings(_ s: CampSession) -> ReportedModelSettings? {
        if let run = conversation(s) { return run.reportedSettings }
        if let remote = cloudSessions[s.id], cloud.live.sessionID == remote.id, let head = cloud.live.head, head.running {
            return ReportedModelSettings(model:head.model.isEmpty ? nil : head.model,effort:head.effort.isEmpty ? nil : head.effort,speed:head.speed.isEmpty ? nil : head.speed)
        }
        let native = sharing.native(for: s.id) ?? s
        guard let live = activityFor(native), ["running", "waiting"].contains(live.state) else { return nil }
        return ReportedModelSettings(model: live.model.isEmpty ? nil : live.model, effort: live.effort, speed: live.speed)
    }
    func selectModel(_ s: CampSession, _ model: String) {
        guard conversation(s) == nil else { return }
        modelChoices[s.id] = model.isEmpty ? nil : model
        do {
            let url = stateDirectory.appendingPathComponent("models.json")
            try JSONEncoder().encode(modelChoices).write(to: url, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        } catch { banner = "Could not save the model choice: " + error.localizedDescription }
    }
    func effectiveModel(_ s: CampSession) -> String { modelChoices[s.id] ?? s.model }
    func capabilities(_ s: CampSession) -> ModelOption {
        let model = effectiveModel(s)
        return availableModels(s).first { $0.id == model } ?? ModelOption(id: model, label: model)
    }
    func availableModels(_ s: CampSession) -> [ModelOption] {
        let source = sharing.native(for: s.id)?.sourceID ?? s.sourceID
        return modelCatalogs[source] ?? []
    }
    func shareSource(_ s: CampSession) -> CampSession? { s.isPlan ? nil : s.isCloud ? sharing.native(for: s.id) : s }
    private func checkPlanningSources() {
        planning.sourcesChanged { references in
            references.allSatisfy { ref in
                if let source = ref.cloud { return ref.scope == self.cloud.scope?.key && self.cloudSessions[ref.id]?.permissionRevision == source.permissionRevision }
                return self.sessions.contains { $0.id == ref.id && !$0.isCloud }
            }
        }
        for run in conversations.values where !run.references.allSatisfy({ reference in
            if let source = reference.cloud { return reference.scope == cloud.scope?.key && cloudSessions[reference.id]?.permissionRevision == source.permissionRevision }
            return sessions.contains { $0.id == reference.id && !$0.isCloud }
        }) { run.stop() }
    }
    func tuning(_ s: CampSession) -> ModelTuning { modelTuning[s.id]?[effectiveModel(s)] ?? ModelTuning() }
    func tune(_ s: CampSession, effort: String? = nil, speed: String? = nil) {
        guard conversation(s) == nil else { return }
        let caps = capabilities(s)
        if let effort, !effort.isEmpty, !caps.efforts.contains(effort) { return }
        if let speed, !caps.speeds.contains(speed) { return }
        var choice = tuning(s)
        if let effort { choice.effort = effort }
        if let speed { choice.speed = speed }
        modelTuning[s.id, default: [:]][effectiveModel(s)] = choice
        do {
            let url = stateDirectory.appendingPathComponent("model-tuning.json")
            try JSONEncoder().encode(modelTuning).write(to: url, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        } catch { banner = "Could not save model settings: " + error.localizedDescription }
    }
    func pollUpdates() {
        codexWatch.observe(sources)
        pollActivity()
        observeHistory()
        if Date() >= nextImportCheck { refresh(background: true) }
    }
    func stopRuntimeObservation() { backgroundPoll?.cancel(); backgroundPoll = nil; codexWatch.stop() }
    func startBackgroundObservation() {
        guard backgroundPoll == nil else { return }
        Task { [weak self] in await self?.restoreCloudAndNotification() }
        backgroundPoll = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
                guard let self else { return }; self.pollUpdates()
            }
        }
    }
    deinit { backgroundPoll?.cancel() }
    func observeHistory() {
        guard view == "Chat", let s = selected else { transcriptWatch.stop(); return }
        guard !s.isCloud, s.provider != "Planning" else { transcriptWatch.stop(); return }
        let source = executionSource(s) ?? s
        var paths = Set<String>()
        if !source.path.isEmpty {
            paths.insert(source.path)
            paths.insert(URL(fileURLWithPath: source.path).deletingLastPathComponent().path)
        }
        if source.provider == "Codex" {
            paths.insert(source.root)
            for name in ["thread_history_1.sqlite", "state_5.sqlite"] {
                paths.insert(source.root + "/" + name)
                paths.insert(source.root + "/" + name + "-wal")
            }
        }
        transcriptWatch.observe(paths.sorted()) { [weak self] in
            Task { @MainActor in self?.historyChanged() }
        }
    }
    private func historyChanged() {
        guard view == "Chat", runningID == nil else { return }
        if historyRequestID != nil { historyDirty = true }
        else { loadHistory(silent: true) }
    }
    func pollActivity() {
        guard !observing, !sources.isEmpty else { return }
        observing = true
        let homes = sources
        Task {
            defer { observing = false }
            do {
                let values = try JSONSerialization.jsonObject(with: JSONEncoder().encode(homes))
                let selectedExecution = selected.flatMap { planning.ticket($0.id)?.implementation }
                let focus = selectedExecution.map { $0.executor.session.sourceID + ":" + $0.providerSessionID } ?? selectedID.flatMap { sharing.native(for: $0)?.id } ?? selectedID ?? ""
                let input = try JSONSerialization.data(withJSONObject: ["sources": values, "selectedID": focus])
                let result = try decoder.decode(ActivityResult.self, from: await Bridge.call("activity", input: input))
                applyActivity(result)
            } catch {
                applyActivity(ActivityResult(activity: [], sessions: [], models: modelCatalogs,
                    errors: Dictionary(uniqueKeysWithValues: sources.filter { $0.provider == "Claude" }.map { ($0.id, "Live status unavailable: " + error.localizedDescription) })))
            }
        }
    }
    func applyActivity(_ result: ActivityResult) {
        polledActivity = result
        applyCombinedActivity()
    }
    func applyCodexActivity(_ result: CodexActivityUpdate) {
        codexActivity = result
        applyCombinedActivity()
    }
    private func applyCombinedActivity() {
        let allowed = Set(sources.map(\.id))
        let result = ActivityResult(
            activity: polledActivity.activity + codexActivity.activity.filter { allowed.contains($0.sourceID) },
            sessions: polledActivity.sessions + codexActivity.sessions.filter { allowed.contains($0.sourceID) },
            models: polledActivity.models,
            errors: polledActivity.errors.merging(codexActivity.errors.filter { allowed.contains($0.key) }) { _, new in new })
        var next = Dictionary(result.activity.map { ($0.id, $0) }, uniquingKeysWith: { first, second in second.state == "running" ? second : first })
        for (id, var old) in activity where result.errors[old.sourceID] != nil { old.state = "unknown"; next[id] = old }
        for (id, run) in conversations {
            next.removeValue(forKey: id)
            if planning.ticket(id)?.implementation != nil {
                next.removeValue(forKey: run.session.sourceID + ":" + (run.providerSessionID ?? run.session.sessionID))
            }
        }
        let previousActivity = activity
        for (id, live) in next where live.state == "running" && previousActivity[id]?.state != "running" { externalRuns[id] = UUID().uuidString }
        if activity != next { activity = next }
        for (id, previous) in previousActivity where previous.state == "running" && next[id]?.state != "running" {
            let ticket = planning.tickets.first { plan in
                guard let execution = plan.implementation else { return false }
                return execution.executor.session.sourceID + ":" + execution.providerSessionID == id
            }
            move(ticket?.id ?? id, .attention)
            if next[id]?.state == "idle", let run = externalRuns[id], let session = sessions.first(where: { $0.id == (ticket?.id ?? id) }) {
                notifications.record(key: "external:" + run, sessionID: session.id, title: session.title, kind: "finished")
            }
        }
        externalRuns = externalRuns.filter { next[$0.key]?.state == "running" }
        if modelCatalogs != result.models { modelCatalogs = result.models }
        let currentError = result.errors.values.sorted().joined(separator: " · ")
        if activityError != currentError { activityError = currentError }
        var indexes = Dictionary(uniqueKeysWithValues: sessions.enumerated().map { ($0.element.id, $0.offset) })
        for item in result.sessions {
            if let index = indexes[item.id] {
                if sessions[index].title != item.title || sessions[index].model != item.model || sessions[index].effort != item.effort || sessions[index].speed != item.speed {
                    sessions[index].title = item.title; sessions[index].model = item.model
                    sessions[index].effort = item.effort; sessions[index].speed = item.speed
                }
            }
            else { indexes[item.id] = sessions.count; sessions.append(item) }
        }
        if let selected = selected, let live = activityFor(sharing.native(for: selected.id) ?? selected) { children = live.children }
        if previousActivity != next { sharing.localChanged() }
        if Set(previousActivity.keys) != Set(next.keys) { cloud.worker.scan() }
        planning.drain()
    }
    func loadChildren(_ s: CampSession) {
        guard !s.isCloud else { banner = "Subagent transcripts have not been shared for this session"; return }
        subagentsParent = s; children = activity[s.id]?.children ?? []; childrenLoading = true
        if s.provider == "Codex", let live = activityFor(s) {
            children = live.children; childrenLoading = false; return
        }
        Task {
            defer { childrenLoading = false }
            do {
                let input = try JSONSerialization.data(withJSONObject: ["session": object(s), "live": activity[s.id]?.state == "running"])
                let result = try decoder.decode(ChildrenResult.self, from: await Bridge.call("children", input: input))
                if subagentsParent?.id == s.id { children = result.children }
            } catch { banner = error.localizedDescription }
        }
    }
    func move(_ id: String, _ stage: Stage) {
        guard let session = sessions.first(where: { $0.id == id }), !isBusy(session) else { return }
        if session.isPlan { planning.moveImplementation(id, stage: stage); return }
        if let remote = cloudSessions[id] {
            Task { do { try await cloud.queueStage(session: remote, stage: stage); stages[id] = stage }
                catch { banner = error.localizedDescription } }
            return
        }
        stages[id] = stage
        if let data = try? JSONEncoder().encode(stages) {
            let url = stateDirectory.appendingPathComponent("board.json")
            do {
                try data.write(to: url, options: .atomic)
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
            } catch { banner = "Could not save this board change: " + error.localizedDescription }
        }
    }
    func applyImport(_ result: ImportResult) {
        hasImportedSessions = true
        let failedRoots = Set(result.failedRoots ?? [])
        let importedIDs = Set(result.sessions.map(\.id))
        // A temporary read failure must not remove chats or interrupt the open one.
        let retained = sessions.filter { !$0.isCloud && !importedIDs.contains($0.id) &&
            ($0.isPlan || failedRoots.contains($0.root) || $0.id == selectedID || conversations[$0.id] != nil) }
        let updated = (result.sessions + retained + sessions.filter(\.isCloud)).sorted { $0.updated > $1.updated }
        let indexChanged = sessions != updated || sources != result.sources
        if sessions != updated { sessions = updated }
        var updatedSources = result.sources
        for old in sources where failedRoots.contains(old.root) {
            updatedSources.removeAll { $0.id == old.id }
            updatedSources.append(old)
        }
        if sources != updatedSources { sources = updatedSources }
        notifications.prune(scope: nil, allowed: Set(updated.filter { !$0.isCloud }.map(\.id)))
        if indexChanged { sharing.localChanged() }
        if indexChanged {
            let snapshot = ImportResult(sessions: updated.filter { !$0.isCloud && !$0.isPlan }, sources: updatedSources, errors: [])
            let directory = stateDirectory
            Task { await LocalSessionIndex.shared.save(snapshot, directory: directory) }
        }
        importError = result.errors.joined(separator: " · ")
        checkPlanningSources(); planning.drain()
        codexWatch.observe(sources)
        if selectedID == nil { selectedID = sessions.first?.id }
        observeHistory()
        if notifications.pendingOpen != nil { Task { await openPendingNotification() } }
    }
    func refresh(background: Bool = false) {
        guard !scanning else { if !background { refreshPending = true }; return }
        scanning = true
        nextImportCheck = Date().addingTimeInterval(30)
        if !background { importing = true }
        Task {
            defer {
                scanning = false; importing = false
                if refreshPending { refreshPending = false; refresh() }
            }
            do {
                if !hasImportedSessions {
                    // Show automatically found profiles without waiting for a
                    // large transcript library to finish its initial import.
                    if let data = try? await Bridge.call("discover"),
                       let discovery = try? decoder.decode(ProfileDiscovery.self, from: data) {
                        applyProfileDiscovery(discovery.sources)
                    }
                }
                // Large profile libraries are decoded off the UI thread.
                let result = try await Task.detached {
                    try JSONDecoder().decode(ImportResult.self, from: await Bridge.call("scan"))
                }.value
                applyImport(result)
                pollActivity()
                if view == "Chat" { loadHistory(silent: background) }
            } catch {
                importError = "Session sync unavailable: " + error.localizedDescription
                if !background { banner = importError }
            }
        }
    }
    func open(_ session: CampSession) {
        if cloud.live.sessionID.map({ "camp:" + $0 }) != session.id { cloud.live.stop() }
        sharedPlanResult = nil
        reviewingPlan = nil
        planNavigation = nil
        selectedChildID = nil
        notificationPlanVersion = nil; notificationMessageID = nil
        if selectedID != session.id {
            if let previous = selectedID { drafts[previous] = draft }
            draft = drafts[session.id] ?? ""
            messages = []; historyTotal = 0; historyLimit = 120; banner = ""
        }
        selectedID = session.id; children = activityFor(session)?.children ?? []; view = "Chat"
        if session.isSharedPlan {
            sharedPlanning.open(String(session.id.dropFirst(session.id.hasPrefix("camp-plan-draft:") ? 16 : 10)))
            transcriptWatch.stop(); return
        }
        if let run = conversation(session) { messages = run.messages; banner = run.notice }
        observeHistory(); loadHistory()
    }
    func applyHistory(_ result: Transcript, sessionID: String) {
        guard selectedID == sessionID, runningID == nil else { return }
        if messages != result.messages { messages = result.messages }
        if historyTotal != result.total { historyTotal = result.total }
    }
    func loadHistory(more: Bool = false, silent: Bool = false) {
        guard let s = selected, s.provider != "Planning", runningID == nil else { return }
        if let execution = planning.ticket(s.id)?.implementation {
            if more { historyLimit += 120 }
            if !silent { messages = Array(execution.messages.suffix(historyLimit)); historyTotal = execution.messages.count }
            guard execution.providerSessionCreated, !silent || historyRequestID == nil, let source = executionSource(s) else { loading = false; return }
            let generation = UUID(), limit = historyLimit
            readGeneration = generation; historyRequestID = s.id
            if !silent { loading = true }
            Task {
                defer {
                    if readGeneration == generation {
                        loading = false; historyRequestID = nil
                        if historyDirty { historyDirty = false; historyChanged() }
                    }
                }
                do {
                    let result: Transcript
                    if let implementationTranscriptReader { result = try await implementationTranscriptReader(source, limit) }
                    else {
                        let input = try JSONSerialization.data(withJSONObject: ["session": object(source), "limit": limit])
                        result = try decoder.decode(Transcript.self, from: await Bridge.call("transcript", input: input))
                    }
                    guard readGeneration == generation, planning.ticket(s.id)?.implementation?.id == execution.id else { return }
                    if !result.messages.isEmpty { applyHistory(result, sessionID: s.id) }
                } catch { if readGeneration == generation && !silent { banner = error.localizedDescription } }
            }
            return
        }
        if let remote = cloudSessions[s.id] { loadCloudHistory(remote, more: more, silent: silent); return }
        guard !silent || historyRequestID == nil else { return }
        if more { historyLimit += 120 }
        let generation = UUID(); readGeneration = generation; historyRequestID = s.id
        if !silent { loading = true }
        let limit = historyLimit
        Task {
            defer {
                if readGeneration == generation {
                    loading = false; historyRequestID = nil
                    if historyDirty { historyDirty = false; historyChanged() }
                }
            }
            do {
                let input = try JSONSerialization.data(withJSONObject: ["session": object(s), "limit": limit])
                let result = try decoder.decode(Transcript.self, from: await Bridge.call("transcript", input: input))
                guard readGeneration == generation else { return }
                applyHistory(result, sessionID: s.id)
            } catch { if readGeneration == generation && !silent { banner = error.localizedDescription } }
        }
    }
    private func loadCloudHistory(_ session: CloudSession, more: Bool, silent: Bool) {
        let id = "camp:" + session.id
        if let scope = cloud.scope, let client = cloud.client, let credential = cloud.credential {
            cloud.live.watch(session: session, scope: scope, client: client, credential: credential)
        }
        if silent && historyRequestID != nil { return }
        let generation = UUID(); readGeneration = generation; historyRequestID = id
        if !silent { loading = true }
        Task {
            defer { if readGeneration == generation { loading = false; historyRequestID = nil } }
            if !more && messages.isEmpty {
                let cached = await cloud.cachedTranscript(session: session)
                if readGeneration == generation { messages = cached.map(\.transcriptMessage) }
            }
            do {
                let page = try await cloud.transcript(session: session, before: more ? (cloudHistoryBefore[id] ?? 0) : 0, revision: more ? (cloudHistoryRevision[id] ?? 0) : 0)
                guard readGeneration == generation, selectedID == id else { return }
                let combined = more ? (cloudHistory[id] ?? []) + page.messages : page.messages
                let unique = Dictionary(combined.map { ($0.id, $0) }, uniquingKeysWith: { a, b in a.revision > b.revision ? a : b })
                let ordered = unique.values.filter { !$0.removed }.sorted { $0.ordinal < $1.ordinal }
                cloudHistory[id] = ordered; cloudHistoryBefore[id] = page.before; cloudHistoryRevision[id] = page.revision
                remoteHasMore = page.hasMore; messages = ordered.map(\.transcriptMessage)
                cloud.live.acknowledge(ordered)
                historyTotal = messages.count + (page.hasMore ? 1 : 0)
            } catch { if readGeneration == generation && !silent { banner = error.localizedDescription } }
        }
    }
    func object(_ s: CampSession) -> [String: Any] { (try? JSONSerialization.jsonObject(with: JSONEncoder().encode(s))) as? [String: Any] ?? [:] }
    func send() {
        guard let s = selected, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !preparingPrompts.contains(s.id) else { return }
        let references: [SessionReference]
        do { references = s.isPlan ? try implementationReferences(s, adding: mentions.references(for: s.id)) : mentions.references(for: s.id) }
        catch { banner = error.localizedDescription; return }
        if let remote = cloudSessions[s.id] {
            let text = draft, model = modelChoices[s.id] ?? remote.model, choice = tuning(s)
            preparingPrompts.insert(s.id)
            Task {
                defer { preparingPrompts.remove(s.id) }
                do {
                    guard references.allSatisfy({ $0.scope == cloud.scope?.key && $0.cloud != nil }) else { throw CampError.message("Share local sources with this Camp before attaching them to a shared request") }
                    try mentions.record(references)
                    try await cloud.queuePrompt(session: remote, text: text, model: model, tuning: choice, sources: references.compactMap(\.cloud))
                    if selectedID == s.id && draft == text { draft = "" }
                    try mentions.clear(references, for: s.id)
                }
                catch { banner = error.localizedDescription }
            }
            return
        }
        guard conversation(s) == nil else { return }
        guard activityFor(s) == nil else { banner = "This session is open in \(s.provider). Close it there before sending from Camp."; return }
        let prompt = draft
        let model = modelChoices[s.id] ?? (s.isPlan ? s.model : ""), choice = tuning(s)
        if references.isEmpty { if start(s, prompt: prompt, model: model, tuning: choice) { draft = "" }; return }
        preparingPrompts.insert(s.id)
        Task {
            defer { preparingPrompts.remove(s.id) }
            do {
                let contexts = try await resolveContexts(references, destination: s)
                if start(s, prompt: prompt, model: model, tuning: choice, contexts: contexts, references: references) {
                    if selectedID == s.id && draft == prompt { draft = "" }
                    try mentions.clear(references, for: s.id)
                }
            } catch { banner = error.localizedDescription }
        }
    }
    func conversation(_ session: CampSession) -> LocalConversation? {
        if session.isCloud, let binding = sharing.shares.first(where: { "camp:" + $0.id == session.id }) { return conversations[binding.local.id] }
        return conversations[session.id]
    }
    private func pauseQueue(_ id: String, _ pause: Bool) {
        if pause { enabledQueues.remove(id) } else { enabledQueues.insert(id) }
    }
    @discardableResult
    func start(_ s: CampSession, prompt: String, model: String, tuning: ModelTuning, queuedID: String? = nil,
               history: [Message]? = nil, contexts: [SessionContext] = [], references: [SessionReference] = [], notifyCompletion: Bool = true, completion: ((ConversationResult) -> Void)? = nil) -> Bool {
        let s = planning.ticket(s.id)?.session ?? s
        let execution = planning.ticket(s.id)?.implementation
        let grant = sessionPermissions.grants[s.id]
        let permissions = grant?.settings ?? execution?.executor.permissions
        let fingerprint = grant?.context.fingerprint ?? execution?.context.fingerprint ?? ""
        guard sessionPermissions.error.isEmpty else { banner = sessionPermissions.error; return false }
        if let queuedID, let queued = queuedPrompts.first(where: { $0.id == queuedID }), queued.permissionRevision != permissionRevision(s) {
            banner = "Permissions changed after this prompt was queued. Review and queue it again."; return false
        }
        guard !s.isCloud, s.provider != "Planning", conversations[s.id] == nil else { banner = "This session already has a running turn"; return false }
        guard conversations.count + planning.activeCount + cloud.worker.activePlanningCount < 4, !planning.uses(s.provider), !cloud.worker.usesPlanningProvider(s.provider), !conversations.values.contains(where: { $0.session.provider == s.provider && $0.session.root == s.root }) else {
            banner = "This profile is busy. Queue the prompt or wait for its current turn"; return false
        }
        guard activityFor(s) == nil else { banner = "Close this session in \(s.provider) before sending from Camp"; return false }
        do { try mentions.record(references) } catch { banner = error.localizedDescription; return false }
        let attempt = UUID().uuidString
        let prior = history ?? (selectedID == s.id && !messages.isEmpty ? messages : execution.map { Array($0.messages.suffix(240)) } ?? [])
        if execution != nil && !planning.beginImplementation(s.id, attempt: attempt, prompt: prompt, model: model, tuning: tuning, permissions: permissions, permissionFingerprint: fingerprint) {
            banner = "Review the implementation before starting another turn"; return false
        }
        let run = LocalConversation(session: s, history: prior, prompt: prompt, queueID: queuedID, references: references, promptID: execution == nil ? nil : attempt)
        if execution != nil {
            run.sessionCreated = { [weak self, weak run] id in
                guard let self else { return }
                if !self.planning.bindImplementationSession(s.id, attempt: attempt, providerID: id) { run?.stop() }
            }
        }
        var waitingForApproval = false
        var notifiedApprovals = Set<String>()
        run.changed = { [weak self, weak run] in
            guard let self, let run, self.conversations[s.id]?.id == run.id else { return }
            let waiting = !run.approvals.isEmpty
            if waiting != waitingForApproval { waitingForApproval = waiting; self.filterRevision += 1; self.sharing.localChanged(); self.objectWillChange.send() }
            for approval in run.approvals where notifiedApprovals.insert(approval.id).inserted {
                self.notifications.record(key: "approval:" + approval.id, sessionID: s.id, title: s.title, kind: "attention")
            }
            notifiedApprovals.formIntersection(run.approvals.map(\.id))
            if !run.steering, let restored = run.takePendingSteer() { self.restoreDraft(restored, for: s.id) }
            if self.view == "Chat", let selected = self.selected, self.conversation(selected)?.id == run.id {
                if self.messages != run.messages { self.messages = run.messages }
                if self.banner != run.notice { self.banner = run.notice }
                self.objectWillChange.send()
            }
        }
        run.didAccept = { [weak self, weak run] in
            guard let self else { return }
            if execution != nil && !self.planning.startedImplementation(s.id, attempt: attempt) { run?.stop() }
            guard let queuedID else { return }
            if let index = self.queuedPrompts.firstIndex(where: { $0.id == queuedID }) {
                self.queuedPrompts[index].state = "running"; self.saveQueue()
            }
        }
        run.completed = { [weak self, weak run] result in
            guard let self, let run, self.conversations[s.id]?.id == run.id else { return }
            if !result.accepted, result.state != "completed", queuedID == nil, completion == nil {
                self.restoreDraft(prompt, for: s.id)
            }
            if let restored = run.takePendingSteer() { self.restoreDraft(restored, for: s.id) }
            let visible = self.selected.map { self.conversation($0)?.id == run.id } ?? false
            if visible { self.messages = run.messages; self.banner = result.message }
            if let execution, !self.planning.finishImplementation(s.id, executionID: execution.id, attempt: attempt, messages: run.messages, result: result) {
                self.banner = self.planning.error
            }
            self.conversations.removeValue(forKey: s.id); self.filterRevision += 1
            if result.state != "completed" { self.pauseQueue(s.id, true); self.markUncertainQueue(queuedID) }
            else if let queuedID { self.queuedPrompts.removeAll { $0.id == queuedID }; self.saveQueue() }
            self.move(s.id, .attention)
            if notifyCompletion, result.state != "cancelled" {
                self.notifications.record(key: "turn:" + attempt, sessionID: s.id, title: s.title,
                    kind: result.state == "completed" ? "finished" : "attention", messageID: run.messages.last(where: { $0.role == "assistant" })?.id)
            }
            if visible { self.loadHistory() }
            completion?(result)
            self.sharing.localChanged()
            self.cloud.worker.scan()
            self.planning.drain()
            if result.state == "completed" { self.runNextQueued(sessionID: s.id, history: Array(run.messages.suffix(240))) }
        }
        do {
            try run.start(prompt: prompt, model: model, tuning: tuning, contexts: contexts,
                mode: execution == nil && grant == nil ? .chat : .implementation, executionApproved: execution != nil || grant != nil,
                newSession: execution.map { !$0.providerSessionCreated } ?? false,
                executionContext: fingerprint, permissions: permissions, makeProcess: conversationProcess)
            conversations[s.id] = run; drafts[s.id] = nil; filterRevision += 1
            sharing.localChanged()
            if selectedID == s.id { readGeneration = UUID(); loading = false; historyRequestID = nil; historyDirty = false; messages = run.messages; banner = "" }
            return true
        } catch {
            if let execution { _ = planning.finishImplementation(s.id, executionID: execution.id, attempt: attempt, messages: [], result: ConversationResult(state: "failed", message: error.localizedDescription, accepted: false)) }
            pauseQueue(s.id, true)
            if selectedID == s.id { draft = prompt; banner = error.localizedDescription } else { drafts[s.id] = prompt }
            return false
        }
    }
    func stop() {
        if let s = selected, controlsCloudTurn(s), let remote = cloudSessions[s.id], let head = cloud.live.head {
            Task { await cloud.cancelRunning(session:remote,head:head) }; return
        }
        guard let s = selected, let run = conversation(s) else { return }
        pauseQueue(run.session.id, true); run.stop()
    }
    func steerDraft() {
        guard let s = selected, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        guard mentions.references(for: s.id).isEmpty else { banner = "Queue this prompt to attach new context after the current turn"; return }
        if controlsCloudTurn(s), let remote = cloudSessions[s.id], let head = cloud.live.head {
            let text = draft, id = UUID().uuidString.lowercased()
            Task { do {
                try await cloud.queueControl(session:remote,head:head,value:CloudControlEnvelope(id:id,control:.init(type:"steer",id:id,text:text)))
                if selectedID == s.id, draft == text { draft = "" }
                else if drafts[s.id] == text { drafts[s.id] = nil }
            } catch { banner = error.localizedDescription } }
            return
        }
        guard let run = conversation(s) else { return }
        do { try run.steer(draft); draft = "" } catch { banner = error.localizedDescription }
    }
    func restoreControlDraft(_ text: String, for id: String) { restoreDraft(text,for:id) }
    private func restoreDraft(_ value: String, for id: String) {
        if selectedID == id { draft = draft.isEmpty ? value : value + "\n\n" + draft }
        else { let previous = drafts[id] ?? ""; drafts[id] = previous.isEmpty ? value : value + "\n\n" + previous }
    }
    func enqueueDraft() {
        guard let s = selected, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        if s.isCloud { send(); return }
        let choice = tuning(s)
        let references = mentions.references(for: s.id)
        queuedPrompts.append(QueuedPrompt(id: UUID().uuidString, sessionID: s.id, text: draft, model: modelChoices[s.id] ?? (s.isPlan ? s.model : ""), effort: choice.effort, speed: choice.speed, sources: references, permissionRevision: permissionRevision(s)))
        guard saveQueue() else { queuedPrompts.removeLast(); return }
        draft = ""; queuePaused = runningID != s.id
        do { try mentions.clear(references, for: s.id) } catch { banner = error.localizedDescription }
    }
    func queued(_ s: CampSession) -> [QueuedPrompt] { queuedPrompts.filter { $0.sessionID == s.id && $0.state != "running" } }
    @discardableResult
    func saveQueue() -> Bool {
        do {
            let url = stateDirectory.appendingPathComponent("queued-prompts.json")
            try JSONEncoder().encode(queuedPrompts).write(to: url, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
            return true
        } catch { enabledQueues.removeAll(); banner = "Could not save the prompt queue: " + error.localizedDescription; return false }
    }
    func markUncertainQueue(_ activeQueueID: String?) {
        if let activeQueueID, let index = queuedPrompts.firstIndex(where: { $0.id == activeQueueID }) { queuedPrompts[index].state = "needsReview"; saveQueue() }
    }
    func editQueued(_ item: QueuedPrompt) {
        guard !["sending", "running"].contains(item.state) else { return }
        do {
            var references = mentions.references(for: item.sessionID)
            for ref in item.sources ?? [] { references.removeAll { $0.id == ref.id }; references.append(ref) }
            try mentions.set(references, for: item.sessionID)
        } catch { banner = error.localizedDescription; return }
        draft = draft.isEmpty ? item.text : draft + "\n\n" + item.text
        queuedPrompts.removeAll { $0.id == item.id }; saveQueue()
    }
    func removeQueued(_ item: QueuedPrompt) {
        guard !["sending", "running"].contains(item.state) else { return }
        queuedPrompts.removeAll { $0.id == item.id }; saveQueue()
    }
    func resumeQueue() { queuePaused = false; runNextQueued() }
    func runNextQueued(sessionID: String? = nil, history: [Message]? = nil) {
        guard let id = sessionID ?? selectedID, enabledQueues.contains(id), let s = sessions.first(where: { $0.id == id }), conversation(s) == nil, let item = queued(s).first else { return }
        guard item.state == "waiting" else { pauseQueue(s.id, true); banner = "Check the transcript before editing and resubmitting the unconfirmed prompt"; return }
        guard item.permissionRevision == permissionRevision(s) else { pauseQueue(s.id, true); banner = "Permissions changed after this prompt was queued. Review and queue it again."; return }
        guard activityFor(s) == nil else { pauseQueue(s.id, true); banner = "Close this session in \(s.provider) before resuming its Camp queue"; return }
        if let index = queuedPrompts.firstIndex(where: { $0.id == item.id }) { queuedPrompts[index].state = "sending"; guard saveQueue() else { return } }
        guard enabledQueues.contains(s.id) else { return }
        let references: [SessionReference]
        do { references = s.isPlan ? try implementationReferences(s, adding: item.sources ?? []) : item.sources ?? [] }
        catch { markUncertainQueue(item.id); pauseQueue(s.id, true); banner = error.localizedDescription; return }
        if !references.isEmpty {
            Task {
                do {
                    let contexts = try await resolveContexts(references, destination: s)
                    guard enabledQueues.contains(s.id), queuedPrompts.contains(where: { $0.id == item.id && $0.state == "sending" }) else {
                        if let index = queuedPrompts.firstIndex(where: { $0.id == item.id }) { queuedPrompts[index].state = "waiting"; saveQueue() }; return
                    }
                    if !start(s, prompt: item.text, model: item.model, tuning: ModelTuning(effort: item.effort, speed: item.speed), queuedID: item.id, history: history, contexts: contexts, references: references), let index = queuedPrompts.firstIndex(where: { $0.id == item.id }) {
                        queuedPrompts[index].state = "waiting"; pauseQueue(s.id, true); saveQueue()
                    }
                } catch { markUncertainQueue(item.id); pauseQueue(s.id, true); banner = error.localizedDescription }
            }
            return
        }
        if !start(s, prompt: item.text, model: item.model, tuning: ModelTuning(effort: item.effort, speed: item.speed), queuedID: item.id, history: history), let index = queuedPrompts.firstIndex(where: { $0.id == item.id }) {
            queuedPrompts[index].state = "waiting"; pauseQueue(s.id, true); saveQueue()
        }
    }
    func pausePermissionQueue(_ id: String) { pauseQueue(id, true) }
    func addSource() {
        let panel = NSOpenPanel(); panel.canChooseDirectories = true; panel.canChooseFiles = false; panel.showsHiddenFiles = true
        panel.message = "Choose a Codex home or Claude config folder containing saved sessions"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        if banner == sourceError { banner = "" }
        sourceError = ""
        Task { do { _ = try await Bridge.call("add-source", input: Data(url.path.utf8)); refresh() } catch { sourceError = error.localizedDescription; banner = error.localizedDescription } }
    }
}

struct Mascot: View {
    var size: CGFloat = 48
    var body: some View {
        Group {
            if let image = NSImage(contentsOf: Bundle.main.url(forResource: "capybara", withExtension: "png")!) { Image(nsImage: image).resizable().scaledToFit() }
        }.frame(width: size, height: size)
    }
}
struct RootView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var onboarding: CampOnboarding
    @Environment(\.openWindow) private var openWindow
    @State private var showCamp = false
    @FocusState private var notificationButtonFocused: Bool
    init(store: CampStore) { self.store = store; self.onboarding = store.onboarding }
    var body: some View {
        HStack(spacing: 0) {
            sidebar
            Rectangle().fill(campInk.opacity(0.08)).frame(width: 1)
            VStack(spacing: 0) {
                header
                if !store.banner.isEmpty {
                    HStack(alignment: .top) { Image(systemName: "info.circle"); Text(store.banner).textSelection(.enabled); Spacer(); Button { store.banner = "" } label: { Image(systemName: "xmark") }.buttonStyle(.plain) }
                        .font(.system(size: 12)).padding(14).background(Color(hex: 0xF5E7D4))
                }
                if store.view == "Board" { BoardView(store: store) }
                else if let selected = store.selected, selected.isSharedPlan { SharedPlanningWorkspace(store: store, planning: store.sharedPlanning, id: String(selected.id.dropFirst(selected.id.hasPrefix("camp-plan-draft:") ? 16 : 10))) }
                else if let result = store.sharedPlanResult { SharedPlanningResultView(store: store, result: result) }
                else if let id = store.selectedID, let plan = store.planning.ticket(id), plan.implementation == nil || store.reviewingPlan == id { PlanningWorkspace(store: store, planning: store.planning, id: id) }
                else { ChatView(store: store) }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }.disabled(store.notificationSheet).accessibilityHidden(store.notificationSheet)
        .foregroundStyle(campInk).background(campCream).tint(campGreen)
        .frame(minWidth: 1060, minHeight: 720)
        .onAppear {
            store.refresh(); store.startBackgroundObservation()
            AppDelegate.reopen = { openWindow(id: "camp") }
            AppDelegate.prepareToQuit = { [weak store] in
                AppDelegate.traceShutdown("stop observation"); store?.stopRuntimeObservation()
                AppDelegate.traceShutdown("save notifications"); await store?.notifications.flush()
                AppDelegate.traceShutdown("save shared planning"); await store?.sharedPlanning.flush()
            }
        }
        .campWindowBehavior()
        .overlay {
            if store.notificationSheet {
                ZStack(alignment: .topTrailing) {
                    Button { store.notificationSheet = false } label: {
                        campInk.opacity(0.08).frame(maxWidth: .infinity, maxHeight: .infinity).contentShape(Rectangle())
                    }.buttonStyle(.plain).accessibilityHidden(true)
                    CampNotificationsView(store: store, notices: store.notifications)
                        .frame(width: 430).padding(.top, 90).padding(.bottom, 65).padding(.trailing, 24)
                }.tint(campGreen).foregroundStyle(campInk)
            }
        }
        .overlay(alignment: .bottom) {
            if !store.notificationSheet { CampNotificationToast(store: store, notices: store.notifications).padding(.horizontal, 25).padding(.bottom, 43) }
        }
        .onChange(of: store.notificationSheet) { _, showing in
            if showing { store.notifications.dismissToast() } else { notificationButtonFocused = true }
        }
        .onChange(of: store.view) { _, value in store.observeHistory(); if value == "Chat" { store.loadHistory() } }
        .sheet(isPresented: $store.sourceSheet) { SourcesView(store: store).campWindowBehavior() }
        .sheet(isPresented: Binding(get: { onboarding.presented }, set: { if !$0 { onboarding.pause() } })) {
            CampOnboardingView(store: store, setup: onboarding, cloud: store.cloud, accounts: store.cloud.providerAccounts, git: store.cloud.gitConnections, projects: store.projects, notices: store.notifications).campWindowBehavior()
        }
        .onChange(of: onboarding.presented) { _, showing in if !showing && !onboarding.error.isEmpty { store.banner = onboarding.error } }
        .sheet(isPresented: $store.projectSheet) { CampProjectsView(store: store, local: store.projects, cloud: store.cloud).campWindowBehavior() }
        .sheet(item: $store.permissionSheet) { SessionPermissionsView(store: store, session: $0).campWindowBehavior() }
        .sheet(isPresented: $showCamp) { CampConnectionView(cloud: store.cloud).campWindowBehavior() }
        .sheet(item: $store.shareSheet) { session in CampShareView(store: store, cloud: store.cloud, sharing: store.sharing, session: session).campWindowBehavior() }
        .sheet(item: $store.executionSheet) { session in RunLocationView(store:store,session:session).campWindowBehavior() }
        .sheet(item: $store.contextInspection) { value in ContextInspectionView(store: store, inspection: value).campWindowBehavior() }
        .onOpenURL { store.openContextLink($0) }
        .environment(\.openURL, OpenURLAction { url in
            if url.scheme == "capybara" { store.openContextLink(url); return .handled }
            return .systemAction
        })
    }
    var sidebar: some View {
        VStack(alignment: .leading, spacing: 22) {
            HStack(spacing: 9) { Mascot(size: 46); VStack(alignment: .leading, spacing: 2) { Text("Capybara").font(.system(size: 17, weight: .semibold)); Text("CAMP").font(.system(size: 10, weight: .semibold)).tracking(3).foregroundStyle(campMuted) } }
            ScrollView {
            VStack(alignment:.leading,spacing:22) {
            VStack(alignment: .leading, spacing: 5) {
                Text("YOUR CAMP").font(.system(size: 10, weight: .semibold)).tracking(1.4).foregroundStyle(campMuted).padding(.bottom, 8)
                sourceButton("All sessions", id: "all", icon: "square.grid.2x2", count: store.sessionCount("all"))
                sourceButton("Active now", id: "active", icon: "waveform.path", count: store.sessionCount("active"))
                sourceButton("Codex", id: "Codex", icon: "terminal", count: store.sessionCount("Codex"))
                sourceButton("Claude", id: "Claude", icon: "asterisk", count: store.sessionCount("Claude"))
            }
            VStack(alignment: .leading, spacing: 10) {
                ProjectSidebar(store: store, local: store.projects, cloud: store.cloud)
            }
            VStack(alignment: .leading, spacing: 10) {
                HStack { Text("LOCAL PROFILES").font(.system(size: 10, weight: .semibold)).tracking(1.4).foregroundStyle(campMuted); Spacer(); Button { store.sourceSheet = true } label: { Image(systemName: "plus") }.buttonStyle(.plain) }
                ForEach(store.sources) { source in
                    Button { store.sourceFilter = source.id } label: {
                        HStack(alignment: .top, spacing: 7) { Circle().fill(source.provider == "Codex" ? campGreen : Color(hex: 0xBD815C)).frame(width: 6, height: 6).padding(.top, 5); VStack(alignment: .leading, spacing: 3) { Text(source.label).lineLimit(1); Text("\(source.count.formatted()) sessions").font(.system(size: 10)).foregroundStyle(campMuted) } }
                            .frame(maxWidth: .infinity, alignment: .leading).padding(7).background(store.sourceFilter == source.id ? campGreen.opacity(0.10) : .clear).clipShape(RoundedRectangle(cornerRadius: 7))
                    }.buttonStyle(.plain).font(.system(size: 12))
                }
            }
            }
            }.frame(maxHeight:.infinity)
            VStack(alignment: .leading, spacing: 8) {
                HStack { Circle().fill(campGreen).frame(width: 6, height: 6); Text("Local on this Mac").font(.system(size: 11, weight: .medium)) }
                Text([store.importError, store.activityError].filter { !$0.isEmpty }.joined(separator: " · ").isEmpty ? "Live transcript updates · New sessions every 30s" : [store.importError, store.activityError].filter { !$0.isEmpty }.joined(separator: " · ")).font(.system(size: 11)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
                Button("Manage sources") { store.sourceSheet = true }.buttonStyle(.plain).font(.system(size: 11)).foregroundStyle(campGreen)
            }.padding(14).background(.white.opacity(0.65)).clipShape(RoundedRectangle(cornerRadius: 12))
        }.padding(22).frame(width: 224).frame(maxHeight: .infinity).background(Color(hex: 0xEFEEE6))
    }
    func sourceButton(_ title: String, id: String, icon: String, count: Int) -> some View {
        Button { store.sourceFilter = id } label: {
            HStack {
                if id == "Codex" || id == "Claude" { ProviderMark(provider: id, size: 18) }
                else { Image(systemName: icon).frame(width: 18) }
                Text(title); Spacer(); Text(count.formatted()).font(.system(size: 10)).foregroundStyle(campMuted)
            }
                .font(.system(size: 12, weight: .medium)).padding(10).background(store.sourceFilter == id ? .white.opacity(0.9) : .clear).clipShape(RoundedRectangle(cornerRadius: 8))
        }.buttonStyle(.plain)
    }
    var header: some View {
        VStack(alignment: .leading, spacing: 19) {
            HStack {
                VStack(alignment: .leading, spacing: 5) { Text(headerTitle).font(.system(size: 23, weight: .semibold)).lineLimit(1); Text(headerSubtitle).font(.system(size: 12)).foregroundStyle(campMuted) }
                Spacer()
                if store.view == "Chat", let selected=store.selected { ProjectAssignmentMenu(store:store,session:selected) }
                Menu {
                    Button("Plan on this Mac", systemImage: "lightbulb") { store.newPlan(mode: .single) }
                    Button("Plan with comparison", systemImage: "bubble.left.and.bubble.right") { store.newPlan(mode: .comparison) }
                    Divider()
                    Button("Plan in shared Camp", systemImage: "person.2") { store.newSharedPlan(mode: .single) }.disabled(!store.sharedPlanning.ready || store.cloud.projects.isEmpty)
                    Button("Compare in shared Camp", systemImage: "person.2") { store.newSharedPlan(mode: .comparison) }.disabled(!store.sharedPlanning.ready || store.cloud.projects.isEmpty)
                } label: { Text("Plan") } primaryAction: { store.newPlan(mode: .single) }
                CampConnectionButton(cloud: store.cloud) { showCamp = true }
                CampNotificationButton(notices: store.notifications) { store.notificationSheet = true }.focusable().focused($notificationButtonFocused)
            }
            HStack {
                HStack(spacing: 9) {
                    Text("View").font(.system(size: 12))
                    CampSegmentedControl(options: [("Board", "Board"), ("Chat", "Chat")], selection: $store.view).frame(width: 114)
                }
                Spacer()
                HStack { Image(systemName: "magnifyingglass"); TextField("Find a session or project", text: $store.search).textFieldStyle(.plain) }.font(.system(size: 12)).foregroundStyle(campMuted).padding(9).frame(width: 255).background(.white.opacity(0.8)).clipShape(RoundedRectangle(cornerRadius: 8))
            }
        }.padding(25)
    }
    private var headerTitle: String {
        store.view == "Board" ? "Your Camp for big ideas" : store.selectedChild?.name ?? store.sharedPlanResult?.title ?? store.selected?.title ?? "Choose a session"
    }
    private var headerSubtitle: String {
        if store.view == "Board" { return "\(store.filtered.count.formatted()) \(store.filtered.count == 1 ? "session" : "sessions") · Pick up where you left off" }
        if store.selectedChild != nil { return "Subagent of \(store.selected?.title ?? "")" }
        return store.sharedPlanResult != nil ? "Planning · Shared Camp" : "\(store.selected?.provider ?? "") · \(store.selected.map(store.projectSummary) ?? "")"
    }
}

struct BoardView: View {
    @ObservedObject var store: CampStore
    var body: some View {
        ScrollView(.horizontal) {
            HStack(alignment: .top, spacing: 15) {
                ForEach(Stage.allCases, id: \.self) { stage in BoardColumn(store: store, stage: stage) }
            }.padding(.horizontal, 25).padding(.bottom, 25)
        }
        .overlay { if store.sessions.isEmpty { VStack(spacing: 14) { Mascot(size: 120); Text(store.importing ? "Gathering your sessions…" : "Your camp is ready").font(.title3); Text("Add a local Codex or Claude folder to get started").foregroundStyle(campMuted); Button("Add a source") { store.addSource() } } } }
    }
}
struct BoardColumn: View {
    @ObservedObject var store: CampStore
    let stage: Stage
    @State private var count = 50
    var items: [CampSession] { store.boardSessions(stage) }
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack { Circle().fill(stage.color).frame(width: 7, height: 7); Text(stage.rawValue).font(.system(size: 12, weight: .semibold)); Text(items.count.formatted()).font(.system(size: 10)).foregroundStyle(campMuted); Spacer() }.padding(.horizontal, 4)
            ScrollView {
                LazyVStack(spacing: 10) {
                    ForEach(items.prefix(count)) { s in
                        if s.isSharedPlan { SharedPlanningCard(store: store, planning: store.sharedPlanning, session: s) }
                        else if s.provider == "Planning" { PlanningCard(store: store, planning: store.planning, id: s.id) }
                        else { SessionCard(store: store, session: s) }
                    }
                    if items.count > count { Button("Show 50 more") { count += 50 }.font(.system(size: 11)).padding(8) }
                    if items.isEmpty { VStack(spacing: 10) { Image(systemName: stage == .done ? "leaf" : "tray"); Text("Room for another capybara").font(.system(size: 11)) }.foregroundStyle(campMuted).padding(.vertical, 35).frame(maxWidth: .infinity) }
                }.padding(2)
            }
        }.padding(12).frame(width: 245).frame(maxHeight: .infinity).background(Color(hex: 0xEEEDE5).opacity(0.75)).clipShape(RoundedRectangle(cornerRadius: 14))
    }
}
struct SessionCard: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    var live: LiveState? { store.activityFor(session) }
    var running: Bool { store.isBusy(session) }
    var waiting: Bool { store.conversation(session)?.approvals.isEmpty == false || store.cloudSessions[session.id]?.state == "waiting" || live?.state == "waiting" }
    var statusText: String {
        if waiting { return session.isCloud && store.cloudSessions[session.id]?.ownerUserId != store.cloud.credential?.actor.userId ? "Waiting for the owner" : "Waiting for your approval" }
        guard let live else { return store.executionSession(session)?.executionLocation == "cloud" ? "Running in Cloud" : "Running in Camp" }
        return live.state == "running" ? "Running in \(live.owner)" : live.state == "idle" ? "Open in \(live.owner)" : "\(live.owner) · \(live.state)"
    }
    var body: some View {
        CampTaskCard(store: store, session: session) {
            HStack {
                CampCardProvider(provider: session.provider)
                if session.isCloud && store.cloudSessions[session.id]?.shared == true { Image(systemName: "person.2").font(.system(size: 9)).foregroundStyle(campMuted).help("Shared Camp session") }
                Spacer()
                Menu {
                    ForEach(Stage.allCases, id: \.self) { dest in Button("Move to \(dest.rawValue)") { store.move(session.id, dest) } }
                    ProjectAssignmentMenu(store:store,session:session)
                    if !session.isCloud && session.provider != "Planning" { Button("Permissions…") { store.permissionSheet = session } }
                    if let local = store.shareSource(session) { Divider(); Button("Share with Camp…") { store.shareSheet = local } }
                } label: { Image(systemName: "ellipsis").foregroundStyle(campMuted).frame(width: 28, height: 28) }
                    .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().accessibilityLabel("Move \(session.title)")
            }
        } models: {
            ModelMenu(store: store, session: session).frame(maxWidth: .infinity, alignment: .leading)
        } status: {
            HStack(alignment: .center, spacing: 6) {
                if running || live != nil {
                    HStack(spacing: 7) {
                        if waiting { Image(systemName: "hand.raised").frame(width: 12, height: 12) }
                        else if running { ProgressView().controlSize(.mini).frame(width: 12, height: 12).accessibilityLabel("Session running") }
                        else { Circle().fill(campGreen).frame(width: 5, height: 5) }
                        Text(statusText).lineLimit(1)
                    }.foregroundStyle(campGreen).font(.system(size: 10))
                } else {
                    Image(systemName: store.stage(session) == .done ? "checkmark" : "circle").font(.system(size: 7))
                    Text(store.stage(session).rawValue)
                }
                Spacer(minLength: 0)
                if !store.queued(session).isEmpty { Text("\(store.queued(session).count) queued").font(.system(size: 9)).foregroundStyle(campMuted) }
            }
        } actions: {
            HStack { Button { store.open(session) } label: {
                HStack { Label("Subagents", systemImage: "person.2"); Spacer() }.frame(minHeight: 28).contentShape(Rectangle())
            }.buttonStyle(.plain).font(.system(size: 10)).foregroundStyle(campMuted).help("Show subagents")
                ExecutionLocationButton(store:store,session:session)
            }
        }
            .contextMenu { ForEach(Stage.allCases, id: \.self) { dest in Button("Move to \(dest.rawValue)") { store.move(session.id, dest) } } }
    }
}

struct RecentSessionsList: View {
    @ObservedObject var store: CampStore
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text("Recent sessions").font(.system(size: 11)).foregroundStyle(campMuted)
                    Spacer()
                    Text("\(store.recentSessions.count)").font(.system(size: 10)).foregroundStyle(campMuted)
                }.padding(.horizontal, 16).padding(.top, 10)
                Menu { ForEach(["All types", "Planning", "Codex", "Claude"], id: \.self) { value in Button(value) { store.setRecentType(value) } } } label: {
                    Label(store.recentType, systemImage: "line.3.horizontal.decrease")
                }.menuStyle(.borderlessButton).menuIndicator(.visible).font(.system(size: 11)).controlSize(.small)
                    .padding(.horizontal, 9).frame(maxWidth: .infinity).frame(height: 29)
                    .background(.white.opacity(0.65)).clipShape(RoundedRectangle(cornerRadius: 6)).padding(.horizontal, 14)
                    .help("Filter recent sessions").accessibilityLabel("Filter recent sessions").accessibilityValue(store.recentType)
                ScrollView {
                LazyVStack(spacing: 5) {
                    ForEach(store.recentSessions) { s in Button { store.open(s) } label: {
                        HStack(alignment: .top, spacing: 8) {
                            ProviderMark(provider: s.provider, size: 15).frame(width: 25, height: 25)
                                .background(ProviderMark.wash(s.provider)).clipShape(RoundedRectangle(cornerRadius: 6)).padding(.top, 1)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(s.title).font(.system(size: 12, weight: .medium)).lineLimit(2)
                                Text("\(Text(s.provider).foregroundColor(ProviderMark.ink(s.provider)))\(Text(" · \(store.projectSummary(s))").foregroundColor(campMuted))")
                                    .font(.system(size: 9)).lineLimit(1)
                            }
                        }.frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 7).padding(.vertical, 8)
                            .background(store.selectedID == s.id ? Color(hex: 0xEEEEE7) : .clear).clipShape(RoundedRectangle(cornerRadius: 6))
                    }.buttonStyle(.plain) }
                    if store.recentSessions.isEmpty { Text("No recent \(store.recentType == "All types" ? "" : store.recentType.lowercased() + " ")sessions").font(.caption).foregroundStyle(campMuted).padding() }
                }.padding(10)
                }
                Text("Find other sessions with @").font(.system(size: 10)).foregroundStyle(campMuted)
                    .padding(.horizontal, 16).padding(.bottom, 12)
        }
    }
}
struct ChatView: View {
    @ObservedObject var store: CampStore
    private var pickedChild: ChildSession? {
        get { store.selectedChild }
        nonmutating set { store.selectedChildID = newValue?.id }
    }
    @State private var subagentsExpanded = true
    @State private var contextPicking = false
    @State private var contextQuery = ""
    var currentChild: ChildSession? { store.children.first { $0.id == pickedChild?.id } }
    var body: some View {
        GeometryReader { bounds in
        HStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 6) {
                if let session = store.selected {
                    VStack(alignment: .leading, spacing: 9) {
                        ProviderBadge(provider: session.provider)
                        Text(session.title).font(.system(size: 12, weight: .medium)).lineLimit(2)
                    }.padding(.horizontal, 16).padding(.top, 10).padding(.bottom, 4)
                    Button { pickedChild = nil } label: {
                        HStack(spacing: 9) {
                            ProviderMark(provider: session.provider, size: 15)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(session.isPlan ? "Implementation" : "Main chat").font(.system(size: 12, weight: .medium))
                                Text("\(session.provider) · \(store.projectSummary(session))").font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                        }.frame(maxWidth: .infinity, alignment: .leading).padding(10)
                            .background(pickedChild == nil ? Color(hex: 0xDFE5D8) : .clear).clipShape(RoundedRectangle(cornerRadius: 7))
                    }.buttonStyle(.plain).padding(.horizontal, 10).accessibilityLabel(session.isPlan ? "Implementation" : "Main chat")
                    Button { subagentsExpanded.toggle() } label: {
                        HStack(spacing: 6) {
                            Image(systemName: subagentsExpanded ? "chevron.down" : "chevron.right").font(.system(size: 8))
                            Text("Subagents"); Spacer()
                            if store.childrenLoading { ProgressView().controlSize(.mini) }
                            Text("\(store.children.count)")
                        }.font(.system(size: 10)).foregroundStyle(campMuted).frame(height: 26).contentShape(Rectangle())
                    }.buttonStyle(.plain).padding(.horizontal, 16).accessibilityLabel("Subagents, \(store.children.count)")
                        .accessibilityValue(subagentsExpanded ? "Expanded" : "Collapsed")
                    if subagentsExpanded {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 5) {
                            ForEach(store.children) { child in
                                Button { pickedChild = child } label: {
                                    HStack(alignment: .top, spacing: 8) {
                                        if child.status == "running" { ProgressView().controlSize(.mini).frame(width: 13, height: 13) }
                                        else { ProviderMark(provider: session.provider, size: 13) }
                                        VStack(alignment: .leading, spacing: 4) {
                                            Text(child.name).font(.system(size: 11, weight: .medium)).lineLimit(1)
                                            Text(child.phase.isEmpty ? child.workflow : child.phase).font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                                        }
                                        Spacer(minLength: 0)
                                        Text(child.status.capitalized).font(.system(size: 9)).foregroundStyle(campMuted).lineLimit(1)
                                    }.frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 5).padding(.vertical, 8)
                                        .background(pickedChild?.id == child.id ? Color(hex: 0xDFE5D8) : .clear).clipShape(RoundedRectangle(cornerRadius: 7))
                                }.buttonStyle(.plain)
                            }
                            if store.children.isEmpty && !store.childrenLoading {
                                Text(session.isCloud ? "Subagents have not been shared" : "No saved subagents").font(.system(size: 10)).foregroundStyle(campMuted).padding(10)
                            }
                        }.padding(.leading, 7)
                    }.overlay(alignment: .leading) { Rectangle().fill(Color(hex: 0xCCD4C5)).frame(width: 1) }
                        .padding(.leading, 23).padding(.trailing, 10)
                        .frame(maxHeight: store.children.isEmpty ? 35 : min(CGFloat(store.children.count) * 54, 180))
                    }
                    if session.isPlan {
                        Button { store.reviewingPlan = session.id } label: {
                            Label("Planning history", systemImage: "clock.arrow.circlepath").font(.system(size: 11)).foregroundStyle(campMuted)
                                .frame(maxWidth: .infinity, alignment: .leading).frame(height: 28).contentShape(Rectangle())
                        }.buttonStyle(.plain).padding(.horizontal, 16)
                    }
                    Divider().padding(.horizontal, 14).padding(.top, 6)
                }
                RecentSessionsList(store: store)
            }.frame(width: 230)
            Rectangle().fill(campInk.opacity(0.08)).frame(width: 1)
            VStack(spacing: 0) {
                if let child = currentChild, let parent = store.selected {
                    ChildTranscriptView(store: store, parent: store.shareSource(parent) ?? parent, child: child, openMain: { pickedChild = nil }).id("\(parent.id):\(child.id)")
                } else if let s = store.selected {
                    HStack {
                        HStack(spacing: 7) { ProviderMark(provider: s.provider, size: 16); Text(s.isPlan ? "Implementation" : "Main chat") }.font(.system(size: 11)); Spacer()
                        if let local = store.shareSource(s) { Button { store.shareSheet = local } label: { Image(systemName: "person.2") }.buttonStyle(.plain).help("Share with Camp").accessibilityLabel("Share with Camp") }
                        ModelMenu(store: store, session: s, expanded: true)
                        if !s.isCloud && s.provider != "Planning" { Button { store.permissionSheet = s } label: { Image(systemName: "lock.shield") }.buttonStyle(.plain).frame(width: 28, height: 28).help("Permissions").accessibilityLabel("Permissions") }
                    }.padding(.horizontal, 22).padding(.vertical, 8)
                    if store.planning.ticket(s.id)?.implementation != nil { ImplementationStatusView(store: store, planning: store.planning, id: s.id) }
                    if let live = store.activityFor(s) { Text("\(live.owner) · \(live.state) · Model choice applies to the next Camp reply").font(.system(size: 11)).foregroundStyle(campGreen).padding(.bottom, 8) }
                    HStack { Text(s.sourceLabel).font(.system(size: 10, weight: .medium)); if !s.accountID.isEmpty { Text("· author \(s.accountID.prefix(8))").font(.system(size: 10)).foregroundStyle(campMuted) }; ExecutionLocationButton(store:store,session:s); Spacer(); Menu(store.stage(s).rawValue) { ForEach(Stage.allCases, id: \.self) { stage in Button(stage.rawValue) { store.move(s.id, stage) } } }.fixedSize() }.padding(.horizontal, 22).padding(.vertical, 10)
                    SessionContextChips(store: store, mentions: store.mentions, destination: s, transcriptStrip: true)
                      ScrollViewReader { proxy in
                        CampTranscriptViewport(padding: 25, followBottom: { proxy.scrollTo("bottom", anchor: .bottom) }) {
                            // Pages are bounded to 120 messages; measure their real heights so
                            // scrolling to the end cannot land in a lazy stack's estimated gap.
                            VStack(alignment: .leading, spacing: 22) {
                                if s.isCloud ? store.remoteHasMore : store.historyTotal > store.messages.count { Button("Load earlier messages") { store.loadHistory(more: true) }.font(.caption) }
                                if store.loading { ProgressView().controlSize(.small) }
                                ForEach(store.messages) { m in MessageView(message: m, provider: s.provider).id(m.id) }
                                if let run = store.conversation(s) { MessageView(message: run.streamedMessage, provider: s.provider) }
                                if s.isCloud { CloudStreamMessages(live: store.cloud.live, provider: s.provider) }
                                Color.clear.frame(height: 1).id("bottom")
                            }
                        }
                        .onReceive(store.$messages) { new in
                            let target = store.notificationMessageID.flatMap { id in new.contains(where: { $0.id == id }) ? id : nil } ?? "bottom"
                            DispatchQueue.main.async { if store.selectedID == s.id { proxy.scrollTo(target, anchor: .bottom) } }
                        }
                        .onChange(of: store.notificationMessageID) { _, id in if let id, store.messages.contains(where: { $0.id == id }) { proxy.scrollTo(id, anchor: .bottom) } }
                        .onChange(of: store.liveText) { _, _ in
                            DispatchQueue.main.async { if store.selectedID == s.id { proxy.scrollTo("bottom", anchor: .bottom) } }
                        }
                        .onAppear { proxy.scrollTo("bottom", anchor: .bottom); if s.isCloud { store.loadHistory(silent: true) } }
                        .onDisappear { if s.isCloud { store.cloud.live.stop() } }
                      }
                    VStack(alignment: .leading, spacing: 9) {
                        if let conversation = store.conversation(s) { ConversationApprovalsView(conversation: conversation) }
                        if let remote = store.cloudSessions[s.id] { CloudRequestQueueView(cloud: store.cloud, session: remote) }
                        else { PromptQueueView(store: store, session: s) }
                        if let remote = store.cloudSessions[s.id] {
                            CloudControlQueueView(cloud:store.cloud,sessionID:remote.id) { text in store.restoreControlDraft(text,for:s.id) }
                        }
                        TextEditor(text: $store.draft).font(.system(size: 13)).scrollContentBackground(.hidden).frame(minHeight: 64, maxHeight: 84).padding(8).background(.white).clipShape(RoundedRectangle(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(campInk.opacity(0.13)))
                        HStack(spacing: 10) {
                            Text(store.chatRunning(s) ? "Steer now or queue for after this turn" : s.isCloud ? (store.executionSession(s)?.executionLocation == "cloud" ? "Saved to Camp · Runs in Cloud with the selected account" : "Saved to Camp · Runs on the session owner's Mac after approval") : store.activityFor(s) != nil ? "Open in \(s.provider) · Camp queue waits until it is closed" : s.isPlan || store.sessionPermissions.grants[s.id] != nil ? "Uses \(s.sourceLabel) login · \(store.permissionSettings(s).summary)" : "Uses \(s.sourceLabel) login · Chat-only local preview")
                                .font(.system(size: 10)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
                            Spacer(minLength: 8)
                            Button("@ Context") { contextQuery = ""; contextPicking = true }.buttonStyle(.plain).font(.system(size: 11)).foregroundStyle(campGreen)
                                .accessibilityLabel("Attach session context")
                                .popover(isPresented: $contextPicking) { SessionContextPicker(store: store, destination: s, query: contextQuery) { contextPicking = false } }
                            if store.chatRunning(s) || store.activityFor(s) != nil {
                                Button("Queue") { store.enqueueDraft() }.disabled(store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty).keyboardShortcut(.return, modifiers: [.command, .shift])
                            }
                            if store.chatRunning(s) {
                                Button("Stop", role: .destructive) { store.stop() }
                                Button(store.steering ? "Sending…" : "Steer") { store.steerDraft() }.buttonStyle(.borderedProminent).tint(campGreen)
                                    .disabled(!store.readyToSteer || store.steering || store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty).keyboardShortcut(.return, modifiers: .command)
                                    .help(s.provider == "Codex" ? "Add input to the current turn" : "Interrupt the current response and send the new direction")
                            } else {
                                Button { store.send() } label: { Label("Send", systemImage: "arrow.up") }.buttonStyle(.borderedProminent).tint(campGreen)
                                    .disabled(store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || store.loading || store.activityFor(s) != nil || store.preparingPrompts.contains(s.id)).keyboardShortcut(.return, modifiers: .command)
                            }
                        }
                    }.padding(20)
                } else { Spacer(); Mascot(size: 120); Text("Choose a session to continue").foregroundStyle(campMuted); Spacer() }
            }.frame(maxWidth: .infinity)
        }
        .frame(width:bounds.size.width,height:bounds.size.height)
        }
        .task(id: store.selectedID) {
            pickedChild = nil
            contextPicking = false
            if let session = store.selected, let local = store.executionSource(session) { store.loadChildren(local) }
            else { store.children = [] }
        }
        .onChange(of: store.draft) { _, text in
            if !contextPicking, let query = sessionMentionQuery(text) { contextQuery = query; contextPicking = true }
        }
        .onChange(of: store.children.map(\.id)) { _, ids in
            if let child = pickedChild, !ids.contains(child.id) { pickedChild = nil }
        }
    }
}
struct PromptQueueView: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    var items: [QueuedPrompt] { store.queued(session) }
    var body: some View {
        if !items.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Queued prompts · \(items.count)").font(.system(size: 11, weight: .semibold))
                    Spacer()
                    if store.queuePaused {
                        Button("Resume queue") { store.resumeQueue() }.disabled(store.runningID != nil || store.activityFor(session) != nil)
                    } else {
                        Button("Pause queue") { store.queuePaused = true }
                    }
                }.font(.system(size: 10))
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(Array(items.enumerated()), id: \.element.id) { index, item in
                            HStack(alignment: .top, spacing: 9) {
                                Text("\(index + 1)").foregroundStyle(campMuted)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(item.text).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                                    if item.state == "needsReview" { Text("Unconfirmed delivery · Check the transcript before resubmitting").foregroundStyle(Color.orange) }
                                    else if item.state == "sending" { Text("Starting…").foregroundStyle(campMuted) }
                                }
                                Button { store.editQueued(item) } label: { Image(systemName: "pencil") }.help("Edit queued prompt").disabled(item.state == "sending")
                                Button { store.removeQueued(item) } label: { Image(systemName: "xmark") }.help("Remove queued prompt").disabled(item.state == "sending")
                            }.font(.system(size: 11)).buttonStyle(.plain)
                        }
                    }
                }.frame(maxHeight: 110)
            }.padding(12).background(campGreen.opacity(0.06)).clipShape(RoundedRectangle(cornerRadius: 9))
        }
    }
}
struct ModelMenu: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    var expanded = false
    static func effortName(_ value: String) -> String { ModelSettingsMenu.effortName(value) }
    static func speedName(_ value: String) -> String { ModelSettingsMenu.speedName(value) }
    var body: some View {
        ModelSettingsMenu(provider: session.provider, title: session.title, sessionModel: session.model,
            modelLabel: store.modelLabel(session), models: store.availableModels(session), caps: store.capabilities(session),
            choice: store.tuning(session), locked: store.conversation(session) != nil, reported: store.reportedSettings(session), expanded: expanded,
            selectModel: { store.selectModel(session, $0) }, tune: { store.tune(session, effort: $0, speed: $1) })
    }
}
struct ModelSettingsMenu: View {
    let provider: String
    let title: String
    let sessionModel: String
    let modelLabel: String
    let models: [ModelOption]
    let caps: ModelOption
    let choice: ModelTuning
    let locked: Bool
    var reported: ReportedModelSettings? = nil
    var expanded = false
    var planningRow = false
    var context = "Model for the next Camp reply"
    let selectModel: (String) -> Void
    let tune: (String?, String?) -> Void
    var effortTitle: String { reported.map { $0.effort.map(Self.effortName) ?? "Not reported" } ?? Self.effortName(choice.effort) }
    var speedTitle: String { reported.map { $0.speed.map(Self.speedName) ?? "Speed not reported" } ?? Self.speedName(choice.speed) }
    var displayedModel: String { reported == nil ? modelLabel : reported?.model ?? "Model not reported" }
    static func effortName(_ value: String) -> String {
        switch value { case "": return "Default"; case "xhigh": return "Extra high"; default: return value.capitalized }
    }
    static func speedName(_ value: String) -> String { value == "ultrafast" ? "Ultra Fast" : value.capitalized }
    var body: some View {
        if expanded {
            ViewThatFits(in: .horizontal) {
                GeometryReader { geometry in
                    let unit = max(0, geometry.size.width - 16) / 3.6
                    HStack(alignment: .top, spacing: 8) {
                        modelField.frame(width: unit * 1.6)
                        effortField.frame(width: unit)
                        speedField.frame(width: unit)
                    }
                }.frame(minWidth: 396).frame(height: 46)
                VStack(alignment: .leading, spacing: 8) {
                    modelField
                    HStack(alignment: .top, spacing: 8) { effortField; speedField }
                }.frame(minWidth: 220)
            }
        } else { compact }
    }
    private var compact: some View {
        VStack(alignment: .leading, spacing: 3) {
            if locked {
                modelLabelView.accessibilityElement(children: .ignore)
                    .accessibilityLabel("Model settings for \(title)")
                    .accessibilityValue("\(displayedModel), \(effortTitle) effort, \(speedTitle), current run")
            } else { settingsMenu }
            if !planningRow {
                Text("\(effortTitle) effort · \(speedTitle)")
                    .font(.system(size: 9)).foregroundStyle(campMuted).lineLimit(1)
            }
        }.help("\(provider) · \(displayedModel) · \(effortTitle) effort · \(speedTitle)")
    }
    var settingsMenu: some View {
        Menu {
            Text(context)
            modelOptions
            Divider()
            Menu("Effort · \(Self.effortName(choice.effort))") {
                effortOptions
            }
            Menu("Speed · \(Self.speedName(choice.speed))") {
                speedOptions
            }
        } label: {
            modelLabelView
        }
        .menuStyle(.borderlessButton).fixedSize(horizontal: false, vertical: true)
        .disabled(locked).accessibilityLabel("Model settings for \(title)")
        .accessibilityValue("\(displayedModel), \(effortTitle) effort, \(speedTitle)")
    }
    @ViewBuilder private var modelOptions: some View {
        Button("Use session model") { selectModel("") }
        if !sessionModel.isEmpty && !models.contains(where: { $0.id == sessionModel && ($0.showInPicker || provider == "Claude") }) { Button(sessionModel) { selectModel(sessionModel) } }
        if provider == "Claude" {
            Section("Latest aliases") {
                ForEach(models.filter { !$0.id.hasPrefix("claude-") && $0.showInPicker }) { option in modelOption(option) }
            }
            Section("Model versions") {
                ForEach(models.filter { $0.id.hasPrefix("claude-") }) { option in modelOption(option) }
            }
        } else {
            Section("Model versions") { ForEach(models.filter(\.showInPicker)) { option in modelOption(option) } }
        }
    }
    private func modelOption(_ option: ModelOption) -> some View {
        Button { selectModel(option.id) } label: { row(option.label, checked: option.id == modelLabel) }
    }
    @ViewBuilder private var effortOptions: some View {
        Text("More reasoning can take longer and use more tokens")
        Button { tune("", nil) } label: { row("Default", checked: choice.effort.isEmpty) }
        ForEach(caps.efforts, id: \.self) { level in
            Button { tune(level, nil) } label: { row(Self.effortName(level), checked: choice.effort == level) }
        }
        if caps.efforts.isEmpty { Text("Effort is not advertised for this model") }
    }
    @ViewBuilder private var speedOptions: some View {
        Text("Faster modes can use more allowance or credits")
        ForEach(caps.speeds, id: \.self) { speed in
            Button { tune(nil, speed) } label: { row(Self.speedName(speed), checked: choice.speed == speed) }
        }
        if provider == "Claude" && !caps.speeds.contains("fast") { Text("Fast requires a supported Opus model") }
    }
    private var modelField: some View {
        field("Model") {
            Menu { modelOptions } label: { Text(displayedModel).lineLimit(1) }
                .accessibilityLabel("Model settings for \(title)").accessibilityValue(displayedModel)
        }
    }
    private var effortField: some View {
        field("Effort") {
            Menu { effortOptions } label: { Text(effortTitle).lineLimit(1) }
                .accessibilityLabel("Effort for \(title)").accessibilityValue(effortTitle)
        }
    }
    private var speedField: some View {
        field("Speed") {
            Menu { speedOptions } label: { Text(speedTitle).lineLimit(1) }
                .accessibilityLabel("Speed for \(title)").accessibilityValue(speedTitle)
        }
    }
    private func field<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.system(size: 10)).foregroundStyle(campMuted)
            content().menuStyle(.borderlessButton).menuIndicator(.visible).font(.system(size: 11)).controlSize(.small)
                .padding(.horizontal, 8).frame(maxWidth: .infinity).frame(height: 28).background(.white.opacity(locked ? 0.45 : 1))
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(campInk.opacity(0.10))).disabled(locked)
        }
    }
    private var modelLabelView: some View {
        HStack(spacing: 5) {
            if !planningRow { ProviderMark(provider: provider, size: 13, muted: true) }
            Text(displayedModel).font(.system(size: planningRow ? 9 : 10)).foregroundStyle(campInk).lineLimit(1)
        }.frame(minHeight: 24)
    }
    @ViewBuilder func row(_ title: String, checked: Bool) -> some View {
        if checked { Label(title, systemImage: "checkmark") } else { Text(title) }
    }
}
struct MessageView: View {
    let message: Message
    var metadata: String? = nil
    var provider: String? = nil
    var fontSize: CGFloat = 13
    private var responseMetadata: String? {
        guard message.role == "assistant" else { return nil }
        if let metadata { return metadata }
        let parts = [provider, message.settingsDescription].compactMap { $0 }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(alignment: .firstTextBaseline, spacing: 7) {
                Image(systemName: message.role == "user" ? "person.crop.circle" : "sparkle")
                Text("\(Text(message.role == "user" ? "You" : "Assistant").fontWeight(.semibold))\(Text(responseMetadata.map { " · " + $0 } ?? "").font(.system(size: 10)).foregroundColor(campMuted))")
            }.font(.system(size: 11)).foregroundStyle(message.role == "user" ? campMuted : campGreen)
            Text((try? AttributedString(markdown: message.text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(message.text)).font(.system(size: fontSize)).lineSpacing(4).textSelection(.enabled)
                .lineLimit(nil).fixedSize(horizontal: false, vertical: true).frame(maxWidth: .infinity, alignment: .leading)
        }.padding(message.role == "user" ? 15 : 0).background(message.role == "user" ? Color.white.opacity(0.7) : .clear).clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
struct SourcesView: View {
    @ObservedObject var store: CampStore
    @State private var location = "Local"
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack { Mascot(); VStack(alignment: .leading, spacing: 4) { Text("Bring your sessions to camp").font(.title2); Text(location == "Local" ? "Local folders only · Credentials stay with each CLI" : location == "GitHub" ? "Your GitHub account and repository access" : "Separate provider sign-ins on your Camp server").foregroundStyle(campMuted).font(.caption) } }
            CampSegmentedControl(options: [("Local", "Local profiles"), ("Cloud", "Cloud accounts"), ("GitHub", "GitHub")], selection: $location).accessibilityLabel("Source location")
            ScrollView {
                if location == "Local" {
                    VStack(alignment: .leading, spacing: 14) {
                        LocalProfileList(sources: store.sources)
                        Text("Add another account’s CODEX_HOME or CLAUDE_CONFIG_DIR folder. Cloud-only chats and unreadable macOS user folders are not imported. A saved chat’s author may differ from the account currently logged into that profile.").font(.system(size: 12)).foregroundStyle(campMuted)
                        if !store.sourceError.isEmpty { Text(store.sourceError).font(.system(size: 12)).foregroundStyle(.orange) }
                    }
                } else if location == "Cloud" { CampProviderAccountsView(cloud: store.cloud) }
                else { CampGitConnectionsView(cloud: store.cloud) }
            }.frame(maxHeight: .infinity)
            HStack { if location == "Local" { Button("Add profile folder") { store.addSource() } }; Spacer(); Button("Done") { store.sourceSheet = false }.keyboardShortcut(.defaultAction) }
        }.padding(28).frame(width: 640, height: 640).background(campCream).foregroundStyle(campInk).tint(campGreen)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    @MainActor static var reopen: (() -> Void)?
    @MainActor static var prepareToQuit: (() async -> Void)?
    private var quitting = false
    @MainActor static func traceShutdown(_ stage: String) {
        #if CAMP_DEVELOPMENT
        if ProcessInfo.processInfo.environment["CAMP_DEV_SHUTDOWN_TRACE"] == "1" {
            try? FileHandle.standardError.write(contentsOf: Data("Camp shutdown: \(stage)\n".utf8))
        }
        #endif
    }
    func applicationWillFinishLaunching(_ notification: Notification) {
        MacNoticeDelivery.shared.install()
        Self.traceShutdown("installed")
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { if !flag { Self.reopen?() }; return true }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        Self.traceShutdown("termination requested")
        guard !quitting else { return .terminateLater }
        guard let prepare = Self.prepareToQuit else { return .terminateNow }
        quitting = true
        Task {
            await prepare()
            Self.traceShutdown("prepared")
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }
    func applicationWillTerminate(_ notification: Notification) {
        Self.traceShutdown("terminated")
        for process in Bridge.activeProcesses.values where process.isRunning { process.terminate() }
    }
}
#if !CAMP_TESTING
@main struct CapybaraCampApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @StateObject private var store = CampStore()
    var body: some Scene {
        Window("Capybara Camp", id: "camp") { RootView(store: store).preferredColorScheme(.light) }
            .windowStyle(.hiddenTitleBar).defaultSize(width: 1370, height: 860)
            .commands {
                CommandGroup(after: .appSettings) {
                    Button("Set up Capybara Camp…") { AppDelegate.reopen?(); store.onboarding.open() }
                }
            }
    }
}
#endif
