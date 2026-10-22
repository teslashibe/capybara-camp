import Foundation

struct SourceVerification: Decodable, Equatable, Sendable {
    var payloadSHA256: String
    var reviewSHA256: String
}

@MainActor protocol LocalSourceGuard: AnyObject {
    var active: Bool { get }
    func verify() async throws -> SourceVerification
    func stop()
}

// The Go process owns the same lock as local conversations. Keep it alive
// through the source acknowledgement until the server releases its hold.
@MainActor final class LocalTransferHold: LocalSourceGuard {
    private let id = UUID()
    private let session: CampSession
    private var payload: Data?
    private let deadline: Date
    private let makeProcess: () throws -> Process
    private var process: Process?
    private var input: ConversationInput?
    private var pending: CheckedContinuation<SourceVerification, Error>?
    private var closed = false
    var active: Bool { !closed && process?.isRunning == true && Date() < deadline }

    init(session: CampSession, payload: Data, deadline: Date, makeProcess: @escaping () throws -> Process = { try Bridge.process("transfer-source") }) {
        self.session = session; self.payload = payload; self.deadline = deadline; self.makeProcess = makeProcess
    }
    deinit { if process?.isRunning == true { process?.terminate() } }

    func verify() async throws -> SourceVerification {
        guard !closed, pending == nil, Date() < deadline else { throw CampError.message("Source verification expired") }
        return try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                pending = continuation
                do {
                    if process == nil {
                        Task { [weak self] in
                            do { try await self?.start() }
                            catch { self?.fail(error.localizedDescription) }
                        }
                    }
                    else {
                        guard active, let input else { throw CampError.message("Source verification stopped") }
                        Task.detached { [weak self] in
                            do { try input.write(Data("{\"type\":\"verify\"}\n".utf8)) }
                            catch { await self?.fail("Source verification stopped") }
                        }
                    }
                } catch { fail(error.localizedDescription) }
            }
        }, onCancel: { Task { @MainActor [weak self] in self?.stop() } })
    }

    private func start() async throws {
        guard let payload else { throw CampError.message("Source review is no longer available") }
        let sessionData = try JSONEncoder().encode(session), expires = Int64(deadline.timeIntervalSince1970)
        let initial = try await Task.detached {
            var data = try JSONSerialization.data(withJSONObject: [
                "session": JSONSerialization.jsonObject(with: sessionData),
                "payload": payload.base64EncodedString(), "expiresAt": expires
            ])
            guard data.count <= 66 << 20 else { throw CampCloudError.responseTooLarge }
            data.append(10); return data
        }.value
        guard !closed, pending != nil else { throw CancellationError() }
        self.payload = nil
        let p = try makeProcess(), output = Pipe(), errors = Pipe(), stdin = Pipe()
        p.standardInput = stdin; p.standardOutput = output; p.standardError = errors
        let writer = ConversationInput(stdin.fileHandleForWriting)
        try p.run(); process = p; input = writer; Bridge.activeProcesses[id] = p
        let request = initial, processID = id
        Task.detached { [weak self] in
            let drain = Task.detached {
                while let part = try? conversationChunk(errors.fileHandleForReading, limit: 4096), !part.isEmpty {}
                try? errors.fileHandleForReading.close()
            }
            let write = Task.detached { [weak self] in
                do { try writer.write(request) } catch { await self?.fail("Could not send the source review") }
            }
            var buffer = Data(), failure = "Source verification stopped"
            do {
                while case let part = try conversationChunk(output.fileHandleForReading, limit: 4096), !part.isEmpty {
                    buffer.append(part)
                    guard buffer.count <= 8192 else { throw CampCloudError.responseTooLarge }
                    while let end = buffer.firstIndex(of: 10) {
                        let line = Data(buffer[..<end]); buffer.removeSubrange(...end)
                        let event = try JSONSerialization.jsonObject(with: line) as? [String: Any]
                        guard event?["type"] as? String == "verified" else {
                            failure = event?["message"] as? String ?? failure
                            throw CampError.message(failure)
                        }
                        let receipt = try JSONDecoder().decode(SourceVerification.self, from: line)
                        guard [receipt.payloadSHA256, receipt.reviewSHA256].allSatisfy({ $0.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil }) else { throw CampCloudError.invalidResponse }
                        await self?.receive(receipt)
                    }
                }
            } catch { failure = error.localizedDescription; if p.isRunning { p.terminate() } }
            p.waitUntilExit(); writer.close(); try? output.fileHandleForReading.close()
            await write.value; await drain.value
            await MainActor.run { _ = Bridge.activeProcesses.removeValue(forKey: processID) }
            await self?.ended(failure)
        }
    }
    private func receive(_ receipt: SourceVerification) {
        guard active else { fail("Source verification stopped"); return }
        let continuation = pending; pending = nil; continuation?.resume(returning: receipt)
    }
    private func ended(_ message: String) {
        closed = true; process = nil; input = nil; Bridge.activeProcesses.removeValue(forKey: id)
        let continuation = pending; pending = nil; continuation?.resume(throwing: CampError.message(message))
    }
    private func fail(_ message: String) {
        let continuation = pending; pending = nil; continuation?.resume(throwing: CampError.message(message)); stop()
    }
    func stop() {
        guard !closed else { return }; closed = true
        let continuation = pending; pending = nil; continuation?.resume(throwing: CancellationError())
        if process?.isRunning == true { process?.terminate() }
        let writer = input
        Task.detached { writer?.close() }
    }
}
