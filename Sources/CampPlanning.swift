import Foundation
import SwiftUI

struct CampPlanOperation: Codable, Identifiable, Sendable {
    var command: CampPublication
    var draftID: String?
    var planID: String?
    var id: String { command.key }
}
struct CampPlanningState: Codable, Sendable {
    var revision: Int64 = 0
    var drafts: [CloudPlanDraft] = []
    var tickets: [CloudPlanSummary] = []
    var details: [String: CloudPlanningResult] = [:]
    var operations: [CampPlanOperation] = []
}

@MainActor final class CampPlanning: ObservableObject {
    @Published private(set) var drafts: [CloudPlanDraft] = []
    @Published private(set) var tickets: [CloudPlanSummary] = []
    @Published private(set) var ready = false
    @Published private(set) var error = ""
    @Published var selectedID: String?
    @Published var selectedVersion = 0
    var changed: (() -> Void)?
    var created: ((String, String) -> Void)?
    private struct Context {
        var scope: CloudScope; var actor: CloudActor; var transport: any CampPublishingTransport
        var generation: UUID; var epoch: Int64
    }
    private let cache: CampCache?
    private var context: Context?
    private var state = CampPlanningState()
    private var task: Task<Void, Never>?
    private var saveTask: Task<Void, Never>?
    private var dirty = false
    private var visibleSessions: Set<String> = []
    @Published private var executionTargets: [String: CloudPlanningExecution] = [:]
    private var sourceProviders: [String: String] = [:]
    private var accessRevision: UInt64 = 0
    init(cache: CampCache?) { self.cache = cache }
    deinit { task?.cancel(); saveTask?.cancel() }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?, sessions: [CloudSession]) {
        guard let scope, let actor, let transport, let cache else {
            context = nil; task?.cancel(); task = nil; saveTask?.cancel(); ready = false; state = CampPlanningState(); executionTargets = [:]; sourceProviders = [:]; selectedID = nil; selectedVersion = 0; error = ""; publish(); return
        }
        let visible = Set(sessions.map(\.id))
        sourceProviders = sessions.reduce(into: [:]) { $0[$1.id] = $1.provider }
        let targets = sessions.reduce(into: [String: CloudPlanningExecution]()) { values, session in
            if session.campId == scope.campId, let target = CloudPlanningExecution(session: session) { values[session.id] = target }
        }
        if targets != executionTargets { executionTargets = targets }
        if context?.scope == scope, context?.actor.deviceId == actor.deviceId {
            if visible != visibleSessions {
                let lostAccess = !visibleSessions.isSubset(of: visible)
                visibleSessions = visible; accessRevision &+= 1
                state.details = state.details.filter { $0.value.referencedSessions.isSubset(of: visible) }
                if lostAccess { state.tickets = []; state.details = [:] }
                publish(); refresh()
            }
            context?.transport = transport
            return
        }
        task?.cancel(); saveTask?.cancel(); ready = false; state = CampPlanningState(); selectedID = nil; selectedVersion = 0; error = ""; publish()
        visibleSessions = visible; accessRevision &+= 1
        let generation = UUID()
        context = Context(scope: scope, actor: actor, transport: transport, generation: generation, epoch: 0)
        task = Task { [weak self] in
            do {
                let epoch = try await cache.epoch(scope)
                let saved = try await cache.planning(scope)
                guard let self, self.context?.generation == generation else { return }
                self.context?.epoch = epoch; self.state = saved
                self.state.details = saved.details.filter { $0.value.referencedSessions.isSubset(of: self.visibleSessions) }
                self.state.tickets = saved.tickets.filter { value in self.state.details["\(value.id):\(value.activeVersion)"] != nil }
                self.ready = true; self.publish()
                self.task = nil; self.refresh()
            } catch { guard self?.context?.generation == generation else { return }; self?.error = error.localizedDescription; self?.task = nil }
        }
    }
    private func publish() { drafts = state.drafts; tickets = state.tickets; changed?() }
    private func check(_ c: Context) throws {
        guard context?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func persist() async throws {
        guard let c = context, let cache else { throw CampCloudError.cacheUnavailable }
        state.revision += 1
        let snapshot = state
        try await cache.savePlanning(snapshot, scope: c.scope, expectedEpoch: c.epoch)
        try check(c)
    }
    func draft(_ id: String) -> CloudPlanDraft? { state.drafts.first { $0.id == id } }
    func detail(_ id: String, version: Int = 0) -> CloudPlanningResult? {
        let v = version == 0 ? state.tickets.first { $0.id == id }?.activeVersion ?? 1 : version
        return state.details["\(id):\(v)"]
    }
    func pending(_ id: String) -> Bool { state.operations.contains { $0.draftID == id || $0.planID == id } }
    func newDraft(input: CloudPlanningInput) async throws -> String {
        guard ready else { throw CampError.message("Connect to Camp before starting a shared plan") }
        guard state.drafts.count < 50 else { throw CampError.message("Finish or remove a draft before adding another plan") }
        var input = input
        for slot in input.planners.indices where input.planners[slot].execution == nil {
            input.planners[slot].execution = executionTargets[input.planners[slot].sessionId]
        }
        let draft = CloudPlanDraft(input: input)
        state.drafts.insert(draft, at: 0); publish()
        try await persist(); return draft.id
    }
    func edit(_ id: String, _ change: (inout CloudPlanningInput) -> Void) {
        guard let index = state.drafts.firstIndex(where: { $0.id == id }), !pending(id) else { return }
        change(&state.drafts[index].input); state.drafts[index].updatedAt = Date(); publish()
        saveTask?.cancel()
        saveTask = Task { [weak self] in
            do { try await Task.sleep(for: .milliseconds(200)); try Task.checkCancellation(); try await self?.persist() }
            catch is CancellationError {} catch { self?.error = "The planning draft could not be saved" }
        }
    }
    func selectModel(_ id: String, slot: Int, model: String) {
        guard let index = state.drafts.firstIndex(where: { $0.id == id }), !pending(id), state.drafts[index].input.planners.indices.contains(slot) else { return }
        let old = state.drafts[index].input.planners[slot]
        state.drafts[index].tuningHistory[old.sessionId + ":" + old.model] = ModelTuning(effort: old.effort, speed: old.speed)
        let tuning = state.drafts[index].tuningHistory[old.sessionId + ":" + model] ?? ModelTuning()
        edit(id) { value in value.planners[slot].model = model; value.planners[slot].effort = tuning.effort; value.planners[slot].speed = tuning.speed }
    }
    func executionNeedsReview(_ draft: CloudPlanDraft) -> Bool {
        draft.input.planners.contains { !$0.sessionId.isEmpty && ($0.execution == nil || $0.execution != executionTargets[$0.sessionId]) }
    }
    func reviewExecution(_ id: String) {
        edit(id) { input in
            for slot in input.planners.indices { input.planners[slot].execution = self.executionTargets[input.planners[slot].sessionId] }
        }
    }
    @discardableResult func selectDestination(_ id: String, slot: Int, expected: CloudPlannerInput, provider: String, source: CloudSession, scope: CloudScope) -> Bool {
        guard let c = context, c.scope == scope, ready, !pending(id), let draft = draft(id),
              draft.input.planners.indices.contains(slot), draft.input.planners[slot] == expected,
              source.campId == scope.campId, source.projectId == draft.input.projectId,
              source.ownerUserId == c.actor.userId, visibleSessions.contains(source.id),
              (!draft.input.shared || source.shared), ["Codex", "Claude"].contains(provider), source.provider == provider,
              sourceProviders[source.id] == source.provider,
              (expected.sessionId.isEmpty || sourceProviders[expected.sessionId] == source.provider),
              let execution = CloudPlanningExecution(session: source), executionTargets[source.id] == execution,
              execution.location == "cloud" || source.ownerDeviceId == c.actor.deviceId else {
            error = "The planner or destination changed. Review its current settings before applying"; return false
        }
        error = ""
        edit(id) { input in
            // An execution choice does not overwrite the user's model, effort
            // or speed, and does not change any running source session.
            input.planners[slot].sessionId = source.id
            input.planners[slot].execution = execution
        }
        return true
    }
    func restartDraft(_ ticket: CloudPlanningResult) async throws {
        let activeState = state.tickets.first { $0.id == ticket.id }?.state ?? ticket.version.state
        guard ticket.creatorUserId == context?.actor.userId, let revision = ticket.revision, !pending(ticket.id), !["running", "needs_review"].contains(activeState) else { throw CampError.message("Stop or review the current plan before starting over") }
        var input = CloudPlanningInput(title: ticket.title, projectId: ticket.projectId, brief: ticket.version.brief, shared: ticket.shared ?? false,
            planners: ticket.version.planners.sorted { $0.slot < $1.slot }.map { CloudPlannerInput(sessionId: $0.sessionId, model: $0.model, effort: $0.effort, speed: $0.speed, execution: executionTargets[$0.sessionId]) }, sources: ticket.version.sources)
        input.mergeSlot = ticket.version.mergeSlot
        input.mode = ticket.version.mode
        state.drafts.removeAll { $0.id == ticket.id }
        state.drafts.append(CloudPlanDraft(id: ticket.id, input: input, restartPlanID: ticket.id, baseRevision: revision))
        try await persist(); publish()
    }
    func start(_ id: String, sources: [CloudSourceRef]) async {
        guard let c = context, let index = state.drafts.firstIndex(where: { $0.id == id }), !pending(id) else { return }
        guard state.operations.count < 100 else { error = "The planning queue is full. Let pending actions sync first"; return }
        state.drafts[index].input.sources = sources
        let input = state.drafts[index].input
        guard !executionNeedsReview(state.drafts[index]) else {
            error = "A planner’s location changed or is unavailable. Review its current location before starting"; return
        }
        guard !input.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !input.brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              input.brief.utf8.count <= 8192, input.planners.count == input.workflow.plannerCount, input.planners.allSatisfy({ visibleSessions.contains($0.sessionId) }), input.budgetPolicy == "calls" else {
            error = "Choose the shared planning sessions, write a brief and enable the turn limit"; return
        }
        do {
            saveTask?.cancel()
            let draft = state.drafts[index]
            let command: CampPublication
            if let restart = draft.restartPlanID, let revision = draft.baseRevision {
                command = CampPublication(kind: "restart-plan", method: "POST", path: "/v1/camps/\(c.scope.campId)/planning/\(restart)/actions", body: try CampWire.body(["action":"restart", "expected_revision":revision, "input":JSONSerialization.jsonObject(with: CampWire.encoder().encode(input))]))
            } else {
                command = CampPublication(key: id, kind: "create-plan", method: "POST", path: "/v1/camps/\(c.scope.campId)/planning", body: try CampWire.encoder().encode(input))
            }
            state.operations.append(CampPlanOperation(command: command, draftID: id)); publish()
            try await persist(); wake()
        } catch { self.error = "The shared plan could not be queued" }
    }
    func cancelDraft(_ id: String) async {
        guard let index = state.drafts.firstIndex(where: { $0.id == id }) else { return }
        if pending(id) { state.drafts[index].cancelRequested = true }
        else { state.drafts.remove(at: index) }
        publish()
        do { try await persist(); wake() } catch { self.error = "The planning draft could not be saved" }
    }
    func act(_ action: String, on ticket: CloudPlanningResult, input: CloudPlanningInput? = nil) async {
        guard let c = context, ticket.creatorUserId == c.actor.userId, let revision = ticket.revision, !pending(ticket.id),
              ["start", "pause", "resume", "cancel", "restart"].contains(action) else { return }
        guard state.operations.count < 100 else { error = "The planning queue is full. Let pending actions sync first"; return }
        do {
            var body: [String: Any] = ["action": action, "expected_revision": revision]
            if action == "cancel" { body["expected_version"] = ticket.version.version }
            if let input { body["input"] = try JSONSerialization.jsonObject(with: CampWire.encoder().encode(input)) }
            let command = CampPublication(kind: "plan-action", method: "POST", path: "/v1/camps/\(c.scope.campId)/planning/\(ticket.id)/actions", body: try CampWire.body(body))
            state.operations.append(CampPlanOperation(command: command, planID: ticket.id)); publish()
            try await persist(); wake()
        } catch { self.error = "The planning action could not be queued" }
    }
    func open(_ id: String, version: Int = 0) { selectedID = id; selectedVersion = version; refresh() }
    func flush() async {
        saveTask?.cancel(); saveTask = nil
        if ready { do { try await persist() } catch { self.error = "The planning draft could not be saved" } }
    }
    func refresh() { dirty = true; wake() }
    private func wake() {
        guard task == nil, ready, let c = context else { return }
        task = Task { [weak self] in
            var delay = 0.5
            defer { if self?.context?.generation == c.generation { self?.task = nil } }
            while let self, self.context?.generation == c.generation, !Task.isCancelled {
                do {
                    while let op = self.state.operations.first {
                        try self.check(c)
                        let savedDraft = op.draftID.flatMap { self.draft($0) }
                        do {
                            try await self.persist()
                            self.error = ""
                            let data = try await c.transport.perform(op.command)
                            try self.check(c)
                            let result = try CampWire.decoder().decode(CloudPlanningResult.self, from: data)
                            guard result.valid, result.campId == c.scope.campId else { throw CampCloudError.invalidResponse }
                            self.state.operations.removeAll { $0.id == op.id }
                            let cancel = op.draftID.flatMap { self.draft($0)?.cancelRequested } ?? false
                            if let draft = op.draftID { self.state.drafts.removeAll { $0.id == draft } }
                            try self.ingest(result)
                            if cancel, let revision = result.revision {
                                let command = CampPublication(kind: "plan-action", method: "POST", path: "/v1/camps/\(c.scope.campId)/planning/\(result.id)/actions", body: try CampWire.body(["action":"cancel", "expected_revision":revision, "expected_version":result.version.version]))
                                self.state.operations.append(CampPlanOperation(command: command, planID: result.id))
                            }
                            try await self.persist()
                            if let draft = op.draftID { self.created?(draft, result.id) }
                            self.publish()
                        } catch CampCloudError.server(let status, _) where [400, 403, 404, 409].contains(status) {
                            try self.check(c)
                            self.state.operations.removeAll { $0.id == op.id }
                            self.error = status == 409 ? "This plan changed. Review its current version before trying again" : "This planning action is no longer available"
                            try await self.persist(); self.publish()
                        } catch {
                            if !self.state.operations.contains(where: { $0.id == op.id }) { self.state.operations.insert(op, at: 0) }
                            if let draft = savedDraft, self.draft(draft.id) == nil { self.state.drafts.append(draft) }
                            throw error
                        }
                    }
                    if self.dirty { self.dirty = false; try await self.synchronize(c); self.error = "" }
                    if self.state.operations.isEmpty && !self.dirty { return }
                    delay = 0.5
                } catch is CancellationError { return }
                catch CampCloudError.server(401, _) { self.error = "Sign in to Camp again to sync planning"; return }
                catch {
                    guard self.context?.generation == c.generation else { return }
                    self.error = "Shared planning is waiting to sync"
                    do { try await Task.sleep(for: .seconds(delay)) } catch { return }
                    delay = min(delay * 2, 15); self.dirty = true
                }
            }
        }
    }
    private func ingest(_ result: CloudPlanningResult) throws {
        let summary = try CloudPlanSummary(result)
        let key = "\(result.id):\(result.version.version)"
        state.details[key] = result
        if result.version.version == summary.activeVersion {
            if let index = state.tickets.firstIndex(where: { $0.id == result.id }) { state.tickets[index] = summary } else { state.tickets.insert(summary, at: 0) }
        }
        state.tickets = Array(state.tickets.sorted { $0.updatedAt > $1.updatedAt }.prefix(100))
        if state.details.count > 100 {
            let activeSelection = selectedID.flatMap { id in
                let version = selectedVersion > 0 ? selectedVersion : state.tickets.first { $0.id == id }?.activeVersion
                return version.map { "\(id):\($0)" }
            }
            let candidates = state.details.keys.filter { $0 != key && $0 != activeSelection }.sorted()
            for old in candidates.prefix(state.details.count - 100) { state.details[old] = nil }
        }
    }
    func install(_ result: CloudPlanningResult) async throws {
        guard let c = context, result.campId == c.scope.campId else { throw CampCloudError.invalidResponse }
        try ingest(result); try await persist(); publish()
    }
    private func synchronize(_ c: Context) async throws {
        let access = accessRevision
        let revision = state.revision
        let data = try await c.transport.perform(CampPublication(kind: "read", method: "GET", path: "/v1/camps/\(c.scope.campId)/planning", body: Data()))
        try check(c)
        guard accessRevision == access, state.revision == revision else { dirty = true; return }
        let values = try CampWire.decoder().decode([CloudPlanSummary].self, from: data)
        guard values.count <= 100, values.allSatisfy(\.valid), Set(values.map(\.id)).count == values.count else { throw CampCloudError.invalidResponse }
        state.tickets = values
        let allowed = Set(values.map(\.id)); state.details = state.details.filter { allowed.contains($0.value.id) }
        if let id = selectedID, allowed.contains(id) {
            let suffix = selectedVersion > 0 ? "?version=\(selectedVersion)" : ""
            let data = try await c.transport.perform(CampPublication(kind: "read", method: "GET", path: "/v1/camps/\(c.scope.campId)/planning/\(id)" + suffix, body: Data()))
            try check(c)
            guard accessRevision == access else { dirty = true; return }
            let result = try CampWire.decoder().decode(CloudPlanningResult.self, from: data)
            guard result.valid, result.campId == c.scope.campId else { throw CampCloudError.invalidResponse }
            try ingest(result)
        }
        try await persist(); publish()
    }
}
