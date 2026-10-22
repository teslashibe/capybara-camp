import SwiftUI

struct PlanningLocationLabel: View {
    let execution: CloudPlanningExecution?
    var body: some View {
        Label(execution?.label ?? "Location not recorded", systemImage: execution?.location == "cloud" ? "cloud" : execution == nil ? "questionmark.circle" : "desktopcomputer")
            .font(.system(size: 11)).foregroundStyle(campMuted)
            .help(execution == nil ? "Review the current location before starting a new revision" : "Execution location saved for this planning revision")
    }
}

extension CampStore {
    func newSharedPlan(mode: PlanningMode = .single) {
        guard let scope = cloud.scope, let project = cloud.projects.first(where:{ !$0.isArchived && "cloud:"+$0.id==projectFilter }) ?? cloud.projects.first(where:{ !$0.isArchived }) else { banner = "Connect to a Camp and add a project first"; return }
        let available = cloud.sessions.filter { $0.ownerUserId == scope.userId || $0.allowRequests }
        let preferred = available.first { $0.ownerUserId == scope.userId }?.provider ?? available.first?.provider ?? "Codex"
        let planners = (mode == .single ? [preferred] : ["Codex", "Claude"]).map { provider -> CloudPlannerInput in
            let choices = cloud.sessions.filter { $0.provider == provider && ($0.ownerUserId == scope.userId || $0.allowRequests) }
            let source = choices.first { $0.ownerUserId == scope.userId } ?? choices.first
            return CloudPlannerInput(sessionId: source?.id ?? "", model: source?.model ?? "", effort: "", speed: "standard", execution: source.flatMap(CloudPlanningExecution.init(session:)))
        }
        Task {
            do {
                let id = try await sharedPlanning.newDraft(input: CloudPlanningInput(title: "New plan", projectId: project.id, brief: "", shared: false, planners: planners, mode: mode))
                if let session = sessions.first(where: { $0.id == "camp-plan-draft:" + id }) { open(session) }
            } catch { banner = error.localizedDescription }
        }
    }
    func setSharedPlanMode(_ id: String, mode: PlanningMode) {
        guard let draft = sharedPlanning.draft(id) else { return }
        let available = cloud.sessions.filter { $0.ownerUserId == cloud.scope?.userId || $0.allowRequests }
        let old = draft.input.planners
        let providers = mode == .single ? [available.first { $0.id == old.first?.sessionId }?.provider ?? available.first?.provider ?? "Codex"] : ["Codex", "Claude"]
        let planners = providers.map { provider -> CloudPlannerInput in
            if let saved = old.first(where: { item in available.contains { $0.id == item.sessionId && $0.provider == provider } }) { return saved }
            let source = available.first { $0.provider == provider }
            return CloudPlannerInput(sessionId: source?.id ?? "", model: source?.model ?? "", effort: "", speed: "standard", execution: source.flatMap(CloudPlanningExecution.init(session:)))
        }
        sharedPlanning.edit(id) { $0.mode = mode; $0.planners = planners; $0.mergeSlot = 0 }
    }
    func syncSharedPlans() {
        let names = Dictionary(uniqueKeysWithValues: cloud.projects.map { ($0.id, $0.name) })
        let replacing = Set(sharedPlanning.drafts.compactMap(\.restartPlanID))
        var values = sharedPlanning.drafts.map { value in
            CampSession(id: "camp-plan-draft:" + value.id, sessionID: value.id, provider: "Planning", sourceID: "camp-planning", sourceLabel: "Shared Camp",
                root: "", title: value.input.title, cwd: names[value.input.projectId] ?? "Shared project", updated: value.updatedAt.timeIntervalSince1970, path: "", accountID: cloud.scope?.userId ?? "", archived: false)
        }
        values += sharedPlanning.tickets.filter { !replacing.contains($0.id) }.map { value in
            CampSession(id: "camp-plan:" + value.id, sessionID: value.id, provider: "Planning", sourceID: "camp-planning", sourceLabel: "Shared Camp",
                root: "", title: value.title, cwd: names[value.projectId] ?? "Shared project", updated: value.updatedAt.timeIntervalSince1970, path: "", accountID: value.creatorUserId, archived: false)
        }
        let prior = Set(sessions.filter(\.isSharedPlan).map(\.id)), current = Set(values.map(\.id))
        for id in prior.subtracting(current) { stages[id] = nil }
        for value in values {
            if value.id.hasPrefix("camp-plan-draft:") { stages[value.id] = sharedPlanning.pending(value.sessionID) ? .progress : .ready }
            else { stages[value.id] = sharedPlanning.tickets.first { $0.id == value.sessionID }?.stage ?? .ready }
        }
        sessions = (sessions.filter { !$0.isSharedPlan } + values).sorted { $0.updated > $1.updated }
        if let selectedID, prior.contains(selectedID), !current.contains(selectedID) { self.selectedID = nil; view = "Board" }
        if notifications.pendingOpen != nil && sharedPlanning.ready { Task { await self.openPendingNotification() } }
    }
    func openSharedPlan(_ id: String, version: Int = 0) {
        selectedID = "camp-plan:" + id; sharedPlanResult = nil; sharedPlanning.open(id, version: version); view = "Chat"; observeHistory()
    }
    func openSharedDraft(_ id: String) { if let session = sessions.first(where: { $0.id == "camp-plan-draft:" + id }) { open(session) } }
    func restartSharedPlan(_ ticket: CloudPlanningResult) {
        Task {
            do {
                let references = ticket.version.sources.map { SessionReference(id: "camp:" + $0.sessionId, scope: cloud.scope?.key, cloud: $0) }
                try mentions.set(references, for: "camp-plan-draft:" + ticket.id)
                try await sharedPlanning.restartDraft(ticket)
                openSharedDraft(ticket.id)
            } catch { banner = error.localizedDescription }
        }
    }
    func startSharedPlan(_ id: String) {
        let key = "camp-plan-draft:" + id
        let refs = mentions.references(for: key)
        guard refs.allSatisfy({ $0.scope == cloud.scope?.key && $0.cloud != nil }) else { banner = "Share tagged local sessions with Camp before using them in a shared plan"; return }
        Task { await sharedPlanning.start(id, sources: refs.compactMap(\.cloud)) }
    }
}

