import SwiftUI

struct SharedPlanningResultView: View {
    @ObservedObject var store: CampStore
    let result: CloudPlanningResult
    @State private var section = "Plans"
    @State private var focusedSlot: Int?
    var body: some View {
        HStack(spacing: 0) {
            PlanningNavigation(store: store, title: result.title, mode: result.version.workflow,
                participants: participants, section: $section, focusedSlot: $focusedSlot,
                sections: ["Plans", "Combined plan", "History"], canImplement: false)
            Divider()
            VStack(alignment: .leading, spacing: 14) {
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
                Text(result.version.brief).font(.system(size: 12)).textSelection(.enabled).lineLimit(4)
                if let imported = result.version.imported {
                    Label("Imported history · " + imported.state.replacingOccurrences(of: "_", with: " "), systemImage: "tray.and.arrow.down")
                        .font(.system(size: 11)).foregroundStyle(campMuted)
                        .help("Saved local results. Importing did not run the providers")
                }
                SharedPlanningSources(store: store, result: result).id("\(result.id):\(result.version.version)")
                PlanningStageBar(mode: result.version.workflow, section: $section, completed: completedStages, showImplementation: false)
                if section == "History" || section == "Combined plan" {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            plannerPanes.frame(height: 340)
                            VStack(alignment: .leading, spacing: 12) {
                                if section == "History" {
                                    Text("Planning history").font(.headline)
                                    ForEach(Array(max(1, (result.activeVersion ?? result.version.version) - 99)...(result.activeVersion ?? result.version.version)).reversed(), id: \.self) { version in
                                        Button("Version \(version)") { store.sharedPlanning.open(result.id, version: version); section = "Plans"; focusedSlot = nil }
                                    }
                                } else {
                                    messageBlocks(result.version.steps.filter { $0.phase == "merge" })
                                }
                            }.frame(maxWidth: .infinity, alignment: .leading).padding(14)
                                .background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
                        }
                    }
                } else { plannerPanes }
                HStack {
                    Text("\(result.version.steps.filter { $0.state == "completed" }.count) of \(result.version.workflow.turnLimit) · \(result.version.state.replacingOccurrences(of: "_", with: " "))").font(.system(size: 11)).foregroundStyle(campMuted)
                    Spacer()
                    if result.creatorUserId == store.cloud.scope?.userId {
                        if result.version.state == "running" { Button("Pause") { Task { await store.sharedPlanning.act("pause", on: result) } } }
                        if result.version.state == "paused" { Button("Resume") { Task { await store.sharedPlanning.act("resume", on: result) } } }
                        if result.version.state == "draft" { Button("Start") { Task { await store.sharedPlanning.act("start", on: result) } } }
                        if ["running", "paused", "needs_review"].contains(result.version.state) { Button("Cancel", role: .destructive) { Task { await store.sharedPlanning.act("cancel", on: result) } } }
                    }
                }.disabled(store.sharedPlanning.pending(result.id))
                if !store.sharedPlanning.error.isEmpty { Text(store.sharedPlanning.error).font(.caption).foregroundStyle(.orange) }
            }.padding(22).frame(maxWidth: .infinity, maxHeight: .infinity)
        }.onAppear {
            if store.notificationPlanVersion != nil && result.version.workflow == .comparison { section = "Combined plan" }
            applyCardNavigation()
        }
            .onChange(of: store.planNavigation) { _, _ in applyCardNavigation() }
            .onChange(of: result.version.workflow) { _, _ in section = "Plans"; focusedSlot = nil }
            .onChange(of: result.id) { _, _ in
                section = store.notificationPlanVersion != nil && result.version.workflow == .comparison ? "Combined plan" : "Plans"
                focusedSlot = nil
                applyCardNavigation()
            }
    }
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
            let status = steps.contains(where: { ["started", "claimed"].contains($0.state) }) ? "Working" :
                steps.contains(where: { $0.phase == "review" && $0.state == "completed" }) ? "Reviewed" :
                steps.contains(where: { $0.state == "completed" }) ? "Draft ready" : "Ready"
            return PlanningParticipant(id: planner.slot, provider: planner.provider, status: status)
        }
    }
    private var completedStages: Set<String> {
        var result = Set<String>()
        for (section, phase) in [("Plans", "draft"), ("Reviews", "review"), ("Combined plan", "merge")] {
            let steps = self.result.version.steps.filter { $0.phase == phase }
            if !steps.isEmpty && steps.allSatisfy({ $0.state == "completed" }) { result.insert(section) }
        }
        return result
    }
    private func resultMessages(_ steps: [CloudPlanningResult.Step]) -> some View {
        ScrollViewReader { proxy in
            CampTranscriptViewport(followBottom: { proxy.scrollTo("bottom", anchor: .bottom) }) {
                VStack(alignment: .leading, spacing: 14) {
                    messageBlocks(steps)
                    Color.clear.frame(height: 1).id("bottom")
                }
            }.onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: section) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: steps.map(\.output)) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
        }
    }
    private func messageBlocks(_ steps: [CloudPlanningResult.Step]) -> some View {
        ForEach(steps) { step in
            Text(step.phase == "draft" ? "Independent plan" : step.phase == "review" ? "Review" : "Combined plan").font(.system(size: 12, weight: .medium))
            if step.output.isEmpty { HStack { if ["started", "claimed"].contains(step.state) { ProgressView().controlSize(.small) }; Text(step.state.replacingOccurrences(of: "_", with: " ")).font(.caption).foregroundStyle(campMuted) } }
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

private struct SharedPlanningSources: View {
    @ObservedObject var store: CampStore
    let result: CloudPlanningResult
    @State private var task: Task<Void, Never>?
    @State private var loading = false
    @State private var generation = UUID()

    var body: some View {
        if !result.version.sources.isEmpty || result.version.imported?.localSources.isEmpty == false {
            ScrollView(.horizontal) {
                HStack {
                    ForEach(result.version.sources, id: \.sessionId) { source in
                        Button("@ " + (store.sessions.first { $0.id == "camp:" + source.sessionId }?.title ?? "Shared session")) {
                            open(source.sessionId, imported: false)
                        }
                    }
                    ForEach(result.version.imported?.localSources ?? []) { source in
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
