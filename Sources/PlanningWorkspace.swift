import SwiftUI

extension CampStore {
    func planningProfiles(_ provider: String) -> [CampSession] {
        var seen = Set<String>()
        return sessions.filter { $0.provider == provider && !$0.isCloud && !$0.isPlan && !$0.root.isEmpty && seen.insert($0.sourceID).inserted }
    }
    func newPlan(mode: PlanningMode = .single, location: String? = nil) {
        guard !creatingPlan else { return }
        executionDefaultsMessage = ""
        guard executionDefaults.readable else { showExecutionDefaults(executionDefaults.error); return }
        var defaults = executionDefaults.value(for: cloud.scope)
        if let location { defaults.location = location }
        if defaults.location == "Cloud" { newCloudPlan(mode: mode, defaults: defaults); return }
        let available = Set(["Codex", "Claude"].filter { !planningProfiles($0).isEmpty })
        let preferred = defaults.preferredProvider(selected: selected?.provider, localAvailable: available)
        var peers: [LocalPlanner] = []
        for provider in mode == .single ? [preferred] : ["Codex", "Claude"] {
            let id = defaults.localProfiles[provider] ?? ""
            let source = id.isEmpty ? planningProfiles(provider).first : planningProfiles(provider).first { $0.sourceID == id }
            guard let source else { showExecutionDefaults("The default \(provider) profile has no available session. Review its profile before planning"); return }
            peers.append(LocalPlanner(session: source, model: effectiveModel(source), tuning: tuning(source)))
        }
        if let id = planning.create(planners: peers, mode: mode), let plan = planning.ticket(id) {
            if projectFilter.hasPrefix("local:") { assignProject(plan.session,to:String(projectFilter.dropFirst(6))) }
            open(plan.session)
        }
        else { banner = planning.error }
    }
    func showExecutionDefaults(_ message: String) {
        banner = message; executionDefaultsMessage = message; sourceTab = "Defaults"; sourceSheet = true
    }
    private func newCloudPlan(mode: PlanningMode, defaults: ExecutionPreferences) {
        guard let scope = cloud.scope, sharedPlanning.ready else { showExecutionDefaults("Connect to your Camp before creating a cloud plan"); return }
        let projectID = projectFilter.hasPrefix("cloud:") ? String(projectFilter.dropFirst(6)) : defaults.projectID
        guard cloud.projects.contains(where: { $0.id == projectID && !$0.isArchived }) else { showExecutionDefaults("Choose a cloud project in execution defaults"); return }
        let provider = defaults.preferredProvider(selected: selected?.provider, localAvailable: [])
        let providers = mode == .single ? [provider] : ["Codex", "Claude"]
        guard providers.allSatisfy({ !(defaults.cloudAccounts[$0] ?? "").isEmpty }) else { showExecutionDefaults("Choose a default cloud account for each planner"); return }
        creatingPlan = true
        Task {
            defer { creatingPlan = false }
            do {
                var sources: [CloudPlanningSource] = []
                for provider in providers {
                    let choices = try await cloud.planningSources(projectID: projectID, accountID: defaults.cloudAccounts[provider]!)
                    guard let source = choices.first(where: { $0.session.provider == provider }) else {
                        throw CampError.message("The default \(provider) account has no available workspace in this project. Check its sign-in and prepare a session with Run on")
                    }
                    sources.append(source)
                }
                guard cloud.scope == scope, executionDefaults.value(for: scope) == defaults else { throw CampError.message("Execution defaults changed. Review them before creating the plan") }
                try await cloud.installPlanningSources(sources, scope: scope)
                let input = CloudPlanningInput(title: "New plan", projectId: projectID, brief: "", shared: false,
                    planners: sources.map { CloudPlannerInput(sessionId: $0.session.id, model: $0.session.model, effort: "", speed: "standard", execution: CloudPlanningExecution(session: $0.session)) }, mode: mode)
                let id = try await sharedPlanning.newDraft(input: input)
                if let session = sessions.first(where: { $0.id == "camp-plan-draft:" + id }) { open(session) }
            } catch is CancellationError {} catch { showExecutionDefaults(error.localizedDescription) }
        }
    }
    func setPlanMode(_ id: String, mode: PlanningMode) {
        guard let version = planning.ticket(id)?.current, version.editable else { return }
        let providers = mode == .single ? [version.planners[0].session.provider] : ["Codex", "Claude"]
        let peers = providers.compactMap { provider -> LocalPlanner? in
            if let old = version.planners.first(where: { $0.session.provider == provider }) { return old }
            guard let source = planningProfiles(provider).first else { return nil }
            return LocalPlanner(session: source, model: effectiveModel(source), tuning: tuning(source))
        }
        guard mode.accepts(peers.map { $0.session.provider }) else { banner = "Add a Codex and Claude profile to compare plans"; return }
        planning.edit(id) { $0.mode = mode; $0.planners = peers; $0.mergeSlot = 0 }
    }
    func setPlanProvider(_ id: String, provider: String) {
        guard let source = planningProfiles(provider).first else { return }
        planning.edit(id) { $0.planners[0] = LocalPlanner(session: source, model: effectiveModel(source), tuning: tuning(source)) }
    }
    func planModel(_ id: String, slot: Int, model: String) {
        planning.edit(id) { v in
            var p = v.planners[slot]
            p.tuningByModel[p.model] = p.tuning
            p.model = model.isEmpty ? p.session.model : model
            p.tuning = p.tuningByModel[p.model] ?? ModelTuning()
            v.planners[slot] = p
        }
    }
    func planTuning(_ id: String, slot: Int, effort: String?, speed: String?) {
        planning.edit(id) { v in
            if let effort { v.planners[slot].tuning.effort = effort }
            if let speed { v.planners[slot].tuning.speed = speed }
        }
    }
}

