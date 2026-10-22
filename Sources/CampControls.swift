import SwiftUI
import AppKit

// All task types use the reference's six fixed slots. Long titles and a second
// planning model must not move the status or actions into a neighboring card.
struct CampTaskCard<Heading: View, Models: View, Status: View, Actions: View>: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    let heading: Heading
    let models: Models
    let status: Status
    let actions: Actions
    init(store: CampStore, session: CampSession, @ViewBuilder heading: () -> Heading,
         @ViewBuilder models: () -> Models, @ViewBuilder status: () -> Status,
         @ViewBuilder actions: () -> Actions) {
        self.store = store; self.session = session
        self.heading = heading(); self.models = models(); self.status = status(); self.actions = actions()
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            heading.frame(height: 28)
            Button { store.open(session) } label: {
                VStack(alignment: .leading, spacing: 5) {
                    Text(session.title).font(.system(size: 13, weight: .medium)).lineLimit(2)
                        .multilineTextAlignment(.leading).frame(maxWidth: .infinity, alignment: .topLeading).frame(height: 32, alignment: .top)
                    Label(store.projectSummary(session), systemImage: "folder").font(.system(size: 10)).foregroundStyle(campMuted)
                        .lineLimit(1).frame(height: 13)
                    HStack { Text(session.sourceLabel).lineLimit(1); Spacer(minLength: 4)
                        Text(Date(timeIntervalSince1970: session.updated), style: .relative).lineLimit(1)
                    }.font(.system(size: 9)).foregroundStyle(campMuted).frame(height: 12)
                }.frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
            }.buttonStyle(.plain).frame(height: 67)
            Rectangle().fill(Color(hex: 0xECECE7)).frame(height: 1)
            models.frame(maxWidth: .infinity, alignment: .leading).frame(height: 52)
            status.font(.system(size: 10)).foregroundStyle(campMuted).frame(maxWidth: .infinity, alignment: .leading).frame(height: 28)
            actions.font(.system(size: 10)).foregroundStyle(campMuted).frame(maxWidth: .infinity).frame(height: 28)
                .overlay(alignment: .top) { Rectangle().fill(Color(hex: 0xF0F0EC)).frame(height: 1) }
        }.padding(14).frame(maxWidth: .infinity).frame(height: 264, alignment: .topLeading)
            .background(.white).clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color(hex: 0xDEDFD8)))
    }
}

struct CampCardProvider: View {
    let provider: String
    var body: some View {
        HStack(spacing: 5) {
            ProviderMark(provider: provider, size: 13, muted: true)
            Text(provider).font(.system(size: 10, weight: .medium)).foregroundStyle(campMuted)
        }
    }
}

struct CampCardOpenAction: View {
    let title: String
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) { Text(title); Image(systemName: "chevron.right").font(.system(size: 8)) }
                .frame(minHeight: 28).contentShape(Rectangle())
        }.buttonStyle(.plain)
    }
}

struct PlanningCardModelRow<Content: View>: View {
    let provider: String
    let open: () -> Void
    @ViewBuilder let content: () -> Content
    var body: some View {
        HStack(spacing: 8) {
            Button(action: open) { CampCardProvider(provider: provider).frame(width: 62, alignment: .leading).frame(height: 26).contentShape(Rectangle()) }
                .buttonStyle(.plain).accessibilityLabel("Open \(provider) planner")
            content().frame(maxWidth: .infinity, alignment: .leading)
        }.frame(height: 26)
    }
}

struct PlanNavigationRequest: Equatable {
    var id: String
    var section: String
    var slot: Int?
    var nonce = UUID()
}

extension CampStore {
    func openPlan(_ session: CampSession, section: String = "Plans", slot: Int? = nil) {
        open(session)
        reviewingPlan = session.id
        planNavigation = PlanNavigationRequest(id: session.id, section: section, slot: slot)
    }
}

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
    var icons: [Value: String] = [:]
    @FocusState private var focused: Value?
    @Environment(\.isEnabled) private var isEnabled

    var body: some View {
        HStack(spacing: 0) {
            ForEach(Array(options.enumerated()), id: \.offset) { _, option in
                Button { selection = option.value } label: {
                    HStack(spacing: 4) {
                        if let icon = icons[option.value] { Image(systemName: icon).font(.system(size: 11)).accessibilityHidden(true) }
                        Text(option.title)
                    }.font(.system(size: 12)).lineLimit(1)
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

struct PlanningPaneRow<Content: View>: View {
    let slots: [Int]
    var spacing: CGFloat = 12
    @ViewBuilder let content: (Int) -> Content
    var body: some View {
        GeometryReader { bounds in
            let width = max(0, (bounds.size.width - spacing * CGFloat(max(0, slots.count - 1))) / CGFloat(max(1, slots.count)))
            HStack(alignment: .top, spacing: spacing) {
                ForEach(slots, id: \.self) { slot in
                    content(slot).frame(width: width, height: bounds.size.height)
                }
            }
        }
    }
}
