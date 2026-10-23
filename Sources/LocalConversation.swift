import Foundation
import Darwin
import Combine

func conversationChunk(_ handle: FileHandle, limit: Int) throws -> Data {
    var buffer = [UInt8](repeating: 0, count: limit)
    while true {
        let count = Darwin.read(handle.fileDescriptor, &buffer, buffer.count)
        if count >= 0 { return Data(buffer.prefix(count)) }
        if errno != EINTR { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
    }
}

struct ConversationUsage: Codable, Equatable, Sendable {
    var inputTokens: Int64?
    var outputTokens: Int64?
}

enum ConversationMode: String { case chat, planning, implementation }

struct ConversationResult: Sendable {
    let state: String
    let message: String
    let accepted: Bool
    var output = ""
    var usage = ConversationUsage()
}

// A single official CLI conversation. Its state is independent of the visible
// chat, and pipe reads apply backpressure instead of queueing unbounded UI work.
@MainActor final class LocalConversation: ObservableObject {
    let id = UUID()
    let session: CampSession
    let queueID: String?
    let references: [SessionReference]
    private(set) var messages: [Message]
    private(set) var text = ""
    private(set) var ready = false
    private(set) var steering = false
    private(set) var accepted = false
    private(set) var notice = ""
    private(set) var pendingSteer: (id: String, text: String)?
    @Published private(set) var approvals: [ProviderApproval] = []
    @Published private(set) var answering: Set<String> = []
    private(set) var providerSessionID: String?
    private(set) var activity = ""
    private(set) var effectivePermissions = ""
    private(set) var reportedSettings = ReportedModelSettings()
    var streamedMessage: Message {
        Message(id: "live", role: "assistant", text: text.isEmpty ? "Thinking…" : text,
                model: reportedSettings.model, effort: reportedSettings.effort, speed: reportedSettings.speed)
    }
    var changed: (() -> Void)?
    var completed: ((ConversationResult) -> Void)?
    var didAccept: (() -> Void)?
    var sessionCreated: ((String) -> Void)?
    var persistSession: ((String) async throws -> Void)?
    var forwardEvent: ((Data) async throws -> Void)?
    private var process: Process?
    private var input: ConversationInput?
    private var pendingText: String?
    private var flushTask: Task<Void, Never>?
    private var stopTask: Task<Void, Never>?
    private var terminal: String?
    private var stopped = false
    private var finished = false
    private var mode = ConversationMode.chat
    private var usage = ConversationUsage()
    static let lineLimit = 4 * 1024 * 1024

    init(session: CampSession, history: [Message], prompt: String, queueID: String? = nil, references: [SessionReference] = [], promptID: String? = nil) {
        self.session = session; self.queueID = queueID
        self.references = references
        messages = history + [Message(id: promptID ?? UUID().uuidString, role: "user", text: prompt)]
    }

    func start(prompt: String, model: String, tuning: ModelTuning,
               contexts: [SessionContext] = [], mode: ConversationMode = .chat,
               executionApproved: Bool = false, newSession: Bool = false,
               executionContext: String = "", permissions: PermissionSettings? = nil,
               restoredHistory: RestoredHistoryReference? = nil,
               makeProcess: () throws -> Process = { try Bridge.process("conversation") }) throws {
        guard process == nil, !finished, !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              prompt.utf8.count <= 65536 else { throw CampError.message("Write a prompt under 64 KB") }
        self.mode = mode
        guard mode != .implementation || (executionApproved && executionContext.count == 64) else { throw CampError.message("Approve the implementation before enabling project tools") }
        guard permissions?.valid(for: session.provider) ?? true else { throw CampError.message("Invalid provider permission settings") }
        let p = try makeProcess(), out = Pipe(), err = Pipe(), stdin = Pipe()
        var object: [String: Any] = ["session": try JSONSerialization.jsonObject(with: JSONEncoder().encode(session)), "prompt": prompt, "model": model, "effort": tuning.effort, "speed": tuning.speed, "context": try JSONSerialization.jsonObject(with: JSONEncoder().encode(contexts)), "mode": mode.rawValue, "executionApproved": executionApproved, "newSession": newSession, "executionContext": executionContext]
        if let permissions { object["permissions"] = try JSONSerialization.jsonObject(with: JSONEncoder().encode(permissions)) }
        if let restoredHistory { object["restoredHistory"] = try JSONSerialization.jsonObject(with: JSONEncoder().encode(restoredHistory)) }
        var payload = try JSONSerialization.data(withJSONObject: object)
        payload.append(10)
        let initial = payload
        p.standardOutput = out; p.standardError = err; p.standardInput = stdin
        let writer = ConversationInput(stdin.fileHandleForWriting)
        try p.run()
        process = p; input = writer; Bridge.activeProcesses[id] = p
        let limit = Self.lineLimit
        Task.detached { [weak self] in
            let diagnostics = Task.detached { () -> Data in
                var tail = Data()
                while let chunk = try? conversationChunk(err.fileHandleForReading, limit: 4096), !chunk.isEmpty {
                    tail.append(chunk)
                    if tail.count > 32768 { tail = Data(tail.suffix(32768)) }
                }
                try? err.fileHandleForReading.close(); return tail
            }
            let writeTask = Task.detached { () -> Bool in
                do { try writer.write(initial); return true } catch { return false }
            }
            var buffer = Data(), protocolError = ""
            do {
                while case let chunk = try conversationChunk(out.fileHandleForReading, limit: 16384), !chunk.isEmpty {
                    buffer.append(chunk)
                    while let end = buffer.firstIndex(of: 10) {
                        guard end <= limit else { throw CampError.message("The CLI response exceeded the event size limit") }
                        let line = Data(buffer[..<end]); buffer.removeSubrange(...end)
                        if line.isEmpty { continue }
                        guard let value = try JSONSerialization.jsonObject(with: line) as? [String: Any], let type = value["type"] as? String else {
                            throw CampError.message("The local CLI returned an unreadable event")
                        }
                        if type == "session" {
                            guard newSession, mode != .planning, let id = value["sessionID"] as? String,
                                  LocalContinuationBinding.validProviderID(id) else { throw CampError.message("The CLI returned an invalid session identity") }
                            try await self?.createdSession(id)
                            try await self?.forward(line)
                            continue
                        }
                        try await self?.forward(line)
                        if type == "approval" {
                            let approval = try JSONDecoder().decode(ProviderApproval.self, from: line)
                            guard mode == .implementation, approval.valid else { throw CampError.message("The CLI returned an unsupported approval request") }
                            await self?.receive(approval)
                            continue
                        }
                        if type == "settings" {
                            await self?.receiveSettings(value)
                            continue
                        }
                        let usage = ConversationUsage(inputTokens: Self.tokenCount(value["inputTokens"]), outputTokens: Self.tokenCount(value["outputTokens"]))
                        await self?.receive(type, text: value["text"] as? String ?? value["message"] as? String ?? "", requestID: value["requestID"] as? String, usage: usage)
                    }
                    guard buffer.count <= limit else { throw CampError.message("The CLI response exceeded the event size limit") }
                }
                if !buffer.isEmpty { throw CampError.message("The CLI closed during an event") }
            } catch { protocolError = error.localizedDescription; p.terminate() }
            p.waitUntilExit()
            writer.close(); try? out.fileHandleForReading.close()
            let wrote = await writeTask.value, errors = await diagnostics.value
            await self?.finish(code: p.terminationStatus, error: protocolError.isEmpty ? (wrote ? String(data: errors.suffix(4000), encoding: .utf8) ?? "" : "The prompt could not be written to the CLI") : protocolError)
        }
    }

    private func forward(_ data: Data) async throws { try await forwardEvent?(data) }

    func deliverSharedControl(_ value: CloudControlEnvelope.Input) async throws {
        guard mode == .implementation, ready, !stopped, !finished, let input else { throw CampError.message("The implementation is no longer accepting input") }
        let data = try JSONEncoder().encode(value) + Data([10])
        try await Task.detached { try input.write(data) }.value
    }

    nonisolated private static func tokenCount(_ value: Any?) -> Int64? {
        guard let number = value as? NSNumber, String(cString: number.objCType) != "c" else { return nil }
        let count = number.doubleValue
        guard count.isFinite, count >= 0, count <= 9_007_199_254_740_991, count.rounded() == count else { return nil }
        return number.int64Value
    }
    private func receiveSettings(_ value: [String: Any]) {
        guard !finished else { return }
        func text(_ key: String) -> String? {
            guard let s = value[key] as? String, !s.isEmpty, s.utf8.count <= 160, !s.contains(where: { $0.isNewline || $0.asciiValue == 0 }) else { return nil }
            return s
        }
        reportedSettings = ReportedModelSettings(model: text("model"), effort: text("effort"), speed: text("speed"))
        changed?()
    }
    private func receive(_ type: String, text value: String, requestID: String?, usage: ConversationUsage = ConversationUsage()) {
        guard !finished, terminal != "failed" else { return }
        switch type {
        case "started": accepted = true; ready = true; didAccept?()
        case "text":
            if mode == .planning, messages.filter({ $0.role == "assistant" }).reduce(0, { $0 + $1.text.utf8.count + 2 }) + value.utf8.count > 8192 {
                terminal = "failed"; notice = "The planning response exceeded 8 KB. Restart with a shorter brief to try again."
                stop(); changed?(); return
            }
            pendingText = value
            if flushTask == nil {
                flushTask = Task { [weak self] in
                    try? await Task.sleep(for: .milliseconds(40))
                    guard !Task.isCancelled else { return }; self?.flush()
                }
            }
            return
        case "boundary": finishMessage()
        case "steerAccepted" where pendingSteer?.id == requestID:
            if let pendingSteer { messages.append(Message(id: pendingSteer.id, role: "user", text: pendingSteer.text)) }
            pendingSteer = nil; steering = false
        case "steerRejected" where pendingSteer?.id == requestID: steering = false; notice = value
        case "done": terminal = "completed"
        case "cancelled": terminal = "cancelled"; notice = value
        case "error": terminal = "failed"; notice = value
        case "notice": notice = value
        case "activity": activity = String(value.prefix(8192))
        case "permissions": effectivePermissions = String(value.prefix(1024))
        case "approvalResolved":
            approvals.removeAll { $0.id == requestID }; if let requestID { answering.remove(requestID) }
        case "approvalRejected":
            if let requestID { answering.remove(requestID) }; notice = value
        case "usage":
            // Codex chat counters are cumulative for the resumed thread. Planning
            // starts fresh, so its final counters describe this isolated step.
            if let input = usage.inputTokens { self.usage.inputTokens = input }
            if let output = usage.outputTokens { self.usage.outputTokens = output }
        default: return
        }
        changed?()
    }
    private func receive(_ approval: ProviderApproval) {
        guard !finished, !stopped, !approvals.contains(where: { $0.id == approval.id }) else { return }
        guard approvals.count < 16 else { terminal = "failed"; notice = "Too many pending approval requests"; stop(); return }
        approvals.append(approval); changed?()
    }
    private func createdSession(_ id: String) async throws {
        if let previous = providerSessionID {
            guard previous == id else { throw CampError.message("The CLI changed its conversation identity") }
            return
        }
        guard !finished else { throw CancellationError() }
        try await persistSession?(id)
        providerSessionID = id
        if !stopped { sessionCreated?(id); changed?() }
    }
    func answer(_ id: String, allow: Bool, answers: [String: [String]] = [:], decision: String? = nil) {
        guard !finished, !stopped, !answering.contains(id), let approval = approvals.first(where: { $0.id == id }),
              !allow || approval.canApprove, let input else { return }
        do {
            if let decision { guard approval.decisions?.contains(where: { $0.id == decision && $0.allows == allow }) == true else { return } }
            var value: [String: Any] = ["type": "approval", "id": id, "allow": allow, "answers": answers]
            if let decision { value["decision"] = decision }
            var data = try JSONSerialization.data(withJSONObject: value); data.append(10)
            guard data.count <= 65536 else { throw CampError.message("The answer is too long") }
            answering.insert(id); changed?()
            let payload = data
            Task.detached { [weak self] in
                do { try input.write(payload) }
                catch { await self?.receive("approvalRejected", text: "The answer could not be delivered. Check the running session before retrying.", requestID: id) }
            }
        } catch { notice = error.localizedDescription; changed?() }
    }
    private func flush() {
        flushTask?.cancel(); flushTask = nil
        if let pendingText { text = pendingText; self.pendingText = nil; changed?() }
    }
    private func finishMessage() {
        flush()
        if !text.isEmpty { messages.append(Message(id: UUID().uuidString, role: "assistant", text: text,
            model: reportedSettings.model, effort: reportedSettings.effort, speed: reportedSettings.speed)) }
        text = ""
    }
    private func finish(code: Int32, error: String) {
        guard !finished else { return }; finished = true
        stopTask?.cancel(); stopTask = nil
        finishMessage(); ready = false; steering = false
        approvals = []; answering = []
        input?.close(); input = nil; process = nil; Bridge.activeProcesses.removeValue(forKey: id)
        let state: String
        if terminal == "completed", code == 0 { state = "completed" }
        else if terminal == "cancelled" { state = "cancelled" }
        else if terminal == "failed" { state = "failed" }
        else { state = "uncertain" }
        let message: String
        if !notice.isEmpty { message = notice }
        else if state == "completed" { message = "" }
        else if state == "cancelled" { message = "Stopped" }
        else { message = error.isEmpty ? "The CLI stopped before confirming the result. Check the transcript before retrying." : error }
        completed?(ConversationResult(state: state, message: message, accepted: accepted,
            output: messages.filter { $0.role == "assistant" }.map(\.text).joined(separator: "\n\n"), usage: usage))
        changed = nil; completed = nil; didAccept = nil; sessionCreated = nil; persistSession = nil; forwardEvent = nil
    }
    func stop() {
        guard !finished, !stopped else { return }; stopped = true
        approvals = []; answering = []; changed?()
        let writer = input
        Task.detached { try? writer?.write(Data("{\"type\":\"stop\"}\n".utf8)) }
        // The bridge owns and reaps its provider process group on SIGTERM too.
        stopTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(3))
            guard !Task.isCancelled, let self, self.process?.isRunning == true else { return }
            self.process?.terminate()
        }
    }
    func steer(_ value: String) throws {
        guard mode != .planning, ready, !steering, pendingSteer == nil, !stopped, !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, value.utf8.count <= 65536, let input else {
            throw CampError.message("Wait for the active turn before steering")
        }
        let id = UUID().uuidString
        var data = try JSONSerialization.data(withJSONObject: ["type": "steer", "id": id, "text": value]); data.append(10)
        pendingSteer = (id, value); steering = true; changed?()
        let payload = data
        Task.detached { [weak self] in
            do { try input.write(payload) }
            catch { await self?.receive("steerRejected", text: "The steering message could not be delivered. Check the transcript before retrying.", requestID: id) }
        }
    }
    func takePendingSteer() -> String? {
        defer { pendingSteer = nil }
        return pendingSteer?.text
    }
}

final class ConversationInput: @unchecked Sendable {
    private static let ignoreBrokenPipe: Void = { _ = Darwin.signal(SIGPIPE, SIG_IGN) }()
    private let lock = NSLock()
    private var handle: FileHandle?
    init(_ handle: FileHandle) { _ = Self.ignoreBrokenPipe; self.handle = handle }
    func write(_ data: Data) throws {
        lock.lock(); defer { lock.unlock() }
        guard let handle else { throw CampError.message("The CLI input is closed") }
        try handle.write(contentsOf: data)
    }
    func close() { lock.lock(); defer { lock.unlock() }; try? handle?.close(); handle = nil }
}
