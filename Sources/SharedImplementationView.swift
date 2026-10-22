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
    @ObservedObject var history: SharedImplementationHistory
    @ObservedObject var cloud: CampConnection
    let sourceIDs: Set<String>
    var back: () -> Void
    @StateObject private var live = CloudLiveConversation()
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                Button(action: back) { CampIconLabel(symbol: "chevron.left") }.buttonStyle(.plain)
                    .help("Back to planning history").accessibilityLabel("Back to planning history")
                if let run = history.selected { ProviderMark(provider: run.provider, size: 18) }
                Text("Implementation").font(.system(size: 13, weight: .medium))
                Spacer()
                if history.loading { ProgressView().controlSize(.small) }
            }
            if let run = history.selected, let request = history.selectedRequest {
                ModelSettingsMenu(provider: run.provider, title: "Request settings", sessionModel: live.head?.model ?? request.model,
                    modelLabel: live.head?.model ?? request.model, models: [], caps: ModelOption(id: request.model, label: request.model),
                    choice: ModelTuning(effort: live.head?.effort ?? request.effort, speed: live.head?.speed ?? request.speed),
                    locked: true, expanded: true, selectModel: { _ in }, tune: { _, _ in })
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
                            ForEach(live.messages) { MessageView(message: $0, provider: run.provider) }
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
            if !history.error.isEmpty { Text(history.error).font(.caption).foregroundStyle(campMuted) }
        }.onAppear { openOnlyRun(); watch() }
            .onDisappear { live.stop() }
            .onChange(of: history.selectedRequestID) { _, _ in live.stop(); watch() }
            .onChange(of: history.loading) { _, loading in if !loading { openOnlyRun() } }
            .onChange(of: cloud.planningStreamRevision) { _, _ in if let id = live.sessionID { live.invalidate(id) } }
            .onChange(of: history.selectedRequest?.revision) { _, _ in if let id = live.sessionID { live.invalidate(id) } }
            .onChange(of: cloud.scope) { _, _ in live.stop() }
            .onChange(of: access) { _, _ in live.stop() }
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
