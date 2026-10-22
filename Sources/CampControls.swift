import SwiftUI
import AppKit

// App-owned sheets must allow the normal application delegate to save and quit.
// AppKit otherwise rejects termination before asking the delegate.
private struct CampWindowConfiguration: NSViewRepresentable {
    final class View: NSView {
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            configureWindow()
        }
        func configureWindow() {
            window?.preventsApplicationTerminationWhenModal = false
            window?.autorecalculatesKeyViewLoop = true
        }
    }
    func makeNSView(context: Context) -> View { View() }
    func updateNSView(_ view: View, context: Context) { view.configureWindow() }
}

extension View {
    func campWindowBehavior() -> some View {
        background(CampWindowConfiguration().frame(width: 0, height: 0).allowsHitTesting(false))
    }
}

struct CampSegmentedControl<Value: Hashable>: View {
    let options: [(value: Value, title: String)]
    @Binding var selection: Value
    @FocusState private var focused: Value?
    @Environment(\.isEnabled) private var isEnabled

    var body: some View {
        HStack(spacing: 0) {
            ForEach(Array(options.enumerated()), id: \.offset) { _, option in
                Button { selection = option.value } label: {
                    Text(option.title).font(.system(size: 12)).lineLimit(1)
                        .frame(maxWidth: .infinity).frame(minHeight: 26)
                        .background(selection == option.value ? Color(hex: 0xCDCCC6) : .clear)
                        .clipShape(RoundedRectangle(cornerRadius: 7))
                }.buttonStyle(.plain).focusable().focused($focused, equals: option.value)
                    .accessibilityLabel(option.title)
                    .accessibilityValue(selection == option.value ? "Selected" : "")
            }
        }.padding(1).background(Color(hex: 0xE4E3DE)).clipShape(RoundedRectangle(cornerRadius: 8))
            .accessibilityElement(children: .contain)
            .onMoveCommand { direction in
                guard isEnabled, let focused, let index = options.firstIndex(where: { $0.value == focused }) else { return }
                let next: Int
                switch direction { case .left: next = max(0, index - 1); case .right: next = min(options.count - 1, index + 1); default: return }
                selection = options[next].value; self.focused = options[next].value
            }
    }
}

struct PlanningParticipant: Identifiable {
    let id: Int
    let provider: String
    let status: String
}

struct PlanningNavigation: View {
    @ObservedObject var store: CampStore
    let title: String
    let mode: PlanningMode
    let participants: [PlanningParticipant]
    @Binding var section: String
    @Binding var focusedSlot: Int?
    var sections = ["Plans", "Combined plan", "Implementation", "History"]
    var canImplement = true

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            VStack(alignment: .leading, spacing: 8) {
                ProviderBadge(provider: "Planning")
                Text(title).font(.system(size: 12, weight: .medium)).lineLimit(2)
            }.padding(.horizontal, 14).padding(.top, 10).padding(.bottom, 7)
            Button {
                section = "Plans"; focusedSlot = nil
            } label: {
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "bubble.left.and.bubble.right").frame(width: 16)
                    VStack(alignment: .leading, spacing: 3) {
                        Text(mode == .single ? "Plan" : "Both chats").font(.system(size: 12, weight: .medium))
                        Text(mode == .single ? "One model" : "Independent plans and reviews").font(.system(size: 10)).foregroundStyle(campMuted)
                    }
                    Spacer(minLength: 0)
                }.padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .background((section == "Plans" || section == "Reviews") && focusedSlot == nil ? campGreen.opacity(0.10) : .clear)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
            }.buttonStyle(.plain).padding(.horizontal, 10)
            if mode == .comparison {
                VStack(spacing: 5) {
                    ForEach(participants) { participant in
                        Button { section = "Plans"; focusedSlot = participant.id } label: {
                            HStack(spacing: 8) {
                                ProviderMark(provider: participant.provider, size: 15)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(participant.provider).font(.system(size: 11, weight: .medium))
                                    Text(participant.status).font(.system(size: 10)).foregroundStyle(campMuted)
                                }
                                Spacer(minLength: 0)
                                Image(systemName: "chevron.right").font(.system(size: 8))
                            }.padding(8).frame(maxWidth: .infinity, alignment: .leading)
                                .background(ProviderMark.wash(participant.provider).opacity(focusedSlot == participant.id && section == "Plans" ? 1 : 0.55))
                                .clipShape(RoundedRectangle(cornerRadius: 7))
                        }.buttonStyle(.plain).accessibilityLabel("Open \(participant.provider) planner")
                    }
                }.padding(.leading, 24).padding(.trailing, 10)
            }
            ForEach(sections.filter { $0 != "Plans" && (mode == .comparison || $0 != "Combined plan") }, id: \.self) { value in
                Button { section = value; focusedSlot = nil } label: {
                    Label(value == "History" ? "Planning history" : value,
                          systemImage: value == "History" ? "clock.arrow.circlepath" : value == "Implementation" ? "play" : "lightbulb")
                        .font(.system(size: 11)).frame(maxWidth: .infinity, alignment: .leading).padding(8)
                        .background(section == value ? campGreen.opacity(0.10) : .clear).clipShape(RoundedRectangle(cornerRadius: 7))
                }.buttonStyle(.plain).padding(.horizontal, 10).disabled(value == "Implementation" && !canImplement)
            }
            Divider().padding(.horizontal, 14).padding(.top, 8)
            RecentSessionsList(store: store)
        }.frame(width: 230)
    }
}

