import Foundation
import SwiftUI

@MainActor final class CampLocalWorker: ObservableObject {
    @Published private(set) var status = "Local worker idle"
    @Published private(set) var issues: [CloudAttempt] = []
    var nativeSession: ((CloudSession) -> CampSession?)?
    var available: ((CampSession) -> Bool)?
    var acceptHandoffSession: ((CloudSession, CloudScope, Int64) async throws -> Void)?
    var execute: ((CampSession, CloudRequest, String, [Message], [SessionContext], @escaping (ConversationResult) -> Void) -> LocalConversation?)?
    var makeContinuationProcess: () throws -> Process = { try Bridge.process("conversation") }
    var continuationChanged: ((String, CloudScope, Int64) async -> Void)?
    var requestUpdated: ((CloudRequest) -> Void)?
    var readHistory: (CampSession) async throws -> [Message] = { local in
        let body = try CampWire.body(["session": JSONSerialization.jsonObject(with: JSONEncoder().encode(local)), "limit": 120])
        return try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: body)).messages
    }
    var inspectExecution: (CampSession, PermissionSettings) async throws -> ExecutionContext = { local, permissions in
        let body = try CampWire.body(["session": JSONSerialization.jsonObject(with: JSONEncoder().encode(local)), "permissions": JSONSerialization.jsonObject(with: JSONEncoder().encode(permissions))])
        return try JSONDecoder().decode(ExecutionContext.self, from: await Bridge.call("execution-context", input: body))
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
    private var activeStream: CampLocalPlanStream?
    private var implementationStream: CampLocalImplementationStream?
    private var reservedPlanningSession: CampSession?
    private var preparingRequestID: String?
    private var preparationInterruption: String?
    private var heartbeatAt = Date.distantPast
    private var hasWorkHint = true
    private var queueSignal: UInt64 = 0
    private var eligibilitySignal: UInt64 = 0
    private var checkedSessions: Set<String> = []

    init(readFences: (() async throws -> [LocalTransferFence])? = nil, cache: CampCache?, automatic: Bool = true, heartbeatInterval: TimeInterval = 10) {
        self.cache = cache; self.automatic = automatic; self.heartbeatInterval = heartbeatInterval
        self.handoff = CampLocalHandoff(readFences:readFences,cache:cache)
    }
    deinit { task?.cancel(); leaseTask?.cancel() }
    var activePlanningCount: Int { reservedPlanningSession != nil || activeAttempt?.request.isIsolatedWork == true || activeAttempt?.request.localContinuation == true ? 1 : 0 }
    func usesPlanningProvider(_ provider: String) -> Bool {
        reservedPlanningSession?.provider == provider || ((activeAttempt?.request.isIsolatedWork == true || activeAttempt?.request.localContinuation == true) && active?.session.provider == provider)
    }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?, sessions: [CloudSession]) {
        handoff.attach(scope: scope, actor: actor, transport: transport)
        let priorEligibility = Set(self.sessions.map { "\($0.id):\($0.ownerUserId):\($0.ownerDeviceId):\($0.externalOwner):\($0.executionEpoch ?? 0)" })
        let nextEligibility = Set(sessions.map { "\($0.id):\($0.ownerUserId):\($0.ownerDeviceId):\($0.externalOwner):\($0.executionEpoch ?? 0)" })
        self.sessions = sessions
        guard let scope, let actor, let transport else {
            implementationStream?.stop(); implementationStream = nil
            activeStream?.stop(); activeStream = nil
            task?.cancel(); task = nil; leaseTask?.cancel(); leaseTask = nil; active?.stop(); active = nil; activeAttempt = nil
            context = nil; reservedPlanningSession = nil; issues = []; status = "Local worker idle"; return
        }
        if context?.scope == scope, context?.actor.deviceId == actor.deviceId {
            context?.transport = transport
            if priorEligibility != nextEligibility { hasWorkHint = true; queueSignal &+= 1 }
        } else {
            implementationStream?.stop(); implementationStream = nil
            activeStream?.stop(); activeStream = nil
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
        if name == "start", let digest = attempt.request.quotedContextDigest { body["quoted_context_digest"] = digest }
        if name == "start", attempt.request.isImplementation { body["execution_context"] = attempt.request.executionContext }
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
    private func resolveLocal(_ remote: CloudSession, _ c: Context) async throws -> (CampSession, LocalContinuationBinding?)? {
        if remote.executionLocation == "local", (remote.executionEpoch ?? 0) > 0, let cache, let epoch = c.epoch,
           let binding = try await cache.localContinuation(sessionID: remote.id, executionEpoch: remote.executionEpoch ?? 0, scope: c.scope, expectedEpoch: epoch) {
            try check(c)
            guard binding.matches(remote) else { throw CampCloudError.invalidResponse }
            return (binding.nativeSession, binding)
        }
        return nativeSession?(remote).map { ($0, nil) }
    }

    func poll() async {
        guard !polling, var c = context, let cache else { return }
        polling = true; defer { polling = false }
        do {
            if c.epoch == nil {
                c.epoch = try await cache.epoch(c.scope); try check(c); context?.epoch = c.epoch
            }
            if active != nil, let attempt = activeAttempt {
                // The lease outlives a dropped request by up to a minute and
                // watchLease stops the run when it really expires, so a brief
                // network or server error must not kill a healthy turn.
                do {
                    if Date() >= heartbeatAt {
                        let current = try await action("heartbeat", attempt: attempt, context: c)
                        heartbeatAt = Date().addingTimeInterval(heartbeatInterval)
                        watchLease(current.leaseUntil, attempt: attempt, context: c)
                        if current.state == "cancel_requested" || current.contentRestricted == true { active?.stop() }
                    }
                    if attempt.request.isStreamedLocal, active?.ready == true { try await deliverControls(attempt, c) }
                } catch let error where Self.transient(error) {
                    status = "Shared execution reconnecting · " + error.localizedDescription
                }
                return
            }
            let saved = try await cache.attempts(c.scope); try check(c)
            issues = saved.filter { ["uncertain", "start_pending", "running"].contains($0.state) }
            // One attempt the server keeps refusing must not stop this Mac from
            // claiming other shared work.
            for attempt in saved where attempt.state == "finish_pending" {
                do { try await settle(attempt, c) }
                catch is CancellationError { throw CancellationError() }
                catch { status = "Could not record a finished shared turn · " + error.localizedDescription }
            }
            if let attempt = saved.first(where: { $0.state == "claimed" }) {
                try await begin(attempt, c); return
            }
            if saved.allSatisfy({ ["finish_pending","completed","failed","cancelled"].contains($0.state) }) {
                handoff.nativeSession = nativeSession; handoff.available = available; handoff.acceptSession = acceptHandoffSession
                if try await handoff.poll() { status = handoff.status; return }
            }
            guard saved.count < 200 else { status = "Review unconfirmed work before starting more requests"; return }
            guard hasWorkHint else { return }
            if eligibilitySignal != queueSignal {
                checkedSessions.removeAll(); eligibilitySignal = queueSignal
            }
            var eligible: [String] = []
            for remote in sessions where !remote.isHistoryOnly && remote.ownerUserId == c.actor.userId && remote.ownerDeviceId == c.actor.deviceId && !remote.externalOwner {
                if let (local, _) = try await resolveLocal(remote, c), available?(local) == true { eligible.append(remote.id) }
            }
            eligible.sort()
            try check(c)
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

    nonisolated static func transient(_ error: Error) -> Bool {
        if error is URLError { return true }
        if case CampCloudError.server(let code, _) = error { return code >= 500 || code == 408 || code == 429 }
        return false
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
        if current.contentRestricted == true {
            _ = try await action("cancel", attempt: attempt, context: c)
            try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
            status = "Context access changed"; return
        }
        guard current.supportedWork, current.id == attempt.id, current.sessionId == attempt.request.sessionId,
              current.ownerUserId == c.actor.userId, current.ownerDeviceId == c.actor.deviceId else { throw CampCloudError.invalidResponse }
        let lease = attempt.request.leaseToken
        attempt.request = current; attempt.request.leaseToken = lease
        let remote = try await get(CloudSession.self, "/v1/camps/\(c.scope.campId)/sessions/\(attempt.request.sessionId)", c)
        let resolved = try await resolveLocal(remote, c)
        guard !remote.isHistoryOnly, remote.ownerUserId == c.actor.userId, remote.ownerDeviceId == c.actor.deviceId,
              remote.permissionRevision == attempt.request.permissionRevision, !remote.externalOwner,
              let (local, binding) = resolved,
              (binding?.target.sessionID ?? local.sessionID) == remote.localSessionId, local.provider == remote.provider,
              attempt.request.isIsolatedWork || (attempt.request.localContinuation == true) == (binding != nil),
              available?(local) == true else {
            _ = try await action("release", attempt: attempt, context: c, key: attempt.startKey)
            try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
            status = "Waiting for the assigned local session"; return
        }
        let continuation = attempt.request.localContinuation == true ? binding : nil
        if attempt.request.isIsolatedWork || continuation != nil { reservedPlanningSession = local }
        defer { reservedPlanningSession = nil }
        attempt.localSessionId = local.id
        var executionSession = local
        if attempt.request.isImplementation || (continuation != nil && attempt.request.mode == "implementation") {
            guard let permissions = attempt.request.permissionSettings, permissions.valid(for: local.provider), attempt.request.executionLocation == "local" else { throw CampCloudError.invalidResponse }
            do {
                let context = try await inspectExecution(local, permissions)
                guard (continuation != nil || context.fingerprint == attempt.request.executionContext), context.fingerprint.count == 64, context.provider == local.provider,
                      URL(fileURLWithPath: context.cwd).resolvingSymlinksInPath() == URL(fileURLWithPath: local.cwd).resolvingSymlinksInPath(),
                      URL(fileURLWithPath: context.profile).resolvingSymlinksInPath() == URL(fileURLWithPath: local.root).resolvingSymlinksInPath() else { throw CampError.message("The CLI permissions changed since approval") }
                if continuation != nil { attempt.request.executionContext = context.fingerprint }
            } catch {
                _ = try await action("cancel", attempt: attempt, context: c)
                try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
                status = "Review implementation permissions again · " + error.localizedDescription; return
            }
        }
        if attempt.request.isImplementation {
            let existing = attempt.request.providerSessionId ?? ""
            let providerID = existing.isEmpty ? (attempt.localProviderSessionId ?? UUID().uuidString.lowercased()) : existing
            attempt.localProviderSessionId = providerID
            executionSession = CampSession(id: "camp-implementation:" + (attempt.request.implementationId ?? ""), sessionID: providerID, provider: local.provider, sourceID: local.sourceID, sourceLabel: local.sourceLabel, root: local.root, title: local.title, cwd: local.cwd, updated: local.updated, path: "", accountID: local.accountID, archived: false, model: attempt.request.model)
            try await save(attempt, c)
        }
        let history: [Message], contexts: [SessionContext]
        do { history = attempt.request.isIsolatedWork || continuation != nil ? [] : try await readHistory(local); contexts = try await sourceContexts(attempt.request, c) }
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
            attempt.request.streamSeq = started.streamSeq
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
        let completed: (ConversationResult) -> Void = { [weak self] result in Task { await self?.finished(running, result, c) } }
        let launched: LocalConversation?
        if continuation != nil {
            do {
                guard let cache, let epoch = c.epoch else { throw CampCloudError.cacheUnavailable }
                let launch = try await cache.reserveContinuation(session: remote, requestID: attempt.id, scope: c.scope, expectedEpoch: epoch)
                try check(c)
                if preparationInterruption != nil { throw CancellationError() }
                let run = LocalConversation(session: launch.binding.nativeSession, history: [], prompt: attempt.request.prompt, promptID: attempt.id)
                run.completed = completed
                if launch.newSession {
                    run.persistSession = { [weak self] id in
                        guard let self else { throw CancellationError() }
                        try self.check(c)
                        try await cache.bindContinuation(sessionID: remote.id, executionEpoch: remote.executionEpoch!, requestID: running.id, providerSessionID: id, scope: c.scope, expectedEpoch: epoch)
                        await self.continuationChanged?(remote.id, c.scope, epoch)
                        try self.check(c)
                    }
                }
                let stream = CampLocalImplementationStream(request: attempt.request, scope: c.scope, epoch: epoch, cache: cache, transport: c.transport)
                implementationStream = stream
                run.forwardEvent = { [weak stream] data in
                    guard let stream else { throw CancellationError() }
                    try await stream.emit(data)
                }
                try run.start(prompt: attempt.request.prompt, model: attempt.request.model,
                              tuning: ModelTuning(effort: attempt.request.effort, speed: attempt.request.speed), contexts: contexts,
                              mode: attempt.request.mode == "implementation" ? .implementation : .chat,
                              executionApproved: attempt.request.mode == "implementation", newSession: launch.newSession,
                              executionContext: attempt.request.executionContext ?? "", permissions: attempt.request.mode == "implementation" ? attempt.request.permissionSettings : nil,
                              restoredHistory: launch.binding.history, makeProcess: makeContinuationProcess)
                launched = run
            } catch {
                await finished(attempt, ConversationResult(state: error is CancellationError ? "cancelled" : "failed", message: "Continuation stopped before provider submission", accepted: false), c)
                status = "Continuation needs review · " + error.localizedDescription
                return
            }
        } else {
            launched = execute?(executionSession, attempt.request, attempt.request.prompt, history, contexts, completed)
        }
        guard let launched else {
            await finished(attempt, ConversationResult(state: "failed", message: "The local runner was unavailable before submission", accepted: false), c); return
        }
        active = launched; status = attempt.request.isPlanning ? "Planning on this Mac" : "Running shared work on this Mac"
        if attempt.request.isImplementation, let cache, let epoch = c.epoch {
            let stream = CampLocalImplementationStream(request: attempt.request, scope: c.scope, epoch: epoch, cache: cache, transport: c.transport)
            implementationStream = stream
            launched.forwardEvent = { [weak stream] data in
                guard let stream else { throw CancellationError() }
                try await stream.emit(data)
            }
        }
        if attempt.request.isPlanning {
            let stream = CampLocalPlanStream(request: attempt.request, transport: c.transport)
            activeStream = stream
            stream.failed = { [weak self] message in
                if self?.activeAttempt?.id == attempt.id { self?.status = message }
            }
            launched.changed = { [weak launched, weak stream] in
                guard let launched else { return }
                var parts = launched.messages.filter { $0.role == "assistant" }.map(\.text)
                if !launched.text.isEmpty { parts.append(launched.text) }
                stream?.update(text: parts.joined(separator: "\n\n"), settings: launched.reportedSettings)
            }
            launched.changed?()
        }
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
        let quoted = request.quotedSources ?? []
        guard validPlanningQuotes(quoted, sharedCount: request.sources.count) else { throw CampCloudError.invalidResponse }
        var result: [SessionContext] = quoted.map { SessionContext(id: PlanningQuoteLink(planID: request.planId ?? "", version: request.planVersion ?? 0, sourceID: $0.id).id, title: $0.title, revision: $0.revision, messages: $0.messages) }
        var bytes = quoted.reduce(0, { $0 + $1.messages.reduce(0, { $0 + $1.text.utf8.count }) })
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
        if activeAttempt?.id == attempt.id { activeStream?.stop(); activeStream = nil; implementationStream?.stop(); implementationStream = nil; leaseTask?.cancel(); leaseTask = nil; active = nil; activeAttempt = nil }
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
        if attempt.request.isStreamedLocal, let cache, let epoch = c.epoch,
           let saved = try await cache.attempts(c.scope).first(where: { $0.id == attempt.id }), let pending = saved.pendingStream {
            // A staged event can only land while the turn is live. Otherwise, or
            // when the server refuses it outright, it belongs to an ended turn.
            var deliverable = ["started", "cancel_requested"].contains(current.state)
            if deliverable {
                let stream = CampLocalImplementationStream(request: saved.request, scope: c.scope, epoch: epoch, cache: cache, transport: c.transport)
                do { try await stream.flush(pending) }
                catch CampCloudError.server(let code, _) where (400..<500).contains(code) && code != 408 && code != 429 { deliverable = false }
            }
            if !deliverable { try await cache.discardWorkerEvent(attempt.id, body: pending, scope: c.scope, expectedEpoch: epoch) }
        }
        if current.state == "claimed" || current.state == "queued" {
            // The server never saw this turn start: give the claim back rather
            // than report a finish it cannot accept.
            if current.state == "claimed", current.leaseToken == attempt.request.leaseToken {
                _ = try await action("release", attempt: attempt, context: c, key: attempt.startKey)
            }
        } else if !["completed", "failed", "cancelled"].contains(current.state) {
            let actionName = current.state == "outcome_unknown" ? "reconcile" : "finish"
            do { _ = try await action(actionName, attempt: attempt, context: c, key: attempt.finishKey) }
            catch CampCloudError.server(let code, _) where (400..<500).contains(code) && code != 408 && code != 429 {
                // Show it for review again instead of retrying a refused result forever.
                var value = attempt; value.state = "uncertain"; try await save(value, c)
                if !issues.contains(where: { $0.id == value.id }) { issues.append(value) }
                throw CampCloudError.server(code, "finish_refused")
            }
        }
        try await cache?.removeWorkerAttempt(attempt.id, scope: c.scope, expectedEpoch: c.epoch!)
        issues.removeAll { $0.id == attempt.id }; status = "Shared work finished"; hasWorkHint = true; queueSignal &+= 1
    }
    private func deliverControls(_ attempt: CloudAttempt, _ c: Context) async throws {
        guard let cache, let epoch = c.epoch, let active else { return }
        let command = CampPublication(kind: "worker-controls", method: "POST", path: "/v1/camps/\(c.scope.campId)/requests/\(attempt.id)/local-controls", body: try CampWire.body(["lease_token": attempt.request.leaseToken ?? ""]))
        let controls = try CampWire.decoder().decode([LocalWorkerControl].self, from: await c.transport.perform(command))
        try check(c)
        guard controls.count <= 16, controls.allSatisfy(\.valid), activeAttempt?.id == attempt.id else { throw CampCloudError.invalidResponse }
        for control in controls {
            if try await cache.markWorkerControl(attempt.id, control: control.id, scope: c.scope, expectedEpoch: epoch) {
                try check(c)
                try await active.deliverSharedControl(control.control)
            }
        }
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
