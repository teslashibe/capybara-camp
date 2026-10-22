import SwiftUI

struct SharedImplementationList: View {
    @ObservedObject var history: SharedImplementationHistory
    var open: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(history.runs) { run in
                Button {
                    history.select(run); open()
                } label: {
                    HStack(spacing: 10) {
                        ProviderMark(provider: run.provider, size: 17)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(run.model.isEmpty ? run.provider : run.model).font(.system(size: 12, weight: .medium))
                            Text("Implementation · \(run.state.replacingOccurrences(of: "_", with: " "))").font(.system(size: 10)).foregroundStyle(campMuted)
                        }
                        Spacer()
                        Image(systemName: "chevron.right").font(.system(size: 10)).foregroundStyle(campMuted)
                    }.padding(.vertical, 8).frame(maxWidth: .infinity).contentShape(Rectangle())
                }.buttonStyle(.plain)
            }
            if history.runsBefore != nil { Button("Earlier implementations") { history.loadRuns() }.disabled(history.loading) }
            if history.loading { ProgressView().controlSize(.small) }
            if !history.error.isEmpty { Text(history.error).font(.caption).foregroundStyle(campMuted) }
        }
    }
}

struct SharedImplementationView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var history: SharedImplementationHistory
    @ObservedObject var cloud: CampConnection
    let sourceIDs: Set<String>
    var back: () -> Void
    @StateObject private var live = CloudLiveConversation()
    @State private var model = ""
    @State private var tuning = ModelTuning()
    @State private var tuningByModel: [String: ModelTuning] = [:]
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .center, spacing: 16) {
                    heading.fixedSize()
                    Spacer(minLength: 8)
                    settings.frame(width: 420)
                }
                VStack(alignment: .leading, spacing: 10) { heading; settings }
            }
            if let run = history.selected, let request = history.selectedRequest {
                HStack {
                    Menu {
                        ForEach(history.requests) { value in
                            Button(value.prompt.prefix(70) + " · " + value.state.replacingOccurrences(of: "_", with: " ")) { history.selectedRequestID = value.id }
                        }
                    } label: {
                        Label("Request history", systemImage: "clock.arrow.circlepath").padding(.vertical, 6).contentShape(Rectangle())
                    }.menuStyle(.borderlessButton).fixedSize()
                    if history.requestsBefore != nil { Button("Earlier requests") { history.loadEarlier() }.disabled(history.loading) }
                    Button("Latest requests") { history.select(run) }.disabled(history.loading)
                    Spacer()
                    if live.head?.running == true { ProgressView().controlSize(.small) }
                    Text((live.head?.state ?? request.state).replacingOccurrences(of: "_", with: " ")).font(.caption).foregroundStyle(campMuted)
                }
                ScrollViewReader { proxy in
                    CampTranscriptViewport(followBottom: { proxy.scrollTo("shared-implementation-bottom", anchor: .bottom) }) {
                        LazyVStack(alignment: .leading, spacing: 18) {
                            MessageView(message: Message(id: request.id, role: "user", text: request.prompt), provider: run.provider)
                            ForEach(live.messages) { MessageView(message: $0, metadata: "\(live.head?.model ?? request.model) · \(ModelMenu.effortName(live.head?.effort ?? request.effort)) · \(ModelMenu.speedName(live.head?.speed ?? request.speed))", provider: run.provider) }
                            if !live.notice.isEmpty { Text(live.notice).font(.caption).foregroundStyle(campMuted) }
                            Color.clear.frame(height: 1).id("shared-implementation-bottom")
                        }
                    }.onAppear { proxy.scrollTo("shared-implementation-bottom", anchor: .bottom) }
                        .onChange(of: live.revision) { _, _ in proxy.scrollTo("shared-implementation-bottom", anchor: .bottom) }
                }
            } else {
                SharedImplementationList(history: history, open: {})
                Spacer()
            }
            if history.selected != nil {
                SharedImplementationComposer(store: store, history: history, live: live, model: $model, tuning: $tuning).id(history.selected?.id)
            }
            if !history.error.isEmpty { Text(history.error).font(.caption).foregroundStyle(campMuted) }
        }.onAppear { model = history.selected?.model ?? ""; tuning = ModelTuning(effort: history.selected?.effort ?? "", speed: history.selected?.speed ?? "standard"); openOnlyRun(); watch() }
            .onDisappear { live.stop() }
            .onChange(of: history.selected?.id) { _, _ in model = history.selected?.model ?? ""; tuning = ModelTuning(effort: history.selected?.effort ?? "", speed: history.selected?.speed ?? "standard"); tuningByModel = [:] }
            .onChange(of: history.selectedRequestID) { _, _ in live.stop(); watch() }
            .onChange(of: history.loading) { _, loading in if !loading { openOnlyRun() } }
            .onChange(of: cloud.planningStreamRevision) { _, _ in if let id = live.sessionID { live.invalidate(id) } }
            .onChange(of: history.selectedRequest?.revision) { _, _ in if let id = live.sessionID { live.invalidate(id) } }
            .onChange(of: cloud.scope) { _, _ in live.stop() }
            .onChange(of: access) { _, _ in live.stop() }
    }
    private var heading: some View {
        HStack(spacing: 10) {
            Button(action: back) { CampIconLabel(symbol: "chevron.left") }.buttonStyle(.plain)
                .help("Back to planning history").accessibilityLabel("Back to planning history")
            if let run = history.selected { ProviderMark(provider: run.provider, size: 18) }
            Text("Implementation").font(.system(size: 13, weight: .medium))
            if history.loading { ProgressView().controlSize(.small) }
        }
    }
    @ViewBuilder private var settings: some View {
        if let run = history.selected, let request = history.selectedRequest {
            let models = store.sessions.first(where: { $0.id == "camp:" + run.sessionId }).map(store.availableModels) ?? []
            ModelSettingsMenu(provider: run.provider, title: "Next reply model", sessionModel: request.model,
                modelLabel: model, models: models, caps: models.first { $0.id == model } ?? ModelOption(id: model, label: model),
                choice: tuning, locked: !history.canContinue, expanded: true,
                selectModel: { next in tuningByModel[model] = tuning; model = next.isEmpty ? request.model : next; tuning = tuningByModel[model] ?? ModelTuning() },
                tune: { effort, speed in if let effort { tuning.effort = effort }; if let speed { tuning.speed = speed } })
        }
    }
    private var access: [String] {
        cloud.sessions.filter { sourceIDs.contains($0.id) || $0.id == history.selected?.sessionId }
            .map { "\($0.id):\($0.permissionRevision)" }.sorted()
    }
    private func openOnlyRun() {
        guard !history.loading, history.error.isEmpty, history.selected == nil,
              history.runs.count == 1, history.runsBefore == nil, let run = history.runs.first else { return }
        history.select(run)
    }
    private func watch() {
        guard let request = history.selectedRequest, let scope = cloud.scope, let client = cloud.client, let credential = cloud.credential,
              sourceIDs.union([request.sessionId]).isSubset(of: Set(cloud.sessions.map(\.id))) else { return }
        live.watchImplementation(request, scope: scope, client: client, credential: credential)
    }
}
