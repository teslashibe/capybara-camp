import SwiftUI

struct ChildTranscriptView: View {
    @ObservedObject var store: CampStore
    let parent: CampSession
    let child: ChildSession
    var openMain: () -> Void = {}
    @State private var messages: [Message] = []
    @State private var error = ""
    @State private var loading = false
    @State private var watch = TranscriptWatch()
    @State private var fileRevision = 0
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 7) {
                Image(systemName: "arrow.turn.down.right").foregroundStyle(campMuted)
                Text("Subagent · \(child.name)").font(.system(size: 11, weight: .medium))
                Spacer()
                Text("Read-only").font(.system(size: 10)).foregroundStyle(campMuted)
                if loading { ProgressView().controlSize(.small) }
            }.padding(.horizontal, 22).padding(.vertical, 8)
            HStack(spacing: 8) {
                Text([child.phase.isEmpty ? child.workflow : child.phase, child.model].filter { !$0.isEmpty }.joined(separator: " · ")).lineLimit(2)
                Spacer(minLength: 8)
                if child.status == "running" { ProgressView().controlSize(.mini) }
                else { Circle().fill(campGreen).frame(width: 5, height: 5) }
                Text(child.status.capitalized).lineLimit(1)
            }.font(.system(size: 10)).foregroundStyle(campMuted).padding(.horizontal, 22).padding(.vertical, 10)
            ScrollViewReader { proxy in
                CampTranscriptViewport(padding: 25, followBottom: { proxy.scrollTo("child-bottom", anchor: .bottom) }) {
                    VStack(alignment: .leading, spacing: 22) {
                        if !error.isEmpty { Text(error).font(.system(size: 12)).foregroundStyle(Color.orange) }
                        ForEach(messages) { MessageView(message: $0, provider: parent.provider) }
                        Color.clear.frame(height: 1).id("child-bottom")
                    }
                }.onChange(of: messages) { _, _ in
                    DispatchQueue.main.async { proxy.scrollTo("child-bottom", anchor: .bottom) }
                }
            }
            HStack {
                Label("From \(parent.title)", systemImage: "arrow.turn.down.right").font(.system(size: 10)).foregroundStyle(campMuted).lineLimit(1)
                Spacer(minLength: 8)
                Button(action: openMain) { Label("Main chat", systemImage: "bubble.left") }.controlSize(.small)
            }.padding(.horizontal, 22).padding(.vertical, 12)
        }.task(id: "\(parent.id):\(child.id):\(child.updated):\(fileRevision)") {
            loading = true; error = ""
            defer { loading = false }
            let change = $fileRevision
            watch.observe([child.path, URL(fileURLWithPath: child.path).deletingLastPathComponent().path]) {
                change.wrappedValue += 1
            }
            do {
                var value = store.object(parent)
                value["path"] = child.path; value["sessionID"] = child.sessionID; value["isSubagent"] = true
                let input = try JSONSerialization.data(withJSONObject: ["session": value, "limit": 120])
                let result = try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: input))
                guard !Task.isCancelled else { return }; messages = result.messages
            } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
        }.onDisappear { watch.stop() }
    }
}
