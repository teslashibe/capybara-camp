import Foundation
import SwiftUI

struct CloudStreamHead: Codable, Equatable, Sendable {
    var id: String
    var state: String
    var seq: Int64
    var model: String
    var effort: String
    var speed: String
    var running: Bool { ["claimed", "started", "cancel_requested"].contains(state) }
}
struct CloudStreamEvent: Decodable, Sendable {
    struct Value: Decodable, Sendable {
        var type: String
        var text: String?
        var message: String?
        var requestID: String?
        var model: String?
        var effort: String?
        var speed: String?
        var id: String?
        var kind: String?
        var title: String?
        var detail: String?
        var questions: [ProviderQuestion]?
        var canApprove: Bool?
        var acceptLabel: String?
        var decisions: [ProviderApprovalDecision]?
        var approval: ProviderApproval? {
            guard type == "approval", let id, let kind, let title, let detail, let questions, let canApprove, let acceptLabel else { return nil }
            return ProviderApproval(id: id, kind: kind, title: title, detail: detail, questions: questions, canApprove: canApprove, acceptLabel: acceptLabel, decisions: decisions)
        }
    }
    var seq: Int64
    var event: Value
}
struct CloudStreamPage: Decodable, Sendable { var events: [CloudStreamEvent]; var next: Int64 }

// Only the open chat retains its replay projection. The durable source remains
// the server event log; switching Camp/session cancels readers and drops text.
@MainActor final class CloudLiveConversation: ObservableObject {
    @Published private(set) var head: CloudStreamHead?
    @Published private(set) var messages: [Message] = []
    @Published private(set) var approvals: [ProviderApproval] = []
    @Published private(set) var notice = ""
    @Published private(set) var revision: Int64 = 0
    private(set) var sessionID: String?
    private var binding: String?
    private var client: (any CampAccountTransport)?
    private var credential: CloudCredential?
    private var scope: CloudScope?
    private var generation = UUID()
    private var task: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    private var dirty = false
    private var cursor: Int64 = 0
    private var text = ""
    private var completed: [Message] = []
    private var committed: Set<String> = []
    private var headPath = ""
    private var textLimit = 16 << 20
    var changed: (() -> Void)?
    deinit { task?.cancel(); retryTask?.cancel() }

