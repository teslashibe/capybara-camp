import SwiftUI

struct SharedImplementationComposer: View {
    @ObservedObject var store: CampStore
    @ObservedObject var cloud: CampConnection
    @ObservedObject var planning: CampPlanning
    @ObservedObject var history: SharedImplementationHistory
    @ObservedObject var live: CloudLiveConversation
    @Binding var model: String
    @Binding var tuning: ModelTuning
    @State private var draft = ""
    @State private var error = ""
    @State private var sending = false
    @State private var contextPicking = false
    @State private var contextQuery = ""
    init(store: CampStore, history: SharedImplementationHistory, live: CloudLiveConversation, model: Binding<String>, tuning: Binding<ModelTuning>) {
        self.store = store; self.cloud = store.cloud; self.planning = store.sharedPlanning
        self.history = history; self.live = live; self._model = model; self._tuning = tuning
    }
    private var run: SharedImplementationSummary? { history.selected }
    private var source: CloudSession? { cloud.sessions.first { $0.id == run?.sessionId } }
    private var current: CloudRequest? { history.selectedRequest }
    private var owner: Bool { run?.ownerUserId == cloud.scope?.userId }
    private var pending: Bool { sending || history.currentPlan.map { planning.pending($0.id) } == true }
    private var requests: [CloudRequest] {
        var values = Dictionary(uniqueKeysWithValues: history.requests.map { ($0.id, $0) })
        for value in cloud.requests where value.implementationId == run?.id && value.planId == history.currentPlan?.id {
            values[value.id] = value.merging(values[value.id])
        }
        return values.values.filter { !["completed", "failed", "cancelled"].contains($0.state) }.sorted { $0.createdAt < $1.createdAt }
    }
    private var running: Bool { requests.contains { ["claimed", "started", "cancel_requested"].contains($0.state) } || live.head?.running == true }
    private var destination: CampSession? {
        guard let source, let run, let scope = cloud.scope else { return nil }
        return CampSession(id: "camp:\(scope.campId):implementation:\(run.id)", sessionID: run.id, provider: source.provider,
            sourceID: "camp-profile:" + source.profileId, sourceLabel: source.profileLabel, root: "", title: history.currentPlan?.title ?? "Implementation",
            cwd: "", updated: 0, path: "", accountID: source.ownerUserId, archived: false, model: model)
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            if let run { CampWorkerStatusView(worker: cloud.worker, sessionID: run.sessionId, implementationID: run.id, showStatus: false) }
            if let source, let current {
                CloudStreamApprovals(live: live, cloud: cloud, session: source, implementation: current)
                CloudControlQueueView(cloud: cloud, sessionID: source.id, requestID: current.id) { text in draft = text }
            }
            if !requests.isEmpty {
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(requests) { request in
                            HStack(alignment: .top) {
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(request.promptLabel).lineLimit(2)
                                    Text(request.state == "pending_approval" ? "Waiting for the executor owner's approval" : request.state == "outcome_unknown" ? "Outcome unconfirmed · Review before continuing" : request.state.replacingOccurrences(of: "_", with: " ")).foregroundStyle(campMuted)
                                }
                                Spacer()
                                if owner, request.state == "pending_approval", request.contentRestricted != true {
                                    Button("Approve") { Task { await cloud.queueAction(request: request, action: "approve") } }
                                }
                                if request.state != "outcome_unknown" && request.state != "cancel_requested" {
                                    Button { Task { await cloud.queueAction(request: request, action: "cancel") } } label: { CampIconLabel(symbol: "xmark") }
                                        .buttonStyle(.plain).help("Cancel request").accessibilityLabel("Cancel request")
                                }
                            }
                        }
                    }.font(.system(size: 11))
                }.frame(height: min(132, CGFloat(requests.count) * 60))
                    .id(requests.map { $0.id + ":" + $0.state }.joined(separator: "|"))
            }
            if let destination {
                SessionContextChips(store: store, mentions: store.mentions, destination: destination)
                TextEditor(text: $draft).font(.system(size: 13)).scrollContentBackground(.hidden)
                    .frame(minHeight: 64, maxHeight: 84).padding(8).background(.white)
                    .clipShape(RoundedRectangle(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(campInk.opacity(0.13)))
                    .accessibilityLabel("Implementation message")
                HStack(spacing: 10) {
                    Text(owner ? "Uses the approved project permissions" : "The executor's owner approves each follow-up")
                        .font(.system(size: 10)).foregroundStyle(campMuted)
                    Spacer(minLength: 8)
                    Button { contextQuery = ""; contextPicking = true } label: { Text("@ Context").frame(minHeight: 28).contentShape(Rectangle()) }
                        .buttonStyle(.plain).foregroundStyle(campGreen).accessibilityLabel("Attach session context")
                        .popover(isPresented: $contextPicking) {
                            SessionContextPicker(store: store, destination: destination, query: contextQuery, attached: clearMention) { contextPicking = false }
                        }
                    if owner, let scope = cloud.scope, let head = live.head, head.state == "started", let source, let current,
                       live.watchesImplementation(current, scope: scope) {
                        Button("Steer") { sendControl(source, current, head) }
                            .disabled(pending || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || cloud.pendingControl(requestID: head.id, kind: "steer") || !store.mentions.references(for: destination.id).isEmpty)
                            .help("Steering changes the current turn; attached context is sent with a queued follow-up")
                    }
                    Button(running ? "Queue" : "Send") { send(destination) }.buttonStyle(.borderedProminent).tint(campGreen)
                        .disabled(pending || !history.canContinue || requests.contains { $0.state == "outcome_unknown" || $0.contentRestricted == true } || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            ForEach(planning.failedImplementationPrompts.filter { $0.planID == history.currentPlan?.id && $0.command.path.contains("/implementation-runs/\(run?.id ?? "")/") }) { item in
                HStack {
                    Text(item.failure ?? "Follow-up needs review").font(.caption).foregroundStyle(campMuted)
                    Spacer()
                    Button("Restore prompt") {
                        Task {
                            do { let text = try await planning.recoverImplementationPrompt(item.id); draft = draft.isEmpty ? text : draft + "\n\n" + text }
                            catch { self.error = error.localizedDescription }
                        }
                    }
                }
            }
            if pending { Label("Saved on this Mac · Waiting to sync", systemImage: "clock").font(.caption).foregroundStyle(campMuted) }
            if !history.canContinue { Text("Follow-ups become available once the provider conversation is established").font(.caption).foregroundStyle(campMuted) }
            if !error.isEmpty { Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled) }
            if !planning.error.isEmpty { Text(planning.error).font(.caption).foregroundStyle(.orange) }
            if !cloud.error.isEmpty { Text(cloud.error).font(.caption).foregroundStyle(.orange) }
        }.onChange(of: draft) { _, value in
            if let query = sessionMentionQuery(value), !contextPicking { contextQuery = query; contextPicking = true }
        }
    }
    private func clearMention() {
        if let at = draft.lastIndex(of: "@"), !draft[draft.index(after: at)...].contains(where: \.isWhitespace) { draft = String(draft[..<at]) }
    }
    private func send(_ destination: CampSession) {
        guard !pending, let run, let plan = history.currentPlan, let scope = cloud.scope else { return }
        let selectedModel = model, selectedTuning = tuning
        let text = draft, references = store.mentions.references(for: destination.id)
        guard references.allSatisfy({ $0.scope == scope.key && $0.cloud != nil }) else { error = "Attach sessions from this Camp before sending"; return }
        sending = true; error = ""
        Task {
            defer { sending = false }
            do {
                try await planning.queueImplementationPrompt(run, plan: plan, text: text, model: selectedModel, tuning: selectedTuning, sources: references.compactMap(\.cloud))
                if cloud.scope == scope, history.selected?.id == run.id {
                    if draft == text { draft = "" }
                    try store.mentions.clear(references, for: destination.id)
                }
            } catch { self.error = error.localizedDescription }
        }
    }
    private func sendControl(_ source: CloudSession, _ request: CloudRequest, _ head: CloudStreamHead) {
        guard !pending else { return }
        let text = draft, id = UUID().uuidString.lowercased(); sending = true; error = ""
        Task {
            defer { sending = false }
            do {
                try await cloud.queueControl(session: source, head: head, value: CloudControlEnvelope(id: id, control: .init(type: "steer", id: id, text: text)), implementation: request, conversation: live)
                if history.selected?.id == request.implementationId && draft == text { draft = "" }
            } catch { self.error = error.localizedDescription }
        }
    }
}
