import Foundation
import SwiftUI

struct PlanPromotionJob: Codable {
    var plan: LocalPlan
    var projectId: String
    var fingerprint: String
    var phase = "copying"
    var valid: Bool {
        LocalPlanning.valid([plan]) && !plan.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && plan.title.utf8.count <= 2000 &&
        UUID(uuidString: projectId) != nil && ["copying", "importing"].contains(phase) &&
        (try? LocalPlanImport.fingerprint(plan)) == fingerprint && plan.versions.allSatisfy {
            $0.state != "running" && ($0.state != "draft" || $0.steps.isEmpty) &&
                ($0.state != "ready" || $0.steps.count == $0.workflow.turnLimit && $0.steps.allSatisfy { $0.state == "completed" && !$0.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) &&
                $0.steps.allSatisfy { ImportedPlanningStep(ordinal: $0.ordinal, state: $0.state, output: $0.output, error: $0.error, usage: $0.usage).valid }
        } && (plan.implementations ?? []).allSatisfy { !$0.active && $0.attempts.allSatisfy { !["starting", "running"].contains($0.state) } }
    }
}
struct PlanPromotionLink: Codable {
    var planId: String
    var localUpdated: Double
    var fingerprint: String
}
struct PlanPromotionState: Codable {
    var job: PlanPromotionJob?
    var links: [String: PlanPromotionLink] = [:]
    var valid: Bool {
        (job?.valid ?? true) && links.count <= 1000 && links.allSatisfy {
            $0.key.hasPrefix("plan:") && UUID(uuidString: String($0.key.dropFirst(5))) != nil && UUID(uuidString: $0.value.planId) != nil &&
                $0.value.localUpdated.isFinite && $0.value.fingerprint.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil
        }
    }
}

@MainActor final class CampPlanPromotion: ObservableObject {
    private struct Context { var scope: CloudScope; var deviceID: String; var generation = UUID(); var epoch: Int64 = 0 }
    private struct Source { var local: CampSession; var snapshot: HistorySourceSnapshot }
    @Published private(set) var job: PlanPromotionJob?
    @Published private(set) var links: [String: PlanPromotionLink] = [:]
    @Published private(set) var ready = false
    @Published private(set) var working = false
    @Published private(set) var error = ""
    @Published private(set) var copied = 0
    @Published private(set) var total = 0
    let upload: CampPlanImport
    var changed: (() -> Void)?
    var completed: ((String, String) -> Void)?
    private let cache: CampCache?
    private let sharing: CampSharing
    private var context: Context?
    private var sessions: [String: CloudSession] = [:]
    private var state = PlanPromotionState()
    private var task: Task<Void, Never>?
    private var configured: Set<String> = []
    private var sourceCache: [Source] = []
    private var sourceFingerprint = ""
    private var editing = false
    init(cache: CampCache?, sharing: CampSharing) { self.cache = cache; self.sharing = sharing; upload = CampPlanImport(cache: cache) }
    deinit { task?.cancel() }
    func linkedPlan(_ localID: String) -> String? { links[localID]?.planId }
    func replaces(_ plan: LocalPlan) -> Bool { links[plan.id]?.localUpdated == plan.updated }
    private func publish() { job = state.job; links = state.links; changed?() }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?, sessions: [CloudSession]) {
        upload.attach(scope: scope, actor: actor, transport: transport, sessions: sessions)
        self.sessions = Dictionary(sessions.map { ($0.id, $0) }, uniquingKeysWith: { _, new in new })
        if let c = context, c.scope == scope, c.deviceID == actor?.deviceId, transport != nil { return }
        task?.cancel(); task = nil; context = nil; state = PlanPromotionState(); configured = []; sourceCache = []; sourceFingerprint = ""; ready = false; working = false; editing = false; error = ""; copied = 0; total = 0; publish()
        guard let scope, let actor, actor.userId == scope.userId, let cache, transport != nil else { return }
        let c = Context(scope: scope, deviceID: actor.deviceId); context = c
        task = Task { [weak self] in
            do {
                let epoch = try await cache.epoch(scope)
                let data = try await cache.metadata("plan-promotion:" + scope.key)
                let saved = try data.map { try JSONDecoder().decode(PlanPromotionState.self, from: $0) } ?? PlanPromotionState()
                guard saved.valid else { throw CampCloudError.invalidResponse }
                guard let self else { return }; try self.check(c)
                guard try await cache.epoch(scope) == epoch else { throw CancellationError() }
                try self.check(c); self.context?.epoch = epoch; self.state = saved; self.ready = true; self.task = nil; self.publish()
                if saved.job != nil { self.retry() }
            } catch { self?.failed(error, context: c) }
        }
    }
    private func check(_ c: Context) throws {
        guard context?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func authority(_ c: Context) async throws {
        try check(c)
        guard let cache, try await cache.epoch(c.scope) == c.epoch else { throw CancellationError() }
        try check(c)
    }
    private func persist(_ c: Context) async throws {
        guard state.valid, let cache else { throw CampCloudError.invalidResponse }
        try await authority(c)
        try await cache.savePlanPromotion(JSONEncoder().encode(state), scope: c.scope, expectedEpoch: c.epoch)
        try check(c)
    }
    private func validateReferences(_ plan: LocalPlan, _ c: Context, local: Bool) throws {
        for version in plan.versions {
            var quotes: [ImportedPlanningSource] = [], shared = 0
            for ref in version.sources {
                if let source = ref.cloud {
                    guard ref.scope == c.scope.key, ref.id == "camp:" + source.sessionId,
                          let current = sessions[source.sessionId], current.permissionRevision == source.permissionRevision,
                          source.permissionRevision > 0, source.transcriptRevision > 0, current.transcriptRevision >= source.transcriptRevision,
                          let ids = source.messageIds, (1...120).contains(ids.count), Set(ids).count == ids.count,
                          ids.allSatisfy({ !$0.isEmpty && $0.utf8.count <= 256 }) else { throw CampError.message("A tagged Camp source is no longer available") }
                    shared += 1
                } else if local {
                    guard let messages = ref.messages, let digest = ref.digest,
                          digest == PublishMessage.hash(try CampWire.encoder().encode(messages.map(\.contextMessage))) else { throw CampError.message("A tagged local source has no complete saved snapshot") }
                    quotes.append(ImportedPlanningSource(id: PublishMessage.hash(Data(ref.id.utf8)), title: ref.title ?? "", revision: digest, messages: messages))
                }
            }
            if local, !validPlanningQuotes(quotes, sharedCount: shared) { throw CampError.message("The saved context exceeds the import limits") }
        }
    }
    private func sources(_ plan: LocalPlan) throws -> [Source] {
        var output: [Source] = [], seen: Set<String> = []
        for planner in plan.versions.flatMap(\.planners) {
            let id = LocalPlanImport.plannerHistoryID(planner.session)
            if seen.insert(id).inserted {
                output.append(Source(local: LocalPlanImport.historySession(planner.session, id: id, title: planner.session.title, model: planner.model), snapshot: .init(messages: [])))
            }
        }
        for execution in plan.implementations ?? [] {
            let id = LocalPlanImport.implementationHistoryID(execution), snapshot = HistorySourceSnapshot(messages: execution.messages)
            guard snapshot.valid else { throw CampError.message("An implementation transcript exceeds the copy limits") }
            output.append(Source(local: LocalPlanImport.historySession(execution.executor.session, id: id, title: plan.title, model: execution.executor.model), snapshot: snapshot))
        }
        return output
    }
    private func cachedSources(_ job: PlanPromotionJob) throws -> [Source] {
        if sourceFingerprint != job.fingerprint {
            sourceCache = try sources(job.plan); sourceFingerprint = job.fingerprint
        }
        return sourceCache
    }
    func approve(_ plan: LocalPlan, projectID: String) async throws {
        guard ready, !working, !editing, state.job == nil, links[plan.id] == nil, links.count < 1000, let c = context else { throw CampError.message("Finish the current import before saving another ticket") }
        let value = PlanPromotionJob(plan: plan, projectId: projectID, fingerprint: try LocalPlanImport.fingerprint(plan))
        guard value.valid, try JSONEncoder().encode(plan).count <= 16 << 20 else { throw CampError.message("Stop active work and review its outcome before saving this ticket") }
        try validateReferences(plan, c, local: true); _ = try cachedSources(value)
        editing = true; defer { if context?.generation == c.generation { editing = false } }
        try await authority(c); state.job = value
        do { try await persist(c) } catch { if context?.generation == c.generation { state.job = nil; sourceCache = []; sourceFingerprint = "" }; throw error }
        publish(); editing = false; retry()
    }
    func retry() {
        guard ready, !working, !editing, let job = state.job, let c = context else { return }
        do { _ = try cachedSources(job); try validateReferences(job.plan, c, local: true) }
        catch { failed(error, context: c); return }
        error = ""; working = true; configured = []; sharing.retry()
        if let pending = upload.journal, pending.approved, pending.prepared.localFingerprint == job.fingerprint { upload.retry() }
        task = Task { [weak self] in
            do {
                while !Task.isCancelled {
                    guard let done = try await self?.advance(c) else { return }
                    if done { return }
                    try await Task.sleep(for: .milliseconds(300))
                }
            } catch { self?.failed(error, context: c) }
        }
    }
    func discard() async throws {
        guard !working, !editing, let c = context, upload.journal?.approved != true else { throw CampError.message("Reconcile the submitted import before clearing it") }
        editing = true; defer { if context?.generation == c.generation { editing = false } }
        if upload.journal != nil { try await upload.discardReview() }
        try await authority(c); let old = state.job; state.job = nil
        do { try await persist(c) } catch { if context?.generation == c.generation { state.job = old }; throw error }
        error = ""; copied = 0; total = 0; sourceCache = []; sourceFingerprint = ""; publish()
    }
    private func failed(_ failure: Error, context c: Context) {
        guard context?.generation == c.generation else { return }
        working = false; task = nil
        if !(failure is CancellationError) { error = failure.localizedDescription }
    }
    private func advance(_ c: Context) async throws -> Bool {
        try await authority(c)
        guard let job = state.job else { working = false; task = nil; return true }
        try validateReferences(job.plan, c, local: false)
        if let result = upload.imported, upload.completedFingerprint == job.fingerprint {
            var next = state; next.links[job.plan.id] = PlanPromotionLink(planId: result.id, localUpdated: job.plan.updated, fingerprint: job.fingerprint); next.job = nil
            let previous = state; state = next
            do { try await persist(c) } catch { if context?.generation == c.generation { state = previous }; throw error }
            working = false; task = nil; sourceCache = []; sourceFingerprint = ""; completed?(job.plan.id, result.id); publish(); return true
        }
        guard sharing.ready, upload.ready else { return false }
        if job.phase == "copying" {
            let values = try cachedSources(job)
            if total != values.count { total = values.count }
            for value in values where !configured.contains(value.local.id) {
                try await sharing.configureHistory(local: value.local, snapshot: value.snapshot, projectId: job.projectId)
                try await authority(c); configured.insert(value.local.id)
            }
            let records = values.compactMap { value -> LocalShare? in
                guard let record = sharing.binding(for: value.local), let remote = record.remote,
                      record.publishedHistory?.mainRevision == value.snapshot.revision,
                      record.publishedHistory?.transcriptRevision == remote.transcriptRevision,
                      ["Shared", "Saved to Camp"].contains(record.state), sessions[remote.id]?.permissionRevision == remote.permissionRevision else { return nil }
                return record
            }
            if copied != records.count { copied = records.count }
            if let problem = values.compactMap({ sharing.binding(for: $0.local)?.state }).first(where: { ["Needs review", "Sign in to Camp again"].contains($0) }) {
                throw CampError.message(problem + " before continuing the history copy")
            }
            guard records.count == values.count else { return false }
            var planners: [String: LocalShare] = [:], implementations: [String: ImportedTranscriptBinding] = [:]
            for planner in job.plan.versions.flatMap(\.planners) {
                let id = LocalPlanImport.plannerHistoryID(planner.session)
                guard let record = records.first(where: { $0.local.id == id }) else { throw CampCloudError.invalidResponse }
                planners[planner.session.id] = record
            }
            for item in job.plan.implementations ?? [] {
                implementations[item.id] = try await sharing.historyTranscript(for: LocalPlanImport.implementationHistoryID(item))
                try await authority(c)
            }
            let prepared = try LocalPlanImport.prepare(job.plan, scope: c.scope, projectID: job.projectId, planners: planners, implementations: implementations, visible: sessions)
            if let pending = upload.journal, pending.approved {
                guard pending.prepared == prepared else { throw CampError.message("A different import is awaiting reconciliation") }
            } else { try await upload.review(prepared) }
            try await authority(c); state.job?.phase = "importing"; try await persist(c); publish()
        }
        if state.job?.phase == "importing" {
            if let pending = upload.journal {
                guard pending.prepared.localFingerprint == job.fingerprint else { throw CampCloudError.invalidResponse }
                if !pending.approved { try await upload.submit(current: job.plan) }
                else if !upload.working {
                    if !upload.error.isEmpty { throw CampError.message(upload.error) }
                    upload.retry()
                }
            } else if upload.completedFingerprint != job.fingerprint {
                state.job?.phase = "copying"; try await persist(c); configured = []; publish()
            }
        }
        return false
    }
}