struct PlanningModelMenu: View {
    @ObservedObject var store: CampStore
    let id: String
    let version: LocalPlanVersion
    let slot: Int
    var expanded = false
    var cardRow = false
    var body: some View {
        let p = version.planners[slot]
        let models = store.availableModels(p.session)
        ModelSettingsMenu(provider: p.session.provider, title: "\(p.session.provider) planner", sessionModel: p.session.model,
            modelLabel: p.model.isEmpty ? "Session default" : p.model, models: models,
            caps: models.first { $0.id == p.model } ?? ModelOption(id: p.model, label: p.model), choice: p.tuning,
            locked: !version.editable, expanded: expanded, planningRow: cardRow, context: "Model for this planning revision",
            selectModel: { store.planModel(id, slot: slot, model: $0) },
            tune: { store.planTuning(id, slot: slot, effort: $0, speed: $1) })
    }
}

struct PlanningCard: View {
    @ObservedObject var store: CampStore
    let planning: LocalPlanning
    let id: String
    var body: some View {
        if let plan = planning.ticket(id) {
            CampTaskCard(store: store, session: plan.session) {
                HStack {
                    CampCardProvider(provider: "Planning")
                    Spacer()
                    Menu {
                        Button("Open plan") { store.open(plan.session) }
                        Button("Save to Camp", systemImage: "icloud.and.arrow.up") { store.planPromotionSheet = plan.session }
                        ProjectAssignmentMenu(store:store,session:plan.session)
                        Button("Start over") { planning.restart(id); store.open(plan.session) }
                        Button("Cancel planning") { planning.cancel(id) }.disabled(plan.current.state == "ready" || plan.current.state == "cancelled")
                    } label: { CampIconLabel(symbol: "ellipsis").foregroundStyle(campMuted) }
                        .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().accessibilityLabel("Plan actions for \(plan.title)")
                }
            } models: {
                VStack(spacing: 0) {
                    ForEach(plan.current.planners.indices, id: \.self) { slot in
                        PlanningCardModelRow(provider: plan.current.planners[slot].session.provider, open: { store.openPlan(plan.session, slot: slot) }) {
                            PlanningModelMenu(store: store, id: id, version: plan.current, slot: slot, cardRow: true)
                        }
                    }
                }
            } status: {
                HStack(spacing: 6) {
                    if plan.current.state == "running" { ProgressView().controlSize(.mini).frame(width: 12, height: 12) }
                    else { Image(systemName: plan.current.state == "ready" ? "checkmark" : "circle").font(.system(size: 8)) }
                    Text(plan.current.state == "ready" ? "Plan ready to review" : "\(plan.current.completedCount) of \(plan.current.workflow.turnLimit) · \(plan.current.state.replacingOccurrences(of: "_", with: " "))")
                        .font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                }
            } actions: {
                HStack {
                    Button { store.openPlan(plan.session, section: "History") } label: { Label("History", systemImage: "clock.arrow.circlepath").frame(minHeight: 28).contentShape(Rectangle()) }.buttonStyle(.plain)
                    Spacer(minLength: 4)
                    CampCardOpenAction(title: "Open plan") { store.openPlan(plan.session) }.accessibilityLabel("Open \(plan.title)")
                }
            }
        }
    }
}

