import SwiftUI

struct ImportedImplementationList: View {
    @ObservedObject var history: ImportedImplementationHistory
    var open: (ImportedImplementationRecord) -> Void
    var body: some View {
        if !history.implementations.isEmpty || history.loading || !history.error.isEmpty {
            VStack(alignment: .leading, spacing: 10) {
                Text("Implementation history").font(.headline)
                ForEach(history.implementations) { record in
                    Button { open(record) } label: {
                        HStack(spacing: 8) {
                            ProviderMark(provider: record.provider, size: 17)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(record.model.isEmpty ? record.provider : record.model).font(.system(size: 12, weight: .medium))
                                Text("\(record.messageCount) messages · \(record.attemptCount) attempts · \(record.state.replacingOccurrences(of: "_", with: " "))")
                                    .font(.system(size: 10)).foregroundStyle(campMuted)
                            }
                            Spacer()
                            Image(systemName: "chevron.right").font(.system(size: 10)).foregroundStyle(campMuted)
                        }.padding(.vertical, 6).contentShape(Rectangle())
                    }.buttonStyle(.plain)
                }
                if history.implementationNext > 0 { Button("Load more implementations") { history.loadImplementations() }.disabled(history.loading) }
                if history.loading { ProgressView().controlSize(.small) }
                if !history.error.isEmpty { Text(history.error).font(.caption).foregroundStyle(campMuted) }
            }
        }
    }
}

struct ImportedImplementationView: View {
    @ObservedObject var history: ImportedImplementationHistory
    var back: () -> Void
    @State private var section = "Chat"
    var body: some View {
        if let record = history.selected {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 10) {
                    Button(action: back) { CampIconLabel(symbol: "chevron.left") }
                        .buttonStyle(.plain).help("Back to planning history").accessibilityLabel("Back to planning history")
                    ProviderMark(provider: record.provider, size: 18)
                    Text("Implementation · Version \(record.planVersion)").font(.system(size: 13, weight: .medium))
                    Spacer()
                    Text("Saved history").font(.system(size: 11)).foregroundStyle(campMuted)
                    if history.loading { ProgressView().controlSize(.small) }
                }
                ModelSettingsMenu(provider: record.provider, title: "Saved request settings", sessionModel: record.model,
                    modelLabel: record.model.isEmpty ? "Not recorded" : record.model, models: [], caps: ModelOption(id: record.model, label: record.model),
                    choice: ModelTuning(effort: record.effort, speed: record.speed), locked: true, expanded: true, selectModel: { _ in }, tune: { _, _ in })
                CampSegmentedControl(options: [("Chat", "Chat"), ("Attempts", "Attempts")], selection: $section)
                    .frame(width: 200).accessibilityLabel("Implementation history")
                if !history.error.isEmpty { Text(history.error).font(.caption).foregroundStyle(campMuted) }
                if section == "Chat" {
                    ScrollViewReader { proxy in
                        CampTranscriptViewport(followBottom: { proxy.scrollTo("implementation-bottom", anchor: .bottom) }) {
                            LazyVStack(alignment: .leading, spacing: 18) {
                                if history.hasEarlier { Button("Load earlier messages") { history.loadEarlier() }.disabled(history.loading) }
                                ForEach(history.messages) { MessageView(message: $0, provider: record.provider).id($0.id) }
                                if history.messages.isEmpty && !history.loading { Text("No saved messages").foregroundStyle(campMuted) }
                                Color.clear.frame(height: 1).id("implementation-bottom")
                            }
                        }.onAppear { proxy.scrollTo("implementation-bottom", anchor: .bottom) }
                            .onChange(of: history.selected?.id) { _, _ in proxy.scrollTo("implementation-bottom", anchor: .bottom) }
                            .onChange(of: history.messages.last?.id) { _, _ in proxy.scrollTo("implementation-bottom", anchor: .bottom) }
                    }
                } else {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 18) {
                            if let prompt = record.initialPrompt, !prompt.isEmpty {
                                DisclosureGroup("Initial implementation prompt") { MessageView(message: Message(id: "initial", role: "user", text: prompt), provider: record.provider) }
                            }
                            ForEach(history.attempts) { attempt in
                                VStack(alignment: .leading, spacing: 10) {
                                    Text("Attempt \(attempt.ordinal) · \(attempt.state)").font(.system(size: 12, weight: .medium))
                                    MessageView(message: Message(id: attempt.id, role: "user", text: attempt.prompt), provider: record.provider)
                                    Text([attempt.model, attempt.effort.isEmpty ? "" : ModelMenu.effortName(attempt.effort) + " effort",
                                          attempt.speed.isEmpty ? "" : ModelMenu.speedName(attempt.speed)].filter { !$0.isEmpty }.joined(separator: " · "))
                                        .font(.caption).foregroundStyle(campMuted)
                                    if !attempt.error.isEmpty { Text(attempt.error).font(.caption).foregroundStyle(campMuted).textSelection(.enabled) }
                                    if let usage = attempt.usage {
                                        Text([usage.inputTokens.map { "\($0) input tokens" }, usage.outputTokens.map { "\($0) output tokens" }].compactMap { $0 }.joined(separator: " · ")).font(.caption).foregroundStyle(campMuted)
                                    }
                                    if let permissions = attempt.permissions { Text(permissions.summary).font(.caption).foregroundStyle(campMuted) }
                                }.frame(maxWidth: .infinity, alignment: .leading)
                            }
                            if history.attemptNext > 0 { Button("Load more attempts") { history.loadAttempts() }.disabled(history.loading) }
                            if history.attempts.isEmpty && !history.loading { Text("No saved attempts").foregroundStyle(campMuted) }
                        }.padding(14)
                    }
                }
            }.onChange(of: section) { _, value in if value == "Attempts" { history.loadAttempts() } }
                .onChange(of: history.loading) { _, loading in if !loading && section == "Attempts" && history.attempts.isEmpty && record.attemptCount > 0 && history.error.isEmpty { history.loadAttempts() } }
        } else {
            VStack(alignment: .leading, spacing: 12) {
                ImportedImplementationList(history: history) { history.select($0) }
                if history.implementations.isEmpty && !history.loading && history.error.isEmpty {
                    Text("No saved implementations for this revision").foregroundStyle(campMuted)
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                .onAppear { openOnlyImplementation() }
                .onChange(of: history.loading) { _, loading in if !loading { openOnlyImplementation() } }
        }
    }
    private func openOnlyImplementation() {
        guard !history.loading, history.selected == nil, history.implementationNext == 0,
              history.implementations.count == 1, let record = history.implementations.first else { return }
        history.select(record)
    }
}
