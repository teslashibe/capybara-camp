import SwiftUI

struct SharedPlanningResultView: View {
    @ObservedObject var store: CampStore
    @ObservedObject private var sharedPlanning: CampPlanning
    let result: CloudPlanningResult
    @State private var section = "Plans"
    @State private var focusedSlot: Int?
    @StateObject private var implementationHistory: ImportedImplementationHistory
    @StateObject private var sharedHistory: SharedImplementationHistory
    @State private var showingSharedImplementation = false
    @StateObject private var executionReview: SharedExecutionReview
    @State private var showingExecutionSetup = false
    init(store: CampStore, result: CloudPlanningResult) {
        self.store = store; self.result = result; self.sharedPlanning = store.sharedPlanning
        _implementationHistory = StateObject(wrappedValue: ImportedImplementationHistory(planning: store.sharedPlanning))
        _sharedHistory = StateObject(wrappedValue: SharedImplementationHistory(planning: store.sharedPlanning))
        _executionReview = StateObject(wrappedValue: SharedExecutionReview(planning: store.sharedPlanning))
    }
    private var workspace: some View {
        HStack(spacing: 0) {
            PlanningNavigation(store: store, title: result.title, mode: result.version.workflow,
                participants: participants, section: $section, focusedSlot: $focusedSlot,
                sections: ["Plans", "Combined plan", "History"] + (canOpenImplementation ? ["Implementation"] : []), canImplement: canOpenImplementation)
            Divider()
            VStack(alignment: .leading, spacing: 14) {
                header
                Text(result.version.brief).font(.system(size: 12)).textSelection(.enabled).lineLimit(4)
                if let imported = result.version.imported {
                    Label("Imported history · " + imported.state.replacingOccurrences(of: "_", with: " "), systemImage: "tray.and.arrow.down")
                        .font(.system(size: 11)).foregroundStyle(campMuted)
                        .help("Saved local results. Importing did not run the providers")
                }
                SharedPlanningSources(store: store, result: result).id("\(result.id):\(result.version.version)")
                PlanningStageBar(mode: result.version.workflow, section: $section, completed: completedStages, canImplement: canOpenImplementation, showImplementation: canOpenImplementation)
                resultContent
                HStack {
                    Text("\(result.version.steps.filter { $0.state == "completed" }.count) of \(result.version.workflow.turnLimit) · \(result.version.state.replacingOccurrences(of: "_", with: " "))").font(.system(size: 11)).foregroundStyle(campMuted)
                    Spacer()
                    if result.creatorUserId == store.cloud.scope?.userId,
                       result.version.imported == nil, result.activeVersion == result.version.version {
                        if result.version.state == "running" { Button("Pause") { Task { await store.sharedPlanning.act("pause", on: result) } } }
                        if result.version.state == "paused" { Button("Resume") { Task { await store.sharedPlanning.act("resume", on: result) } } }
                        if result.version.state == "draft" { Button("Start") { Task { await store.sharedPlanning.act("start", on: result) } } }
                        if ["running", "paused", "needs_review"].contains(result.version.state) { Button("Cancel", role: .destructive) { Task { await store.sharedPlanning.act("cancel", on: result) } } }
                    }
                }.disabled(store.sharedPlanning.pending(result.id))
                if !store.sharedPlanning.error.isEmpty { Text(store.sharedPlanning.error).font(.caption).foregroundStyle(.orange) }
            }.padding(22).frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
    private var header: some View {
                HStack {
                    ProviderBadge(provider: "Planning")
                    Text(result.version.workflow.title).font(.system(size: 12, weight: .medium))
                    Text("Version \(result.version.version)").foregroundStyle(campMuted)
                    Spacer()
                    if let maxVersion = result.activeVersion {
                        Menu { ForEach(Array(max(1, maxVersion-99)...maxVersion), id: \.self) { version in Button("Version \(version)") { store.sharedPlanning.open(result.id, version: version) } } } label: { Image(systemName: "clock.arrow.circlepath").frame(width: 28, height: 28) }.menuStyle(.borderlessButton).fixedSize().help("Plan history")
                    }
                    if result.creatorUserId == store.cloud.scope?.userId {
                        Button {
                            store.restartSharedPlan(result)
                        } label: { Image(systemName: "arrow.counterclockwise").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Start over and keep history")
                            .disabled(["running", "needs_review"].contains(store.sharedPlanning.tickets.first { $0.id == result.id }?.state ?? result.version.state) || store.sharedPlanning.pending(result.id))
                    }
                    Button { store.sharedPlanResult = nil; store.view = "Board" } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                        .buttonStyle(.plain).help("Back to board").accessibilityLabel("Back to board")
                }
    }
    @ViewBuilder private var resultContent: some View {
                if section == "Implementation" {
                    if executionReview.status != nil || canConfigureImplementation && (showingExecutionSetup || !hasImplementation) {
                        SharedExecutionSetupView(store: store, review: executionReview, plan: result).id("\(result.id):\(result.version.version)")
                    } else if showingSharedImplementation || implementationHistory.implementations.isEmpty {
                        SharedImplementationView(history: sharedHistory, cloud: store.cloud, sourceIDs: result.referencedSessions) { section = "History" }
                    } else { ImportedImplementationView(history: implementationHistory) { section = "History" } }
                } else if section == "History" || section == "Combined plan" {
                    ScrollViewReader { proxy in ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            plannerPanes.frame(height: 340)
                            VStack(alignment: .leading, spacing: 12) {
                                if section == "History" {
                                    Text("Planning history").font(.headline)
                                    ForEach(Array(max(1, (result.activeVersion ?? result.version.version) - 99)...(result.activeVersion ?? result.version.version)).reversed(), id: \.self) { version in
                                        Button("Version \(version)") { store.sharedPlanning.open(result.id, version: version); section = "Plans"; focusedSlot = nil }
                                    }
                                    ImportedImplementationList(history: implementationHistory) { record in
                                        showingSharedImplementation = false; implementationHistory.select(record); section = "Implementation"
                                    }
                                    SharedImplementationList(history: sharedHistory) { showingSharedImplementation = true; showingExecutionSetup = false; section = "Implementation" }
                                    if canConfigureImplementation, result.creatorUserId == store.cloud.scope?.userId {
                                        Button("Choose executor") { showingExecutionSetup = true; section = "Implementation" }
                                    }
                                } else {
                                    messageBlocks(result.version.steps.filter { $0.phase == "merge" }) { proxy.scrollTo("shared-result-bottom", anchor: .bottom) }
                                }
                            }.frame(maxWidth: .infinity, alignment: .leading).padding(14)
                                .background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
                            Color.clear.frame(height: 1).id("shared-result-bottom")
                        }
                    }.onAppear { proxy.scrollTo("shared-result-bottom", anchor: .bottom) }
                        .onChange(of: section) { _, _ in proxy.scrollTo("shared-result-bottom", anchor: .bottom) }
                    }
                } else { plannerPanes }
    }
    private var boundWorkspace: some View {
        workspace.onAppear {
            if store.notificationPlanVersion != nil && result.version.workflow == .comparison { section = "Combined plan" }
            applyCardNavigation()
        }
            .task(id: "\(result.id):\(result.version.version)") { implementationHistory.bind(result); sharedHistory.bind(result); executionReview.bind(result); showingExecutionSetup = false }
            .onDisappear { implementationHistory.cancel(); sharedHistory.clear(); executionReview.clear() }
            .onChange(of: store.cloud.scope) { _, _ in implementationHistory.cancel(); sharedHistory.clear(); executionReview.clear() }
    }
    private var liveWorkspace: some View {
        boundWorkspace
            .onChange(of: sharedPlanning.executionRevision) { _, _ in executionReview.refresh() }
            .onChange(of: result.revision) { _, _ in executionReview.bind(result) }
            .onChange(of: store.cloud.requests.filter { $0.planId == result.id }) { _, values in
                sharedHistory.synchronize(values)
                if values.contains(where: { $0.isImplementation && ["queued", "claimed", "started"].contains($0.state) }) { showingExecutionSetup = false }
            }
            .onChange(of: store.cloudSessions.mapValues(\.permissionRevision)) { _, _ in
                implementationHistory.bind(result)
                sharedHistory.bind(result)
                executionReview.refresh()
                if section == "Implementation" { section = "History" }
            }
    }
    var body: some View {
        liveWorkspace
            .onChange(of: store.planNavigation) { _, _ in applyCardNavigation() }
            .onChange(of: result.version.workflow) { _, _ in section = "Plans"; focusedSlot = nil }
            .onChange(of: result.version.version) { _, _ in section = "Plans"; focusedSlot = nil }
            .onChange(of: result.id) { _, _ in
                section = store.notificationPlanVersion != nil && result.version.workflow == .comparison ? "Combined plan" : "Plans"
                focusedSlot = nil
                applyCardNavigation()
            }
    }
    private var hasImplementation: Bool { !implementationHistory.implementations.isEmpty || !sharedHistory.runs.isEmpty }
    private var canConfigureImplementation: Bool { result.version.state == "ready" && result.version.imported == nil && result.activeVersion == result.version.version }
    private var canOpenImplementation: Bool { hasImplementation || canConfigureImplementation || executionReview.status != nil }
    private var plannerPanes: some View {
        PlanningPaneRow(slots: result.version.planners.filter { focusedSlot == nil || focusedSlot == $0.slot }.map(\.slot)) { slot in
            if let planner = result.version.planners.first(where: { $0.slot == slot }) {
                VStack(alignment: .leading, spacing: 10) {
                    HStack { ProviderMark(provider: planner.provider, size: 16); Text(planner.provider).font(.headline) }
                    PlanningLocationLabel(execution: planner.execution)
                    ModelSettingsMenu(provider: planner.provider, title: "\(planner.provider) planner", sessionModel: planner.model,
                        modelLabel: planner.model, models: [], caps: ModelOption(id: planner.model, label: planner.model),
                        choice: ModelTuning(effort: planner.effort, speed: planner.speed), locked: true, expanded: true, selectModel: { _ in }, tune: { _, _ in })
                    Divider()
                    resultMessages(result.version.steps.filter { $0.slot == planner.slot && $0.phase != "merge" && (section != "Reviews" || $0.phase == "review") })
                }.padding(14).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
                    .id("\(result.id):\(result.version.version):\(planner.slot):\(section)")
            }
        }
    }
    private func applyCardNavigation() {
        guard let request = store.planNavigation, request.id == "camp-plan:" + result.id else { return }
        section = request.section
        focusedSlot = request.slot.flatMap { slot in result.version.planners.contains { $0.slot == slot } ? slot : nil }
    }
    private var participants: [PlanningParticipant] {
        result.version.planners.map { planner in
            let steps = result.version.steps.filter { $0.slot == planner.slot && $0.phase != "merge" }
            let status = PlanningParticipant.progress(steps.map { (phase: $0.phase, state: $0.state) }, planState: result.version.state)
            return PlanningParticipant(id: planner.slot, provider: planner.provider, status: status)
        }
    }
    private var completedStages: Set<String> {
        var result = Set<String>()
        for (section, phase) in [("Plans", "draft"), ("Reviews", "review"), ("Combined plan", "merge")] {
            let steps = self.result.version.steps.filter { $0.phase == phase }
            if !steps.isEmpty && steps.allSatisfy({ $0.state == "completed" }) { result.insert(section) }
        }
        if implementationHistory.implementationNext == 0,
           implementationHistory.implementations.max(by: { $0.ordinal < $1.ordinal })?.state == "complete" {
            result.insert("Implementation")
        }
        return result
    }
    private func resultMessages(_ steps: [CloudPlanningResult.Step]) -> some View {
        ScrollViewReader { proxy in
            CampTranscriptViewport(followBottom: { proxy.scrollTo("bottom", anchor: .bottom) }) {
                VStack(alignment: .leading, spacing: 14) {
                    messageBlocks(steps) { proxy.scrollTo("bottom", anchor: .bottom) }
                    Color.clear.frame(height: 1).id("bottom")
                }
            }.onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: section) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: steps.map(\.output)) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
        }
    }
    private func messageBlocks(_ steps: [CloudPlanningResult.Step], onUpdate: @escaping () -> Void = {}) -> some View {
        ForEach(steps) { step in
            Text(step.phase == "draft" ? "Independent plan" : step.phase == "review" ? "Review" : "Combined plan").font(.system(size: 12, weight: .medium))
            if step.output.isEmpty, let planner = result.version.planners.first(where: { $0.slot == step.slot }) {
                SharedPlanningLiveStep(cloud: store.cloud, planID: result.id, version: result.version.version, step: step, planner: planner, sourceIDs: result.referencedSessions, onUpdate: onUpdate)
                    .id("\(result.id):\(result.version.version):\(step.ordinal)")
            }
            else {
                let planner = result.version.planners.first { $0.slot == step.slot }
                MessageView(message: Message(id: String(step.id), role: "assistant", text: step.output),
                    metadata: planner.map { "\($0.provider) · \($0.model) · \(ModelMenu.effortName($0.effort)) effort · \(ModelMenu.speedName($0.speed))" }, fontSize: 12)
            }
            if let saved = result.version.imported?.steps.first(where: { $0.ordinal == step.ordinal }) {
                if saved.state == "unknown", !step.output.isEmpty {
                    Label("Unconfirmed outcome", systemImage: "questionmark.circle").font(.caption).foregroundStyle(campMuted)
                }
                if let error = saved.error, !error.isEmpty { Text(error).font(.caption).foregroundStyle(campMuted).textSelection(.enabled) }
            }
        }
    }
}

