import Foundation
import SwiftUI

enum PlanningMode: String, Codable, CaseIterable, Sendable {
    case single, comparison
    var title: String { self == .single ? "Plan" : "Plan with comparison" }
    var plannerCount: Int { self == .single ? 1 : 2 }
    var turnLimit: Int { self == .single ? 1 : 5 }
    var resultOrdinal: Int { turnLimit - 1 }
    var budgetLabel: String { self == .single ? "Limit to one turn" : "Limit to five turns" }
    func accepts(_ providers: [String]) -> Bool {
        providers.count == plannerCount && Set(providers).count == plannerCount && providers.allSatisfy { ["Codex", "Claude"].contains($0) }
    }
}

struct LocalPlanner: Codable, Equatable {
    var session: CampSession
    var model: String
    var tuning: ModelTuning
    var tuningByModel: [String: ModelTuning] = [:]
}

struct LocalPlanStep: Codable, Equatable, Identifiable {
    var ordinal: Int
    var slot: Int
    var phase: String
    var state = "pending"
    var output = ""
    var error = ""
    var usage = ConversationUsage()
    var id: Int { ordinal }
}

struct LocalPlanVersion: Codable, Equatable, Identifiable {
    var number: Int
    var brief: String
    var planners: [LocalPlanner]
    var sources: [SessionReference] = []
    var budgetPolicy = "strict_tokens"
    var mergeSlot = 0
    var state = "draft"
    var steps: [LocalPlanStep] = []
    var mode: PlanningMode? = nil
    var workflow: PlanningMode { mode ?? .comparison }
    var id: Int { number }
    var completedCount: Int { steps.filter { $0.state == "completed" }.count }
    var editable: Bool { state == "draft" && steps.isEmpty }

    func prompt(for ordinal: Int) -> String {
        let intro = "Plan the following feature. Do not execute project changes. Keep the response concise, under 900 words.\n\nBrief:\n" + brief
        if workflow == .single { return intro + "\n\nProduce one plan with scope, implementation steps, risks, acceptance tests, and open questions. Do not implement the plan." }
        let outputs = Dictionary(uniqueKeysWithValues: steps.map { ($0.ordinal, $0.output) })
        switch ordinal {
        case 0, 1:
            return intro + "\n\nDraft your plan independently. Include implementation steps, risks, tests, and open questions."
        case 2, 3:
            let slot = ordinal % 2
            return intro + "\n\nYour independent draft:\n" + (outputs[slot] ?? "") + "\n\nOther provider's draft:\n" + (outputs[1-slot] ?? "") + "\n\nReview the other proposal once. Identify the strongest choices, conflicts, missing tests, and decisions requiring the user. Do not erase disagreements."
        default:
            return intro + "\n\nDraft A:\n" + (outputs[0] ?? "") + "\n\nDraft B:\n" + (outputs[1] ?? "") + "\n\nReview A:\n" + (outputs[2] ?? "") + "\n\nReview B:\n" + (outputs[3] ?? "") + "\n\nProduce one combined plan. Preserve unresolved disagreements and alternatives explicitly. Include scope, implementation steps, acceptance tests, and decisions requiring approval. Do not implement the plan."
        }
    }
}

struct LocalPlan: Codable, Equatable, Identifiable {
    var id: String
    var title: String
    var updated: Double
    var versions: [LocalPlanVersion]
    var implementations: [LocalImplementation]?
    var executorChoices: [String: LocalPlanner]?
    var selectedExecutor: String?
    var current: LocalPlanVersion { versions.last! }
    var implementation: LocalImplementation? { implementations?.last { $0.planVersion == current.number } }
    var session: CampSession {
        if let implementation { return implementation.session(id: id, title: title, updated: updated) }
        return CampSession(id: id, sessionID: id, provider: "Planning", sourceID: "planning", sourceLabel: current.workflow.title,
            root: "", title: title, cwd: current.planners.first?.session.cwd ?? "", updated: updated, path: "", accountID: "", archived: false)
    }
    var stage: Stage {
        if let implementation {
            if implementation.active { return .progress }
            if let stored = implementation.boardStage.flatMap(Stage.init(rawValue:)) { return stored }
            return implementation.state == "approved" || implementation.state == "idle" ? .ready : .attention
        }
        switch current.state { case "running": return .progress; case "ready", "failed", "needs_review": return .attention; default: return .ready }
    }
}

