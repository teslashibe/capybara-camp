import Foundation

struct CodexActivityUpdate: Codable {
    var activity: [LiveState]
    var sessions: [CampSession]
    var errors: [String: String]
}

// One bounded helper watches all selected Codex profiles. It never starts a
// model or takes ownership of a session held by another client.
@MainActor final class CodexActivityWatch {
    var changed: ((CodexActivityUpdate) -> Void)?
    private var sources: [CampSource] = []
    private var process: Process?
    private var generation = UUID()
    private var task: Task<Void, Never>?
    private let makeProcess: () throws -> Process
    private let identity = UUID()

    init(makeProcess: @escaping () throws -> Process = { try Bridge.process("codex-activity-watch") }) {
        self.makeProcess = makeProcess
    }
    deinit { task?.cancel(); process?.terminate() }

    func observe(_ values: [CampSource]) {
        let wanted = values.filter { $0.provider == "Codex" }.map { source in
            CampSource(id: source.id, provider: source.provider, root: source.root, label: source.label, count: 0)
        }.sorted { $0.id < $1.id }
        guard wanted != sources || process?.isRunning != true else { return }
        stop(); sources = wanted
        guard !wanted.isEmpty else { return }
        let token = UUID(); generation = token
        do {
            let p = try makeProcess(), output = Pipe(), input = Pipe()
            p.standardOutput = output; p.standardError = FileHandle.nullDevice; p.standardInput = input
            let payload = try JSONEncoder().encode(wanted) + Data([10])
            try p.run(); process = p; Bridge.activeProcesses[identity] = p
            task = Task.detached { [weak self] in
                var buffer = Data(), failed = false
                do {
                    try await blockingIO {
                        try input.fileHandleForWriting.write(contentsOf: payload)
                        try input.fileHandleForWriting.close()
                    }
                    while !Task.isCancelled {
                        let chunk = try await conversationChunk(output.fileHandleForReading, limit: 16384)
                        if chunk.isEmpty { break }
                        buffer.append(chunk)
                        while let end = buffer.firstIndex(of: 10) {
                            guard end <= 2 * 1024 * 1024 else { throw CampError.message("Codex activity exceeded its size limit") }
                            let line = Data(buffer[..<end]); buffer.removeSubrange(...end)
                            let value = try JSONDecoder().decode(CodexActivityUpdate.self, from: line)
                            guard value.activity.count <= 8192, value.sessions.count <= 8192 else { throw CampError.message("Codex activity exceeded its size limit") }
                            await self?.receive(value, token: token)
                        }
                        guard buffer.count <= 2 * 1024 * 1024 else { throw CampError.message("Codex activity exceeded its size limit") }
                    }
                    // EOF is unexpected until stop() invalidates this generation.
                    failed = true
                } catch { failed = true }
                try? input.fileHandleForWriting.close()
                if p.isRunning { p.terminate() }
                await blockingIO { p.waitUntilExit() }; try? output.fileHandleForReading.close()
                await self?.ended(token, failed: failed || p.terminationStatus != 0)
            }
        } catch {
            changed?(CodexActivityUpdate(activity: [], sessions: [], errors: Dictionary(uniqueKeysWithValues: wanted.map { ($0.id, "Codex activity observer is unavailable") })))
        }
    }
    private func receive(_ value: CodexActivityUpdate, token: UUID) {
        guard generation == token else { return }
        let allowed = Set(sources.map(\.id))
        guard value.activity.allSatisfy({ allowed.contains($0.sourceID) && $0.id.hasPrefix($0.sourceID + ":") && ["running", "idle", "waiting", "unknown"].contains($0.state) }),
              value.sessions.allSatisfy({ allowed.contains($0.sourceID) && $0.provider == "Codex" && $0.id == $0.sourceID + ":" + $0.sessionID }),
              Set(value.errors.keys).isSubset(of: allowed) else {
            changed?(CodexActivityUpdate(activity: [], sessions: [], errors: Dictionary(uniqueKeysWithValues: sources.map { ($0.id, "Codex returned invalid activity scope") })))
            return
        }
        changed?(value)
    }
    private func ended(_ token: UUID, failed: Bool) {
        guard generation == token else { return }
        process = nil; task = nil; Bridge.activeProcesses.removeValue(forKey: identity)
        if failed { changed?(CodexActivityUpdate(activity: [], sessions: [], errors: Dictionary(uniqueKeysWithValues: sources.map { ($0.id, "Codex activity observer disconnected") }))) }
        // The existing background poll restarts an observer only after its real
        // process has exited, avoiding duplicate observers after a slow read.
    }
    func stop() {
        generation = UUID(); task?.cancel(); task = nil
        if let p = process, p.isRunning { p.terminate() }
        process = nil; sources = []; Bridge.activeProcesses.removeValue(forKey: identity)
    }
}