struct SharedPlanningModelMenu: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: CampPlanning
    let id: String
    let slot: Int
    var expanded = false
    var cardRow = false
    var body: some View {
        if let draft = planning.draft(id), draft.input.planners.indices.contains(slot) {
            let p = draft.input.planners[slot]
            let session = store.sessions.first { $0.id == "camp:" + p.sessionId }
            let provider = session?.provider ?? (slot == 0 ? "Codex" : "Claude")
            let models = session.map(store.availableModels) ?? []
            ModelSettingsMenu(provider: provider, title: "\(provider) planner", sessionModel: p.model,
                modelLabel: p.model.isEmpty ? "Session default" : p.model, models: models,
                caps: models.first { $0.id == p.model } ?? ModelOption(id: p.model, label: p.model),
                choice: ModelTuning(effort: p.effort, speed: p.speed), locked: planning.pending(id), expanded: expanded, planningRow: cardRow, context: "Model for this planning revision",
                selectModel: { planning.selectModel(id, slot: slot, model: $0) },
                tune: { effort, speed in planning.edit(id) { input in if let effort { input.planners[slot].effort = effort }; if let speed { input.planners[slot].speed = speed } } })
        }
    }
}

struct SharedPlanningCard: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: CampPlanning
    let session: CampSession
    var body: some View {
        let id = session.sessionID, draft = planning.draft(id), ticket = planning.tickets.first { $0.id == id }
        let mode = draft?.input.workflow ?? ticket?.workflow ?? .comparison
        CampTaskCard(store: store, session: session) {
            HStack {
                CampCardProvider(provider: "Planning")
                Spacer()
                Image(systemName: "person.2").font(.system(size: 10)).foregroundStyle(campMuted).help("Shared Camp")
                Menu { Button("Open plan") { store.open(session) };ProjectAssignmentMenu(store:store,session:session) } label: { Image(systemName: "ellipsis").foregroundStyle(campMuted).frame(width: 28, height: 28) }
                    .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().accessibilityLabel("Plan actions for \(session.title)")
            }
        } models: {
            VStack(spacing: 0) {
                if let draft {
                    ForEach(draft.input.planners.indices, id: \.self) { slot in
                        let provider = store.cloud.sessions.first { $0.id == draft.input.planners[slot].sessionId }?.provider ?? (slot == 0 ? "Codex" : "Claude")
                        PlanningCardModelRow(provider: provider, open: { store.openPlan(session, slot: slot) }) {
                            SharedPlanningModelMenu(store: store, planning: planning, id: id, slot: slot, cardRow: true)
                        }
                    }
                } else if let ticket {
                    ForEach(ticket.planners, id: \.slot) { p in
                        PlanningCardModelRow(provider: p.provider, open: { store.openPlan(session, slot: p.slot) }) {
                            ModelSettingsMenu(provider: p.provider, title: "\(p.provider) planner", sessionModel: p.model, modelLabel: p.model,
                                models: [], caps: ModelOption(id: p.model, label: p.model), choice: ModelTuning(effort: p.effort, speed: p.speed), locked: true, planningRow: true, selectModel: { _ in }, tune: { _, _ in })
                        }
                    }
                }
            }
        } status: {
            HStack(spacing: 6) {
                if ticket?.state == "running" || planning.pending(id) { ProgressView().controlSize(.mini).frame(width: 12, height: 12) }
                else { Image(systemName: ticket?.state == "ready" ? "checkmark" : "circle").font(.system(size: 8)) }
                Text(draft?.cancelRequested == true ? "Cancellation waiting to sync" : planning.pending(id) ? "Waiting to sync" : ticket?.state == "ready" ? "Plan ready to review" : "\(ticket?.completed ?? 0) of \(mode.turnLimit) · \(ticket?.state ?? "draft")")
                    .font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
            }
        } actions: {
            HStack {
                Button { store.openPlan(session, section: "History") } label: { Label("History", systemImage: "clock.arrow.circlepath").frame(minHeight: 28).contentShape(Rectangle()) }.buttonStyle(.plain).disabled(draft != nil)
                Spacer(minLength: 4)
                CampCardOpenAction(title: "Open plan") { store.openPlan(session) }
            }
        }
    }
}

