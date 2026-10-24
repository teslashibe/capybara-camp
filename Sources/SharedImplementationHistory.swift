import Foundation
import SwiftUI

struct SharedImplementationSummary: Codable, Equatable, Identifiable, Sendable {
    var id: String; var approvalId: String; var planVersion: Int; var createdAt: Date
    var requestId: String; var sessionId: String; var ownerUserId: String
    var provider: String; var model: String; var effort: String; var speed: String; var state: String; var executionLocation: String
    var stage: Stage { ["queued", "claimed", "started", "cancel_requested"].contains(state) ? .progress : .attention }
    var statusLabel: String {
        switch state {
        case "pending_approval": return "Waiting for approval"
        case "queued": return "Queued for implementation"
        case "claimed": return "Starting implementation"
        case "started": return "Implementing"
        case "cancel_requested": return "Stopping implementation"
        case "completed": return "Implementation ready to review"
        case "outcome_unknown": return "Implementation needs review"
        case "failed": return "Implementation failed"
        case "cancelled": return "Implementation cancelled"
        default: return "Implementation"
        }
    }
    var valid: Bool {

        [id, approvalId, requestId, sessionId, ownerUserId].allSatisfy { UUID(uuidString: $0) != nil } &&
        planVersion > 0 && ["Codex", "Claude"].contains(provider) && model.utf8.count <= 200 && effort.utf8.count <= 64 && speed.utf8.count <= 64 &&
        ["local", "cloud"].contains(executionLocation) && sharedImplementationStates.contains(state)
    }
}
private let sharedImplementationStates: Set<String> = ["pending_approval", "queued", "claimed", "started", "cancel_requested", "outcome_unknown", "completed", "failed", "cancelled"]
struct SharedImplementationPage: Codable, Sendable { var implementations: [SharedImplementationSummary]; var before: String? }
struct SharedImplementationRequests: Codable, Sendable {
    var id: String; var approvalId: String; var planId: String; var planVersion: Int
    var providerSessionId: String?; var createdAt: Date; var requests: [CloudRequest]; var requestsBefore: String?
}

