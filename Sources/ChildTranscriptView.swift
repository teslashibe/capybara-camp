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
    @State private var cloudBefore:Int64 = 0
    @State private var cloudRevision:Int64 = 0
    @State private var cloudHasMore=false
    @State private var requestID=UUID()
    @State private var cachedAt:Date?
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 7) {
                Image(systemName: "arrow.turn.down.right").foregroundStyle(campMuted)
                Text("Subagent · \(child.name)").font(.system(size: 11, weight: .medium))
                Spacer()
                Text(cachedAt==nil ? "Read-only" : "Offline copy").font(.system(size: 10)).foregroundStyle(campMuted)
                    .help(cachedAt.map{"Saved \($0.formatted(date:.abbreviated,time:.shortened))"} ?? "This subagent transcript is read-only")
                if loading { ProgressView().controlSize(.small) }
            }.padding(.horizontal, 22).padding(.vertical, 8)
            HStack(spacing: 8) {
                Text([child.phase.isEmpty ? child.workflow : child.phase, child.model].filter { !$0.isEmpty }.joined(separator: " · ")).lineLimit(2)
                Spacer(minLength: 8)
                if child.status == "running" || child.status == "syncing" { ProgressView().controlSize(.mini) }
                else { Circle().fill(campGreen).frame(width: 5, height: 5) }
                Text(child.status.capitalized).lineLimit(1)
            }.font(.system(size: 10)).foregroundStyle(campMuted).padding(.horizontal, 22).padding(.vertical, 10)
            ScrollViewReader { proxy in
                CampTranscriptViewport(padding: 25, followBottom: { proxy.scrollTo("child-bottom", anchor: .bottom) }) {
                    VStack(alignment: .leading, spacing: 22) {
                        if !error.isEmpty { Text(error).font(.system(size: 12)).foregroundStyle(Color.orange) }
                        if parent.isCloud && cloudHasMore {
                            Button("Load earlier messages"){Task{await readCloud(more:true)}}.font(.caption).disabled(loading)
                        }
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
        }.task(id: "\(parent.id):\(child.id):\(child.updated):\(child.status):\(fileRevision)") {
            watch.stop()
            if parent.isCloud {await readCloud(more:false);return}
            loading = true; error = ""
            defer { loading = false }
            let change = $fileRevision
            watch.observe([child.path, URL(fileURLWithPath: child.path).deletingLastPathComponent().path]) { [weak store] in
                change.wrappedValue += 1
                store?.sharing.localChanged()
            }
            do {
                var value = store.object(parent)
                value["path"] = child.path; value["sessionID"] = child.sessionID; value["isSubagent"] = true
                let input = try JSONSerialization.data(withJSONObject: ["session": value, "limit": 120])
                let result = try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: input))
                guard !Task.isCancelled else { return }; messages = result.messages
            } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
        }.onDisappear {watch.stop();requestID=UUID()}
    }
    @MainActor private func readCloud(more:Bool) async {
        let token=UUID();requestID=token;loading=true;error=""
        defer{if requestID==token{loading=false}}
        do {
            guard let remote=store.cloudSessions[parent.id] else{throw CampCloudError.invalidResponse}
            let before=more ? cloudBefore : 0,revision=more ? cloudRevision : 0,base=more ? messages : []
            func apply(_ result:CloudChildTranscript) {
                guard !Task.isCancelled,requestID==token else{return}
                messages=result.transcript.messages.map(\.transcriptMessage)+base
                cachedAt=result.cachedAt
                cloudBefore=result.transcript.before;cloudRevision=result.parentRevision;cloudHasMore=result.transcript.hasMore
            }
            let result=try await store.cloud.childTranscript(session:remote,childID:child.id,before:before,revision:revision,onCached:apply)
            apply(result)
        } catch {
            if !Task.isCancelled,requestID==token {
                if !childHistoryTransportUnavailable(error) {messages=[];cloudHasMore=false;cachedAt=nil}
                self.error=error.localizedDescription
            }
        }
    }
}