    func watch(session: CloudSession, scope: CloudScope, client: any CampAccountTransport, credential: CloudCredential) {
        let key = scope.key + ":" + session.id
        if binding != key { stop(); binding = key; sessionID = session.id; self.scope = scope }
        self.client = client; self.credential = credential
        headPath = "/v1/camps/\(scope.campId)/sessions/\(session.id)/stream"
        textLimit = 16 << 20
        invalidate(session.id)
    }
    func watchPlanning(planID: String, version: Int, ordinal: Int, sessionID: String, scope: CloudScope, client: any CampAccountTransport, credential: CloudCredential) {
        let key = "\(scope.key):plan:\(planID):\(version):\(ordinal)"
        if binding != key { stop(); binding = key; self.sessionID = sessionID; self.scope = scope }
        self.client = client; self.credential = credential
        headPath = "/v1/camps/\(scope.campId)/planning/\(planID)/steps/\(ordinal)/stream?version=\(version)"
        textLimit = 8192
        invalidate(sessionID)
    }
    func stop() {
        generation = UUID(); task?.cancel(); task = nil; retryTask?.cancel(); retryTask = nil; dirty = false
        binding = nil; sessionID = nil; scope = nil; client = nil; credential = nil
        headPath = ""; textLimit = 16 << 20
        reset(); committed = []
    }
    private func reset() {
        head = nil; cursor = 0; text = ""; completed = []; messages = []; approvals = []; approvalSequences = [:]; notice = ""
        revision &+= 1; changed?()
    }
    func acknowledge(_ values: [CloudMessage]) {
        committed = Set(values.compactMap { value in
            let parts = value.id.split(separator: ":")
            return parts.count == 3 && parts[0] == "cloud" ? String(parts[1]) : nil
        })
        publish()
    }
    func invalidate(_ id: String) {
        guard sessionID == id, let client, let scope, let credential else { return }
        dirty = true
        guard task == nil else { return }
        retryTask?.cancel(); retryTask = nil
        let token = generation
        task = Task { [weak self] in
            guard let self else { return }
            defer { if self.generation == token { self.task = nil } }
            while self.dirty && self.generation == token && !Task.isCancelled {
                self.dirty = false
                do {
                    let data = try await client.data("GET", path: self.headPath, body: nil, key: nil, token: credential.token)
                    guard self.generation == token, !Task.isCancelled else { return }
                    let decoded: CloudStreamHead?
                    do { decoded = try CampWire.decoder().decode(CloudStreamHead?.self, from: data) }
                    catch { throw CampCloudError.invalidResponse }
                    guard let result = decoded else { self.reset(); return }
                    if self.notice == "Live output disconnected · Reconnecting" { self.notice = "" }
                    guard UUID(uuidString: result.id) != nil, result.seq >= 0, result.seq <= 32768 else { throw CampCloudError.invalidResponse }
                    if self.head?.id != result.id { self.reset() }
                    guard result.seq >= self.cursor else { throw CampCloudError.invalidResponse }
                    self.head = result
                    if self.committed.contains(result.id) { self.publish(); continue }
                    while self.cursor < result.seq {
                        let bytes = try await client.data("GET", path: "/v1/camps/\(scope.campId)/requests/\(result.id)/events?after=\(self.cursor)", body: nil, key: nil, token: credential.token)
                        guard self.generation == token, !Task.isCancelled else { return }
                        let page: CloudStreamPage
                        do { page = try CampWire.decoder().decode(CloudStreamPage.self, from: bytes) }
                        catch { throw CampCloudError.invalidResponse }
                        try self.apply(page)
                        // Yield between bounded pages during reconnect replay.
                        await Task.yield()
                    }
                    self.publish()
                } catch is CancellationError { return }
                catch CampCloudError.server(let status, _) where status == 401 || status == 403 || status == 404 {
                    guard self.generation == token else { return }; self.stop(); return
                } catch CampCloudError.invalidResponse {
                    self.notice = "Live output needs review · The stream could not be read"; return
                } catch CampCloudError.responseTooLarge {
                    self.notice = "Live output exceeded its display limit · Open the saved transcript"; return
                } catch {
                    guard self.generation == token else { return }
                    self.notice = "Live output disconnected · Reconnecting"
                    self.retryTask = Task { [weak self] in
                        do { try await Task.sleep(for: .seconds(1)) } catch { return }
                        guard self?.generation == token else { return }
                        self?.invalidate(id)
                    }
                    return
                }
            }
        }
    }
    func apply(_ page: CloudStreamPage) throws {
        guard page.events.count <= 100, page.next >= cursor, !page.events.isEmpty else { throw CampCloudError.invalidResponse }
        for value in page.events {
            guard value.seq == cursor + 1 else { throw CampCloudError.invalidResponse }
            let event = value.event
            switch event.type {
            case "delta": text += event.text ?? ""
            case "text": text = event.text ?? ""
            case "reset": text = ""
            case "settings":
                if let model = event.model, !model.isEmpty { head?.model = model }
                if let effort = event.effort, !effort.isEmpty { head?.effort = effort }
                if let speed = event.speed, !speed.isEmpty { head?.speed = speed }
            case "boundary":
                if !text.isEmpty { completed.append(liveMessage(text, segment: completed.count)); text = "" }
            case "approval":
                guard let approval = event.approval, approval.valid else { throw CampCloudError.invalidResponse }
                if !approvals.contains(where: { $0.id == approval.id }) { approvals.append(approval) }
                approvalSequences[approval.id] = value.seq
            case "approvalResolved":
                approvals.removeAll { $0.id == event.requestID }; if let id = event.requestID { approvalSequences[id] = nil }
            case "approvalRejected":
                notice = event.message ?? "The answer was not accepted"
            case "notice", "error": notice = event.message ?? ""
            default: break
            }
            guard text.utf8.count + completed.reduce(0, { $0 + $1.text.utf8.count }) <= textLimit,
                  completed.count <= 128, approvals.count <= 64 else { throw CampCloudError.responseTooLarge }
            cursor = value.seq
        }
        guard page.next == cursor else { throw CampCloudError.invalidResponse }
        publish()
    }
    private var approvalSequences: [String: Int64] = [:]
    private func liveMessage(_ value: String, segment: Int) -> Message {
        Message(id: "stream:\(head?.id ?? ""):\(segment)", role: "assistant", text: value, model: head?.model, effort: head?.effort, speed: head?.speed)
    }
    private func publish() {
        let hidden = head.map { committed.contains($0.id) } ?? false
        messages = hidden ? [] : completed + (text.isEmpty ? [] : [liveMessage(text, segment: completed.count)])
        if hidden { approvals = [] }
        revision &+= 1; changed?()
    }
    func approvalSequence(_ id: String) -> Int64? { approvalSequences[id] }
}

struct CloudStreamMessages: View {
    @ObservedObject var live: CloudLiveConversation
    var provider: String
    var body: some View {
        ForEach(live.messages) { message in MessageView(message: message, provider: provider).id(message.id) }
    }
}
struct CloudStreamApprovals: View {
    @ObservedObject var live: CloudLiveConversation
    @ObservedObject var cloud: CampConnection
    var session: CloudSession
    var body: some View {
        if live.head?.state == "started", let approval = live.approvals.first {
            if session.ownerUserId == cloud.credential?.actor.userId {
                ProviderApprovalView(approval: approval, sending: live.head.map { cloud.pendingControl(requestID:$0.id,kind:"approval",providerID:approval.id) } ?? false, answer: { allow, decision, answers in
                    guard let head = live.head, let seq = live.approvalSequence(approval.id) else { return }
                    let value = CloudControlEnvelope(id:UUID().uuidString.lowercased(),approvalSeq:seq,control:.init(type:"approval",id:approval.id,allow:allow,decision:decision,answers:answers))
                    Task { do { try await cloud.queueControl(session:session,head:head,value:value) } catch { cloud.error=error.localizedDescription } }
                }).id(approval.id)
            } else {
                Label("Waiting for the session owner to review", systemImage: "hand.raised").font(.system(size: 11)).foregroundStyle(campMuted)
            }
        }
        if !live.notice.isEmpty { Text(live.notice).font(.system(size: 11)).foregroundStyle(campMuted) }
    }
}
