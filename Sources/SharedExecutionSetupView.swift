import SwiftUI

struct SharedExecutionSetupView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var planning: CampPlanning
    @ObservedObject var review: SharedExecutionReview
    @ObservedObject var cloud: CampConnection
    let plan: CloudPlanningResult
    @State private var sessionID = ""
    @State private var model = ""
    @State private var tuning = ModelTuning()
    @State private var tuningByModel: [String: ModelTuning] = [:]
    @State private var permissions = PermissionSettings()
    private struct ExecutorDraft {
        var model: String
        var tuning: ModelTuning
        var tuningByModel: [String: ModelTuning]
        var permissions: PermissionSettings
    }
    @State private var executorDrafts: [String: ExecutorDraft] = [:]
    @State private var catalog: PermissionCatalog?
    @State private var busy = false
    @State private var error = ""
    init(store: CampStore, review: SharedExecutionReview, plan: CloudPlanningResult) {
        self.store = store; self.review = review; self.plan = plan; self.planning = store.sharedPlanning; self.cloud = store.cloud
    }
    private var choices: [CloudSession] {
        cloud.sessions.filter { $0.campId == plan.campId && $0.projectId == plan.projectId && !$0.isHistoryOnly && !$0.externalOwner &&
            ($0.ownerUserId == cloud.scope?.userId || $0.allowRequests) && CloudPlanningExecution(session: $0) != nil }
    }
    private var source: CloudSession? { choices.first { $0.id == sessionID } }
    private var targetKey: String { "\(cloud.scope?.key ?? ""):\(plan.id):\(plan.version.version):\(plan.revision ?? 0):\(sessionID):\(source?.revision ?? 0):\(source?.executionEpoch ?? 0):\(source?.permissionRevision ?? 0)" }
    private var pending: Bool { busy || planning.pending(plan.id) || review.loading }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Implementation").font(.system(size: 13, weight: .medium))
                if let status = review.status {
                    if let approval = review.approval { approvalPanel(approval) }
                    else {
                        Text(status.reviewRequired ? "This plan, executor or its permissions changed. Withdraw this review before choosing again" : "Loading the implementation review")
                            .font(.system(size: 12)).foregroundStyle(campMuted)
                    }
                    HStack {
                        Button("Withdraw review", role: .destructive) { perform { try await review.cancel() } }
                        Spacer()
                        if let value = review.approval {
                            if value.state == "approved" {
                                Button("Start implementation") { perform { try await review.dispatch() } }.buttonStyle(.borderedProminent).tint(campGreen)
                            } else if value.ownerUserId == cloud.scope?.userId {
                                Button("Approve") { perform { try await review.approve(inspect: inspect) } }.buttonStyle(.borderedProminent).tint(campGreen)
                                    .disabled(value.execution.location == "local" && value.ownerDeviceId != cloud.credential?.actor.deviceId)
                            } else { Text("Waiting for the executor's owner").font(.caption).foregroundStyle(campMuted) }
                        }
                    }.disabled(pending)
                } else if plan.creatorUserId == cloud.scope?.userId {
                    Text("Choose who implements the approved plan").font(.system(size: 11)).foregroundStyle(campMuted)
                    Picker("Session", selection: $sessionID) {
                        Text("Choose an executor").tag("")
                        ForEach(choices) { value in Text("\(value.provider) · \(ownerName(value.ownerUserId)) · \(value.profileLabel) · \(value.title)").tag(value.id) }
                    }.disabled(pending)
                    if let source {
                        settingsPanel(source)
                        DisclosureGroup("Permissions · \(permissions.summary)") {
                            PermissionFields(provider: source.provider, catalog: catalog ?? .requested(source.provider, inherited: "The owner reviews these permissions"), selection: $permissions)
                        }.font(.system(size: 12)).disabled(pending || review.target?.location == "cloud")
                        if let target = review.target {
                            HStack {
                                Label(target.location == "cloud" ? "Cloud" : source.ownerDeviceId == cloud.credential?.actor.deviceId ? "This Mac" : "Owner’s Mac",
                                    systemImage: target.location == "cloud" ? "cloud" : "desktopcomputer").font(.system(size: 11)).foregroundStyle(campMuted)
                                Spacer()
                                if source.ownerUserId == cloud.scope?.userId, let native = store.sharing.native(for: "camp:" + source.id) ?? store.sessions.first(where: { $0.id == "camp:" + source.id }) {
                                    Button("Run on") { store.executionSheet = native }.disabled(pending)
                                }
                            }
                            if target.location == "cloud" && target.mode != "implementation" {
                                Text("The owner needs to prepare an implementation workspace in Run on").font(.caption).foregroundStyle(campMuted)
                            }
                            if !target.available { Text("This executor is not currently available").font(.caption).foregroundStyle(campMuted) }
                        } else if review.targetError.isEmpty { ProgressView().controlSize(.small) }
                        if !review.targetError.isEmpty { Text(review.targetError).font(.caption).foregroundStyle(.orange) }
                        Button("Review implementation") {
                            perform { try await review.prepare(source: source, model: model, tuning: tuning, permissions: permissions) }
                        }.buttonStyle(.borderedProminent).tint(campGreen)
                            .disabled(pending || model.isEmpty || review.target?.available != true || !permissions.valid(for: source.provider) || (review.target?.location == "cloud" && review.target?.mode != "implementation"))
                    }
                    if choices.isEmpty { Text("Share an executor session in this project to continue").font(.caption).foregroundStyle(campMuted) }
                } else { Text("The plan's creator chooses an executor, then its owner approves").font(.system(size: 12)).foregroundStyle(campMuted) }
                if pending { ProgressView().controlSize(.small) }
                if planning.pending(plan.id) { Text("Waiting to sync · The same request is retained across reconnects").font(.caption).foregroundStyle(campMuted) }
                ForEach(Array(Set([error, review.error, planning.error].filter { !$0.isEmpty })).sorted(), id: \.self) { Text($0).font(.caption).foregroundStyle(.orange).textSelection(.enabled) }
            }.padding(14).frame(maxWidth: .infinity, alignment: .leading).background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
        }.task(id: targetKey) {
            catalog = nil
            await review.loadTarget(source)
            if let target = review.target, target.location == "cloud", let value = PermissionSettings(wireValues: target.permissions) { permissions = value }
            if let source, source.ownerUserId == cloud.scope?.userId, source.ownerDeviceId == cloud.credential?.actor.deviceId,
               let native = store.sharing.native(for: "camp:" + source.id) {
                do { let value = try await store.permissionCatalog(native); if !Task.isCancelled { catalog = value } }
                catch { if !Task.isCancelled { self.error = error.localizedDescription } }
            }
        }.onChange(of: sessionID) { old, _ in
            if !old.isEmpty {
                executorDrafts[old] = ExecutorDraft(model: model, tuning: tuning, tuningByModel: tuningByModel, permissions: permissions)
            }
            error = ""
            if let saved = executorDrafts[sessionID] {
                model = saved.model; tuning = saved.tuning; tuningByModel = saved.tuningByModel; permissions = saved.permissions
            } else {
                tuning = ModelTuning(); tuningByModel = [:]
                model = source.map { $0.model.isEmpty ? models($0).first?.id ?? "" : $0.model } ?? ""
                permissions = source?.provider == "Claude" ? PermissionSettings(claudeMode: "manual") : PermissionSettings(sandbox: "workspace-write", approval: "on-request", reviewer: "user")
            }
            executorDrafts = executorDrafts.filter { key, _ in choices.contains { $0.id == key } }
        }
    }
    private func settingsPanel(_ source: CloudSession) -> some View {
        let models = models(source)
        return ModelSettingsMenu(provider: source.provider, title: "Implementation model", sessionModel: source.model, modelLabel: model,
            models: models, caps: models.first { $0.id == model } ?? ModelOption(id: model, label: model), choice: tuning, locked: pending, expanded: true,
            selectModel: { next in tuningByModel[model] = tuning; model = next.isEmpty ? source.model : next; tuning = tuningByModel[model] ?? ModelTuning() },
            tune: { effort, speed in if let effort { tuning.effort = effort }; if let speed { tuning.speed = speed } })
    }
    private func models(_ source: CloudSession) -> [ModelOption] {
        (store.sharing.native(for: "camp:" + source.id) ?? store.sessions.first { $0.id == "camp:" + source.id }).map(store.availableModels) ?? []
    }
    private func ownerName(_ id: String) -> String { id == cloud.scope?.userId ? "You" : cloud.members.first { $0.userId == id }?.name ?? "Teammate" }
    private func approvalPanel(_ value: SharedExecutionApproval) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack { ProviderMark(provider: value.provider, size: 18); Text(value.state == "approved" ? "Approved · Ready to implement" : "Review implementation").font(.headline) }
            ModelSettingsMenu(provider: value.provider, title: "Implementation model", sessionModel: value.model, modelLabel: value.model,
                models: [], caps: ModelOption(id: value.model, label: value.model), choice: ModelTuning(effort: value.effort, speed: value.speed), locked: true, expanded: true, selectModel: { _ in }, tune: { _, _ in })
            PlanningLocationLabel(execution: value.execution)
            Text(value.settings?.summary ?? "Permissions unavailable").font(.caption).foregroundStyle(campMuted)
            Text("Plan version \(value.planVersion) · \(ownerName(value.ownerUserId)) · \(cloud.sessions.first { $0.id == value.sessionId }?.profileLabel ?? value.provider)").font(.caption).foregroundStyle(campMuted)
            DisclosureGroup("Approved plan and instructions") { MessageView(message: Message(id: value.id, role: "user", text: value.prompt), provider: value.provider) }
            if value.execution.location == "local", value.ownerUserId == cloud.scope?.userId, value.ownerDeviceId != cloud.credential?.actor.deviceId {
                Text("Open this Camp on the assigned Mac to approve its actual CLI settings").font(.caption).foregroundStyle(campMuted)
            }
        }
    }
    private func inspect(_ value: SharedExecutionApproval) async throws -> String {
        guard let remote = cloud.sessions.first(where: { $0.id == value.sessionId }), remote.profileId == value.profileId,
              remote.ownerDeviceId == value.ownerDeviceId, remote.permissionRevision == value.permissionRevision,
              let local = store.sharing.native(for: "camp:" + remote.id), local.sessionID == remote.localSessionId,
              local.provider == value.provider, let settings = value.settings else { throw CampError.message("Import this executor profile on its owning Mac before approval") }
        let context = try await store.inspectExecution(local, permissions: settings)
        guard context.provider == local.provider,
              URL(fileURLWithPath: context.profile).resolvingSymlinksInPath() == URL(fileURLWithPath: local.root).resolvingSymlinksInPath(),
              URL(fileURLWithPath: context.cwd).resolvingSymlinksInPath() == URL(fileURLWithPath: local.cwd).resolvingSymlinksInPath() else { throw CampError.message("The executor project or profile changed during review") }
        return context.fingerprint
    }
    private func perform(_ operation: @escaping () async throws -> Void) {
        busy = true; error = ""
        Task { do { try await operation() } catch { self.error = error.localizedDescription }; busy = false }
    }
}
