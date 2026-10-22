import Foundation
import SwiftUI

struct SessionReference: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var title: String? // Only local titles are persisted outside the scoped Camp cache.
    var scope: String?
    var cloud: CloudSourceRef?
    var messages: [Message]?
    var digest: String?
    var count: Int { cloud?.messageIds?.count ?? messages?.count ?? 0 }
    var contextID: String { cloud == nil ? "source-" + PublishMessage.hash(Data(id.utf8)) : id }
}

struct SessionContext: Codable, Sendable {
    var id: String
    var title: String
    var revision: String
    var messages: [Message]
    private enum CodingKeys: String, CodingKey { case id, title, revision, messages }
    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(title, forKey: .title)
        try container.encode(revision, forKey: .revision)
        try container.encode(messages.map(\.contextMessage), forKey: .messages)
    }
}

@MainActor final class SessionMentions: ObservableObject {
    @Published private(set) var drafts: [String: [SessionReference]] = [:]
    private(set) var receipts: [SessionReference] = []
    private struct State: Codable { var drafts: [String: [SessionReference]]; var receipts: [SessionReference] }
    private let directory: URL
    init(directory: URL) {
        self.directory = directory
        let url = directory.appendingPathComponent("context-drafts.json")
        if let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
           let size = attributes[.size] as? NSNumber, size.intValue <= 4 << 20,
           let data = try? Data(contentsOf: url) {
            if let value = try? JSONDecoder().decode(State.self, from: data) { drafts = value.drafts; receipts = value.receipts }
            else if let value = try? JSONDecoder().decode([String: [SessionReference]].self, from: data) { drafts = value }
        }
    }
    func references(for id: String) -> [SessionReference] { drafts[id] ?? [] }
    func set(_ references: [SessionReference], for id: String) throws {
        guard references.count <= 20, Set(references.map(\.id)).count == references.count else { throw CampError.message("Attach up to 20 different sessions") }
        var next = drafts
        next[id] = references.isEmpty ? nil : references
        try persist(next, receipts: receipts)
    }
    private func persist(_ next: [String: [SessionReference]], receipts: [SessionReference]) throws {
        let data = try JSONEncoder().encode(State(drafts: next, receipts: receipts))
        guard data.count <= 4 << 20 else { throw CampError.message("Saved context is full. Remove unused attachments") }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let path = directory.appendingPathComponent("context-drafts.json")
        try data.write(to: path, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
        drafts = next
        self.receipts = receipts
    }
    func add(_ reference: SessionReference, to id: String) throws {
        try set(references(for: id).filter { $0.id != reference.id } + [reference], for: id)
    }
    func remove(_ reference: SessionReference, from id: String) throws { try set(references(for: id).filter { $0.id != reference.id }, for: id) }
    func clear(_ references: [SessionReference], for id: String) throws {
        if self.references(for: id) == references { try set([], for: id) }
    }
    func record(_ references: [SessionReference]) throws {
        let fresh = references.filter { !receipts.contains($0) }
        if !fresh.isEmpty { try persist(drafts, receipts: receipts + fresh) }
    }
    func source(id: String, revision: String, message: String) -> SessionReference? {
        let values = receipts + drafts.values.flatMap { $0 }
        return values.first { source in
            (source.contextID == id || source.id == id) && (source.cloud.map { String($0.transcriptRevision) } ?? source.digest) == revision &&
                (source.cloud?.messageIds ?? source.messages?.map(\.id) ?? []).contains(message)
        }
    }
}

struct ContextInspection: Identifiable {
    var id = UUID()
    var sourceID: String
    var title: String
    var messages: [Message]
    var selectedMessage: String
    var scope: String? = nil
    var permissionRevision: Int64? = nil
    var childID: String? = nil
    var planID: String? = nil
    var sourcePermissions: [String: Int64]? = nil
    func remainsVisible(scope current: String?, permissionRevision observed: Int64?, availablePermissions: [String: Int64] = [:]) -> Bool {
        guard let scope else { return true }
        if let sourcePermissions { return scope == current && sourcePermissions.allSatisfy { availablePermissions[$0.key] == $0.value } }
        return scope == current && observed != nil && permissionRevision == observed
    }
}

func inspectHistoryMessage(id:String,title:String,scope:String,reference:CloudSourceRef,snapshot:CloudTranscript) throws -> ContextInspection {
    guard snapshot.revision==reference.transcriptRevision, snapshot.messages.count==1,
          let expected=reference.messageIds, expected.count==1, let value=snapshot.messages.first,
          value.id==expected[0], !value.removed else {throw CampCloudError.invalidResponse}
    return ContextInspection(sourceID:id,title:title,messages:[value.transcriptMessage],selectedMessage:value.id,scope:scope,permissionRevision:reference.permissionRevision)
}

func inspectChildHistoryMessage(parent:CloudSession,childID:String,scope:String,revision:Int64,messageID:String,snapshot:CloudChildTranscript)throws->ContextInspection {
    guard snapshot.parentRevision==revision,snapshot.permissionRevision==parent.permissionRevision,
          snapshot.child.valid(parent:parent.id),snapshot.child.id==childID,snapshot.child.complete,!snapshot.child.removed,
          snapshot.transcript.revision==snapshot.child.transcriptRevision,snapshot.transcript.messages.count==1,
          let message=snapshot.transcript.messages.first,message.id==messageID,!message.removed else{throw CampCloudError.invalidResponse}
    return ContextInspection(sourceID:"camp:"+parent.id,title:snapshot.child.name,messages:[message.transcriptMessage],selectedMessage:messageID,scope:scope,permissionRevision:parent.permissionRevision,childID:childID)
}

struct ContextPreview: Sendable {
    var messages: [Message]
    var total: Int
    var cloud: CloudSourceRef?
    var before: Int64 = 0
}

extension CampStore {
    func openContextLink(_ url: URL) {
        guard let parts = URLComponents(url: url, resolvingAgainstBaseURL: false), parts.scheme == "capybara", parts.host == "session" else { return }
        let id = String(parts.path.dropFirst())
        let revision = parts.queryItems?.first { $0.name == "revision" }?.value ?? ""
        let message = parts.queryItems?.first { $0.name == "message" }?.value ?? ""
        if id.hasPrefix("plan-source:") { openQuotedPlanningLink(id: id, revision: revision, message: message); return }
        guard let source = mentions.source(id: id, revision: revision, message: message), let destination = selected else {
            openCloudHistoryLink(id:id,revision:revision,message:message)
            return
        }
        Task {
            do {
                let contexts = try await resolveContexts([source], destination: destination)
                guard let value = contexts.first else { return }
                contextInspection = ContextInspection(sourceID: source.id, title: value.title, messages: value.messages, selectedMessage: message,scope:source.scope,permissionRevision:source.cloud?.permissionRevision)
            } catch { banner = error.localizedDescription }
        }
    }
    private func openCloudHistoryLink(id:String,revision:String,message:String) {
        let childParts=id.components(separatedBy:"/child:")
        if childParts.count==2 {openCloudChildHistoryLink(parentID:childParts[0],childID:childParts[1],revision:revision,message:message);return}
        guard id.hasPrefix("camp:"),let remote=cloudSessions[id],let scope=cloud.scope,
              remote.campId==scope.campId,let version=Int64(revision),version>0,version<=remote.transcriptRevision else {
            banner = "This source snapshot is not saved in this Camp";return
        }
        if message.isEmpty {
            if let original=sessions.first(where:{$0.id==id}){open(original)}
            return
        }
        guard message.utf8.count<=256 else {banner="Invalid source message";return}
        let reference=CloudSourceRef(sessionId:remote.id,transcriptRevision:version,permissionRevision:remote.permissionRevision,messageIds:[message])
        Task {
            do {
                let snapshot=try await cloud.contextMessage(session:remote,source:reference)
                guard cloud.scope==scope,cloudSessions[id]?.permissionRevision==reference.permissionRevision else {throw CampCloudError.invalidResponse}
                contextInspection=try inspectHistoryMessage(id:id,title:remote.title,scope:scope.key,reference:reference,snapshot:snapshot)
            } catch is CancellationError {} catch {if cloud.scope==scope {banner=error.localizedDescription}}
        }
    }
    private func openCloudChildHistoryLink(parentID:String,childID:String,revision:String,message:String) {
        guard let parent=cloudSessions[parentID],let scope=cloud.scope,UUID(uuidString:childID) != nil,
              let version=Int64(revision),version>0,version<=parent.transcriptRevision,message.utf8.count<=256 else{banner="This child snapshot is not saved in this Camp";return}
        if message.isEmpty {openCloudChild(parentID:parentID,childID:childID);return}
        Task {
            do {
                let value=try await cloud.childMessage(session:parent,childID:childID,revision:version,messageID:message)
                guard cloud.scope==scope,cloudSessions[parentID]?.permissionRevision==parent.permissionRevision else{throw CancellationError()}
                contextInspection=try inspectChildHistoryMessage(parent:parent,childID:childID,scope:scope.key,revision:version,messageID:message,snapshot:value)
            } catch is CancellationError {} catch {if cloud.scope==scope{banner=error.localizedDescription}}
        }
    }
    func openCloudChild(parentID:String,childID:String) {
        guard let parent=cloudSessions[parentID],let local=sessions.first(where:{$0.id==parentID}),let scope=cloud.scope else{return}
        open(local)
        Task {
            do {
                let values=try await cloud.childHistories(session:parent)
                guard cloud.scope==scope,selectedID==parentID,let child=values.children.first(where:{$0.id==childID && !$0.removed && $0.complete}) else{return}
                children=values.displayChildren;selectedChildID=child.id
            } catch {if selectedID==parentID{banner=error.localizedDescription}}
        }
    }
    func contextTitle(_ reference: SessionReference) -> String {
        sessions.first { $0.id == reference.id }?.title ?? reference.title ?? "Source unavailable"
    }
    func contextPreview(_ candidate: CampSession, limit: Int = 120) async throws -> ContextPreview {
        guard let session = sessions.first(where: { $0.id == candidate.id }) else { throw CampError.message("This session is no longer available") }
        if let plan = planning.ticket(session.id) {
            let messages = plan.versions.flatMap { version in
                [Message(id: "\(plan.id):\(version.number):brief", role: "user", text: version.brief)] +
                    version.steps.filter { !$0.output.isEmpty }.map { Message(id: "\(plan.id):\(version.number):\($0.ordinal)", role: "assistant", text: $0.output) }
            }
            return ContextPreview(messages: Array(messages.suffix(limit)), total: messages.count)
        }
        if let remote = cloudSessions[session.id] {
            let page = try await cloud.transcript(session: remote, before: 0, revision: 0)
            return ContextPreview(messages: page.messages.filter { !$0.removed }.map(\.transcriptMessage), total: page.messages.count + (page.hasMore ? 1 : 0), cloud: CloudSourceRef(sessionId: remote.id, transcriptRevision: page.revision, permissionRevision: remote.permissionRevision), before: page.before)
        }
        guard (1...1000).contains(limit) else { throw CampCloudError.invalidResponse }
        let input = try CampWire.body(["session": object(session), "limit": limit])
        let result = try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: input))
        return ContextPreview(messages: result.messages, total: result.total)
    }
    func earlierContext(_ candidate: CampSession, preview: ContextPreview) async throws -> ContextPreview {
        guard let source = preview.cloud, let remote = cloudSessions[candidate.id], source.permissionRevision == remote.permissionRevision,
              preview.before > 0 else { throw CampError.message("This source changed or has no earlier messages") }
        let page = try await cloud.transcript(session: remote, before: preview.before, revision: source.transcriptRevision)
        let messages = page.messages.map(\.transcriptMessage) + preview.messages
        guard messages.count <= 1000 else { throw CampError.message("Choose a range from these 1,000 messages before loading more") }
        return ContextPreview(messages: messages, total: messages.count + (page.hasMore ? 1 : 0), cloud: source, before: page.before)
    }
    func makeReference(_ candidate: CampSession, preview: ContextPreview, range: ClosedRange<Int>) throws -> SessionReference {
        guard sessions.contains(where: { $0.id == candidate.id }), range.lowerBound >= 0, range.upperBound < preview.messages.count,
              range.count <= 120 else { throw CampError.message("Choose up to 120 messages") }
        let chosen = Array(preview.messages[range])
        guard !chosen.contains(where: { $0.text.utf8.count > 48 << 10 }) else { throw CampError.message("Choose messages under 48 KB each") }
        guard chosen.reduce(0, { $0 + $1.text.utf8.count }) <= 256 << 10 else { throw CampError.message("Choose a smaller range, under 256 KB") }
        if var source = preview.cloud {
            guard let scope = cloud.scope, cloudSessions[candidate.id]?.permissionRevision == source.permissionRevision else { throw CampError.message("This source changed its sharing permissions") }
            source.messageIds = chosen.map(\.id)
            return SessionReference(id: candidate.id, scope: scope.key, cloud: source)
        }
        return SessionReference(id: candidate.id, title: candidate.title, messages: chosen, digest: PublishMessage.hash(try CampWire.encoder().encode(chosen.map(\.contextMessage))))
    }
    func resolveContexts(_ references: [SessionReference], destination: CampSession) async throws -> [SessionContext] {
        guard references.count <= 20 else { throw CampError.message("Attach up to 20 sessions") }
        var result: [SessionContext] = []
        var bytes = 0
        for reference in references {
            guard let source = sessions.first(where: { $0.id == reference.id }) else { throw CampError.message("A tagged session is no longer available") }
            let messages: [Message], revision: String
            if let remote = reference.cloud {
                guard reference.scope == cloud.scope?.key, let session = cloudSessions[reference.id],
                      session.permissionRevision == remote.permissionRevision else { throw CampError.message("A tagged session is no longer shared with this Camp") }
                let page = try await cloud.context(session: session, source: remote)
                messages = page.messages.map(\.transcriptMessage); revision = String(remote.transcriptRevision)
            } else {
                guard !destination.isCloud, let saved = reference.messages, let digest = reference.digest,
                      PublishMessage.hash(try CampWire.encoder().encode(saved.map(\.contextMessage))) == digest else { throw CampError.message("Share this local source with the Camp before using it in a shared request") }
                messages = saved; revision = digest
            }
            bytes += messages.reduce(0, { $0 + $1.text.utf8.count })
            guard messages.count <= 120, bytes <= 256 << 10 else { throw CampError.message("Choose a smaller context range, under 256 KB total") }
            result.append(SessionContext(id: reference.contextID, title: source.title, revision: revision, messages: messages))
        }
        guard references.allSatisfy({ reference in
            if let source = reference.cloud { return reference.scope == cloud.scope?.key && cloudSessions[reference.id]?.permissionRevision == source.permissionRevision }
            return sessions.contains { $0.id == reference.id && !$0.isCloud }
        }) else { throw CampError.message("A tagged source changed while preparing this prompt") }
        return result
    }
}

