import Foundation
import SwiftUI

@MainActor final class CampLocalWorker: ObservableObject {
    @Published private(set) var status = "Local worker idle"
    @Published private(set) var issues: [CloudAttempt] = []
    var nativeSession: ((CloudSession) -> CampSession?)?
    var available: ((CampSession) -> Bool)?
    var execute: ((CampSession, CloudRequest, String, [Message], [SessionContext], @escaping (ConversationResult) -> Void) -> LocalConversation?)?
    var requestUpdated: ((CloudRequest) -> Void)?
    var readHistory: (CampSession) async throws -> [Message] = { local in
        let body = try CampWire.body(["session": JSONSerialization.jsonObject(with: JSONEncoder().encode(local)), "limit": 120])
        return try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: body)).messages
    }
    private struct Context {
        var scope: CloudScope; var actor: CloudActor; var transport: any CampPublishingTransport
        var generation: UUID; var epoch: Int64?
    }
    private let cache: CampCache?
    private let automatic: Bool
    private let heartbeatInterval: TimeInterval
    private let handoff: CampLocalHandoff
    private var context: Context?
    private var sessions: [CloudSession] = []
    private var task: Task<Void, Never>?
    private var leaseTask: Task<Void, Never>?
    private var polling = false
    private var active: LocalConversation?
    private var activeAttempt: CloudAttempt?
    private var reservedPlanningSession: CampSession?
    private var preparingRequestID: String?
    private var preparationInterruption: String?
    private var heartbeatAt = Date.distantPast
    private var hasWorkHint = true
    private var queueSignal: UInt64 = 0
    private var eligibilitySignal: UInt64 = 0
    private var checkedSessions: Set<String> = []

    init(cache: CampCache?, automatic: Bool = true, heartbeatInterval: TimeInterval = 10) {
        self.cache = cache; self.automatic = automatic; self.heartbeatInterval = heartbeatInterval
        self.handoff = CampLocalHandoff(cache: cache)
    }
    deinit { task?.cancel(); leaseTask?.cancel() }
    var activePlanningCount: Int { reservedPlanningSession != nil || activeAttempt?.request.isPlanning == true ? 1 : 0 }
    func usesPlanningProvider(_ provider: String) -> Bool {
        reservedPlanningSession?.provider == provider || (activeAttempt?.request.isPlanning == true && active?.session.provider == provider)
    }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?, sessions: [CloudSession]) {
        handoff.attach(scope: scope, actor: actor, transport: transport)
        let priorEligibility = Set(self.sessions.map { "\($0.id):\($0.ownerUserId):\($0.ownerDeviceId):\($0.externalOwner)" })
        let nextEligibility = Set(sessions.map { "\($0.id):\($0.ownerUserId):\($0.ownerDeviceId):\($0.externalOwner)" })
        self.sessions = sessions
        guard let scope, let actor, let transport else {
            task?.cancel(); task = nil; leaseTask?.cancel(); leaseTask = nil; active?.stop(); active = nil; activeAttempt = nil
            context = nil; reservedPlanningSession = nil; issues = []; status = "Local worker idle"; return
        }
        if context?.scope == scope, context?.actor.deviceId == actor.deviceId {
            context?.transport = transport
            if priorEligibility != nextEligibility { hasWorkHint = true; queueSignal &+= 1 }
        } else {
            task?.cancel(); task = nil; leaseTask?.cancel(); leaseTask = nil; active?.stop(); active = nil; activeAttempt = nil
            context = Context(scope: scope, actor: actor, transport: transport, generation: UUID())
            hasWorkHint = true; queueSignal &+= 1
        }
        wake()
    }
    func wake() {
        guard automatic, task == nil, let generation = context?.generation else { return }
        task = Task { [weak self] in
            while !Task.isCancelled, self?.context?.generation == generation {
                await self?.poll()
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }
    func changed(_ request: CloudRequest) {
        if ["queued", "completed", "failed", "cancelled"].contains(request.state) { hasWorkHint = true; queueSignal &+= 1 }
        if request.id == activeAttempt?.id, ["cancel_requested", "cancelled", "outcome_unknown"].contains(request.state) { active?.stop() }
        if request.id == preparingRequestID, ["cancel_requested", "cancelled", "outcome_unknown"].contains(request.state) { preparationInterruption = request.state }
        wake()
    }
    func scan() { hasWorkHint = true; queueSignal &+= 1; handoff.wake(); wake() }
    private func check(_ c: Context) throws {
        guard context?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func get<T: Decodable>(_ type: T.Type, _ path: String, _ c: Context) async throws -> T {
        try check(c)
        let data = try await c.transport.perform(CampPublication(kind: "read", method: "GET", path: path, body: Data()))
        try check(c); return try CampWire.decoder().decode(type, from: data)
    }
    private func action(_ name: String, attempt: CloudAttempt, context c: Context, key: String = UUID().uuidString) async throws -> CloudRequest {
        try check(c)
        var body: [String: Any] = ["action": name, "lease_token": attempt.request.leaseToken ?? ""]
        if let value = attempt.outcome { body["outcome"] = value }
        if let value = attempt.evidence { body["evidence"] = value }
        if attempt.request.isPlanning, ["finish", "reconcile"].contains(name) {
            if let output = attempt.output { body["output"] = output }
            if let usage = attempt.usage { body["usage"] = try JSONSerialization.jsonObject(with: CampWire.encoder().encode(usage)) }
        }
        let data = try await c.transport.perform(CampPublication(key: key, kind: name, method: "POST", path: "/v1/camps/\(c.scope.campId)/requests/\(attempt.id)/actions", body: try CampWire.body(body)))
        try check(c)
        let request = try CampWire.decoder().decode(CloudRequest.self, from: data)
        requestUpdated?(request); return request
    }
    private func save(_ attempt: CloudAttempt, _ c: Context) async throws {
        guard let cache, let epoch = c.epoch else { throw CampCloudError.cacheUnavailable }
        try await cache.saveWorkerAttempt(attempt, scope: c.scope, expectedEpoch: epoch)
    }

    func poll() async {
        guard !polling, var c = context, let cache else { return }
        polling = true; defer { polling = false }
        do {
            if c.epoch == nil {
                c.epoch = try await cache.epoch(c.scope); try check(c); context?.epoch = c.epoch
            }
            if active != nil, let attempt = activeAttempt {
                if Date() >= heartbeatAt {
                    let current = try await action("heartbeat", attempt: attempt, context: c)
                    heartbeatAt = Date().addingTimeInterval(heartbeatInterval)
                    watchLease(current.leaseUntil, attempt: attempt, context: c)
                    if current.state == "cancel_requested" { active?.stop() }
                }
                return
            }
            let saved = try await cache.attempts(c.scope); try check(c)
            issues = saved.filter { ["uncertain", "start_pending", "running"].contains($0.state) }
            for attempt in saved where attempt.state == "finish_pending" { try await settle(attempt, c) }
            if let attempt = saved.first(where: { $0.state == "claimed" }) {
                try await begin(attempt, c); return
            }
            if saved.allSatisfy({ ["finish_pending","completed","failed","cancelled"].contains($0.state) }) {
                handoff.nativeSession = nativeSession; handoff.available = available
                if try await handoff.poll() { status = handoff.status; return }
            }
            guard saved.count < 200 else { status = "Review unconfirmed work before starting more requests"; return }
            guard hasWorkHint else { return }
            if eligibilitySignal != queueSignal {
                checkedSessions.removeAll(); eligibilitySignal = queueSignal
            }
            let eligible = sessions.filter { remote in
                remote.ownerUserId == c.actor.userId && remote.ownerDeviceId == c.actor.deviceId && !remote.externalOwner &&
                nativeSession?(remote).map { available?($0) == true } == true
            }.map(\.id).sorted()
            guard !eligible.isEmpty else { status = "Waiting for an available local session"; return }
            let unchecked = eligible.filter { !checkedSessions.contains($0) }
            guard !unchecked.isEmpty else { hasWorkHint = false; status = "Local worker ready"; return }
            let signal = queueSignal
            let command = try await cache.workerClaim(scope: c.scope, sessions: Array(unchecked.prefix(200)), expectedEpoch: c.epoch!)
            let data: Data
            do { data = try await c.transport.perform(command) }
            catch CampCloudError.server(409, _) {
                try await cache.finishWorkerClaim(nil, scope: c.scope, expectedEpoch: c.epoch!); return
            }
            try check(c)
            let request = try CampWire.decoder().decode(CloudRequest?.self, from: data)
            guard let request else {
                try await cache.finishWorkerClaim(nil, scope: c.scope, expectedEpoch: c.epoch!)
                if signal == queueSignal {
                    // Read the persisted command: a recovered claim may contain
                    // a different batch than this poll's freshly computed one.
                    let body = try JSONSerialization.jsonObject(with: command.body) as? [String: Any]
                    checkedSessions.formUnion(body?["session_ids"] as? [String] ?? [])
                    hasWorkHint = eligible.contains { !checkedSessions.contains($0) }
                }
                status = "Local worker ready"; return
            }
            guard request.ownerUserId == c.actor.userId, request.ownerDeviceId == c.actor.deviceId,
                  request.campId == c.scope.campId, request.supportedWork, request.state == "claimed", UUID(uuidString: request.leaseToken ?? "") != nil else { throw CampCloudError.invalidResponse }
            let attempt = CloudAttempt(id: request.id, scope: c.scope.key, request: request, localSessionId: "", state: "claimed", startKey: UUID().uuidString, finishKey: UUID().uuidString)
            try await cache.finishWorkerClaim(attempt, scope: c.scope, expectedEpoch: c.epoch!)
            requestUpdated?(request); try await begin(attempt, c)
        } catch is CancellationError {} catch {
            if context?.generation == c.generation {
                status = "Shared execution paused · " + error.localizedDescription
                // Never continue a remotely authorized run after losing its lease.
                active?.stop()
            }
        }
    }

    private func begin(_ saved: CloudAttempt, _ c: Context) async throws {
        var attempt = saved
        guard attempt.request.supportedWork else { throw CampCloudError.invalidResponse }
        preparingRequestID = attempt.id; preparationInterruption = nil
        defer { preparingRequestID = nil; preparationInterruption = nil }
        let current = try await get(CloudRequest.self, "/v1/camps/\(c.scope.campId)/requests/\(attempt.id)", c)
        guard current.state == "claimed" else {
            if ["queued", "completed", "failed", "cancelled"].contains(current.state) {
                try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!); return
            }
            attempt.state = "uncertain"; try await save(attempt, c); issues.append(attempt); return
        }
        let remote = try await get(CloudSession.self, "/v1/camps/\(c.scope.campId)/sessions/\(attempt.request.sessionId)", c)
        guard remote.ownerUserId == c.actor.userId, remote.ownerDeviceId == c.actor.deviceId,
              remote.permissionRevision == attempt.request.permissionRevision, !remote.externalOwner,
              let local = nativeSession?(remote), local.sessionID == remote.localSessionId, local.provider == remote.provider,
              available?(local) == true else {
            _ = try await action("release", attempt: attempt, context: c, key: attempt.startKey)
            try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
            status = "Waiting for the assigned local session"; return
        }
        if attempt.request.isPlanning { reservedPlanningSession = local }
        defer { reservedPlanningSession = nil }
        attempt.localSessionId = local.id
        let history: [Message], contexts: [SessionContext]
        do { history = attempt.request.isPlanning ? [] : try await readHistory(local); contexts = try await sourceContexts(attempt.request, c) }
        catch {
            // No provider process has started. Persist a failed admission instead
            // of repeatedly claiming an unreadable source or oversized snapshot.
            attempt.state = "start_pending"; try await save(attempt, c)
            _ = try await action("start", attempt: attempt, context: c, key: attempt.startKey)
            await finished(attempt, ConversationResult(state: "failed", message: "Preflight failed before provider submission", accepted: false), c)
            status = "Request needs review · " + error.localizedDescription; return
        }
        try check(c)
        // Commit uncertainty locally before making the remotely visible Start.
        attempt.state = "start_pending"; try await save(attempt, c)
        do {
            let started = try await action("start", attempt: attempt, context: c, key: attempt.startKey)
            guard started.state == "started", let deadline = started.leaseUntil, deadline.timeIntervalSinceNow > 5 else { throw CampCloudError.invalidResponse }
            attempt.request.leaseUntil = deadline
        }
        catch {
            attempt.state = "uncertain"; try await save(attempt, c)
            issues.append(attempt); throw error
        }
        attempt.state = "running"; try await save(attempt, c); try check(c)
        if let interrupted = preparationInterruption {
            await finished(attempt, ConversationResult(state: interrupted == "outcome_unknown" ? "uncertain" : "cancelled", message: "Stopped before provider submission", accepted: false), c)
            return
        }
        activeAttempt = attempt; heartbeatAt = Date().addingTimeInterval(heartbeatInterval)
        let running = attempt
        let launched = execute?(local, attempt.request, attempt.request.prompt, history, contexts, { [weak self] result in
            Task { await self?.finished(running, result, c) }
        })
        guard let launched else {
            await finished(attempt, ConversationResult(state: "failed", message: "The local runner was unavailable before submission", accepted: false), c); return
        }
        active = launched; status = attempt.request.isPlanning ? "Planning on this Mac" : "Running shared work on this Mac"
        watchLease(attempt.request.leaseUntil, attempt: attempt, context: c)
    }
    private func watchLease(_ deadline: Date?, attempt: CloudAttempt, context c: Context) {
        leaseTask?.cancel()
        let seconds = max(0, (deadline?.timeIntervalSinceNow ?? 0) - 5)
        leaseTask = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(seconds)) } catch { return }
            guard self?.context?.generation == c.generation, self?.activeAttempt?.id == attempt.id else { return }
            self?.active?.stop(); self?.status = "Execution lease expired · Reviewing the local result"
        }
    }
    private func sourceContexts(_ request: CloudRequest, _ c: Context) async throws -> [SessionContext] {
        var result: [SessionContext] = [], bytes = 0
        for source in request.sources {
            if let ids = source.messageIds, !ids.isEmpty {
                let operation = CampPublication(kind: "read", method: "POST", path: "/v1/camps/\(c.scope.campId)/sessions/\(source.sessionId)/context", body: try CampWire.encoder().encode(source))
                let page = try CampWire.decoder().decode(CloudTranscript.self, from: await c.transport.perform(operation))
                try check(c)
                guard page.revision == source.transcriptRevision, Set(page.messages.map(\.id)) == Set(ids), page.messages.count == ids.count else { throw CampCloudError.invalidResponse }
                let messages = page.messages.filter { !$0.removed }.map(\.transcriptMessage)
                bytes += messages.reduce(0) { $0 + $1.text.utf8.count }
                guard bytes <= 256 << 10 else { throw CampError.message("The selected source context exceeds 256 KB") }
                result.append(SessionContext(id: "camp:" + source.sessionId, title: "Shared session", revision: String(source.transcriptRevision), messages: messages))
                continue
            }
            var before: Int64 = 0, pages: [[CloudMessage]] = [], ended = false
            for _ in 0..<100 {
                let page = try await get(CloudTranscript.self, "/v1/camps/\(c.scope.campId)/sessions/\(source.sessionId)/transcript?revision=\(source.transcriptRevision)&before=\(before)&limit=120", c)
                guard page.revision == source.transcriptRevision else { throw CampCloudError.invalidResponse }
                pages.append(page.messages)
                let size = pages.flatMap { $0 }.reduce(0) { $0 + $1.text.utf8.count }
                guard size + bytes <= 256 << 10 else { throw CampError.message("The tagged context is too large for this request. Choose a smaller source snapshot") }
                if !page.hasMore { ended = true; break }
                guard page.before > 0, before == 0 || page.before < before else { throw CampCloudError.invalidResponse }
                before = page.before
            }
            guard ended else { throw CampError.message("The tagged transcript exceeds the request page limit") }
            let messages = pages.reversed().flatMap { $0 }.filter { !$0.removed }.map(\.transcriptMessage)
            bytes += messages.reduce(0) { $0 + $1.text.utf8.count }
            result.append(SessionContext(id: "camp:" + source.sessionId, title: "Shared session", revision: String(source.transcriptRevision), messages: messages))
        }
        guard result.allSatisfy({ !$0.messages.isEmpty && $0.messages.count <= 120 && !$0.messages.contains(where: { $0.text.utf8.count > 48 << 10 }) }) else {
            throw CampError.message("Choose a source range of up to 120 messages, each under 48 KB")
        }
        return result
    }
    private func finished(_ saved: CloudAttempt, _ result: ConversationResult, _ c: Context) async {
        var attempt = saved
        if activeAttempt?.id == attempt.id { leaseTask?.cancel(); leaseTask = nil; active = nil; activeAttempt = nil }
        attempt.state = result.state == "uncertain" ? "uncertain" : "finish_pending"
        attempt.outcome = result.state == "uncertain" ? nil : result.state
        attempt.evidence = "Local CLI \(result.state); attempt \(attempt.startKey)"
        if attempt.request.isPlanning {
            let validOutput = result.output.utf8.count <= 8192
            let validUsage = (result.usage.inputTokens ?? 0) >= 0 && (result.usage.outputTokens ?? 0) >= 0
            if validOutput && validUsage {
                attempt.output = result.output; attempt.usage = result.usage
            }
            if result.state == "completed" && (!validOutput || !validUsage || result.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) {
                attempt.state = "finish_pending"; attempt.outcome = "failed"
                attempt.evidence = "The planning CLI returned an invalid or empty result"
            }
        }
        do {
            try await save(attempt, c)
            if context?.generation == c.generation {
                if attempt.state == "finish_pending" { try await settle(attempt, c) }
                else { issues.append(attempt); status = "Review the unconfirmed CLI result" }
            }
        } catch { if context?.generation == c.generation { status = "Completion needs review · " + error.localizedDescription } }
    }
    private func settle(_ attempt: CloudAttempt, _ c: Context) async throws {
        let current = try await get(CloudRequest.self, "/v1/camps/\(c.scope.campId)/requests/\(attempt.id)", c)
        if !["completed", "failed", "cancelled"].contains(current.state) {
            let actionName = current.state == "outcome_unknown" ? "reconcile" : "finish"
            _ = try await action(actionName, attempt: attempt, context: c, key: attempt.finishKey)
        }
        try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
        issues.removeAll { $0.id == attempt.id }; status = "Shared work finished"; hasWorkHint = true; queueSignal &+= 1
    }
    func confirm(_ attempt: CloudAttempt, outcome: String) async {
        guard let c = context, ["completed", "failed", "cancelled"].contains(outcome), issues.contains(where: { $0.id == attempt.id }) else { return }
        guard !attempt.request.isPlanning || outcome != "completed" || !(attempt.output ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            status = "The planning output is unavailable. Mark this turn failed or cancelled before restarting the plan"; return
        }
        var value = attempt; value.state = "finish_pending"; value.outcome = outcome
        value.evidence = "Owner reviewed the local transcript; attempt \(value.startKey)"
        do { try await save(value, c); try await settle(value, c) }
        catch { status = "Could not confirm the result · " + error.localizedDescription }
    }
}
