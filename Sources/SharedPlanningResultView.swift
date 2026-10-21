import SwiftUI

struct SharedPlanningResultView: View {
    @ObservedObject var store: CampStore
    let result: CloudPlanningResult
    @State private var section = "Combined plan"
    var body: some View {
        HStack(spacing: 0) {
            RecentSessionsList(store: store).frame(width: 230)
            Divider()
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("Plan together", systemImage: "lightbulb").font(.headline)
                    Text("Version \(result.version.version)").foregroundStyle(campMuted)
                    Spacer()
                    Button { store.sharedPlanResult = nil; store.view = "Board" } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }
                        .buttonStyle(.plain).help("Back to board").accessibilityLabel("Back to board")
                }
                Text(result.version.brief).font(.system(size: 12)).textSelection(.enabled).lineLimit(4)
                Picker("View", selection: $section) {
                    ForEach(["Plans", "Reviews", "Combined plan"], id: \.self) { Text($0).tag($0) }
                }.pickerStyle(.segmented).frame(maxWidth: 400)
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
            }.padding(22).frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
    private func resultMessages(_ steps: [CloudPlanningResult.Step]) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(steps) { step in MessageView(message: Message(id: String(step.id), role: "assistant", text: step.output)) }
                    Color.clear.frame(height: 1).id("bottom")
                }.frame(maxWidth: .infinity, alignment: .leading).padding(14)
            }.onAppear { proxy.scrollTo("bottom", anchor: .bottom) }
                .onChange(of: section) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
        }
    }
}