struct ContextInspectionView: View {
    @ObservedObject var store: CampStore
    let inspection: ContextInspection
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        let provider = store.sessions.first(where: { $0.id == inspection.sourceID && $0.provider != "Planning" })?.provider
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text(inspection.title).font(.headline).lineLimit(2)
                    Text("Selected source snapshot").font(.caption).foregroundStyle(campMuted)
                }
                Spacer()
                Button { dismiss() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Close source snapshot")
            }
            ScrollViewReader { proxy in
                ScrollView { VStack(alignment: .leading, spacing: 18) { ForEach(inspection.messages) { message in MessageView(message: message, provider: provider).id(message.id) } }.padding(12) }
                    .onAppear { proxy.scrollTo(inspection.selectedMessage, anchor: .top) }
            }
            if let original = store.sessions.first(where: { $0.id == inspection.sourceID }) {
                Button("Open original session") {
                    if let childID=inspection.childID {store.openCloudChild(parentID:original.id,childID:childID)} else {store.open(original)}
                    dismiss()
                }
            }
        }.padding(22).frame(width: 680, height: 600)
    }
}

struct SessionContextComposer: View {
    @ObservedObject var store: CampStore
    @ObservedObject var mentions: SessionMentions
    let destination: CampSession
    @State private var picking = false
    @State private var initialQuery = ""
    var body: some View {
        HStack(spacing: 8) {
            Button { initialQuery = ""; picking = true } label: { Image(systemName: "at").frame(width: 28, height: 28) }
                .buttonStyle(.plain).help("Attach a session as context").accessibilityLabel("Attach session context")
                .popover(isPresented: $picking) { SessionContextPicker(store: store, destination: destination, query: initialQuery) { picking = false } }
            SessionContextChips(store: store, mentions: mentions, destination: destination, showPlaceholder: true)
        }
        .onChange(of: store.draft) { _, text in
            if !picking, let query = sessionMentionQuery(text) { initialQuery = query; picking = true }
        }
    }
}

