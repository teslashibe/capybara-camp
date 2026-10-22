import SwiftUI

struct ExecutionLocationPicker: View {
    @Binding var selection: String
    var body: some View {
        CampSegmentedControl(options: [("Local", "Local"), ("Cloud", "Cloud")], selection: $selection,
            icons: ["Local": "desktopcomputer", "Cloud": "cloud"])
            .frame(width: 150).accessibilityLabel("Execution location")
    }
}

struct ExecutionDefaultsForm: View {
    @ObservedObject var store: CampStore
    @ObservedObject var cloud: CampConnection
    @ObservedObject var accounts: CampProviderAccounts
    @Binding var preferences: ExecutionPreferences
    @State private var showAccounts = false
    @State private var showCamp = false
    init(store: CampStore, preferences: Binding<ExecutionPreferences>) {
        self.store = store; cloud = store.cloud; accounts = store.cloud.providerAccounts; _preferences = preferences
    }
    private func profile(_ provider: String) -> Binding<String> {
        Binding(get: { preferences.localProfiles[provider] ?? "" }, set: { preferences.localProfiles[provider] = $0 })
    }
    private func account(_ provider: String) -> Binding<String> {
        Binding(get: { preferences.cloudAccounts[provider] ?? "" }, set: { preferences.cloudAccounts[provider] = $0 })
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("New plans").font(.system(size: 16, weight: .semibold))
            Text("Choose where new plans run. Existing tickets keep their saved settings.").font(.system(size: 12)).foregroundStyle(campMuted)
            ExecutionLocationPicker(selection: $preferences.location)
            Picker("Single-plan provider", selection: $preferences.provider) {
                Text("Automatic").tag("")
                Text("Codex").tag("Codex")
                Text("Claude").tag("Claude")
            }
            if preferences.location == "Local" {
                ForEach(["Codex", "Claude"], id: \.self) { provider in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack { ProviderMark(provider: provider, size: 17); Text(provider).fontWeight(.medium) }
                        Picker("Profile", selection: profile(provider)) {
                            Text("First available profile").tag("")
                            ForEach(store.sources.filter { $0.provider == provider }) { Text($0.label).tag($0.id) }
                            if let id = preferences.localProfiles[provider], !id.isEmpty, !store.sources.contains(where: { $0.id == id }) { Text("Saved profile unavailable").tag(id) }
                        }
                    }
                }
                Text("A saved profile uses its current CLI login. If it becomes unavailable, Camp asks you to choose again.").font(.system(size: 12)).foregroundStyle(campMuted)
            } else if cloud.scope == nil {
                Text("Connect to a Camp to choose cloud accounts and a project").font(.system(size: 12))
                Button("Connect a Camp") { showCamp = true }
            } else {
                Picker("Default cloud project", selection: $preferences.projectID) {
                    Text("Choose a project").tag("")
                    ForEach(cloud.projects.filter { !$0.isArchived }) { Text($0.name).tag($0.id) }
                    if !preferences.projectID.isEmpty, !cloud.projects.contains(where: { $0.id == preferences.projectID }) { Text("Saved project unavailable").tag(preferences.projectID) }
                }
                ForEach(["Codex", "Claude"], id: \.self) { provider in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack { ProviderMark(provider: provider, size: 17); Text(provider).fontWeight(.medium) }
                        Picker("Account", selection: account(provider)) {
                            Text("Choose an account").tag("")
                            ForEach(accounts.accounts.filter { $0.provider == provider }) { Text($0.label + " · " + $0.statusLabel).tag($0.id) }
                            if let id = preferences.cloudAccounts[provider], !id.isEmpty, !accounts.accounts.contains(where: { $0.id == id }) { Text("Saved account unavailable").tag(id) }
                        }
                    }
                }
                Button("Manage cloud accounts") { showAccounts = true }
                Text("Prepare a session in the selected project with Run on. New plans use a prepared workspace on the selected account and show it for review before starting. An unavailable account never falls back to Local.")
                    .font(.system(size: 12)).foregroundStyle(campMuted)
                if !accounts.error.isEmpty { Text(accounts.error).font(.system(size: 12)).foregroundStyle(.orange) }
            }
            Text("Saved for this Mac and the current Camp login").font(.system(size: 11)).foregroundStyle(campMuted)
        }
        .task(id: cloud.scope?.key) { if cloud.scope != nil { await accounts.refresh() } }
        .sheet(isPresented: $showAccounts) {
            VStack(alignment: .leading) {
                ScrollView { CampProviderAccountsView(cloud: cloud) }
                HStack { Spacer(); Button("Done") { showAccounts = false } }
            }.padding(24).frame(width: 600, height: 560).background(campCream).campWindowBehavior()
        }
        .sheet(isPresented: $showCamp) { CampConnectionView(cloud: cloud).campWindowBehavior() }
    }
}

struct ExecutionDefaultsEditor: View {
    @ObservedObject var store: CampStore
    @ObservedObject var defaults: ExecutionDefaults
    @ObservedObject var cloud: CampConnection
    @State private var preferences = ExecutionPreferences()
    init(store: CampStore) { self.store = store; defaults = store.executionDefaults; cloud = store.cloud }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            if !store.executionDefaultsMessage.isEmpty {
                Label(store.executionDefaultsMessage, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange)
            }
            if !defaults.error.isEmpty { Text(defaults.error).font(.system(size: 12)).foregroundStyle(.orange) }
            ExecutionDefaultsForm(store: store, preferences: $preferences)
            HStack {
                if defaults.readable && defaults.value(for: cloud.scope) == preferences { Text("Saved").font(.system(size: 12)).foregroundStyle(campMuted) }
                Spacer()
                Button("Save defaults") { if defaults.save(preferences, for: cloud.scope) { store.executionDefaultsMessage = "" } }
                    .disabled(!preferences.configured || preferences.location == "Cloud" && cloud.scope == nil)
            }
        }.task(id: cloud.scope?.key) { preferences = defaults.value(for: cloud.scope) }
    }
}
