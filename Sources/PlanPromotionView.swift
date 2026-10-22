import SwiftUI

struct PlanPromotionView: View {
    @ObservedObject var store: CampStore
    @ObservedObject private var promotion: CampPlanPromotion
    @ObservedObject private var cloud: CampConnection
    let localID: String
    @Environment(\.dismiss) private var dismiss
    @State private var projectID = ""
    @State private var saving = false
    @State private var error = ""
    init(store: CampStore, localID: String) {
        self.store = store; self.localID = localID; promotion = store.planPromotion; cloud = store.cloud
    }
    private var plan: LocalPlan? { store.planning.ticket(localID) ?? (promotion.job?.plan.id == localID ? promotion.job?.plan : nil) }
    private var active: PlanPromotionJob? { promotion.job?.plan.id == localID ? promotion.job : nil }
    private var linked: String? { promotion.linkedPlan(localID) }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack {
                Text("Save to Camp").font(.title2)
                Spacer()
                Button { dismiss() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                    .buttonStyle(.plain).keyboardShortcut(.cancelAction).help("Close").accessibilityLabel("Close import review")
            }
            if let plan {
                Text(plan.title).font(.system(size: 14, weight: .medium)).fixedSize(horizontal: false, vertical: true)
                Text("Save the complete planning history privately. The original ticket stays on this Mac, and saving does not start an agent.")
                    .font(.system(size: 12)).foregroundStyle(campMuted).fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 18) {
                    Label("\(plan.versions.count) revisions", systemImage: "clock.arrow.circlepath")
                    Label("\(plan.implementations?.count ?? 0) implementation chats", systemImage: "bubble.left.and.bubble.right")
                }.font(.system(size: 11)).foregroundStyle(campMuted)
                if linked != nil {
                    Label(promotion.replaces(plan) ? "Saved to Camp" : "An earlier copy is saved to Camp", systemImage: "checkmark.circle").foregroundStyle(campGreen)
                } else if !cloud.signedIn || cloud.scope == nil {
                    Text("Connect to a Camp from the app header first").font(.system(size: 12))
                } else if cloud.projects.filter({ !$0.isArchived }).isEmpty {
                    Text("Add a project in Camp settings first").font(.system(size: 12))
                } else if let other = promotion.job, other.plan.id != localID {
                    Text("Finish saving “\(other.plan.title)” before starting another import").font(.system(size: 12))
                    Button("View progress") { store.planPromotionSheet = other.plan.session }
                } else {
                    Picker("Project", selection: $projectID) {
                        ForEach(cloud.projects.filter { !$0.isArchived || $0.id == active?.projectId }) { Text($0.name).tag($0.id) }
                    }.disabled(active != nil || saving)
                    Label("Private to you", systemImage: "lock").font(.system(size: 11)).foregroundStyle(campMuted)
                    if let active {
                        HStack(spacing: 9) {
                            if promotion.working { ProgressView().controlSize(.small) }
                            Text(active.phase == "importing" ? "Saving the plan" : promotion.total == 0 ? "Preparing history" : "Copying history · \(promotion.copied) of \(promotion.total) sources")
                                .font(.system(size: 12))
                        }
                        Text("You can close this window while the import continues").font(.system(size: 11)).foregroundStyle(campMuted)
                    }
                }
                if !error.isEmpty || !promotion.error.isEmpty {
                    Text(error.isEmpty ? promotion.error : error).font(.system(size: 12)).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                }
                HStack {
                    if active != nil && !promotion.working {
                        Button("Start over", systemImage: "arrow.counterclockwise") {
                            Task { do { try await promotion.discard() } catch { self.error = error.localizedDescription } }
                        }.disabled(promotion.upload.journal?.approved == true)
                    }
                    Spacer()
                    if let linked {
                        Button("Open saved plan") { dismiss(); store.openSharedPlan(linked) }.buttonStyle(.borderedProminent)
                    } else if active != nil {
                        Button("Retry", systemImage: "arrow.clockwise") { error = ""; promotion.retry() }
                            .buttonStyle(.borderedProminent).disabled(promotion.working)
                    } else {
                        Button("Save privately") { save(plan) }.buttonStyle(.borderedProminent)
                            .disabled(saving || !promotion.ready || projectID.isEmpty || promotion.job != nil || cloud.scope == nil)
                            .keyboardShortcut(.defaultAction)
                    }
                }.tint(campGreen)
            } else {
                Text("This local ticket is no longer available").font(.system(size: 12))
            }
        }.padding(26).frame(width: 540).background(campCream)
            .onAppear { projectID = active?.projectId ?? cloud.projects.first(where: { !$0.isArchived })?.id ?? "" }
            .onChange(of: cloud.projects) { _, values in
                if active == nil && !values.contains(where: { $0.id == projectID && !$0.isArchived }) {
                    projectID = values.first(where: { !$0.isArchived })?.id ?? ""
                }
            }
    }
    private func save(_ plan: LocalPlan) {
        saving = true; error = ""
        Task {
            defer { saving = false }
            do { try await promotion.approve(plan, projectID: projectID) }
            catch { self.error = error.localizedDescription }
        }
    }
}
