import SwiftUI

struct ProviderQuestion: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var header: String
    var prompt: String
    var options: [String]
    var multiple: Bool
    var secret: Bool
    var allowText: Bool
}

struct ProviderApprovalDecision: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var label: String
    var allows: Bool
}

struct ProviderApproval: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var kind: String
    var title: String
    var detail: String
    var questions: [ProviderQuestion]
    var canApprove: Bool
    var acceptLabel: String
    var decisions: [ProviderApprovalDecision]? = nil

    var valid: Bool {
        UUID(uuidString: id) != nil && ["command", "file", "permissions", "tool", "question"].contains(kind) &&
        detail.utf8.count <= 65536 && title.utf8.count <= 512 && questions.count <= 8 &&
        Set(questions.map(\.id)).count == questions.count && questions.allSatisfy { $0.options.count <= 16 && $0.prompt.utf8.count <= 8192 } &&
        (decisions ?? []).count <= 16 && Set((decisions ?? []).map(\.id)).count == (decisions ?? []).count &&
        (decisions ?? []).allSatisfy { !$0.id.isEmpty && $0.id.utf8.count <= 200 && !$0.label.isEmpty && $0.label.utf8.count <= 256 }
    }
}

struct ConversationApprovalsView: View {
    @ObservedObject var conversation: LocalConversation
    var body: some View {
        if let approval = conversation.approvals.first {
            VStack(alignment: .leading, spacing: 6) {
                if conversation.approvals.count > 1 {
                    Text("\(conversation.approvals.count) requests awaiting review").font(.caption).foregroundStyle(campMuted)
                }
                ProviderApprovalView(conversation: conversation, approval: approval).id(approval.id)
            }
        }
    }
}

struct ProviderApprovalView: View {
    @ObservedObject var conversation: LocalConversation
    let approval: ProviderApproval
    @State private var selections: [String: Set<String>] = [:]
    @State private var text: [String: String] = [:]
    var answers: [String: [String]] {
        Dictionary(uniqueKeysWithValues: approval.questions.map { question in
            let typed = text[question.id, default: ""].trimmingCharacters(in: .whitespacesAndNewlines)
            return (question.id, typed.isEmpty ? question.options.filter { selections[question.id, default: []].contains($0) } : [typed])
        })
    }
    var sending: Bool { conversation.answering.contains(approval.id) }
    var ready: Bool { approval.questions.allSatisfy { answers[$0.id]?.isEmpty == false } }
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(approval.title, systemImage: "hand.raised").font(.system(size: 12, weight: .semibold))
            ScrollView {
              if approval.kind == "question" {
                ForEach(approval.questions) { question in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(question.prompt).font(.system(size: 12)).fixedSize(horizontal: false, vertical: true)
                        ForEach(question.options, id: \.self) { option in
                            Toggle(option, isOn: Binding(get: { selections[question.id, default: []].contains(option) }, set: { selected in
                                if !question.multiple { selections[question.id] = selected ? [option] : [] }
                                else if selected { selections[question.id, default: []].insert(option) }
                                else { selections[question.id]?.remove(option) }
                                text[question.id] = ""
                            })).toggleStyle(.checkbox).font(.system(size: 11))
                        }
                        if question.allowText || question.options.isEmpty {
                            if question.secret { SecureField("Your answer", text: Binding(get: { text[question.id, default: ""] }, set: { text[question.id] = $0 })) }
                            else { TextField("Your answer", text: Binding(get: { text[question.id, default: ""] }, set: { text[question.id] = $0 })) }
                        }
                    }
                }
              } else {
                Text(approval.detail).font(.system(size: 11, design: .monospaced)).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
              }
            }.frame(minHeight: 60, idealHeight: 90, maxHeight: 120)
            HStack {
                if !approval.canApprove { Text("The full action is unavailable for review").font(.caption).foregroundStyle(campMuted) }
                Spacer()
                if let decisions = approval.decisions, !decisions.isEmpty {
                    ForEach(decisions.filter { !$0.allows }) { decision in
                        Button(decision.label) { conversation.answer(approval.id, allow: false, decision: decision.id) }
                    }
                    let allowed = decisions.filter(\.allows)
                    if allowed.count == 1, let decision = allowed.first {
                        Button(sending ? "Sending…" : decision.label) { conversation.answer(approval.id, allow: true, decision: decision.id) }
                            .buttonStyle(.borderedProminent).tint(campGreen).disabled(!approval.canApprove)
                    } else if !allowed.isEmpty {
                        Menu(sending ? "Sending…" : "Allow…") {
                            ForEach(allowed) { decision in Button(decision.label) { conversation.answer(approval.id, allow: true, decision: decision.id) } }
                        }.disabled(!approval.canApprove)
                    }
                } else {
                    Button(approval.kind == "question" ? "Cancel" : "Decline") { conversation.answer(approval.id, allow: false) }
                    Button(sending ? "Sending…" : approval.acceptLabel) { conversation.answer(approval.id, allow: true, answers: answers) }
                        .buttonStyle(.borderedProminent).tint(campGreen).disabled(!approval.canApprove || !ready)
                }
            }.disabled(sending).fixedSize(horizontal: false, vertical: true)
        }.padding(12).background(campGreen.opacity(0.06)).clipShape(RoundedRectangle(cornerRadius: 9))
    }
}
