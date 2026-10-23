import Foundation
import SwiftUI

struct PlanImportJournal: Codable, Sendable {
    var key = UUID().uuidString.lowercased()
    var prepared: PreparedLocalPlanImport
    var approved = false
    var valid: Bool { UUID(uuidString: key) != nil && prepared.valid }
}

@MainActor final class CampPlanImport: ObservableObject {
    private struct Context {
        var scope: CloudScope
        var actor: CloudActor
        var transport: any CampPublishingTransport
        var generation = UUID()
        var epoch: Int64 = 0
    }
    @Published private(set) var journal: PlanImportJournal?
    @Published private(set) var working = false
    @Published private(set) var ready = false
    @Published private(set) var error = ""
    @Published private(set) var imported: CloudPlanningResult?
    @Published private(set) var completedFingerprint: String?
    var installed: ((CloudPlanningResult) async throws -> Void)?
    private let cache: CampCache?
    private var context: Context?
    private var visible: [String: Int64] = [:]
    private var task: Task<Void, Never>?
    private var operation = UUID()
    private var editing = false
    init(cache: CampCache?) { self.cache = cache }
    deinit { task?.cancel() }

    func attach(scope: CloudScope?, actor: CloudActor?, transport: (any CampPublishingTransport)?, sessions: [CloudSession]) {
        let permissions = Dictionary(sessions.map { ($0.id, $0.permissionRevision) }, uniquingKeysWith: max)
        if let old = context, old.scope == scope, old.actor.userId == actor?.userId, old.actor.deviceId == actor?.deviceId, let transport {
            context?.transport = transport; visible = permissions
            if let journal, !hasAccess(journal.prepared) { pause(); error = "Source access changed. Reconnect it before retrying the saved import" }
            return
        }
        task?.cancel(); operation = UUID(); editing = false; task = nil; context = nil; journal = nil; imported = nil; completedFingerprint = nil; error = ""; ready = false; working = false; visible = permissions
        guard let scope, let actor, actor.userId == scope.userId, let transport, let cache else { return }
        let c = Context(scope: scope, actor: actor, transport: transport)
        context = c
        task = Task { [weak self] in
            do {
                let epoch = try await cache.epoch(scope)
                let data = try await cache.metadata("planning-import:" + scope.key)
                guard let self else { return }
                try self.check(c)
                let saved = try data.map { try CampWire.decoder().decode(PlanImportJournal.self, from: $0) }
                guard saved?.valid ?? true else { throw CampCloudError.invalidResponse }
                guard try await cache.epoch(scope) == epoch else { throw CancellationError() }
                try self.check(c)
                self.context?.epoch = epoch; self.journal = saved; self.ready = true; self.task = nil
                // The saved approval authorizes only this immutable import body.
                if saved?.approved == true { self.retry() }
            } catch { self?.finish(error, context: c) }
        }
    }
    private func check(_ c: Context) throws {
        guard context?.generation == c.generation, !Task.isCancelled else { throw CancellationError() }
    }
    private func hasAccess(_ prepared: PreparedLocalPlanImport) -> Bool {
        prepared.permissions.allSatisfy { visible[$0.key] == $0.value }
    }
    private func authority(_ c: Context, prepared: PreparedLocalPlanImport? = nil) async throws {
        try check(c)
        guard let cache, try await cache.epoch(c.scope) == c.epoch else { throw CancellationError() }
        try check(c)
        if let prepared, !hasAccess(prepared) { throw CampError.message("A source changed access before the import finished") }
    }
    func review(_ prepared: PreparedLocalPlanImport) async throws {
        guard ready, !working, !editing, let c = context, let cache, prepared.valid, hasAccess(prepared) else { throw CampCloudError.cacheUnavailable }
        guard journal?.approved != true else { throw CampError.message("Finish the saved import before preparing another one") }
        editing = true
        defer { if context?.generation == c.generation { editing = false } }
        try await authority(c, prepared: prepared)
        let value = PlanImportJournal(prepared: prepared)
        try await cache.savePlanImport(value, scope: c.scope, expectedEpoch: c.epoch)
        try await authority(c, prepared: prepared)
        journal = value; imported = nil; completedFingerprint = nil; error = ""
    }
    func submit(current plan: LocalPlan) async throws {
        guard ready, !working, !editing, let c = context, let cache, var value = journal else { throw CampCloudError.cacheUnavailable }
        guard value.prepared.localFingerprint == (try LocalPlanImport.fingerprint(plan)) else {
            throw CampError.message("The local ticket changed. Review its latest history before importing")
        }
        editing = true
        defer { if context?.generation == c.generation { editing = false } }
        try await authority(c, prepared: value.prepared)
        value.approved = true
        try await cache.savePlanImport(value, scope: c.scope, expectedEpoch: c.epoch)
        try await authority(c, prepared: value.prepared)
        journal = value; editing = false; retry()
    }
    func discardReview() async throws {
        guard let c = context, let cache, !working, !editing, journal?.approved != true else { throw CampError.message("A submitted import must be reconciled before clearing it") }
        editing = true
        defer { if context?.generation == c.generation { editing = false } }
        try await authority(c)
        try await cache.savePlanImport(nil, scope: c.scope, expectedEpoch: c.epoch)
        try check(c); journal = nil; error = ""
    }
    func pause() { operation = UUID(); task?.cancel(); task = nil; working = false }
    func retry() {
        guard ready, !working, !editing, let c = context, let value = journal, value.approved, hasAccess(value.prepared) else { return }
        working = true; error = ""; operation = UUID(); let work = operation
        task = Task { [weak self] in
            guard let self else { return }
            do {
                try await self.authority(c, prepared: value.prepared)
                let operation = CampPublication(key: value.key, kind: "plan-import", method: "POST",
                    path: "/v1/camps/\(c.scope.campId)/planning/import", body: try value.prepared.body)
                let data = try await c.transport.perform(operation)
                try await self.authority(c, prepared: value.prepared)
                let result = try CampWire.decoder().decode(CloudPlanningResult.self, from: data)
                guard Self.matches(result, value.prepared, scope: c.scope) else { throw CampCloudError.invalidResponse }
                try await self.installed?(result)
                try await self.authority(c, prepared: value.prepared)
                try await self.cache?.savePlanImport(nil, scope: c.scope, expectedEpoch: c.epoch)
                try self.check(c)
                self.imported = result; self.completedFingerprint = value.prepared.localFingerprint; self.journal = nil; self.working = false; self.task = nil
            } catch CampCloudError.server(let code, let message) where [400, 403, 404, 409, 413, 422].contains(code) {
                // The server refused this import outright and recorded nothing.
                // Return it to review so it can be discarded or started over.
                guard self.operation == work else { return }
                var refused = value; refused.approved = false
                if (try? await self.cache?.savePlanImport(refused, scope: c.scope, expectedEpoch: c.epoch)) != nil, self.context?.generation == c.generation { self.journal = refused }
                self.finish(CampCloudError.server(code, message), context: c)
            } catch { if self.operation == work { self.finish(error, context: c) } }
        }
    }
    private func finish(_ failure: Error, context c: Context) {
        guard context?.generation == c.generation else { return }
        working = false; task = nil
        if !(failure is CancellationError) { error = failure.localizedDescription }
    }
    private static func matches(_ result: CloudPlanningResult, _ prepared: PreparedLocalPlanImport, scope: CloudScope) -> Bool {
        guard result.valid, result.campId == scope.campId, result.creatorUserId == scope.userId, result.shared == false,
              result.projectId == prepared.input.projectId, result.title == prepared.input.title,
              result.activeVersion == prepared.input.versions.count, let last = prepared.input.versions.last else { return false }
        let actual = result.version
        return actual.version == last.version && actual.brief == last.brief && actual.workflow == last.mode &&
            actual.budgetPolicy == last.budgetPolicy && actual.mergeSlot == last.mergeSlot && actual.sources == last.sources &&
            actual.imported == ImportedPlanningSnapshot(state: last.state, localSources: last.localSources, steps: last.steps) &&
            actual.planners.map { CloudPlannerInput(sessionId: $0.sessionId, model: $0.model, effort: $0.effort, speed: $0.speed, execution: $0.execution) } == last.planners
    }
}
