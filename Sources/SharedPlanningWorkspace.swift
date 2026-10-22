import SwiftUI

extension CampStore {
    func newSharedPlan() {
        guard let scope = cloud.scope, let project = cloud.projects.first else { banner = "Connect to a Camp and add a project first"; return }
        let planners = ["Codex", "Claude"].map { provider -> CloudPlannerInput in
            let choices = cloud.sessions.filter { $0.provider == provider && ($0.ownerUserId == scope.userId || $0.allowRequests) }
            let source = choices.first { $0.ownerUserId == scope.userId } ?? choices.first
            return CloudPlannerInput(sessionId: source?.id ?? "", model: source?.model ?? "", effort: "", speed: "standard")
        }
        Task {
            do {
                let id = try await sharedPlanning.newDraft(input: CloudPlanningInput(title: "New plan", projectId: project.id, brief: "", shared: false, planners: planners))
                if let session = sessions.first(where: { $0.id == "camp-plan-draft:" + id }) { open(session) }
            } catch { banner = error.localizedDescription }
        }
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
    var body: some View {
        if let draft = planning.draft(id), draft.input.planners.indices.contains(slot) {
            let p = draft.input.planners[slot]
            let provider = slot == 0 ? "Codex" : "Claude"
            let session = store.sessions.first { $0.id == "camp:" + p.sessionId }
            let models = session.map(store.availableModels) ?? []
            ModelSettingsMenu(provider: provider, title: "\(provider) planner", sessionModel: p.model,
                modelLabel: p.model.isEmpty ? "Session default" : p.model, models: models,
                caps: models.first { $0.id == p.model } ?? ModelOption(id: p.model, label: p.model),
                choice: ModelTuning(effort: p.effort, speed: p.speed), locked: planning.pending(id), context: "Model for this planning revision",
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
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                ProviderMark(provider: "Planning", size: 17)
                Text("Planning").font(.system(size: 10, weight: .medium)).foregroundStyle(campMuted)
                Spacer()
                Image(systemName: "person.2").font(.system(size: 10)).foregroundStyle(campMuted).help("Shared Camp")
                Menu { Button("Open plan") { store.open(session) } } label: { Image(systemName: "ellipsis").frame(width: 24, height: 20) }
                    .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().accessibilityLabel("Plan actions for \(session.title)")
            }
            Button { store.open(session) } label: {
                VStack(alignment: .leading, spacing: 10) {
                    Text(session.title).font(.system(size: 13, weight: .medium)).lineLimit(2).multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, minHeight: 34, maxHeight: 34, alignment: .topLeading)
                    Label(session.project, systemImage: "folder").font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                    HStack { Text("Plan together"); Spacer(); Text(Date(timeIntervalSince1970: session.updated), style: .relative) }.font(.system(size: 9)).foregroundStyle(campMuted)
                }.frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
            }.buttonStyle(.plain)
            Divider().overlay(campInk.opacity(0.04))
            if draft != nil {
                ForEach(0..<2) { slot in SharedPlanningModelMenu(store: store, planning: planning, id: id, slot: slot) }
            } else if let ticket {
                ForEach(ticket.planners, id: \.slot) { p in
                    ModelSettingsMenu(provider: p.provider, title: "\(p.provider) planner", sessionModel: p.model, modelLabel: p.model,
                        models: [], caps: ModelOption(id: p.model, label: p.model), choice: ModelTuning(effort: p.effort, speed: p.speed), locked: true, selectModel: { _ in }, tune: { _, _ in })
                }
            }
            Spacer(minLength: 0)
            HStack(spacing: 6) {
                if ticket?.state == "running" || planning.pending(id) { ProgressView().controlSize(.mini).frame(width: 12, height: 12) }
                Text(draft?.cancelRequested == true ? "Cancellation waiting to sync" : planning.pending(id) ? "Waiting to sync" : ticket?.state == "ready" ? "Plan ready to review" : "\(ticket?.completed ?? 0) of 5 · \(ticket?.state ?? "draft")")
                    .font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                Spacer()
                Button { store.open(session) } label: { Image(systemName: "chevron.right").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Open plan")
            }.frame(height: 17)
        }.modifier(CampCardStyle())
    }
}

struct SharedPlanningWorkspace: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: CampPlanning
    let id: String
    var body: some View {
        if let draft = planning.draft(id) {
            HStack(spacing: 0) {
                RecentSessionsList(store: store).frame(width: 230)
                Divider()
                VStack(alignment: .leading, spacing: 14) {
                    HStack {
                        Label("Plan together", systemImage: "lightbulb").font(.headline)
                        Text("Shared Camp").foregroundStyle(campMuted)
                        Spacer()
                        Button { store.view = "Board" } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Back to board").accessibilityLabel("Back to board")
                    }
                    TextField("Plan title", text: Binding(get: { planning.draft(id)?.input.title ?? "" }, set: { value in planning.edit(id) { $0.title = String(value.prefix(120)) } })).accessibilityLabel("Plan title").disabled(planning.pending(id) || draft.restartPlanID != nil)
                    TextEditor(text: Binding(get: { planning.draft(id)?.input.brief ?? "" }, set: { value in planning.edit(id) { $0.brief = value } }))
                        .font(.system(size: 13)).scrollContentBackground(.hidden).frame(height: 90).padding(8).background(.white).clipShape(RoundedRectangle(cornerRadius: 8)).accessibilityLabel("Planning brief").disabled(planning.pending(id))
                    if let destination = store.selected { SessionContextComposer(store: store, mentions: store.mentions, destination: destination).disabled(planning.pending(id)) }
                    HStack {
                        Picker("Project", selection: Binding(get: { draft.input.projectId }, set: { value in planning.edit(id) { $0.projectId = value } })) { ForEach(store.cloud.projects) { Text($0.name).tag($0.id) } }.frame(maxWidth: 300)
                        Toggle("Share plan with Camp", isOn: Binding(get: { draft.input.shared }, set: { value in planning.edit(id) { $0.shared = value } })).toggleStyle(.checkbox)
                    }.disabled(planning.pending(id) || draft.restartPlanID != nil)
                    Text("Choose shared sessions below. Each provider runs on its session owner's Mac, using that owner's login.").font(.system(size: 11)).foregroundStyle(campMuted)
                    HStack(alignment: .top, spacing: 12) {
                        ForEach(0..<2) { slot in
                            VStack(alignment: .leading, spacing: 10) {
                                let provider = slot == 0 ? "Codex" : "Claude"
                                Label { Text(provider) } icon: { ProviderMark(provider: provider, size: 17) }.font(.headline)
                                Picker("Session", selection: Binding(get: { draft.input.planners[slot].sessionId }, set: { value in
                                    if let source = store.cloud.sessions.first(where: { $0.id == value }) {
                                        planning.edit(id) { $0.planners[slot] = CloudPlannerInput(sessionId: value, model: source.model, effort: "", speed: "standard") }
                                    }
                                })) {
                                    Text("Choose a shared session").tag("")
                                    ForEach(store.cloud.sessions.filter { $0.provider == provider && ($0.ownerUserId == store.cloud.scope?.userId || $0.allowRequests) }) { source in Text(source.title).tag(source.id) }
                                }.disabled(planning.pending(id))
                                SharedPlanningModelMenu(store: store, planning: planning, id: id, slot: slot)
                                Spacer()
                            }.padding(14).frame(maxWidth: .infinity, maxHeight: .infinity).background(.white.opacity(0.55)).clipShape(RoundedRectangle(cornerRadius: 10))
                        }
                    }
                    Picker("Combine with", selection: Binding(get: { draft.input.mergeSlot }, set: { value in planning.edit(id) { $0.mergeSlot = value } })) {
                        Text("Codex").tag(0); Text("Claude").tag(1)
                    }.frame(maxWidth: 250).disabled(planning.pending(id))
                    Toggle("Limit to five turns", isOn: Binding(get: { draft.input.budgetPolicy == "calls" }, set: { value in planning.edit(id) { $0.budgetPolicy = value ? "calls" : "strict_tokens" } })).toggleStyle(.checkbox).disabled(planning.pending(id))
                    if !planning.error.isEmpty { Text(planning.error).font(.caption).foregroundStyle(.orange) }
                    HStack {
                        Text(planning.pending(id) ? "Waiting to sync" : "CLI token totals are reported when available; five turns is not a hard token or cost ceiling").font(.system(size: 10)).foregroundStyle(campMuted)
                        Spacer()
                        Button("Cancel") { Task { await planning.cancelDraft(id) } }
                        Button("Start") { store.startSharedPlan(id) }.buttonStyle(.borderedProminent).tint(campGreen)
                            .disabled(planning.pending(id) || draft.input.budgetPolicy != "calls" || draft.input.brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }.padding(22).disabled(planning.pending(id) && draft.cancelRequested)
            }.disabled(!planning.ready)
        } else if let result = planning.detail(id, version: planning.selectedVersion) {
            SharedPlanningResultView(store: store, result: result)
        } else {
            VStack(spacing: 12) { ProgressView(); Text(planning.error.isEmpty ? "Loading plan" : planning.error); Button("Back to board") { store.view = "Board" } }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}