struct SharedPlanningWorkspace: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: CampPlanning
    let id: String
    @State private var section = "Plans"
    @State private var focusedSlot: Int?
    var body: some View {
        if let draft = planning.draft(id) {
            HStack(spacing: 0) {
                PlanningNavigation(store: store, title: draft.input.title, mode: draft.input.workflow,
                    participants: draft.input.planners.indices.map { slot in
                        PlanningParticipant(id: slot, provider: store.cloud.sessions.first { $0.id == draft.input.planners[slot].sessionId }?.provider ?? (slot == 0 ? "Codex" : "Claude"), status: planning.pending(id) ? "Waiting to sync" : "Ready")
                    }, section: $section, focusedSlot: $focusedSlot, sections: ["Plans"], canImplement: false)
                Divider()
                VStack(alignment: .leading, spacing: 14) {
                    HStack {
                        ProviderBadge(provider: "Planning")
                        Text(draft.input.workflow.title).font(.system(size: 12, weight: .medium))
                        Text("Shared Camp").foregroundStyle(campMuted)
                        Spacer()
                        Button { store.view = "Board" } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Back to board").accessibilityLabel("Back to board")
                    }
                    TextField("Plan title", text: Binding(get: { planning.draft(id)?.input.title ?? "" }, set: { value in planning.edit(id) { $0.title = String(value.prefix(120)) } })).accessibilityLabel("Plan title").disabled(planning.pending(id) || draft.restartPlanID != nil)
                    TextEditor(text: Binding(get: { planning.draft(id)?.input.brief ?? "" }, set: { value in planning.edit(id) { $0.brief = value } }))
                        .font(.system(size: 13)).scrollContentBackground(.hidden).frame(height: 90).padding(8).background(.white).clipShape(RoundedRectangle(cornerRadius: 8)).accessibilityLabel("Planning brief").disabled(planning.pending(id))
                    if let destination = store.selected { SessionContextComposer(store: store, mentions: store.mentions, destination: destination).disabled(planning.pending(id)) }
                    HStack {
                        Picker("Project", selection: Binding(get: { draft.input.projectId }, set: { value in planning.edit(id) { $0.projectId = value } })) { ForEach(store.cloud.projects.filter { !$0.isArchived }) { Text($0.name).tag($0.id) } }.frame(maxWidth: 300)
                        Toggle("Share plan with Camp", isOn: Binding(get: { draft.input.shared }, set: { value in planning.edit(id) { $0.shared = value } })).toggleStyle(.checkbox)
                    }.disabled(planning.pending(id) || draft.restartPlanID != nil)
                    Picker("Planning mode", selection: Binding(get: { draft.input.workflow }, set: { store.setSharedPlanMode(id, mode: $0) })) {
                        ForEach(PlanningMode.allCases, id: \.self) { Text($0.title).tag($0) }
                    }.frame(maxWidth: 300).disabled(planning.pending(id))
                    Text("Choose sessions below. Each planner uses its session’s assigned location and login.").font(.system(size: 11)).foregroundStyle(campMuted)
                    PlanningPaneRow(slots: draft.input.planners.indices.filter { focusedSlot == nil || focusedSlot == $0 }) { slot in
                            VStack(alignment: .leading, spacing: 10) {
                                let provider = store.cloud.sessions.first { $0.id == draft.input.planners[slot].sessionId }?.provider ?? (slot == 0 ? "Codex" : "Claude")
                                Label { Text(provider) } icon: { ProviderMark(provider: provider, size: 17) }.font(.headline)
                                Picker("Session", selection: Binding(get: { draft.input.planners[slot].sessionId }, set: { value in
                                    if let source = store.cloud.sessions.first(where: { $0.id == value }) {
                                        planning.edit(id) { $0.planners[slot] = CloudPlannerInput(sessionId: value, model: source.model, effort: "", speed: "standard", execution: CloudPlanningExecution(session: source)) }
                                    }
                                })) {
                                    Text("Choose a shared session").tag("")
                                    ForEach(store.cloud.sessions.filter { (draft.input.workflow == .single || $0.provider == provider) && ($0.ownerUserId == store.cloud.scope?.userId || $0.allowRequests) }) { source in Text(source.provider + " · " + source.title).tag(source.id) }
                                }.disabled(planning.pending(id))
                                PlanningLocationLabel(execution: draft.input.planners[slot].execution)
                                SharedPlanningModelMenu(store: store, planning: planning, id: id, slot: slot, expanded: true)
                                Spacer()
                            }.padding(14).frame(maxWidth: .infinity, maxHeight: .infinity).background(.white.opacity(0.55)).clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                    if draft.input.workflow == .comparison { Picker("Combine with", selection: Binding(get: { draft.input.mergeSlot }, set: { value in planning.edit(id) { $0.mergeSlot = value } })) {
                        Text("Codex").tag(0); Text("Claude").tag(1)
                    }.frame(maxWidth: 250).disabled(planning.pending(id)) }
                    Toggle(draft.input.workflow.budgetLabel, isOn: Binding(get: { draft.input.budgetPolicy == "calls" }, set: { value in planning.edit(id) { $0.budgetPolicy = value ? "calls" : "strict_tokens" } })).toggleStyle(.checkbox).disabled(planning.pending(id))
                    if planning.executionNeedsReview(draft) {
                        HStack {
                            Label("A planner’s location changed or is unavailable", systemImage: "exclamationmark.circle").font(.system(size: 11))
                            Spacer()
                            Button("Review current locations") { planning.reviewExecution(id) }
                        }.foregroundStyle(campMuted).disabled(planning.pending(id))
                    }
                    if !planning.error.isEmpty { Text(planning.error).font(.caption).foregroundStyle(.orange) }
                    HStack {
                        Text(planning.pending(id) ? "Waiting to sync" : "CLI token totals are reported when available; the turn limit is not a hard token or cost ceiling").font(.system(size: 10)).foregroundStyle(campMuted)
                        Spacer()
                        Button("Cancel") { Task { await planning.cancelDraft(id) } }
                        Button("Start") { store.startSharedPlan(id) }.buttonStyle(.borderedProminent).tint(campGreen)
                            .disabled(planning.pending(id) || planning.executionNeedsReview(draft) || draft.input.budgetPolicy != "calls" || draft.input.brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }.padding(22).disabled(planning.pending(id) && draft.cancelRequested)
            }.disabled(!planning.ready).onChange(of: draft.input.workflow) { _, _ in focusedSlot = nil }
                .onAppear { applyCardNavigation() }.onChange(of: store.planNavigation) { _, _ in applyCardNavigation() }
        } else if let result = planning.detail(id, version: planning.selectedVersion) {
            SharedPlanningResultView(store: store, result: result)
        } else {
            VStack(spacing: 12) { ProgressView(); Text(planning.error.isEmpty ? "Loading plan" : planning.error); Button("Back to board") { store.view = "Board" } }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
    private func applyCardNavigation() {
        guard let request = store.planNavigation, request.id == "camp-plan-draft:" + id else { return }
        focusedSlot = request.slot.flatMap { planning.draft(id)?.input.planners.indices.contains($0) == true ? $0 : nil }
    }
}
