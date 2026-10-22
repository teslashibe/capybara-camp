import SwiftUI

struct ChildTranscriptView: View {
    @ObservedObject var store: CampStore
    let parent: CampSession
    let child: ChildSession
    @State private var messages: [Message] = []
    @State private var error = ""
    @State private var loading = false
    @State private var watch = TranscriptWatch()
    @State private var fileRevision = 0
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .top) {
                Image(systemName: "arrow.turn.down.right").foregroundStyle(campMuted)
                VStack(alignment: .leading, spacing: 4) {
                    Text(child.name).font(.system(size: 15, weight: .medium))
                    Text("\(child.status) · \(child.model) · Read-only transcript").font(.system(size: 11)).foregroundStyle(campMuted)
                }
                Spacer()
                if loading { ProgressView().controlSize(.small) }
            }.padding(22)
            Divider()
            ScrollViewReader { proxy in
                CampTranscriptViewport(padding: 25, followBottom: { proxy.scrollTo("child-bottom", anchor: .bottom) }) {
                    VStack(alignment: .leading, spacing: 22) {
                        if !error.isEmpty { Text(error).font(.system(size: 12)).foregroundStyle(Color.orange) }
                        ForEach(messages) { MessageView(message: $0) }
                        Color.clear.frame(height: 1).id("child-bottom")
                    }
                }.onChange(of: messages) { _, _ in
                    DispatchQueue.main.async { proxy.scrollTo("child-bottom", anchor: .bottom) }
                }
            }
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