func sessionMentionQuery(_ text: String) -> String? {
    guard let suffix = text.split(separator: "\n", omittingEmptySubsequences: false).last,
          let at = suffix.lastIndex(of: "@"), at == suffix.startIndex || suffix[suffix.index(before: at)].isWhitespace else { return nil }
    let search = String(suffix[suffix.index(after: at)...])
    return search.count <= 80 && !search.contains(" ") ? search : nil
}

struct SessionContextChips: View {
    @ObservedObject var store: CampStore
    @ObservedObject var mentions: SessionMentions
    let destination: CampSession
    var showPlaceholder = false
    var transcriptStrip = false
    private var references: [SessionReference] { mentions.references(for: destination.id) }
    var body: some View {
        if !references.isEmpty || showPlaceholder {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 7) {
                    ForEach(references) { source in
                        HStack(spacing: 6) {
                            Button { if let session = store.sessions.first(where: { $0.id == source.id }) { store.open(session) } } label: {
                                Label("\(store.contextTitle(source)) · \(source.count)", systemImage: "text.bubble").lineLimit(1)
                            }.buttonStyle(.plain).help("Open source session")
                            Button { do { try mentions.remove(source, from: destination.id) } catch { store.banner = error.localizedDescription } } label: { Image(systemName: "xmark").frame(width: 22, height: 24) }
                                .buttonStyle(.plain).help("Remove source")
                        }.font(.system(size: 10)).padding(.leading, 8).background(campGreen.opacity(0.07)).clipShape(RoundedRectangle(cornerRadius: 7))
                    }
                    if references.isEmpty && showPlaceholder { Text("Tag any session with @").font(.system(size: 10)).foregroundStyle(campMuted) }
                }
            }.frame(height: 28).padding(.horizontal, transcriptStrip ? 22 : 0).padding(.bottom, transcriptStrip ? 7 : 0)
        }
    }
}

