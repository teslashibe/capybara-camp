import SwiftUI

struct PlanningDestinationRequest: Identifiable {
    let id = UUID()
    let draftID: String
    let slot: Int
    let provider: String
    let original: CloudPlannerInput
    let location: String
    let projectID: String
    let scope: CloudScope
}

struct PlanningRunOnMenu: View {
    let provider: String
    let execution: CloudPlanningExecution?
    var localLabel = "This Mac"
    let choose: (String) -> Void
    var body: some View {
        CampSettingsField("Run on") {
            Menu {
                Button("This Mac", systemImage: "desktopcomputer") { choose("Local") }
                Button("Cloud", systemImage: "cloud") { choose("Cloud") }
            } label: {
                Label(execution?.location == "cloud" ? "Cloud" : execution == nil ? "Choose location" : localLabel, systemImage: execution?.location == "cloud" ? "cloud" : "desktopcomputer")
            }
                .accessibilityLabel("\(provider) plan location")
        }
    }
}

struct PlanningDestinationView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var cloud: CampConnection
    @ObservedObject var planning: CampPlanning
    @ObservedObject var accounts: CampProviderAccounts
    let request: PlanningDestinationRequest
    @Environment(\.dismiss) private var dismiss
    @State private var location: String
    @State private var accountID = ""
    @State private var sourceID: String
    @State private var prepared: [CloudPlanningSource] = []
    @State private var loading = false
    @State private var error = ""
    @State private var loadToken = UUID()
    @State private var reload = 0
    @State private var preparingSession: CampSession?
    @State private var applying = false
    @State private var applyTask: Task<Void, Never>?

    init(store: CampStore, request: PlanningDestinationRequest) {
        self.store = store; self.request = request; cloud = store.cloud
        planning = store.sharedPlanning; accounts = store.cloud.providerAccounts
        _location = State(initialValue: request.location)
        _sourceID = State(initialValue: request.original.sessionId)
    }
    private var validScope: Bool { cloud.scope == request.scope }
    private var shared: Bool { planning.draft(request.draftID)?.input.shared ?? false }
    private var localSources: [CloudSession] {
        cloud.sessions.filter { source in
            source.campId == request.scope.campId && source.projectId == request.projectID &&
            source.provider == request.provider && source.ownerUserId == request.scope.userId &&
            source.ownerDeviceId == cloud.credential?.actor.deviceId && (!shared || source.shared) &&
            CloudPlanningExecution(session: source)?.location == "local"
        }
    }
    private var sources: [CloudSession] {
        if location == "Local" { return localSources }
        return prepared.map(\.session).filter { $0.provider == request.provider && (!shared || $0.shared) }
    }
    private var selected: CloudSession? { sources.first { $0.id == sourceID } }
    private var original: CloudSession? { cloud.sessions.first { $0.id == request.original.sessionId } }
    private var native: CampSession? {
        guard let source = selected ?? original, source.ownerUserId == request.scope.userId else { return nil }
        return store.sharing.native(for: "camp:" + source.id) ?? store.sessions.first { $0.id == "camp:" + source.id }
    }
    private var queryKey: String { "\(cloud.scope?.key ?? ""): \(location):\(accountID):\(reload)" }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                ProviderMark(provider: request.provider, size: 20)
                Text("Run on").font(.title2)
                Spacer()
                Button { dismiss() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).help("Close").accessibilityLabel("Close planning location").disabled(applying)
            }
            ExecutionLocationPicker(selection: $location)
            Text(cloud.projects.first { $0.id == request.projectID }?.name ?? "Project unavailable")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            if !validScope { Text("Reconnect to this plan's Camp before changing its location").font(.system(size: 12)) }
            else {
                if location == "Cloud" {
                    Picker("Account", selection: $accountID) {
                        Text("All connected accounts").tag("")
                        ForEach(accounts.accounts.filter { $0.provider == request.provider && $0.state == "connected" }) { Text($0.label).tag($0.id) }
                        if !accountID.isEmpty, !accounts.accounts.contains(where: { $0.id == accountID && $0.state == "connected" }) { Text("Selected account unavailable").tag(accountID) }
                    }
                }
                if loading { ProgressView("Checking available destinations").controlSize(.small) }
                Picker(location == "Cloud" ? "Prepared session" : "Session on this Mac", selection: $sourceID) {
                    Text("Choose a session").tag("")
                    ForEach(sources) { source in Text(source.profileLabel + " · " + source.title).tag(source.id) }
                    if !sourceID.isEmpty, selected == nil { Text("Previous session unavailable here").tag(sourceID) }
                }.disabled(loading)
                if let source = selected {
                    Text(source.profileLabel).font(.system(size: 12, weight: .medium))
                    if let value = prepared.first(where: { $0.session.id == source.id }), location == "Cloud" {
                        Text([accounts.accounts.first { $0.id == value.accountId }?.label ?? request.provider, value.repositoryName].joined(separator: " · "))
                            .font(.system(size: 12)).foregroundStyle(campMuted)
                    }
                } else if !loading {
                    Text(location == "Cloud" ? "Choose a prepared workspace, or set up the selected session below" : "Choose a session from this Mac that is already saved in this Camp project")
                        .font(.system(size: 12)).foregroundStyle(campMuted)
                }
                if native != nil {
                    Button(location == "Cloud" ? "Set up Cloud for selected session" : "Review selected session's location") { preparingSession = native }
                        .disabled(loading || planning.pending(request.draftID))
                }
            }
            Text("This changes this planner's execution source. Its model settings and attached context stay the same. Selecting a destination does not start work.")
                .font(.system(size: 12)).foregroundStyle(campMuted)
            if !error.isEmpty { Label(error, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange) }
            HStack {
                Button("Cancel") { dismiss() }.disabled(applying)
                Spacer()
                Button("Use selection") { applyTask = Task { await apply() } }.buttonStyle(.borderedProminent)
                    .disabled(!validScope || selected == nil || loading || applying || planning.pending(request.draftID))
            }
        }.padding(24).frame(width: 520).background(campCream).foregroundStyle(campInk).tint(campGreen).disabled(applying)
            .interactiveDismissDisabled(applying)
            .onDisappear { applyTask?.cancel() }
            .task(id: queryKey) { await load() }
            .sheet(item: $preparingSession, onDismiss: { reload += 1 }) { session in
                RunLocationView(store: store, session: session, requestedLocation: location).campWindowBehavior()
            }
    }
    private func load() async {
        let token = UUID(); loadToken = token; error = ""; prepared = []
        guard validScope, location == "Cloud" else { loading = false; return }
        loading = true
        defer { if loadToken == token { loading = false } }
        do {
            await accounts.refresh()
            let values = try await cloud.planningSources(projectID: request.projectID, accountID: accountID.isEmpty ? nil : accountID)
            guard !Task.isCancelled, validScope, loadToken == token else { return }
            prepared = values
            if values.count == 100 && accountID.isEmpty { error = "Choose an account to narrow the available sessions" }
        } catch is CancellationError {} catch {
            if loadToken == token, validScope, !Task.isCancelled { self.error = error.localizedDescription }
        }
    }
    private func apply() async {
        guard !applying, validScope, let source = selected else { return }
        applying = true; defer { applying = false }
        do {
            if let value = prepared.first(where: { $0.session.id == source.id }), location == "Cloud" {
                try await cloud.installPlanningSources([value], scope: request.scope)
            }
            try Task.checkCancellation()
            guard validScope else { return }
            if planning.selectDestination(request.draftID, slot: request.slot, expected: request.original, provider: request.provider, source: source, scope: request.scope) { dismiss() }
            else { error = planning.error }
        } catch is CancellationError {} catch { self.error = error.localizedDescription }
    }
}