// Private, offline plans share the cloud workflow's bounded steps and immutable
// versions. The owning Mac persists before launching an official CLI process.
@MainActor final class LocalPlanning: ObservableObject {
    @Published private(set) var tickets: [LocalPlan] = []
    @Published private(set) var live: [String: String] = [:]
    @Published private(set) var error = ""
    var changed: ((Bool) -> Void)?
    var available: ((LocalPlanner) -> Bool)?
    var prepare: ((LocalPlanner, [SessionReference]) async throws -> [SessionContext])?
    private let directory: URL
    private let makeProcess: () throws -> Process
    private var jobs: [String: LocalConversation] = [:]
    private var preparing: [String: Task<Void, Never>] = [:]
    private var reservedProviders: [String: String] = [:]
    private var unreadable = false

    init(directory: URL, makeProcess: @escaping () throws -> Process = { try Bridge.process("conversation") }) {
        self.directory = directory; self.makeProcess = makeProcess
        let file = directory.appendingPathComponent("local-plans.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        do {
            let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
            guard ((attributes[.size] as? NSNumber)?.intValue ?? Int.max) <= 16 << 20 else { throw CampError.message("Saved plans exceed the local storage limit") }
            tickets = try JSONDecoder().decode([LocalPlan].self, from: Data(contentsOf: file))
            guard Self.valid(tickets) else {
                throw CampError.message("Saved planning history is invalid")
            }
            var interrupted = false
            for t in tickets.indices {
                for i in tickets[t].implementations?.indices ?? 0..<0 {
                    if tickets[t].implementations![i].active {
                        tickets[t].implementations![i].state = "needs_review"
                        for a in tickets[t].implementations![i].attempts.indices where ["starting", "running"].contains(tickets[t].implementations![i].attempts[a].state) {
                            tickets[t].implementations![i].attempts[a].state = "uncertain"
                        }
                        interrupted = true
                    }
                }
                for v in tickets[t].versions.indices {
                    var uncertain = false
                    for s in tickets[t].versions[v].steps.indices where ["starting", "running"].contains(tickets[t].versions[v].steps[s].state) {
                        tickets[t].versions[v].steps[s].state = "unknown"; uncertain = true
                        tickets[t].versions[v].steps[s].error = "The app closed before confirming this turn. Review before restarting."
                    }
                    if uncertain { tickets[t].versions[v].state = "needs_review"; interrupted = true }
                    else if tickets[t].versions[v].state == "running" { tickets[t].versions[v].state = "paused"; interrupted = true }
                }
            }
            if interrupted { try persist(tickets) }
        } catch {
            tickets = []; unreadable = true
            self.error = "Saved plans could not be opened: " + error.localizedDescription
        }
    }
    deinit { for task in preparing.values { task.cancel() } }

    func ticket(_ id: String) -> LocalPlan? { tickets.first { $0.id == id } }
    func liveText(_ id: String, version: Int, ordinal: Int) -> String { live[key(id, version, ordinal)] ?? "" }
    var activeCount: Int { reservedProviders.count }
    func uses(_ provider: String) -> Bool { reservedProviders.values.contains(provider) }