struct SessionContextPicker: View {
    @ObservedObject var store: CampStore
    let destination: CampSession
    @State var query: String
    var close: () -> Void
    @State private var selected: CampSession?
    @State private var preview: ContextPreview?
    @State private var start = 0
    @State private var end = 0
    @State private var error = ""
    @State private var loading = false
    @State private var limit = 120
    @State private var expanded: Set<String> = []
    @FocusState private var searchFocused: Bool
    var candidates: [CampSession] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace)
        var found: [CampSession] = []
        let members = Dictionary(uniqueKeysWithValues: store.cloud.members.map { ($0.userId, $0.name) })
        for source in store.visibleSessions {
            if source.provider == "Planning" { continue }
            if destination.isCloud && !source.isCloud { continue }
            let remote = store.cloudSessions[source.id]
            let person = remote.flatMap { members[$0.ownerUserId] } ?? ""
            let value = [source.title, store.projectSummary(source), source.provider, source.sourceLabel, person].joined(separator: " ").lowercased()
            if words.allSatisfy({ value.contains($0) }) { found.append(source) }
            if found.count == 30 { break }
        }
        return found
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                if selected != nil { Button { selected = nil; preview = nil; error = "" } label: { Image(systemName: "arrow.left").frame(width: 28, height: 28) }.help("Back to sessions") }
                Text(selected?.title ?? "Session context").font(.headline).lineLimit(1)
                Spacer()
                Button(action: close) { Image(systemName: "xmark").frame(width: 28, height: 28) }.help("Close session picker")
            }.buttonStyle(.plain)
            if let selected {
                Text("Choose the messages this conversation can read").font(.system(size: 11)).foregroundStyle(campMuted)
                if loading { ProgressView().frame(maxWidth: .infinity) }
                if let preview, !preview.messages.isEmpty {
                    HStack {
                        Picker("From", selection: $start) { ForEach(preview.messages.indices, id: \.self) { Text("\($0 + 1)").tag($0) } }
                        Picker("To", selection: $end) { ForEach(preview.messages.indices, id: \.self) { Text("\($0 + 1)").tag($0) } }
                    }.font(.system(size: 11))
                    ScrollViewReader { proxy in ScrollView {
                        VStack(alignment: .leading, spacing: 9) {
                            ForEach(Array(preview.messages.enumerated()), id: \.element.id) { index, message in
                                VStack(alignment: .leading, spacing: 4) {
                                    Text("\(index + 1) · \(message.role.capitalized)").font(.system(size: 10, weight: .semibold)).foregroundStyle(campMuted)
                                    Text(verbatim: message.text).font(.system(size: 12)).lineLimit(expanded.contains(message.id) ? nil : 5).textSelection(.enabled)
                                    Button(expanded.contains(message.id) ? "Show less" : "Show full message") {
                                        if expanded.contains(message.id) { expanded.remove(message.id) } else { expanded.insert(message.id) }
                                    }.buttonStyle(.plain).font(.system(size: 10)).foregroundStyle(campGreen)
                                }.frame(maxWidth: .infinity, alignment: .leading).padding(10)
                                    .background(index >= start && index <= end ? campGreen.opacity(0.08) : .clear).clipShape(RoundedRectangle(cornerRadius: 7)).id(index)
                            }
                        }
                    }.onAppear { proxy.scrollTo(start, anchor: .top) }.onChange(of: start) { _, value in proxy.scrollTo(value, anchor: .top) } }
                    HStack {
                        if preview.total > preview.messages.count && preview.messages.count < 1000 {
                            Button("Load earlier messages") {
                                if preview.cloud == nil { limit = min(1000, limit + 120); load(selected) }
                                else { earlier(selected, preview: preview) }
                            }.disabled(loading)
                        }
                        Spacer()
                        Button("Use \(max(0, end - start + 1)) messages") {
                            do {
                                guard start <= end else { throw CampError.message("Choose a valid message range") }
                                let reference = try store.makeReference(selected, preview: preview, range: start...end)
                                try store.mentions.add(reference, to: destination.id)
                                if let at = store.draft.lastIndex(of: "@"), !store.draft[store.draft.index(after: at)...].contains(where: \.isWhitespace) { store.draft = String(store.draft[..<at]) }
                                close()
                            } catch { self.error = error.localizedDescription }
                        }.buttonStyle(.borderedProminent).tint(campGreen).disabled(start > end || end - start >= 120)
                    }
                } else if !loading { Text("No messages available").foregroundStyle(campMuted) }
            } else {
                TextField("Search title, project, person or provider", text: $query).textFieldStyle(.roundedBorder).focused($searchFocused)
                Text(destination.isCloud ? "Only sessions accessible to this Camp can be attached" : "All accessible sessions, including older conversations").font(.system(size: 10)).foregroundStyle(campMuted)
                ScrollView {
                    LazyVStack(spacing: 4) {
                        ForEach(candidates) { source in
                            Button { selected = source; limit = 120; load(source) } label: {
                                HStack(alignment: .top, spacing: 9) {
                                    ProviderMark(provider: source.provider, size: 16)
                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(source.title).font(.system(size: 12)).lineLimit(2)
                                        Text("\(source.provider) · \(store.projectSummary(source)) · \(source.sourceLabel)").font(.system(size: 10)).foregroundStyle(campMuted)
                                    }; Spacer(minLength: 0)
                                }.padding(10).contentShape(Rectangle())
                            }.buttonStyle(.plain)
                        }
                        if candidates.isEmpty { Text("No accessible sessions match").font(.caption).foregroundStyle(campMuted).padding() }
                    }
                }
            }
            if !error.isEmpty { Text(error).font(.system(size: 11)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
        }.padding(16).frame(width: 480, height: 460).onAppear { searchFocused = true }
    }
    func load(_ source: CampSession) {
        loading = true; preview = nil; error = ""
        Task {
            do {
                let value = try await store.contextPreview(source, limit: limit)
                guard selected?.id == source.id else { return }
                preview = value; end = max(0, value.messages.count - 1); start = max(0, end - 5); loading = false
            } catch { if selected?.id == source.id { self.error = error.localizedDescription; loading = false } }
        }
    }
    func earlier(_ source: CampSession, preview previous: ContextPreview) {
        loading = true; error = ""
        Task {
            do {
                let value = try await store.earlierContext(source, preview: previous)
                guard selected?.id == source.id else { return }
                preview = value; end = max(0, value.messages.count - previous.messages.count - 1); start = max(0, end - 5); loading = false
            } catch { if selected?.id == source.id { self.error = error.localizedDescription; loading = false } }
        }
    }
}
