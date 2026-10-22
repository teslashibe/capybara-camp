import SwiftUI

struct SharedPlanningResultView: View {
    @ObservedObject var store: CampStore
    let result: CloudPlanningResult
    @State private var section = "Plans"
    var body: some View {
        HStack(spacing: 0) {
            RecentSessionsList(store: store).frame(width: 230)
            Divider()
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label(result.version.workflow.title, systemImage: "lightbulb").font(.headline)
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
                if !result.version.sources.isEmpty {
                    ScrollView(.horizontal) { HStack {
                        ForEach(result.version.sources, id: \.sessionId) { source in
                            Button("@ " + (store.sessions.first { $0.id == "camp:" + source.sessionId }?.title ?? "Shared session")) {
                                Task {
                                    do {
                                        guard let session = store.cloudSessions["camp:" + source.sessionId] else { throw CampError.message("This source is no longer shared") }
                                        let page = try await store.cloud.context(session: session, source: source)
                                        let messages = page.messages.map { Message(id: $0.id, role: $0.role, text: $0.text) }
                                        store.contextInspection = ContextInspection(sourceID: "camp:" + source.sessionId, title: session.title, messages: messages, selectedMessage: messages.first?.id ?? "")
                                    } catch { store.banner = error.localizedDescription }
                                }
                            }.font(.system(size: 11)).buttonStyle(.borderless)
                        }
                    } }
                }
                if result.version.workflow == .comparison { Picker("View", selection: $section) {
                    ForEach(["Plans", "Reviews", "Combined plan"], id: \.self) { Text($0).tag($0) }
                }.pickerStyle(.segmented).frame(maxWidth: 400) }
                if section == "Combined plan" {
                    resultMessages(result.version.steps.filter { $0.phase == "merge" })
                } else {
                    HStack(alignment: .top, spacing: 14) {
                        ForEach(result.version.planners, id: \.slot) { planner in
                            VStack(alignment: .leading, spacing: 10) {
                                HStack { ProviderMark(provider: planner.provider, size: 16); Text(planner.provider).font(.headline) }
                                ModelSettingsMenu(provider: planner.provider, title: "\(planner.provider) planner", sessionModel: planner.model,
                                    modelLabel: planner.model, models: [], caps: ModelOption(id: planner.model, label: planner.model),
                                    choice: ModelTuning(effort: planner.effort, speed: planner.speed), locked: true, selectModel: { _ in }, tune: { _, _ in })
                                Divider()
                                resultMessages(result.version.steps.filter { $0.slot == planner.slot && $0.phase == (section == "Plans" ? "draft" : "review") })
                            }.padding(14).frame(maxWidth: .infinity, maxHeight: .infinity).background(.white.opacity(0.55)).clipShape(RoundedRectangle(cornerRadius: 10))
                        }
                    }
                }
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
        }.onAppear { if store.notificationPlanVersion != nil && result.version.workflow == .comparison { section = "Combined plan" } }
            .onChange(of: result.version.workflow) { _, _ in section = "Plans" }
    }
    private func resultMessages(_ steps: [CloudPlanningResult.Step]) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(steps) { step in
                        if step.output.isEmpty { HStack { if ["started", "claimed"].contains(step.state) { ProgressView().controlSize(.small) }; Text(step.state.replacingOccurrences(of: "_", with: " ")).font(.caption).foregroundStyle(campMuted) } }
                        else { MessageView(message: Message(id: String(step.id), role: "assistant", text: step.output)) }
                    }
                    Color.clear.frame(height: 1).id("bottom")
                }.frame(maxWidth: .infinity, alignment: .leading).padding(14)
            }.onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: section) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
        }
    }
}