// Retain only the open plan's bounded history. Provider text is replayed by the
// shared live reader for the selected request, rather than for every old turn.
@MainActor final class SharedImplementationHistory: ObservableObject {
    @Published private(set) var runs: [SharedImplementationSummary] = []
    @Published private(set) var selected: SharedImplementationSummary?
    @Published private(set) var requests: [CloudRequest] = []
    @Published var selectedRequestID: String?
    @Published private(set) var loading = false
    @Published private(set) var error = ""
    @Published private(set) var runsBefore: String?
    @Published private(set) var requestsBefore: String?
    @Published private(set) var providerSessionID: String?
    private var latestPage = true
    private var requestRefreshPending = false
    var currentPlan: CloudPlanningResult? { plan }
    var canContinue: Bool {
        guard let selected, let plan, selected.planVersion == plan.activeVersion else { return false }
        return !(providerSessionID ?? requests.first(where: { !($0.providerSessionId ?? "").isEmpty })?.providerSessionId ?? "").isEmpty &&
            !requests.contains { $0.state == "outcome_unknown" || $0.contentRestricted == true }
    }
    var selectedRequest: CloudRequest? { requests.first { $0.id == selectedRequestID } }
    private let planning: CampPlanning
    private var plan: CloudPlanningResult?
    private var task: Task<Void, Never>?
    private var generation = UUID()
    private var refreshPending = false
    init(planning: CampPlanning) { self.planning = planning }
    deinit { task?.cancel() }
    func clear() {
        generation = UUID(); task?.cancel(); task = nil; plan = nil; loading = false; error = ""; refreshPending = false
        providerSessionID = nil; latestPage = true; requestRefreshPending = false
        runs = []; selected = nil; requests = []; selectedRequestID = nil; runsBefore = nil; requestsBefore = nil
    }
    func bind(_ plan: CloudPlanningResult) { clear(); self.plan = plan; loadRuns() }
    func synchronize(_ updates: [CloudRequest]) {
        guard let plan else { return }
        let relevant = updates.filter { $0.isImplementation && $0.planId == plan.id && $0.planVersion == plan.version.version }
        if relevant.contains(where: { $0.contentRestricted == true }) {
            // Re-read after invalidation; never merge older text over a revoked grant.
            bind(plan); return
        }
        requests = requests.map { saved in relevant.first(where: { $0.id == saved.id }).map { $0.merging(saved) } ?? saved }
        for index in runs.indices {
            if let request = relevant.first(where: { $0.id == runs[index].requestId }) { runs[index].state = request.state }
        }
        if let selected, relevant.contains(where: { $0.implementationId == selected.id && !requests.contains($0) }) {
            // Only follow new turns while reading the latest request page. Older
            // history stays put until the user explicitly returns to Latest.
            if latestPage && (selectedRequestID == requests.first?.id || requests.isEmpty),
               relevant.contains(where: { update in update.implementationId == selected.id && !requests.contains { $0.id == update.id } }) {
                requestRefreshPending = true
                if !loading { refreshSelectedRequests() }
            }
        }
        if relevant.contains(where: { value in !runs.contains { $0.id == value.implementationId } }) {
            refreshPending = true
            if !loading { reloadRuns() }
        }
    }
    private func reloadRuns() { refreshPending = false; runsBefore = nil; loadRuns() }
    private func start(_ operation: @escaping (UUID, CloudPlanningResult) async throws -> Void) {
        guard let plan else { return }
        generation = UUID(); task?.cancel(); let token = generation; loading = true; error = ""
        task = Task { [weak self] in
            guard let self else { return }
            defer {
                if self.generation == token {
                    self.loading = false; self.task = nil
                    if self.requestRefreshPending { self.refreshSelectedRequests() }
                    else if self.refreshPending { self.reloadRuns() }
                }
            }
            do { try await operation(token, plan) }
            catch is CancellationError {}
            catch {
                guard self.generation == token else { return }
                if case CampCloudError.server(let status, _) = error, [401,403,404].contains(status) { self.clear() }
                self.error = error.localizedDescription
            }
        }
    }
    private func check(_ token: UUID) throws { if generation != token || Task.isCancelled { throw CancellationError() } }
    func loadRuns() {
        guard !loading else { return }
        let cursor = runsBefore
        start { [weak self] token, plan in
            guard let self else { return }
            let data = try await self.planning.readImplementationRuns(plan, before: cursor)
            let page = try CampWire.decoder().decode(SharedImplementationPage.self, from: data)
            try self.check(token)
            guard page.implementations.count <= 20,
                  page.implementations.allSatisfy({ $0.valid && $0.planVersion == plan.version.version && self.planning.implementationSourceVisible($0.sessionId) }),
                  page.before == nil || page.before == page.implementations.last?.id && page.before != cursor,
                  Set(page.implementations.map(\.id)).count == page.implementations.count else { throw CampCloudError.invalidResponse }
            self.runs = page.implementations; self.runsBefore = page.before
        }
    }
    func select(_ run: SharedImplementationSummary) {
        guard runs.contains(run) else { return }
        selected = run; requests = []; requestsBefore = nil; selectedRequestID = nil; providerSessionID = nil; latestPage = true; requestRefreshPending = false
        start { [weak self] token, plan in try await self?.readRequests(run, token: token, plan: plan, before: nil) }
    }
    private func refreshSelectedRequests() {
        requestRefreshPending = false
        guard let selected else { return }
        start { [weak self] token, plan in try await self?.readRequests(selected, token: token, plan: plan, before: nil) }
    }
    func loadEarlier() {
        guard !loading, let selected, let cursor = requestsBefore else { return }
        latestPage = false; requestRefreshPending = false
        start { [weak self] token, plan in try await self?.readRequests(selected, token: token, plan: plan, before: cursor) }
    }
    private func readRequests(_ run: SharedImplementationSummary, token: UUID, plan: CloudPlanningResult, before: String?) async throws {
        let data = try await planning.readImplementationRuns(plan, implementationID: run.id, before: before)
        let page = try CampWire.decoder().decode(SharedImplementationRequests.self, from: data)
        try check(token)
        guard page.id == run.id, page.approvalId == run.approvalId, page.planId == plan.id, page.planVersion == plan.version.version,
              page.requests.count <= 20, page.requests.allSatisfy({ request in
                  request.isImplementation && request.contentRestricted != true && request.campId == plan.campId &&
                  request.implementationId == run.id && request.planId == plan.id && request.planVersion == run.planVersion &&
                  request.sessionId == run.sessionId && request.ownerUserId == run.ownerUserId && UUID(uuidString: request.id) != nil &&
                  request.prompt.utf8.count <= 65536 && request.model.utf8.count <= 200 && sharedImplementationStates.contains(request.state) &&
                  (request.leaseToken == nil || request.leaseToken == "")
              }), page.requestsBefore == nil || page.requestsBefore == page.requests.last?.id && page.requestsBefore != before,
              Set(page.requests.map(\.id)).count == page.requests.count else { throw CampCloudError.invalidResponse }
        providerSessionID = page.providerSessionId; latestPage = before == nil
        requests = page.requests; requestsBefore = page.requestsBefore
        selectedRequestID = requests.first?.id
    }
}
