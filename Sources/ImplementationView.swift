import SwiftUI

struct ImplementationSetupView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: LocalPlanning
    let id: String
    @State private var busy = false
    @State private var reviewedUnknown = false
    @State private var choices: [CampSession] = []
    var plan: LocalPlan? { planning.ticket(id) }
    var executor: LocalPlanner? { plan.flatMap { $0.executorChoices?[$0.selectedExecutor ?? ""] } }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Implementation").font(.headline)
                Text("Choose who implements the approved plan").font(.system(size: 12)).foregroundStyle(campMuted)
                if let plan, let executor {
                    Picker("Account", selection: Binding(get: { plan.selectedExecutor ?? "" }, set: { id in
                        if let source = choices.first(where: { $0.id == id }) { planning.configureExecutor(self.id, source: source, model: store.effectiveModel(source), tuning: store.tuning(source)) }
                    })) {
                        ForEach(choices) { source in Text("\(source.provider) · \(source.sourceLabel)").tag(source.id) }
                    }.disabled(busy || plan.implementation?.active == true)
                    let models = store.availableModels(executor.session)
                    ModelSettingsMenu(provider: executor.session.provider, title: "Implementation", sessionModel: executor.session.model,
                        modelLabel: executor.model.isEmpty ? "Session default" : executor.model, models: models,
                        caps: models.first { $0.id == executor.model } ?? ModelOption(id: executor.model, label: executor.model),
                        choice: executor.tuning, locked: busy || plan.implementation?.active == true, expanded: true, context: "Model for implementation",
                        selectModel: { model in planning.editExecutor(id) { p in p.tuningByModel[p.model] = p.tuning; p.model = model.isEmpty ? p.session.model : model; p.tuning = p.tuningByModel[p.model] ?? ModelTuning() } },
                        tune: { effort, speed in planning.editExecutor(id) { p in if let effort { p.tuning.effort = effort }; if let speed { p.tuning.speed = speed } } })
                    Label("This Mac · \(executor.session.cwd)", systemImage: "desktopcomputer").font(.system(size: 11)).foregroundStyle(campMuted).textSelection(.enabled)
                    ImplementationPermissions(store: store, planning: planning, id: id, executor: executor)
                        .disabled(busy || plan.implementation?.active == true)
                    if plan.implementation?.state == "needs_review" {
                        Text("The previous implementation has an unconfirmed outcome. A new implementation starts another provider session and may repeat work.").font(.system(size: 12)).foregroundStyle(.orange)
                        Toggle("I reviewed the previous implementation", isOn: $reviewedUnknown).toggleStyle(.checkbox)
                    }
                    if plan.implementation?.active == true { Text("Stop the running implementation before choosing another executor").font(.caption).foregroundStyle(campMuted) }
                    HStack {
                        if busy { ProgressView().controlSize(.small) }
                        Button("Approve and implement") {
                            busy = true
                            Task { await store.approveAndImplement(id, expectedVersion: plan.current.number, executor: executor, reviewedUnknown: reviewedUnknown); busy = false }
                        }.buttonStyle(.borderedProminent).tint(campGreen)
                            .disabled(busy || plan.current.state != "ready" || plan.implementation?.active == true || (plan.implementation?.state == "needs_review" && !reviewedUnknown))
                    }
                } else { Text("Connect a local profile before choosing an executor").font(.system(size: 12)).foregroundStyle(campMuted) }
            }.padding(22).frame(maxWidth: .infinity, alignment: .leading)
        }.task(id: id) {
            choices = store.planningProfiles("Codex") + store.planningProfiles("Claude")
            if executor == nil, let source = choices.first { planning.configureExecutor(id, source: source, model: store.effectiveModel(source), tuning: store.tuning(source)) }
        }
    }
}

struct ImplementationStatusView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: LocalPlanning
    let id: String
    @State private var reviewed = false
    var body: some View {
        if let execution = planning.ticket(id)?.implementation {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Label("Plan approved · Version \(execution.planVersion)", systemImage: "checkmark")
                    Text(execution.state == "complete" ? "Ready to review" : execution.state.replacingOccurrences(of: "_", with: " ").capitalized)
                    Spacer()
                    Button("View plan and history") { store.reviewingPlan = id }
                }
                if execution.attempts.isEmpty { Button("Start implementation") { store.startApprovedImplementation(id) } }
                if execution.state == "needs_review" {
                    Text("Check the provider session before continuing. The last turn may already have changed files.").foregroundStyle(campMuted)
                    Toggle("I reviewed the previous turn", isOn: $reviewed).toggleStyle(.checkbox)
                    if execution.providerSessionCreated {
                        Button("Continue this session") { _ = planning.reconcileImplementation(id, reviewed: reviewed); reviewed = false }.disabled(!reviewed)
                    } else { Text("Choose a new implementation from the plan after reviewing its history").foregroundStyle(campMuted) }
                }
                if let error = execution.attempts.last?.error, !error.isEmpty {
                    DisclosureGroup("Last result") { Text(error).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }
                }
            }.font(.system(size: 11)).padding(12).background(.white.opacity(0.7)).clipShape(RoundedRectangle(cornerRadius: 8)).padding(.horizontal, 22).padding(.vertical, 8)
        }
    }
}