private struct SharedPlanningLiveStep: View {
    @ObservedObject var cloud: CampConnection
    let planID: String
    let version: Int
    let step: CloudPlanningResult.Step
    let planner: CloudPlanningResult.Planner
    let sourceIDs: Set<String>
    var onUpdate: () -> Void
    @StateObject private var live = CloudLiveConversation()
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if live.messages.isEmpty {
                HStack {
                    if ["started", "claimed"].contains(step.state) { ProgressView().controlSize(.small) }
                    Text(step.state.replacingOccurrences(of: "_", with: " ")).font(.caption).foregroundStyle(campMuted)
                }
            } else {
                ForEach(live.messages) { message in
                    MessageView(message: message,
                        metadata: "\(planner.provider) · \(live.head?.model ?? planner.model) · \(ModelMenu.effortName(live.head?.effort ?? planner.effort)) effort · \(ModelMenu.speedName(live.head?.speed ?? planner.speed))", fontSize: 12)
                }
            }
            if !live.notice.isEmpty { Text(live.notice).font(.caption).foregroundStyle(campMuted) }
        }
        .onAppear { watch() }
        .onDisappear { live.stop() }
        .onChange(of: cloud.planningStreamRevision) { _, _ in live.invalidate(planner.sessionId) }
        .onChange(of: cloud.scope) { _, _ in live.stop(); watch() }
        .onChange(of: sourceAccess) { _, _ in live.stop(); watch() }
        .onChange(of: live.revision) { _, _ in
            Task { @MainActor in await Task.yield(); if !Task.isCancelled { onUpdate() } }
        }
    }
    private var sourceAccess: [String] {
        cloud.sessions.filter { sourceIDs.contains($0.id) }.map { "\($0.id):\($0.permissionRevision)" }.sorted()
    }
    private func watch() {
        guard let scope = cloud.scope, let client = cloud.client, let credential = cloud.credential,
              cloud.sessions.contains(where: { $0.id == planner.sessionId }) else { return }
        live.watchPlanning(planID: planID, version: version, ordinal: step.ordinal, sessionID: planner.sessionId, scope: scope, client: client, credential: credential)
    }
}