struct PlanningWorkspace: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: LocalPlanning
    let id: String
    @State private var section = "Plans"
    @State private var brief = ""
    @State private var saveTask: Task<Void, Never>?
    @State private var profiles: [String: [CampSession]] = [:]
    @State private var reviewedUnknown = false
    @State private var expandedVersions: Set<Int> = []
    @State private var focusedSlot: Int?
    var plan: LocalPlan? { planning.ticket(id) }
    var body: some View {
        if let plan {
            HStack(spacing: 0) {
                PlanningNavigation(store: store, title: plan.title, mode: plan.current.workflow,
                    participants: participants(plan), section: $section, focusedSlot: $focusedSlot,
                    canImplement: plan.current.state == "ready" || plan.implementation != nil)
                Rectangle().fill(campInk.opacity(0.08)).frame(width: 1)
                VStack(spacing: 0) {
                    HStack {
                        ProviderBadge(provider: "Planning")
                        Text(plan.current.workflow.title).font(.system(size: 12, weight: .medium))
                        Text("\(plan.current.completedCount) of \(plan.current.workflow.turnLimit) \(plan.current.workflow == .single ? "turn" : "turns")").font(.system(size: 11)).foregroundStyle(campMuted)
                        Spacer()
                        Button { store.planPromotionSheet = plan.session } label: { CampIconLabel(symbol: "icloud.and.arrow.up") }
                            .help("Save this ticket to Camp").accessibilityLabel("Save this ticket to Camp")
                        Button {
                            if plan.current.state == "needs_review" && !reviewedUnknown { section = "History" }
                            else { planning.restart(id, reviewedUnknown: reviewedUnknown); section = "Plans" }
                        } label: { CampIconLabel(symbol: "arrow.counterclockwise") }
                            .help("Start over and keep history").accessibilityLabel("Start planning over")
                        Button { store.view = "Board" } label: { CampIconLabel(symbol: "xmark") }
                            .help("Back to board").accessibilityLabel("Back to board")
                    }.buttonStyle(.plain).padding(.horizontal, 22).padding(.vertical, 10)
                    if plan.current.editable {
                        VStack(alignment: .leading, spacing: 10) {
                            TextEditor(text: $brief).font(.system(size: 13)).scrollContentBackground(.hidden).frame(height: 75)
                                .padding(8).background(.white).clipShape(RoundedRectangle(cornerRadius: 8)).accessibilityLabel("Planning brief")
                            SessionContextComposer(store: store, mentions: store.mentions, destination: plan.session)
                            Picker("Planning mode", selection: Binding(get: { plan.current.workflow }, set: { store.setPlanMode(id, mode: $0) })) {
                                ForEach(PlanningMode.allCases, id: \.self) { Text($0.title).tag($0) }
                            }.frame(maxWidth: 300)
                            if plan.current.workflow == .comparison { Picker("Combine with", selection: Binding(get: { plan.current.mergeSlot }, set: { slot in planning.edit(id) { $0.mergeSlot = slot } })) {
                                ForEach(0..<2) { slot in Text(plan.current.planners[slot].session.provider).tag(slot) }
                            }.font(.system(size: 11)).frame(maxWidth: 240) }
                        }.padding(.horizontal, 22).padding(.bottom, 12)
                    } else {
                        Text(plan.current.brief).font(.system(size: 12)).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 22).padding(.bottom, 10)
                        if !plan.current.sources.isEmpty {
                            ScrollView(.horizontal) {
                                HStack {
                                    ForEach(plan.current.sources) { source in
                                        Button("@ " + store.contextTitle(source)) {
                                            Task {
                                                do {
                                                    if let snapshot = try await store.resolveContexts([source], destination: plan.session).first {
                                                        store.contextInspection = ContextInspection(sourceID: source.id, title: snapshot.title, messages: snapshot.messages, selectedMessage: snapshot.messages.first?.id ?? "")
                                                    }
                                                } catch { store.banner = error.localizedDescription }
                                            }
                                        }.font(.system(size: 11)).buttonStyle(.borderless)
                                    }
                                }.padding(.horizontal, 22)
                            }.fixedSize(horizontal: false, vertical: true).padding(.bottom, 10)
                        }
                    }
                    PlanningStageBar(mode: plan.current.workflow, section: $section, completed: completedStages(plan),
                        canImplement: plan.current.state == "ready" || plan.implementation != nil)
                        .padding(.horizontal, 22).padding(.bottom, 12)
                    if section == "Implementation" { detailReview(plan) { ImplementationSetupView(store: store, planning: planning, id: id, scrolls: false) } }
                    else if section == "Combined plan" { detailReview(plan) { combined(plan) } }
                    else if section == "History" { detailReview(plan) { history(plan) } }
                    else {
                        panes(plan).padding(.horizontal, 22)
                    }
                    footer(plan)
                }.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            .task(id: "\(id):\(plan.current.number):\(plan.current.workflow.rawValue):\(store.notificationPlanVersion ?? 0)") {
                brief = plan.current.brief
                section = store.notificationPlanVersion.map { $0 == plan.current.number ? (plan.current.workflow == .single ? "Plans" : "Combined plan") : "History" } ?? "Plans"
                if let version = store.notificationPlanVersion { expandedVersions.insert(version) }
                reviewedUnknown = false
                focusedSlot = nil
                applyCardNavigation()
                profiles = ["Codex": store.planningProfiles("Codex"), "Claude": store.planningProfiles("Claude")]
            }
            .onChange(of: brief) { _, value in
                saveTask?.cancel()
                let version = plan.current.number
                saveTask = Task {
                    try? await Task.sleep(for: .milliseconds(350))
                    guard !Task.isCancelled, planning.ticket(id)?.current.number == version else { return }
                    planning.edit(id) { $0.brief = value }
                }
            }
            .onChange(of: plan.current.workflow) { _, _ in focusedSlot = nil; section = "Plans" }
            .onChange(of: store.planNavigation) { _, _ in applyCardNavigation() }
            .onDisappear { saveTask?.cancel(); if planning.ticket(id)?.current.number == plan.current.number { planning.edit(id) { $0.brief = brief } } }
        }
    }
    private func applyCardNavigation() {
        guard let request = store.planNavigation, request.id == id else { return }
        section = request.section
        focusedSlot = request.slot.flatMap { planning.ticket(id)?.current.planners.indices.contains($0) == true ? $0 : nil }
    }
    private func participants(_ plan: LocalPlan) -> [PlanningParticipant] {
        plan.current.planners.indices.map { slot in
            let steps = plan.current.steps.filter { $0.slot == slot && $0.phase != "combined" }
            let status = PlanningParticipant.progress(steps.map { (phase: $0.phase, state: $0.state) }, planState: plan.current.state)
            return PlanningParticipant(id: slot, provider: plan.current.planners[slot].session.provider, status: status)
        }
    }
    private func completedStages(_ plan: LocalPlan) -> Set<String> {
        var result = Set<String>()
        for (section, phase) in [("Plans", "draft"), ("Reviews", "review"), ("Combined plan", "combined")] {
            let steps = plan.current.steps.filter { $0.phase == phase }
            if !steps.isEmpty && steps.allSatisfy({ $0.state == "completed" }) { result.insert(section) }
        }
        if plan.implementation?.state == "complete" { result.insert("Implementation") }
        return result
    }
    private func panes(_ plan: LocalPlan, all: Bool = false) -> some View {
        PlanningPaneRow(slots: plan.current.planners.indices.filter { all || focusedSlot == nil || focusedSlot == $0 }) { slot in
            pane(plan, slot: slot).id("\(plan.id):\(plan.current.number):\(slot):\(section)")
        }
    }
    private func detailReview<Content: View>(_ plan: LocalPlan, @ViewBuilder content: @escaping () -> Content) -> some View {
        ScrollViewReader { proxy in
            CampTranscriptViewport(padding: 0, followBottom: { proxy.scrollTo("plan-detail-bottom", anchor: .bottom) }) {
                VStack(alignment: .leading, spacing: 16) {
                    panes(plan, all: true).frame(height: 340)
                    content()
                        .background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
                    Color.clear.frame(height: 1).id("plan-detail-bottom")
                }.padding(.horizontal, 22).padding(.bottom, 16)
            }
            .task(id: "\(section):\(plan.current.number):\(plan.selectedExecutor ?? "")") {
                await Task.yield()
                guard !Task.isCancelled else { return }
                proxy.scrollTo("plan-detail-bottom", anchor: .bottom)
            }
            .onChange(of: plan.current.steps.last?.output) { _, _ in
                if section == "Combined plan" { proxy.scrollTo("plan-detail-bottom", anchor: .bottom) }
            }
        }
    }
    private func pane(_ plan: LocalPlan, slot: Int) -> some View {
        let version = plan.current, planner = version.planners[slot]
        return VStack(alignment: .leading, spacing: 10) {
            HStack { ProviderMark(provider: planner.session.provider, size: 17); Text(planner.session.provider).font(.system(size: 13, weight: .medium)); Spacer(); Text("This Mac").font(.system(size: 10)).foregroundStyle(campMuted) }
            if version.editable {
                if version.workflow == .single {
                    Picker("Provider", selection: Binding(get: { planner.session.provider }, set: { store.setPlanProvider(id, provider: $0) })) {
                        ForEach(["Codex", "Claude"].filter { !(profiles[$0] ?? []).isEmpty }, id: \.self) { Text($0).tag($0) }
                    }.font(.system(size: 11))
                }
                Picker("Profile", selection: Binding(get: { planner.session.id }, set: { value in
                    if let source = profiles[planner.session.provider]?.first(where: { $0.id == value }) {
                        planning.edit(id) { $0.planners[slot] = LocalPlanner(session: source, model: store.effectiveModel(source), tuning: store.tuning(source)) }
                    }
                })) {
                    ForEach(profiles[planner.session.provider] ?? [planner.session]) { source in Text(source.sourceLabel).tag(source.id) }
                }.font(.system(size: 11))
            } else { Text(planner.session.sourceLabel).font(.system(size: 11)).foregroundStyle(campMuted) }
            PlanningModelMenu(store: store, id: id, version: version, slot: slot, expanded: true)
            Divider()
            ScrollViewReader { proxy in
                CampTranscriptViewport(padding: 12, followBottom: { proxy.scrollTo("bottom", anchor: .bottom) }) {
                    VStack(alignment: .leading, spacing: 18) {
                        let steps = version.steps.filter { $0.slot == slot && $0.phase != "combined" && (section != "Reviews" || $0.phase == "review") }
                        ForEach(steps) { step in output(plan, version: version, step: step) }
                        if steps.isEmpty { Text(version.editable ? "Start to generate an independent plan" : section == "Reviews" ? "The review will appear after both drafts" : "Waiting for this provider").font(.system(size: 12)).foregroundStyle(campMuted) }
                        Color.clear.frame(height: 1).id("bottom")
                    }
                }.onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
                    .onChange(of: planning.live) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
                    .onChange(of: version) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
            }
        }.padding(14).frame(minWidth: 0, maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(campInk.opacity(0.08)))
    }
    private func output(_ plan: LocalPlan, version: LocalPlanVersion, step: LocalPlanStep) -> some View {
        let live = planning.liveText(id, version: version.number, ordinal: step.ordinal)
        return VStack(alignment: .leading, spacing: 8) {
            HStack { Text(version.workflow == .single ? "Plan" : step.phase == "draft" ? "Independent plan" : step.phase == "review" ? "Review" : "Combined plan").font(.system(size: 12, weight: .medium)); Spacer(); if ["starting", "running"].contains(step.state) { ProgressView().controlSize(.mini) } }
            if !step.output.isEmpty || !live.isEmpty {
                let planner = version.planners[step.slot]
                MessageView(message: Message(id: "\(version.number):\(step.ordinal)", role: "assistant", text: step.output.isEmpty ? live : step.output),
                    metadata: "\(planner.session.provider) · \(planner.model) · \(ModelMenu.effortName(planner.tuning.effort)) effort · \(ModelMenu.speedName(planner.tuning.speed))", fontSize: 12)
            }
            else { Text(step.state == "pending" ? "Waiting" : step.state.replacingOccurrences(of: "_", with: " ").capitalized).font(.system(size: 11)).foregroundStyle(campMuted) }
            if !step.error.isEmpty { Text(step.error).font(.system(size: 11)).foregroundStyle(.orange) }
            if let count = step.usage.outputTokens { Text("\(count.formatted()) output tokens").font(.system(size: 10)).foregroundStyle(campMuted) }
        }
    }
    private func combined(_ plan: LocalPlan) -> some View {
            VStack(alignment: .leading, spacing: 16) {
                if let step = plan.current.steps.first(where: { $0.phase == "combined" }) { output(plan, version: plan.current, step: step) }
                else { Text("The combined plan follows both drafts and reviews").foregroundStyle(campMuted) }
                if plan.current.state == "ready" {
                    implementationLink(plan)
                }
            }.frame(maxWidth: .infinity, alignment: .leading).padding(22)
    }
    private func implementationLink(_ plan: LocalPlan) -> some View {
        Button(plan.implementation == nil ? "Continue to implementation" : "Open implementation") {
            if plan.implementation == nil { section = "Implementation" } else { store.reviewingPlan = nil }
        }.buttonStyle(.borderedProminent).tint(campGreen)
    }
    private func history(_ plan: LocalPlan) -> some View {
            VStack(alignment: .leading, spacing: 16) {
                Text("Planning history").font(.system(size: 13, weight: .medium))
                ForEach(plan.versions.reversed()) { version in
                    DisclosureGroup("Version \(version.number) · \(version.state.replacingOccurrences(of: "_", with: " "))", isExpanded: Binding(get: { expandedVersions.contains(version.number) }, set: { expanded in if expanded { expandedVersions.insert(version.number) } else { expandedVersions.remove(version.number) } })) {
                        VStack(alignment: .leading, spacing: 18) {
                            Text(version.brief).font(.system(size: 12)).textSelection(.enabled)
                            ForEach(version.planners, id: \.session.id) { planner in Text("\(planner.session.provider) · \(planner.model) · \(ModelMenu.effortName(planner.tuning.effort)) effort · \(ModelMenu.speedName(planner.tuning.speed))").font(.system(size: 10)).foregroundStyle(campMuted) }
                            ForEach(version.steps) { output(plan, version: version, step: $0) }
                        }.padding(.vertical, 12)
                    }
                }
                ForEach((plan.implementations ?? []).reversed()) { execution in
                    DisclosureGroup("Implementation · \(execution.executor.session.provider) · \(execution.state)") {
                        ForEach(execution.messages) { MessageView(message: $0, provider: execution.executor.session.provider) }
                    }
                }
            }.frame(maxWidth: .infinity, alignment: .leading).padding(22)
    }
    private func footer(_ plan: LocalPlan) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if !planning.error.isEmpty { Text(planning.error).font(.system(size: 11)).foregroundStyle(.orange) }
            if plan.current.state == "needs_review" {
                Text("An earlier provider turn may have completed. Review its history before starting over; a new revision uses allowance again.").font(.system(size: 11)).foregroundStyle(campMuted)
                Toggle("I have reviewed the unconfirmed turn", isOn: $reviewedUnknown).toggleStyle(.checkbox).font(.system(size: 11))
            }
            HStack {
                if plan.current.editable {
                    Toggle(plan.current.workflow.budgetLabel, isOn: Binding(get: { plan.current.budgetPolicy == "calls" }, set: { value in planning.edit(id) { $0.budgetPolicy = value ? "calls" : "strict_tokens" } }))
                        .toggleStyle(.checkbox).font(.system(size: 11))
                } else { Text(plan.current.state == "running" ? "Planning continues while you browse" : plan.current.state.replacingOccurrences(of: "_", with: " ").capitalized).font(.system(size: 11)).foregroundStyle(campMuted) }
                Spacer()
                if plan.current.state == "ready" && plan.current.workflow == .single && section == "Plans" {
                    implementationLink(plan)
                }
                if plan.current.editable || plan.current.state == "paused" {
                    Button(plan.current.state == "paused" ? "Resume" : "Start") {
                        saveTask?.cancel()
                        if plan.current.editable && !planning.edit(id, { $0.brief = brief }) { return }
                        planning.start(id, sources: store.mentions.references(for: id))
                    }.buttonStyle(.borderedProminent).tint(campGreen).disabled(plan.current.budgetPolicy != "calls" || brief.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
                if plan.current.state == "running" { Button("Pause") { planning.pause(id) } }
                if ["running", "paused"].contains(plan.current.state) { Button("Cancel", role: .destructive) { planning.cancel(id) } }
            }
            Text("CLI token totals are reported when available; the turn limit is not a hard token or cost ceiling").font(.system(size: 10)).foregroundStyle(campMuted)
        }.padding(22)
    }
}