    private func persist(_ values: [LocalPlan]) throws {
        guard !unreadable else { throw CampError.message(error) }
        guard Self.valid(values) else { throw CampError.message("Keep the brief under 8 KB and use supported planning settings") }
        let data = try JSONEncoder().encode(values)
        guard data.count <= 16 << 20 else { throw CampError.message("Saved planning history is full") }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let file = directory.appendingPathComponent("local-plans.json")
        try data.write(to: file, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
    }
    private static func valid(_ values: [LocalPlan]) -> Bool {
        let states = Set(["draft", "running", "paused", "ready", "cancelled", "failed", "needs_review"])
        let stepStates = Set(["pending", "starting", "running", "completed", "cancelled", "failed", "unknown"])
        return values.count <= 200 && Set(values.map(\.id)).count == values.count && values.allSatisfy { plan in
            plan.id.hasPrefix("plan:") && UUID(uuidString: String(plan.id.dropFirst(5))) != nil &&
            (plan.implementations ?? []).count <= 100 && (plan.implementations ?? []).allSatisfy(\.valid) &&
            Set((plan.implementations ?? []).map(\.id)).count == (plan.implementations ?? []).count &&
            (plan.implementations ?? []).allSatisfy({ execution in plan.versions.contains { $0.number == execution.planVersion } }) &&
            !plan.versions.isEmpty && plan.versions.count <= 100 &&
            plan.versions.enumerated().allSatisfy({ $0.element.number == $0.offset + 1 }) && plan.versions.allSatisfy { version in
                version.number > 0 && version.brief.utf8.count <= 8192 && states.contains(version.state) &&
                ["calls", "strict_tokens"].contains(version.budgetPolicy) && (0..<version.workflow.plannerCount).contains(version.mergeSlot) &&
                version.sources.count <= 20 && Set(version.sources.map(\.id)).count == version.sources.count &&
                version.planners.count == version.workflow.plannerCount &&
                Set(version.planners.map { $0.session.provider }).count == version.workflow.plannerCount &&
                version.planners.allSatisfy { ["Codex", "Claude"].contains($0.session.provider) } &&
                version.planners.allSatisfy { !$0.session.isCloud && !$0.session.root.isEmpty && $0.model.utf8.count <= 200 } &&
                (version.steps.isEmpty || version.steps.count == version.workflow.turnLimit) && version.steps.enumerated().allSatisfy { index, step in
                    step.ordinal == index && step.slot == (index == 4 ? version.mergeSlot : index % 2) &&
                    step.phase == (index < 2 ? "draft" : index < 4 ? "review" : "combined") &&
                    stepStates.contains(step.state) && step.output.utf8.count <= 8192
                }
            }
        }
    }
    @discardableResult private func commit(_ values: [LocalPlan]) -> Bool {
        do { try persist(values); tickets = values; error = ""; changed?(true); return true }
        catch { self.error = error.localizedDescription; return false }
    }
    @discardableResult func modifyTicket(_ id: String, _ operation: (inout LocalPlan) -> Void) -> Bool {
        var values = tickets
        guard let index = values.firstIndex(where: { $0.id == id }) else { return false }
        operation(&values[index]); values[index].updated = Date().timeIntervalSince1970
        return commit(values)
    }
    @discardableResult private func update(_ id: String, version: Int? = nil, _ operation: (inout LocalPlanVersion) -> Void) -> Bool {
        var values = tickets
        guard let t = values.firstIndex(where: { $0.id == id }),
              let v = values[t].versions.firstIndex(where: { $0.number == (version ?? values[t].current.number) }) else { return false }
        operation(&values[t].versions[v]); values[t].updated = Date().timeIntervalSince1970
        let title = values[t].current.brief.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        values[t].title = title.isEmpty ? values[t].current.workflow.title : String(title.prefix(160))
        return commit(values)
    }
    func create(planners: [LocalPlanner], mode: PlanningMode = .comparison) -> String? {
        guard planners.count == mode.plannerCount, Set(planners.map { $0.session.provider }).count == mode.plannerCount,
              planners.allSatisfy({ ["Codex", "Claude"].contains($0.session.provider) }),
              planners.allSatisfy({ !$0.session.isCloud && !$0.session.root.isEmpty }), tickets.count < 200 else {
            error = mode == .single ? "Choose a local Codex or Claude profile" : "Choose one local Codex profile and one local Claude profile"; return nil
        }
        let value = LocalPlan(id: "plan:" + UUID().uuidString, title: mode.title, updated: Date().timeIntervalSince1970,
            versions: [LocalPlanVersion(number: 1, brief: "", planners: planners, mode: mode)])
        return commit([value] + tickets) ? value.id : nil
    }
    @discardableResult func edit(_ id: String, _ operation: (inout LocalPlanVersion) -> Void) -> Bool {
        guard ticket(id)?.current.editable == true else { return false }
        return update(id, operation)
    }
    func start(_ id: String, sources: [SessionReference]? = nil) {
        guard let plan = ticket(id), ["draft", "paused"].contains(plan.current.state) else { return }
        guard plan.current.budgetPolicy == "calls" else { error = "These CLI logins cannot enforce a hard token ceiling. Choose the turn limit to continue."; return }
        guard !plan.current.brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, plan.current.brief.utf8.count <= 8192 else {
            error = "Write a planning brief under 8 KB"; return
        }
        guard (sources ?? plan.current.sources).count <= 20 else { error = "Attach up to 20 sessions"; return }
        guard update(id, { value in
            if value.steps.isEmpty {
                value.sources = sources ?? value.sources
                value.steps = (0..<value.workflow.turnLimit).map { LocalPlanStep(ordinal: $0, slot: $0 == 4 ? value.mergeSlot : $0 % 2, phase: $0 < 2 ? "draft" : $0 < 4 ? "review" : "combined") }
            }
            value.state = "running"
        }) else { return }
        drain()
    }
    func pause(_ id: String) { guard ticket(id)?.current.state == "running" else { return }; update(id) { $0.state = "paused" } }
    func cancel(_ id: String) {
        guard let plan = ticket(id), !["ready", "cancelled"].contains(plan.current.state) else { return }
        guard update(id, { v in v.state = "cancelled"; for s in v.steps.indices where v.steps[s].state == "pending" { v.steps[s].state = "cancelled" } }) else { return }
        stopJobs(id, version: plan.current.number)
    }
    func restart(_ id: String, reviewedUnknown: Bool = false) {
        guard var plan = ticket(id), plan.versions.count < 100 else { error = "Planning version history is full"; return }
        guard plan.implementation?.active != true, plan.implementation?.state != "needs_review" else { error = "Stop and review the implementation before restarting its plan"; return }
        guard plan.current.state != "needs_review" || reviewedUnknown else {
            error = "Review the unconfirmed provider turn before starting a new revision"; return
        }
        let previous = plan.current
        var next = previous; next.number += 1; next.state = "draft"; next.steps = []
        let current = plan.versions.count - 1
        if !["ready", "failed", "needs_review", "cancelled"].contains(plan.versions[current].state) { plan.versions[current].state = "cancelled" }
        for i in plan.versions[current].steps.indices where plan.versions[current].steps[i].state == "pending" { plan.versions[current].steps[i].state = "cancelled" }
        plan.versions.append(next); plan.updated = Date().timeIntervalSince1970
        var values = tickets; values[values.firstIndex { $0.id == id }!] = plan
        if commit(values) { stopJobs(id, version: previous.number) }
    }
    private func stopJobs(_ id: String, version: Int) {
        let prefix = "\(id):\(version):"
        for (key, task) in preparing where key.hasPrefix(prefix) { task.cancel() }
        for (key, job) in jobs where key.hasPrefix(prefix) { job.stop() }
    }
    func sourcesChanged(_ valid: ([SessionReference]) -> Bool) {
        for plan in tickets where plan.current.state == "running" || plan.current.state == "paused" {
            if !valid(plan.current.sources) {
                if update(plan.id, { $0.state = "failed" }) { stopJobs(plan.id, version: plan.current.number) }
            }
        }
    }
    func drain() {
        guard !unreadable else { return }
        for plan in tickets where plan.current.state == "running" {
            let v = plan.current
            let eligible = v.workflow == .single ? [0] : v.completedCount < 2 ? [0, 1] : v.steps.prefix(4).allSatisfy({ $0.state == "completed" }) ? [4] : [2, 3]
            for ordinal in eligible {
                guard let step = v.steps.first(where: { $0.ordinal == ordinal }), step.state == "pending" else { continue }
                let planner = v.planners[step.slot], provider = planner.session.provider
                guard !uses(provider), available?(planner) ?? true else { continue }
                launch(plan.id, version: v, ordinal: ordinal)
            }
        }
    }
    private func key(_ id: String, _ version: Int, _ ordinal: Int) -> String { "\(id):\(version):\(ordinal)" }
    private func launch(_ id: String, version: LocalPlanVersion, ordinal: Int) {
        let step = version.steps[ordinal], planner = version.planners[step.slot], token = key(id, version.number, ordinal)
        guard update(id, version: version.number, { $0.steps[ordinal].state = "starting" }) else { return }
        reservedProviders[token] = planner.session.provider
        preparing[token] = Task { [weak self] in
            guard let self else { return }
            do {
                let contexts = try await self.prepare?(planner, version.sources) ?? []
                guard !Task.isCancelled, self.ticket(id)?.current.number == version.number, self.ticket(id)?.current.state == "running" else {
                    self.release(token)
                    self.update(id, version: version.number) { v in if v.steps[ordinal].state == "starting" { v.steps[ordinal].state = v.state == "paused" ? "pending" : "cancelled" } }
                    self.drain(); return
                }
                let prompt = version.prompt(for: ordinal)
                let job = LocalConversation(session: planner.session, history: [], prompt: prompt, references: version.sources)
                self.jobs[token] = job
                job.changed = { [weak self, weak job] in
                    guard let self, let job else { return }
                    self.live[token] = job.messages.filter { $0.role == "assistant" }.map(\.text).joined(separator: "\n\n") + job.text
                    self.changed?(false)
                }
                job.didAccept = { [weak self, weak job] in
                    guard let self else { return }
                    if !self.update(id, version: version.number, { $0.steps[ordinal].state = "running" }) { job?.stop() }
                }
                job.completed = { [weak self] result in self?.finish(id, version: version.number, ordinal: ordinal, result: result) }
                try job.start(prompt: prompt, model: planner.model, tuning: planner.tuning, contexts: contexts, mode: .planning, makeProcess: self.makeProcess)
                self.preparing.removeValue(forKey: token)
            } catch {
                self.finish(id, version: version.number, ordinal: ordinal,
                    result: ConversationResult(state: "failed", message: error.localizedDescription, accepted: false))
            }
        }
    }
    private func release(_ token: String) {
        jobs.removeValue(forKey: token); preparing.removeValue(forKey: token); reservedProviders.removeValue(forKey: token); live.removeValue(forKey: token)
    }
    private func finish(_ id: String, version: Int, ordinal: Int, result: ConversationResult) {
        let token = key(id, version, ordinal)
        release(token)
        let complete = result.state == "completed" && !result.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && result.output.utf8.count <= 8192
        let success = update(id, version: version) { v in
            v.steps[ordinal].state = complete ? "completed" : result.state == "uncertain" ? "unknown" : result.state == "cancelled" ? "cancelled" : "failed"
            v.steps[ordinal].output = result.output.utf8.count <= 8192 ? result.output : ""
            v.steps[ordinal].usage = result.usage
            v.steps[ordinal].error = complete ? "" : result.message.isEmpty ? "No complete planning response was returned" : result.message
            if !["cancelled", "failed", "needs_review"].contains(v.state) {
                if !complete { v.state = result.state == "uncertain" ? "needs_review" : "failed" }
                else if v.completedCount == v.workflow.turnLimit { v.state = "ready" }
            }
        }
        if !success || !complete { stopJobs(id, version: version) }
        drain()
    }
}