private struct SharedPlanningSources: View {
    @ObservedObject var store: CampStore
    let result: CloudPlanningResult
    @State private var task: Task<Void, Never>?
    @State private var loading = false
    @State private var generation = UUID()

    var body: some View {
        if !result.version.sources.isEmpty || !result.version.allQuotedSources.isEmpty {
            ScrollView(.horizontal) {
                HStack {
                    ForEach(result.version.sources, id: \.sessionId) { source in
                        Button("@ " + (store.sessions.first { $0.id == "camp:" + source.sessionId }?.title ?? "Shared session")) {
                            open(source.sessionId, imported: false)
                        }
                    }
                    ForEach(result.version.allQuotedSources) { source in
                        Button("@ " + (source.title.isEmpty ? "Imported source" : source.title)) { open(source.id, imported: true) }
                    }
                    if loading { ProgressView().controlSize(.small) }
                }.font(.system(size: 11)).buttonStyle(.borderless).disabled(loading)
            }.onDisappear { generation = UUID(); task?.cancel(); task = nil; loading = false }
        }
    }
    private func open(_ id: String, imported: Bool) {
        task?.cancel(); loading = true
        generation = UUID()
        let token = generation
        task = Task {
            defer { if generation == token { loading = false; task = nil } }
            do {
                let inspection = try await store.inspectPlanningSource(result, sourceID: id, imported: imported)
                try Task.checkCancellation()
                store.contextInspection = inspection
            } catch is CancellationError {} catch { if generation == token && !Task.isCancelled { store.banner = error.localizedDescription } }
        }
    }
}
