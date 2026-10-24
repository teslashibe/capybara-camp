import Foundation
import SwiftUI

@MainActor final class SharedExecutionReview: ObservableObject {
    @Published private(set) var status: SharedExecutionStatus?
    @Published private(set) var approval: SharedExecutionApproval?
    @Published private(set) var loading = false
    @Published private(set) var error = ""
    @Published private(set) var target: CloudExecutionTarget?
    @Published private(set) var targetError = ""
    private let planning: CampPlanning
    private var plan: CloudPlanningResult?
    private var generation = UUID()
    private var targetGeneration = UUID()
    private var task: Task<Void, Never>?
    init(planning: CampPlanning) { self.planning = planning }
    deinit { task?.cancel() }
    func clear() {
        generation = UUID(); targetGeneration = UUID(); task?.cancel(); task = nil
        plan = nil; status = nil; approval = nil; target = nil; loading = false; error = ""; targetError = ""
    }
    func bind(_ plan: CloudPlanningResult) { clear(); self.plan = plan; refresh() }
    func refresh() {
        guard let plan else { return }
        generation = UUID(); let token = generation; task?.cancel(); loading = true; error = ""
        approval = nil; status = nil
        task = Task { [weak self] in
            guard let self else { return }
            defer { if self.generation == token { self.loading = false; self.task = nil } }
            do {
                let data = try await self.planning.readExecutionResource(planID: plan.id)
                let values = try CampWire.decoder().decode([SharedExecutionStatus].self, from: data)
                try self.check(token)
                guard values.count <= 100, values.allSatisfy(\.valid), Set(values.map(\.id)).count == values.count,
                      values.filter(\.actionable).count <= 1 else { throw CampCloudError.invalidResponse }
                self.status = values.first(where: \.actionable)
                if let status = self.status, !status.reviewRequired {
                    let bytes = try await self.planning.readExecutionResource(planID: plan.id, approvalID: status.id)
                    let value = try CampWire.decoder().decode(SharedExecutionApproval.self, from: bytes)
                    try self.check(token)
                    guard value.valid, value.id == status.id, value.revision == status.revision, value.state == status.state,
                          value.campId == plan.campId, value.planId == plan.id, value.planVersion == plan.version.version,
                          value.creatorUserId == self.planning.currentActor?.userId || value.ownerUserId == self.planning.currentActor?.userId else { throw CampError.message("The implementation review changed. Open its current plan version") }
                    self.approval = value
                }
            } catch is CancellationError {}
            catch { if self.generation == token { self.approval = nil; self.error = error.localizedDescription } }
        }
    }
    private func check(_ token: UUID) throws {
        if generation != token || Task.isCancelled { throw CancellationError() }
    }
    func loadTarget(_ source: CloudSession?) async {
        targetGeneration = UUID(); let token = targetGeneration; target = nil; targetError = ""
        guard let source, let plan else { return }
        do {
            let data = try await planning.readExecutionResource(planID: plan.id, executorID: source.id)
            let value = try CampWire.decoder().decode(CloudExecutionTarget.self, from: data)
            guard token == targetGeneration, !Task.isCancelled else { return }
            guard value.valid, value.location == source.executionLocation, value.epoch == source.executionEpoch,
                  value.workspaceId == source.executionWorkspaceId, let settings = PermissionSettings(wireValues: value.permissions),
                  settings.valid(for: source.provider) else { throw CampCloudError.invalidResponse }
            target = value
        } catch is CancellationError {}
        catch { if token == targetGeneration { targetError = error.localizedDescription } }
    }
    func prepare(source: CloudSession, model: String, tuning: ModelTuning, permissions: PermissionSettings) async throws {
        guard let plan, let scope = planning.currentScope, plan.creatorUserId == scope.userId,
              plan.version.state == "ready", plan.version.imported == nil, plan.activeVersion == plan.version.version,
              let revision = plan.revision, let execution = CloudPlanningExecution(session: source),
              let target, target.available, target.location == execution.location, target.epoch == execution.epoch, target.workspaceId == execution.workspaceId,
              source.campId == scope.campId, source.projectId == plan.projectId, !source.externalOwner, !source.isHistoryOnly,
              source.ownerUserId == scope.userId || source.allowRequests, !model.isEmpty, model.utf8.count <= 200,
              permissions.valid(for: source.provider), target.location != "cloud" || target.mode == "implementation" && target.permissions == permissions.wireValues else { throw CampError.message("Review the executor, location and permissions before continuing") }
        let input = SharedExecutionInput(expectedRevision: revision, expectedVersion: plan.version.version,
            executor: CloudPlannerInput(sessionId: source.id, model: model, effort: tuning.effort, speed: tuning.speed, execution: execution),
            expectedSessionRevision: source.revision, expectedTranscriptRevision: source.transcriptRevision, permissions: permissions.wireValues)
        try await planning.queueExecution(.prepare, planID: plan.id, body: CampWire.encoder().encode(input))
    }
    func approve(inspect: (SharedExecutionApproval) async throws -> String) async throws {
        guard let plan, let value = approval, let actor = planning.currentActor, value.state == "pending", value.ownerUserId == actor.userId,
              value.execution.location == "cloud" || value.ownerDeviceId == actor.deviceId else { throw CampError.message("Approve on the executor owner's assigned device") }
        let token = generation
        let fingerprint = value.execution.location == "local" ? try await inspect(value) : ""
        try check(token)
        guard approval == value, value.execution.location != "local" || fingerprint.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil else { throw CampCloudError.invalidResponse }
        try await planning.queueExecution(.approve, planID: plan.id, approvalID: value.id,
            body: CampWire.body(["expected_revision": value.revision, "execution_context": fingerprint]))
    }
    func dispatch() async throws {
        guard let plan, let value = approval, value.state == "approved", status?.reviewRequired == false else { throw CampCloudError.invalidResponse }
        try await planning.queueExecution(.dispatch, planID: plan.id, approvalID: value.id, body: CampWire.body(["expected_revision": value.revision]))
    }
    func cancel() async throws {
        guard let plan, let status, status.actionable else { throw CampCloudError.invalidResponse }
        try await planning.queueExecution(.cancel, planID: plan.id, approvalID: status.id, body: CampWire.body(["expected_revision": status.revision]))
    }
}