struct PlanningStageBar: View {
    let mode: PlanningMode
    @Binding var section: String
    var completed: Set<String> = []
    var canImplement = true
    var showImplementation = true
    var body: some View {
        let values = (mode == .single ? ["Plans"] : ["Plans", "Reviews", "Combined plan"]) + (showImplementation ? ["Implementation"] : [])
        ScrollView(.horizontal) {
            HStack(spacing: 6) {
                ForEach(Array(values.enumerated()), id: \.element) { index, value in
                    Button { section = value } label: {
                        HStack(spacing: 7) {
                            if completed.contains(value) { Image(systemName: "checkmark").font(.system(size: 9)) }
                            else { Text("\(index + 1)").font(.system(size: 10)).foregroundStyle(campMuted) }
                            Text(mode == .single && value == "Plans" ? "Plan" : value).font(.system(size: 11))
                        }.padding(.horizontal, 10).frame(height: 30)
                            .background(section == value ? Color(hex: 0xE4E3DE) : .clear).clipShape(RoundedRectangle(cornerRadius: 6))
                    }.buttonStyle(.plain)
                        .accessibilityElement(children: .ignore)
                        .accessibilityAddTraits(.isButton)
                        .accessibilityLabel(mode == .single && value == "Plans" ? "Plan" : value)
                        .accessibilityValue(value == "Implementation" && !canImplement ? "Unavailable until the plan is ready" : section == value ? "Selected" : completed.contains(value) ? "Complete" : "Not complete")
                        .disabled(value == "Implementation" && !canImplement)
                }
            }
        }.fixedSize(horizontal: false, vertical: true)
    }
}

// A fixed content width makes narrow planning columns wrap their transcripts
// instead of allowing text to draw over the adjacent provider's conversation.
struct CampTranscriptViewport<Content: View>: View {
    let padding: CGFloat
    let followBottom: () -> Void
    let content: Content
    init(padding: CGFloat = 14, followBottom: @escaping () -> Void = {}, @ViewBuilder content: () -> Content) {
        self.padding = padding; self.followBottom = followBottom; self.content = content()
    }
    var body: some View {
        GeometryReader { viewport in
            ScrollView {
                content.frame(width: max(0, viewport.size.width - padding * 2), alignment: .leading).padding(padding)
            }.clipped()
                .task(id: [viewport.size.width, viewport.size.height]) {
                    await Task.yield()
                    guard !Task.isCancelled else { return }
                    followBottom()
                }
        }
    }
}
