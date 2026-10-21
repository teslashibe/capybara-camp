import SwiftUI
import AppKit
import Darwin

struct CampSession: Codable, Identifiable, Equatable {
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
    var project: String { URL(fileURLWithPath: cwd).lastPathComponent }
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
}
struct ModelOption: Codable, Identifiable {
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
struct Message: Codable, Identifiable, Equatable { let id: String; let role: String; let text: String }
struct QueuedPrompt: Codable, Identifiable {
    let id: String
    let sessionID: String
    let text: String
    let model: String
    let effort: String
    let speed: String
    var state = "waiting"
}
struct Transcript: Codable { let messages: [Message]; let total: Int }
enum Stage: String, CaseIterable, Codable {
    case ready = "Ready", progress = "In progress", attention = "Needs you", done = "Done"
    var color: Color {
        switch self { case .ready: return Color(hex: 0x91856F); case .progress: return Color(hex: 0x62816B); case .attention: return Color(hex: 0xC18549); case .done: return Color(hex: 0x77969C) }
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
    static var activeProcess: Process?
    static let support = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Capybara Camp")
    static let python: String = {
        let candidates = ["/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"] +
            (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":").map { String($0) + "/python3" }
        for candidate in candidates where FileManager.default.isExecutableFile(atPath: candidate) {
            let probe = Process()
            probe.executableURL = URL(fileURLWithPath: candidate)
            probe.arguments = ["-c", "import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)"]
            probe.standardOutput = FileHandle.nullDevice; probe.standardError = FileHandle.nullDevice
            do { try probe.run(); probe.waitUntilExit(); if probe.terminationStatus == 0 { return candidate } } catch { continue }
        }
        return ""
    }()
    static func process(_ action: String) throws -> Process {
        let p = Process()
        guard let script = Bundle.main.url(forResource: "bridge", withExtension: "py") else { throw CampError.message("The local session adapter is missing. Rebuild the app.") }
        guard FileManager.default.isExecutableFile(atPath: python) else { throw CampError.message("Install Python 3.11 or newer to read local sessions") }
        p.executableURL = URL(fileURLWithPath: python)
        p.arguments = [script.path, action]
        p.environment = ProcessInfo.processInfo.environment.merging(["PYTHONUNBUFFERED": "1", "PYTHONDONTWRITEBYTECODE": "1"]) { _, new in new }
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
    @Published var sessions: [CampSession] = [] { didSet { filterRevision += 1 } }
    @Published var sources: [CampSource] = []
    @Published var stages: [String: Stage] = [:]
    @Published var selectedID: String?
    @Published var sourceFilter = "all"
    @Published var search = ""
    @Published var view = "Board"
    @Published var importing = false
    @Published var loading = false
    @Published var messages: [Message] = []
    @Published var historyTotal = 0
    @Published var draft = ""
    @Published var liveText = ""
    @Published var runningID: String? { didSet { filterRevision += 1 } }
    @Published var queuedPrompts: [QueuedPrompt] = []
    @Published var queuePaused = true
    @Published var readyToSteer = false
    @Published var steering = false
    @Published var banner = ""
    @Published var sourceSheet = false
    @Published var activity: [String: LiveState] = [:] { didSet { filterRevision += 1 } }
    @Published var modelCatalogs: [String: [ModelOption]] = [:]
    @Published var modelChoices: [String: String] = [:]
    @Published var modelTuning: [String: [String: ModelTuning]] = [:]
    @Published var activityError = ""
    @Published var importError = ""
    @Published var showSubagents = false
    @Published var subagentsParent: CampSession?
    @Published var children: [ChildSession] = []
    @Published var childrenLoading = false
    private var observing = false
    private var scanning = false
    private var refreshPending = false
    private var nextImportCheck = Date.distantPast
    private var historyRequestID: String?
    private let transcriptWatch = TranscriptWatch()
    private var historyDirty = false
    private var process: Process?
    private var controlInput: FileHandle?
    private var pendingSteer: (id: String, text: String)?
    private var activeQueueID: String?
    private var runSucceeded = false
    private var pendingLiveText: String?
    private var liveFlushTask: Task<Void, Never>?
    private var historyLimit = 120
    private var readGeneration = UUID()
    private var drafts: [String: String] = [:]
    private var filterRevision = 0
    private var cachedFilterRevision = -1
    private var cachedFilterSearch = ""
    private var cachedFilterSource = ""
    private var cachedFiltered: [CampSession] = []
    private let stateDirectory: URL
    private let decoder = JSONDecoder()
    var selected: CampSession? { sessions.first { $0.id == selectedID } }
    var filtered: [CampSession] {
        if cachedFilterRevision == filterRevision && cachedFilterSearch == search && cachedFilterSource == sourceFilter { return cachedFiltered }
        cachedFiltered = sessions.filter { s in
            (sourceFilter == "all" || (sourceFilter == "active" && isBusy(s)) || s.sourceID == sourceFilter || s.provider == sourceFilter) &&
            (search.isEmpty || (s.title + " " + s.cwd + " " + s.sourceLabel).localizedCaseInsensitiveContains(search))
        }
        cachedFilterRevision = filterRevision; cachedFilterSearch = search; cachedFilterSource = sourceFilter
        return cachedFiltered
    }
    init(stateDirectory: URL = Bridge.support) {
        self.stateDirectory = stateDirectory
        try? FileManager.default.createDirectory(at: stateDirectory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("model-tuning.json")), let saved = try? decoder.decode([String: [String: ModelTuning]].self, from: data) { modelTuning = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("models.json")), let saved = try? decoder.decode([String: String].self, from: data) { modelChoices = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("board.json")), let saved = try? decoder.decode([String: Stage].self, from: data) { stages = saved }
        if let data = try? Data(contentsOf: stateDirectory.appendingPathComponent("queued-prompts.json")), let saved = try? decoder.decode([QueuedPrompt].self, from: data) {
            queuedPrompts = saved.map { item in var item = item; if item.state == "sending" { item.state = "needsReview" }; return item }
            saveQueue()
        }
    }
    func isBusy(_ s: CampSession) -> Bool { runningID == s.id || activity[s.id]?.state == "running" }
    func stage(_ session: CampSession) -> Stage {
        if isBusy(session) { return .progress }
        if let live = activity[session.id], ["waiting", "unknown"].contains(live.state) { return .attention }
        return stages[session.id] ?? (session.archived ? .done : .ready)
    }
    func modelLabel(_ s: CampSession) -> String { modelChoices[s.id] ?? (s.model.isEmpty ? "Session default" : s.model) }
    func selectModel(_ s: CampSession, _ model: String) {
        guard runningID != s.id else { return }
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
        return modelCatalogs[s.sourceID]?.first { $0.id == model } ?? ModelOption(id: model, label: model)
    }
    func tuning(_ s: CampSession) -> ModelTuning { modelTuning[s.id]?[effectiveModel(s)] ?? ModelTuning() }
    func tune(_ s: CampSession, effort: String? = nil, speed: String? = nil) {
        guard runningID != s.id else { return }
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
        pollActivity()
        observeHistory()
        if Date() >= nextImportCheck { refresh(background: true) }
    }
    func observeHistory() {
        guard view == "Chat", let s = selected else { transcriptWatch.stop(); return }
        var paths = Set<String>()
        if !s.path.isEmpty {
            paths.insert(s.path)
            paths.insert(URL(fileURLWithPath: s.path).deletingLastPathComponent().path)
        }
        if s.provider == "Codex" {
            paths.insert(s.root)
            for name in ["thread_history_1.sqlite", "state_5.sqlite"] {
                paths.insert(s.root + "/" + name)
                paths.insert(s.root + "/" + name + "-wal")
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
                let input = try JSONSerialization.data(withJSONObject: ["sources": values, "selectedID": (showSubagents ? subagentsParent?.id : selectedID) ?? ""])
                let result = try decoder.decode(ActivityResult.self, from: await Bridge.call("activity", input: input))
                applyActivity(result)
            } catch {
                activityError = "Live status unavailable: " + error.localizedDescription
                for id in activity.keys { activity[id]?.state = "unknown" }
            }
        }
    }
    func applyActivity(_ result: ActivityResult) {
        var next = Dictionary(result.activity.map { ($0.id, $0) }, uniquingKeysWith: { first, second in second.state == "running" ? second : first })
        for (id, var old) in activity where result.errors[old.sourceID] != nil { old.state = "unknown"; next[id] = old }
        if let runningID { next.removeValue(forKey: runningID) }
        let previousActivity = activity
        if activity != next { activity = next }
        for (id, previous) in previousActivity where previous.state == "running" && next[id]?.state == "idle" { move(id, .attention) }
        modelCatalogs = result.models
        activityError = result.errors.values.sorted().joined(separator: " · ")
        var indexes = Dictionary(uniqueKeysWithValues: sessions.enumerated().map { ($0.element.id, $0.offset) })
        for item in result.sessions {
            if let index = indexes[item.id] { sessions[index].title = item.title; sessions[index].model = item.model }
            else { indexes[item.id] = sessions.count; sessions.append(item) }
        }
        if showSubagents, let parent = subagentsParent, let live = next[parent.id] { children = live.children }
        if let selected = selected, let live = next[selected.id], !showSubagents { children = live.children }
    }
    func loadChildren(_ s: CampSession) {
        subagentsParent = s; children = activity[s.id]?.children ?? []; showSubagents = true; childrenLoading = true
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
        let failedRoots = Set(result.failedRoots ?? [])
        let importedIDs = Set(result.sessions.map(\.id))
        // A temporary read failure must not remove chats or interrupt the open one.
        let retained = sessions.filter { !importedIDs.contains($0.id) &&
            (failedRoots.contains($0.root) || $0.id == selectedID || $0.id == runningID) }
        let updated = (result.sessions + retained).sorted { $0.updated > $1.updated }
        if sessions != updated { sessions = updated }
        var updatedSources = result.sources
        for old in sources where failedRoots.contains(old.root) {
            updatedSources.removeAll { $0.id == old.id }
            updatedSources.append(old)
        }
        if sources != updatedSources { sources = updatedSources }
        importError = result.errors.joined(separator: " · ")
        if selectedID == nil { selectedID = sessions.first?.id }
        observeHistory()
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
        guard runningID == nil || runningID == session.id else { banner = "Stop or finish the current reply before opening another chat"; return }
        if selectedID != session.id {
            if let previous = selectedID { drafts[previous] = draft }
            draft = drafts[session.id] ?? ""
            messages = []; historyTotal = 0; historyLimit = 120; banner = ""
        }
        selectedID = session.id; children = activity[session.id]?.children ?? []; view = "Chat"; observeHistory(); loadHistory()
    }
    func applyHistory(_ result: Transcript, sessionID: String) {
        guard selectedID == sessionID, runningID == nil else { return }
        if messages != result.messages { messages = result.messages }
        if historyTotal != result.total { historyTotal = result.total }
    }
    func loadHistory(more: Bool = false, silent: Bool = false) {
        guard let s = selected, runningID == nil else { return }
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
    func object(_ s: CampSession) -> [String: Any] { (try? JSONSerialization.jsonObject(with: JSONEncoder().encode(s))) as? [String: Any] ?? [:] }
    func send() {
        guard let s = selected, runningID == nil, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        guard activity[s.id] == nil else { banner = "This session is open in \(s.provider). Close it there before sending from Camp."; return }
        let prompt = draft
        draft = ""
        start(s, prompt: prompt, model: modelChoices[s.id] ?? "", tuning: tuning(s))
    }
    func start(_ s: CampSession, prompt: String, model: String, tuning: ModelTuning, queuedID: String? = nil) {
        let p: Process
        do { p = try Bridge.process("conversation") } catch { draft = prompt; banner = error.localizedDescription; queuePaused = true; return }
        drafts[s.id] = nil; liveText = ""; banner = ""; runningID = s.id
        readyToSteer = false; runSucceeded = false; activeQueueID = queuedID
        readGeneration = UUID(); loading = false; historyRequestID = nil; historyDirty = false
        messages.append(Message(id: UUID().uuidString, role: "user", text: prompt))
        let out = Pipe(), err = Pipe(), stdin = Pipe()
        p.standardOutput = out; p.standardError = err; p.standardInput = stdin
        process = p
        Bridge.activeProcess = p
        controlInput = stdin.fileHandleForWriting
        do {
            try p.run()
        } catch { runningID = nil; process = nil; controlInput = nil; Bridge.activeProcess = nil; draft = prompt; banner = error.localizedDescription; queuePaused = true; markUncertainQueue(); return }
        var input = (try? JSONSerialization.data(withJSONObject: ["session": object(s), "prompt": prompt, "model": model, "effort": tuning.effort, "speed": tuning.speed])) ?? Data()
        input.append(10)
        Task.detached { [weak self] in
            let errors = Task.detached { err.fileHandleForReading.readDataToEndOfFile() }
            let writer = Task.detached { try? stdin.fileHandleForWriting.write(contentsOf: input) }
            var buffer = Data()
            var terminal = false
            while let chunk = try? out.fileHandleForReading.read(upToCount: 4096), !chunk.isEmpty {
                buffer.append(chunk)
                while let end = buffer.firstIndex(of: 10) {
                    let line = Data(buffer[..<end]); buffer.removeSubrange(...end)
                    if let event = (try? JSONSerialization.jsonObject(with: line)) as? [String: Any] {
                        let type = event["type"] as? String ?? ""
                        if ["done", "error", "cancelled"].contains(type) { terminal = true }
                        let text = event["text"] as? String ?? event["message"] as? String ?? ""
                        await self?.receive(type, text: text, session: s, requestID: event["requestID"] as? String)
                    }
                }
            }
            p.waitUntilExit()
            _ = await writer.value
            let errorData = await errors.value
            await self?.finished(s, code: p.terminationStatus, completed: terminal, diagnostics: String(data: errorData.suffix(4000), encoding: .utf8) ?? "")
        }
    }
    func receive(_ type: String, text: String, session: CampSession, requestID: String? = nil) {
        if type == "started" {
            readyToSteer = true
            if let activeQueueID { queuedPrompts.removeAll { $0.id == activeQueueID }; saveQueue() }
        }
        if type == "text" {
            pendingLiveText = text
            if liveFlushTask == nil {
                liveFlushTask = Task { [weak self] in
                    try? await Task.sleep(for: .milliseconds(150))
                    guard !Task.isCancelled else { return }
                    self?.flushLiveText()
                }
            }
        }
        if type == "boundary" { finishLiveMessage() }
        if type == "steerAccepted", let pending = pendingSteer, pending.id == requestID {
            messages.append(Message(id: pending.id, role: "user", text: pending.text))
            pendingSteer = nil; steering = false
        }
        if type == "steerRejected", pendingSteer?.id == requestID { restoreSteer(); banner = text }
        if type == "done" { runSucceeded = true }
        if type == "notice" { banner = text }
        if type == "error" || type == "cancelled" { banner = text; stages[session.id] = .attention }
    }
    func finishLiveMessage() {
        flushLiveText()
        if !liveText.isEmpty { messages.append(Message(id: UUID().uuidString, role: "assistant", text: liveText)) }
        liveText = ""
    }
    func flushLiveText() {
        liveFlushTask?.cancel(); liveFlushTask = nil
        if let pendingLiveText { liveText = pendingLiveText; self.pendingLiveText = nil }
    }
    func finished(_ session: CampSession, code: Int32, completed: Bool, diagnostics: String) {
        finishLiveMessage(); restoreSteer()
        runningID = nil; process = nil; readyToSteer = false; controlInput = nil; Bridge.activeProcess = nil
        if (code != 0 || !completed) && banner.isEmpty { banner = diagnostics.isEmpty ? "The local CLI exited before finishing. Refresh history before retrying." : diagnostics }
        if code != 0 || !runSucceeded { queuePaused = true; markUncertainQueue() }
        activeQueueID = nil
        move(session.id, .attention)
        loadHistory()
        if code == 0 && runSucceeded { runNextQueued() }
    }
    func stop() { queuePaused = true; if let process, process.isRunning { process.terminate() } }
    func steerDraft() {
        guard let s = selected, runningID == s.id, readyToSteer, !steering, let input = controlInput, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let pending = (id: UUID().uuidString, text: draft)
        do {
            var data = try JSONSerialization.data(withJSONObject: ["type": "steer", "id": pending.id, "text": pending.text])
            data.append(10)
            try input.write(contentsOf: data)
            pendingSteer = pending; steering = true; draft = ""
        } catch { banner = "Could not send steering: " + error.localizedDescription }
    }
    func restoreSteer() {
        if let pending = pendingSteer { draft = draft.isEmpty ? pending.text : pending.text + "\n\n" + draft }
        pendingSteer = nil; steering = false
    }
    func enqueueDraft() {
        guard let s = selected, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let choice = tuning(s)
        queuedPrompts.append(QueuedPrompt(id: UUID().uuidString, sessionID: s.id, text: draft, model: modelChoices[s.id] ?? "", effort: choice.effort, speed: choice.speed))
        draft = ""; queuePaused = runningID != s.id
        saveQueue()
    }
    func queued(_ s: CampSession) -> [QueuedPrompt] { queuedPrompts.filter { $0.sessionID == s.id } }
    func saveQueue() {
        do {
            let url = stateDirectory.appendingPathComponent("queued-prompts.json")
            try JSONEncoder().encode(queuedPrompts).write(to: url, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        } catch { queuePaused = true; banner = "Could not save the prompt queue: " + error.localizedDescription }
    }
    func markUncertainQueue() {
        if let activeQueueID, let index = queuedPrompts.firstIndex(where: { $0.id == activeQueueID }) { queuedPrompts[index].state = "needsReview"; saveQueue() }
    }
    func editQueued(_ item: QueuedPrompt) {
        guard item.state != "sending" else { return }
        draft = draft.isEmpty ? item.text : draft + "\n\n" + item.text
        queuedPrompts.removeAll { $0.id == item.id }; saveQueue()
    }
    func removeQueued(_ item: QueuedPrompt) {
        guard item.state != "sending" else { return }
        queuedPrompts.removeAll { $0.id == item.id }; saveQueue()
    }
    func resumeQueue() { queuePaused = false; runNextQueued() }
    func runNextQueued() {
        guard !queuePaused, runningID == nil, let s = selected, let item = queued(s).first else { return }
        guard item.state == "waiting" else { queuePaused = true; banner = "Check the transcript before editing and resubmitting the unconfirmed prompt"; return }
        guard activity[s.id] == nil else { queuePaused = true; banner = "Close this session in \(s.provider) before resuming its Camp queue"; return }
        if let index = queuedPrompts.firstIndex(where: { $0.id == item.id }) { queuedPrompts[index].state = "sending"; saveQueue() }
        guard !queuePaused else { return }
        start(s, prompt: item.text, model: item.model, tuning: ModelTuning(effort: item.effort, speed: item.speed), queuedID: item.id)
    }
    func addSource() {
        let panel = NSOpenPanel(); panel.canChooseDirectories = true; panel.canChooseFiles = false; panel.showsHiddenFiles = true
        panel.message = "Choose a Codex home or Claude config folder containing saved sessions"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { do { _ = try await Bridge.call("add-source", input: Data(url.path.utf8)); refresh() } catch { banner = error.localizedDescription } }
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
    @StateObject var store = CampStore()
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
                if store.view == "Board" { BoardView(store: store) } else { ChatView(store: store) }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }.foregroundStyle(campInk).background(campCream)
        .frame(minWidth: 1060, minHeight: 720)
        .onAppear { store.refresh() }
        .task { while !Task.isCancelled { try? await Task.sleep(for: .seconds(5)); if !Task.isCancelled { store.pollUpdates() } } }
        .onChange(of: store.view) { _, value in store.observeHistory(); if value == "Chat" { store.loadHistory() } }
        .sheet(isPresented: $store.sourceSheet) { SourcesView(store: store) }
        .sheet(isPresented: $store.showSubagents) { SubagentsView(store: store) }
    }
    var sidebar: some View {
        VStack(alignment: .leading, spacing: 22) {
            HStack(spacing: 9) { Mascot(size: 46); VStack(alignment: .leading, spacing: 2) { Text("Capybara").font(.system(size: 17, weight: .semibold)); Text("CAMP").font(.system(size: 10, weight: .semibold)).tracking(3).foregroundStyle(campMuted) } }
            VStack(alignment: .leading, spacing: 5) {
                Text("YOUR CAMP").font(.system(size: 10, weight: .semibold)).tracking(1.4).foregroundStyle(campMuted).padding(.bottom, 8)
                sourceButton("All sessions", id: "all", icon: "square.grid.2x2", count: store.sessions.count)
                sourceButton("Active now", id: "active", icon: "waveform.path", count: store.sessions.filter { store.isBusy($0) }.count)
                sourceButton("Codex", id: "Codex", icon: "terminal", count: store.sessions.filter { $0.provider == "Codex" }.count)
                sourceButton("Claude", id: "Claude", icon: "asterisk", count: store.sessions.filter { $0.provider == "Claude" }.count)
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
            Spacer()
            VStack(alignment: .leading, spacing: 8) {
                HStack { Circle().fill(campGreen).frame(width: 6, height: 6); Text("Local on this Mac").font(.system(size: 11, weight: .medium)) }
                Text([store.importError, store.activityError].filter { !$0.isEmpty }.joined(separator: " · ").isEmpty ? "Live transcript updates · New sessions every 30s" : [store.importError, store.activityError].filter { !$0.isEmpty }.joined(separator: " · ")).font(.system(size: 11)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
                Button("Manage sources") { store.sourceSheet = true }.buttonStyle(.plain).font(.system(size: 11)).foregroundStyle(campGreen)
            }.padding(14).background(.white.opacity(0.65)).clipShape(RoundedRectangle(cornerRadius: 12))
        }.padding(22).frame(width: 224).frame(maxHeight: .infinity).background(Color(hex: 0xEFEEE6))
    }
    func sourceButton(_ title: String, id: String, icon: String, count: Int) -> some View {
        Button { store.sourceFilter = id } label: {
            HStack { Image(systemName: icon).frame(width: 18); Text(title); Spacer(); Text(count.formatted()).font(.system(size: 10)).foregroundStyle(campMuted) }
                .font(.system(size: 12, weight: .medium)).padding(10).background(store.sourceFilter == id ? .white.opacity(0.9) : .clear).clipShape(RoundedRectangle(cornerRadius: 8))
        }.buttonStyle(.plain)
    }
    var header: some View {
        VStack(alignment: .leading, spacing: 19) {
            HStack {
                VStack(alignment: .leading, spacing: 5) { Text(store.view == "Board" ? "Your Camp for big ideas" : store.selected?.title ?? "Choose a session").font(.system(size: 23, weight: .semibold)).lineLimit(1); Text(store.view == "Board" ? "\(store.filtered.count.formatted()) \(store.filtered.count == 1 ? "session" : "sessions") · Pick up where you left off" : "\(store.selected?.provider ?? "") · \(store.selected?.project ?? "")").font(.system(size: 12)).foregroundStyle(campMuted) }
                Spacer()
                Button { store.refresh() } label: { if store.importing { ProgressView().controlSize(.small) } else { Label("Refresh", systemImage: "arrow.clockwise").font(.system(size: 12)) } }.disabled(store.importing)
            }
            HStack {
                Picker("View", selection: $store.view) { Text("Board").tag("Board"); Text("Chat").tag("Chat") }.pickerStyle(.segmented).frame(width: 180)
                Spacer()
                HStack { Image(systemName: "magnifyingglass"); TextField("Find a session or project", text: $store.search).textFieldStyle(.plain) }.font(.system(size: 12)).foregroundStyle(campMuted).padding(9).frame(width: 255).background(.white.opacity(0.8)).clipShape(RoundedRectangle(cornerRadius: 8))
            }
        }.padding(25)
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
    var items: [CampSession] { store.filtered.filter { store.stage($0) == stage } }
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack { Circle().fill(stage.color).frame(width: 7, height: 7); Text(stage.rawValue).font(.system(size: 12, weight: .semibold)); Text(items.count.formatted()).font(.system(size: 10)).foregroundStyle(campMuted); Spacer() }.padding(.horizontal, 4)
            ScrollView {
                LazyVStack(spacing: 10) {
                    ForEach(items.prefix(count)) { s in
                        SessionCard(store: store, session: s)
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
    var live: LiveState? { store.activity[session.id] }
    var running: Bool { store.isBusy(session) }
    var statusText: String {
        guard let live else { return "Running in Camp" }
        return live.state == "running" ? "Running in \(live.owner)" : live.state == "idle" ? "Open in \(live.owner)" : "\(live.owner) · \(live.state)"
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text(session.provider.uppercased()).font(.system(size: 9, weight: .semibold)).tracking(0.7).foregroundStyle(session.provider == "Codex" ? campGreen : Color(hex: 0xB57554))
                Spacer()
                Menu { ForEach(Stage.allCases, id: \.self) { dest in Button("Move to \(dest.rawValue)") { store.move(session.id, dest) } } } label: { Image(systemName: "ellipsis").frame(width: 24, height: 20) }
                    .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().accessibilityLabel("Move \(session.title)")
            }
            Button { store.open(session) } label: {
                VStack(alignment: .leading, spacing: 10) {
                    Text(session.title).font(.system(size: 13, weight: .medium)).lineLimit(3).multilineTextAlignment(.leading).frame(maxWidth: .infinity, alignment: .leading)
                    HStack(spacing: 4) { Image(systemName: "folder"); Text(session.project.isEmpty ? "No project" : session.project).lineLimit(1) }.font(.system(size: 10)).foregroundStyle(campMuted)
                    HStack { Text(session.sourceLabel).lineLimit(1); Spacer(); Text(Date(timeIntervalSince1970: session.updated), style: .relative).lineLimit(1) }.font(.system(size: 9)).foregroundStyle(campMuted)
                }.frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
            }.buttonStyle(.plain)
            Divider().overlay(campInk.opacity(0.04))
            ModelMenu(store: store, session: session).frame(maxWidth: .infinity, alignment: .leading)
            Button { store.loadChildren(session) } label: {
                HStack { Label("Subagents", systemImage: "person.2"); Spacer(); Image(systemName: "chevron.right").font(.system(size: 8, weight: .semibold)) }.contentShape(Rectangle())
            }.buttonStyle(.plain).font(.system(size: 10)).foregroundStyle(campMuted).help("Show subagents")
            if !store.queued(session).isEmpty {
                Label("\(store.queued(session).count) queued", systemImage: "text.line.first.and.arrowtriangle.forward").font(.system(size: 10)).foregroundStyle(campGreen)
            }
            if running || live != nil {
                VStack(alignment: .leading, spacing: 7) {
                    HStack(spacing: 7) {
                        if running { ProgressView().controlSize(.mini).frame(width: 12, height: 12).accessibilityLabel("Session running") }
                        else { Circle().fill(campGreen).frame(width: 5, height: 5) }
                        Text(statusText).fixedSize(horizontal: false, vertical: true)
                    }.foregroundStyle(campGreen)
                    ForEach((live?.children ?? []).filter { $0.status == "running" }.prefix(3)) { child in
                        Label("\(child.name) · \(child.phase)", systemImage: "arrow.turn.down.right").lineLimit(2).fixedSize(horizontal: false, vertical: true)
                    }
                }.font(.system(size: 10)).frame(maxWidth: .infinity, alignment: .leading).padding(9).background(campGreen.opacity(0.07)).clipShape(RoundedRectangle(cornerRadius: 7))
            }
        }.padding(14).frame(maxWidth: .infinity, alignment: .leading).background(.white).clipShape(RoundedRectangle(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(campInk.opacity(0.08)))
            .contextMenu { ForEach(Stage.allCases, id: \.self) { dest in Button("Move to \(dest.rawValue)") { store.move(session.id, dest) } } }
    }
}

struct TranscriptBottomKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}
struct ChatView: View {
    @ObservedObject var store: CampStore
    @State private var followsLatest = true
    var body: some View {
        HStack(spacing: 0) {
            ScrollView {
                LazyVStack(spacing: 5) {
                    ForEach(store.filtered.prefix(250)) { s in Button { store.open(s) } label: {
                        VStack(alignment: .leading, spacing: 5) { Text(s.title).font(.system(size: 12, weight: .medium)).lineLimit(2); Text("\(s.provider) · \(s.project)").font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1) }.frame(maxWidth: .infinity, alignment: .leading).padding(12).background(store.selectedID == s.id ? campGreen.opacity(0.10) : .clear).clipShape(RoundedRectangle(cornerRadius: 8))
                    }.buttonStyle(.plain) }
                    if store.filtered.count > 250 { Text("Search to narrow \(store.filtered.count.formatted()) sessions").font(.caption).foregroundStyle(campMuted).padding() }
                }.padding(10)
            }.frame(width: 230)
            Rectangle().fill(campInk.opacity(0.08)).frame(width: 1)
            VStack(spacing: 0) {
                if let s = store.selected {
                    HStack { ModelMenu(store: store, session: s); Button { store.loadChildren(s) } label: { Label("Subagents", systemImage: "person.2") }.buttonStyle(.plain).font(.system(size: 11)); Spacer() }.padding(.horizontal, 22).padding(.vertical, 8)
                    if let live = store.activity[s.id] { Text("\(live.owner) · \(live.state) · Model choice applies to the next Camp reply").font(.system(size: 11)).foregroundStyle(campGreen).padding(.bottom, 8) }
                    HStack { Text(s.sourceLabel).font(.system(size: 10, weight: .medium)); if !s.accountID.isEmpty { Text("· author \(s.accountID.prefix(8))").font(.system(size: 10)).foregroundStyle(campMuted) }; Spacer(); Menu(store.stage(s).rawValue) { ForEach(Stage.allCases, id: \.self) { stage in Button(stage.rawValue) { store.move(s.id, stage) } } }.fixedSize() }.padding(.horizontal, 22).padding(.vertical, 10)
                    GeometryReader { viewport in
                      ScrollViewReader { proxy in
                        ScrollView {
                            // Pages are bounded to 120 messages; measure their real heights so
                            // scrolling to the end cannot land in a lazy stack's estimated gap.
                            VStack(alignment: .leading, spacing: 22) {
                                if store.historyTotal > store.messages.count { Button("Load earlier messages (\(store.historyTotal - store.messages.count))") { store.loadHistory(more: true) }.font(.caption) }
                                if store.loading { ProgressView().controlSize(.small) }
                                ForEach(store.messages) { m in MessageView(message: m) }
                                if store.runningID == s.id { MessageView(message: Message(id: "live", role: "assistant", text: store.liveText.isEmpty ? "Thinking…" : store.liveText)) }
                                Color.clear.frame(height: 1).id("bottom")
                                    .background(GeometryReader { geometry in
                                        Color.clear.preference(key: TranscriptBottomKey.self, value: geometry.frame(in: .named("transcript")).maxY)
                                    })
                            }.padding(25)
                        }.coordinateSpace(name: "transcript")
                        .onPreferenceChange(TranscriptBottomKey.self) { bottom in followsLatest = bottom <= viewport.size.height + 60 }
                        .onReceive(store.$messages) { new in
                            // Published sends before the new message changes the measured
                            // bottom. Capture the reader's position before that layout pass.
                            let old = store.messages
                            if old.isEmpty || (followsLatest && old.first?.id == new.first?.id) {
                                DispatchQueue.main.async { if store.selectedID == s.id { proxy.scrollTo("bottom", anchor: .bottom) } }
                            }
                        }
                        .onReceive(store.$liveText) { _ in
                            if followsLatest {
                                DispatchQueue.main.async { if store.selectedID == s.id { proxy.scrollTo("bottom", anchor: .bottom) } }
                            }
                        }
                        .onChange(of: store.selectedID) { _, _ in followsLatest = true }
                      }
                    }
                    VStack(alignment: .leading, spacing: 9) {
                        PromptQueueView(store: store, session: s)
                        TextEditor(text: $store.draft).font(.system(size: 13)).scrollContentBackground(.hidden).frame(minHeight: 55, maxHeight: 100).padding(8).background(.white).clipShape(RoundedRectangle(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(campInk.opacity(0.13)))
                        HStack(spacing: 10) {
                            Text(store.runningID == s.id ? "Steer now or queue for after this turn" : store.activity[s.id] == nil ? "Uses \(s.sourceLabel) login · Chat-only local preview" : "Open in \(s.provider) · Camp queue waits until it is closed")
                                .font(.system(size: 10)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
                            Spacer(minLength: 8)
                            if store.runningID == s.id || store.activity[s.id] != nil {
                                Button("Queue") { store.enqueueDraft() }.disabled(store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty).keyboardShortcut(.return, modifiers: [.command, .shift])
                            }
                            if store.runningID == s.id {
                                Button("Stop", role: .destructive) { store.stop() }
                                Button(store.steering ? "Sending…" : "Steer") { store.steerDraft() }.buttonStyle(.borderedProminent).tint(campGreen)
                                    .disabled(!store.readyToSteer || store.steering || store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty).keyboardShortcut(.return, modifiers: .command)
                                    .help(s.provider == "Codex" ? "Add input to the current turn" : "Interrupt the current response and send the new direction")
                            } else {
                                Button { store.send() } label: { Label("Send", systemImage: "arrow.up") }.buttonStyle(.borderedProminent).tint(campGreen)
                                    .disabled(store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || store.loading || store.activity[s.id] != nil).keyboardShortcut(.return, modifiers: .command)
                            }
                        }
                    }.padding(20)
                } else { Spacer(); Mascot(size: 120); Text("Choose a session to continue").foregroundStyle(campMuted); Spacer() }
            }.frame(maxWidth: .infinity)
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
                        Button("Resume queue") { store.resumeQueue() }.disabled(store.runningID != nil || store.activity[session.id] != nil)
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
    var caps: ModelOption { store.capabilities(session) }
    var choice: ModelTuning { store.tuning(session) }
    var effortTitle: String { Self.effortName(choice.effort) }
    static func effortName(_ value: String) -> String {
        switch value { case "": return "Default"; case "xhigh": return "Extra high"; default: return value.capitalized }
    }
    static func speedName(_ value: String) -> String { value == "ultrafast" ? "Ultra Fast" : value.capitalized }
    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            settingsMenu
            Text("\(effortTitle) effort · \(Self.speedName(choice.speed))")
                .font(.system(size: 9)).foregroundStyle(campMuted).lineLimit(1)
        }
    }
    var settingsMenu: some View {
        Menu {
            Text("Model for the next Camp reply")
            Button("Use session model") { store.selectModel(session, "") }
            if !session.model.isEmpty { Button(session.model) { store.selectModel(session, session.model) } }
            ForEach((store.modelCatalogs[session.sourceID] ?? []).filter(\.showInPicker)) { option in
                Button(option.label) { store.selectModel(session, option.id) }
            }
            Divider()
            Menu("Effort · \(effortTitle)") {
                Text("More reasoning can take longer and use more tokens")
                Button { store.tune(session, effort: "") } label: { row("Default", checked: choice.effort.isEmpty) }
                ForEach(caps.efforts, id: \.self) { level in
                    Button { store.tune(session, effort: level) } label: { row(Self.effortName(level), checked: choice.effort == level) }
                }
                if caps.efforts.isEmpty { Text("Effort is not advertised for this model") }
            }
            Menu("Speed · \(Self.speedName(choice.speed))") {
                Text("Faster modes can use more allowance or credits")
                ForEach(caps.speeds, id: \.self) { speed in
                    Button { store.tune(session, speed: speed) } label: { row(Self.speedName(speed), checked: choice.speed == speed) }
                }
                if session.provider == "Claude" && !caps.speeds.contains("fast") { Text("Fast requires a supported Opus model") }
            }
        } label: {
            Label(store.modelLabel(session), systemImage: "cpu").font(.system(size: 10)).lineLimit(1)
        }
        .menuStyle(.borderlessButton).fixedSize(horizontal: false, vertical: true)
        .disabled(store.runningID == session.id).accessibilityLabel("Model settings for \(session.title)")
        .accessibilityValue("\(store.modelLabel(session)), \(effortTitle) effort, \(Self.speedName(choice.speed)) speed")
    }
    @ViewBuilder func row(_ title: String, checked: Bool) -> some View {
        if checked { Label(title, systemImage: "checkmark") } else { Text(title) }
    }
}
struct SubagentsView: View {
    @ObservedObject var store: CampStore
    @State private var picked: ChildSession?
    @State private var messages: [Message] = []
    @State private var error = ""
    var currentChild: ChildSession? { store.children.first { $0.id == picked?.id } ?? picked }
    var loadKey: String { "\(currentChild?.id ?? ""):\(currentChild?.updated ?? 0)" }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack { Text("Subagents (\(store.children.count))").font(.title2); Spacer(); if store.childrenLoading { ProgressView().controlSize(.small) }; Button("Done") { store.showSubagents = false } }
            Text("\(store.subagentsParent?.title ?? "Session") · Read-only transcripts").foregroundStyle(campMuted).lineLimit(1)
            HStack(alignment: .top) {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 12) {
                        ForEach(store.children) { child in
                            Button { picked = child } label: {
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(child.name).fontWeight(.medium)
                                    Text("\(child.status) · \(child.phase)").foregroundStyle(child.status == "running" ? campGreen : campMuted)
                                    Text(child.model).font(.caption).foregroundStyle(campMuted)
                                    Text(child.workflow).font(.caption).foregroundStyle(campMuted)
                                }.frame(maxWidth: .infinity, alignment: .leading).padding(10).background(picked?.id == child.id ? campGreen.opacity(0.1) : .white).clipShape(RoundedRectangle(cornerRadius: 8))
                            }.buttonStyle(.plain)
                        }
                        if store.children.isEmpty && !store.childrenLoading { Text("No saved subagents for this session").foregroundStyle(campMuted) }
                    }
                }.frame(width: 245)
                Divider()
                ScrollView { VStack(alignment: .leading, spacing: 18) {
                    if picked == nil { Text("Select a subagent to read its latest messages").foregroundStyle(campMuted) }
                    if !error.isEmpty { Text(error) }
                    ForEach(messages) { MessageView(message: $0) }
                }.padding(12).frame(maxWidth: .infinity, alignment: .leading) }
            }
        }.padding(22).frame(width: 880, height: 620).background(campCream)
        .task(id: loadKey) {
            messages = []; error = ""
            guard let child = currentChild, let parent = store.subagentsParent else { return }
            do {
                var value = store.object(parent); value["path"] = child.path; value["sessionID"] = child.sessionID; value["isSubagent"] = true
                let input = try JSONSerialization.data(withJSONObject: ["session": value, "limit": 120])
                let result = try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: input))
                if picked?.id == child.id { messages = result.messages }
            } catch { self.error = error.localizedDescription }
        }
    }
}
struct MessageView: View {
    let message: Message
    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(spacing: 7) { Image(systemName: message.role == "user" ? "person.crop.circle" : "sparkle"); Text(message.role == "user" ? "You" : "Assistant").fontWeight(.semibold) }.font(.system(size: 11)).foregroundStyle(message.role == "user" ? campMuted : campGreen)
            Text(verbatim: message.text).font(.system(size: 13)).lineSpacing(4).textSelection(.enabled)
                .lineLimit(nil).fixedSize(horizontal: false, vertical: true).frame(maxWidth: .infinity, alignment: .leading)
        }.padding(message.role == "user" ? 15 : 0).background(message.role == "user" ? Color.white.opacity(0.7) : .clear).clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
struct SourcesView: View {
    @ObservedObject var store: CampStore
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack { Mascot(); VStack(alignment: .leading, spacing: 4) { Text("Bring your sessions to camp").font(.title2); Text("Local folders only · Credentials stay with each CLI").foregroundStyle(campMuted).font(.caption) } }
            ScrollView { VStack(spacing: 10) { ForEach(store.sources) { s in VStack(alignment: .leading, spacing: 5) { HStack { Text(s.provider + " · " + s.label).fontWeight(.medium); Spacer(); Text(s.count.formatted()).foregroundStyle(campMuted) }; Text(s.root).font(.system(size: 11, design: .monospaced)).textSelection(.enabled).foregroundStyle(campMuted) }.padding(13).background(campCream).clipShape(RoundedRectangle(cornerRadius: 10)) } } }.frame(maxHeight: 280)
            Text("Add another account’s CODEX_HOME or CLAUDE_CONFIG_DIR folder. Cloud-only chats and unreadable macOS user folders are not imported. A saved chat’s author may differ from the account currently logged into that profile.").font(.system(size: 12)).foregroundStyle(campMuted)
            HStack { Button("Add profile folder") { store.addSource() }; Spacer(); Button("Done") { store.sourceSheet = false }.keyboardShortcut(.defaultAction) }
        }.padding(28).frame(width: 600)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
    func applicationWillTerminate(_ notification: Notification) {
        if let process = Bridge.activeProcess, process.isRunning { process.terminate() }
    }
}
#if !CAMP_TESTING
@main struct CapybaraCampApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    var body: some Scene {
        Window("Capybara Camp", id: "camp") { RootView().preferredColorScheme(.light) }
            .windowStyle(.hiddenTitleBar).defaultSize(width: 1370, height: 860)
    }
}
#endif
