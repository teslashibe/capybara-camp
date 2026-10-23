import Foundation

struct TransferHistoryStream: Codable, Sendable {
    var id: String
    var child: CloudChildHistory?
    var pages = 0
    var before: Int64 = 0
    var complete = false
    var messageCount = 0
    var textBytes = 0
    var hashes: [String] = []
}
struct TransferHistoryJournal: Codable, Sendable {
    var id: String
    var revision: Int64 = 1
    var session: CloudSession
    var streams: [TransferHistoryStream]
    var storedBytes = 0
    var complete: Bool { streams.allSatisfy(\.complete) }
}
struct TransferHistoryArchive: Codable, Sendable {
    struct Conversation: Codable, Sendable {
        var id: String
        var child: CloudChildHistory?
        var messages: [CloudMessage]
    }
    var version = 1
    var origin: String
    var session: CloudSession
    var conversations: [Conversation]
}

// A revision-pinned download, separate from the bounded UI/offline history cache.
// One page is saved per advance. Only an authenticated, unchanged source can
// release a complete archive to the destination installer.
actor CampTransferHistory {
    private let cache: CampCache
    private let scope: CloudScope
    private let credential: CloudCredential
    private let transport: any CampAccountTransport
    private let epoch: Int64
    private var busy = false
    static let maxBytes = 32 << 20

    init(cache: CampCache, scope: CloudScope, credential: CloudCredential, transport: any CampAccountTransport, epoch: Int64) throws {
        guard scope.origin == credential.origin, scope.userId == credential.actor.userId,
              [scope.campId, scope.userId, credential.actor.deviceId].allSatisfy({ UUID(uuidString:$0) != nil }) else { throw CampCloudError.invalidResponse }
        self.cache=cache; self.scope=scope; self.credential=credential; self.transport=transport; self.epoch=epoch
    }
    private func authority() async throws {
        try Task.checkCancellation()
        guard try await cache.epoch(scope) == epoch else { throw CancellationError() }
    }
    private func base(_ s: CloudSession) -> String { "/v1/camps/\(scope.campId)/sessions/\(s.id)" }
    private func get<T: Decodable>(_ type: T.Type, _ path: String) async throws -> T {
        try await authority()
        let raw=try await transport.data("GET",path:path,body:nil,key:nil,token:credential.token)
        try await authority()
        return try CampWire.decoder().decode(type,from:raw)
    }
    private func valid(_ s: CloudSession) -> Bool {
        s.campId == scope.campId && s.ownerUserId == scope.userId && !s.isHistoryOnly &&
        [s.id,s.profileId,s.ownerDeviceId,s.executionWorkspaceId ?? ""].allSatisfy({UUID(uuidString:$0) != nil}) &&
        ["Codex","Claude"].contains(s.provider) && s.executionLocation == "cloud" &&
        (s.executionEpoch ?? 0)>0 && s.revision>0 && s.transcriptRevision>=0 && s.permissionRevision>0
    }
    private func checkSource(_ source: CloudSession) async throws {
        let now=try await get(CloudSession.self,base(source))
        guard valid(now), now.id == source.id, now.projectId == source.projectId, now.profileId == source.profileId,
              now.ownerDeviceId == source.ownerDeviceId, now.provider == source.provider,
              now.executionWorkspaceId == source.executionWorkspaceId, now.executionEpoch == source.executionEpoch,
              now.revision == source.revision, now.transcriptRevision == source.transcriptRevision,
              now.permissionRevision == source.permissionRevision else { throw CampError.message("This session changed. Review its transfer again") }
    }
    func current() async throws -> TransferHistoryJournal? {
        try await authority()
        guard let raw=try await cache.metadata("transfer-history:"+scope.key) else { return nil }
        guard raw.count<=2<<20 else { throw CampCloudError.invalidResponse }
        let j=try CampWire.decoder().decode(TransferHistoryJournal.self,from:raw)
        guard UUID(uuidString:j.id) != nil, j.revision>0, valid(j.session), !j.streams.isEmpty, j.streams.count<=513,
              j.storedBytes>=0, j.storedBytes<=Self.maxBytes, j.streams[0].id == j.session.id, j.streams[0].child == nil,
              Set(j.streams.map(\.id)).count == j.streams.count else { throw CampCloudError.invalidResponse }
        for (index,s) in j.streams.enumerated() {
            guard UUID(uuidString:s.id) != nil, s.pages>=0, s.pages<=2000, s.hashes.count==s.pages,
                  s.hashes.allSatisfy({$0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil}),
                  s.before>=0, (0...2000).contains(s.messageCount), (0...(8<<20)).contains(s.textBytes),
                  !s.complete || s.pages>0 else { throw CampCloudError.invalidResponse }
            if index>0 {
                guard let c=s.child, c.id==s.id, c.valid(parent:j.session.id), c.complete, !c.removed,
                      c.revision<=j.session.transcriptRevision else { throw CampCloudError.invalidResponse }
            }
        }
        try await authority()
        return j
    }
    func begin(session: CloudSession) async throws {
        guard !busy else { throw CampError.message("History is already being downloaded") }
        busy=true; defer {busy=false}
        guard valid(session), try await current() == nil else { throw CampCloudError.invalidResponse }
        try await checkSource(session)
        let catalog=try await get(CloudChildHistoryList.self,base(session)+"/children?revision=\(session.transcriptRevision)")
        guard catalog.valid(parent:session.id), catalog.parentRevision==session.transcriptRevision,
              catalog.permissionRevision==session.permissionRevision,
              catalog.children.allSatisfy({$0.complete && !$0.removed}) else { throw CampError.message("Finish syncing sub-agent history before moving this session") }
        try await checkSource(session)
        let streams=[TransferHistoryStream(id:session.id)] + catalog.children.map { TransferHistoryStream(id:$0.id,child:$0) }
        try await cache.saveTransferHistory(TransferHistoryJournal(id:UUID().uuidString.lowercased(),session:session,streams:streams),scope:scope,expectedEpoch:epoch,previousRevision:nil)
    }
    private func validatePage(_ p: CloudTranscript, stream s: TransferHistoryStream, session: CloudSession) throws {
        guard p.messages.allSatisfy({ ($0.attachments ?? []).isEmpty }) else {
            throw CampError.message("This history contains attachments; destination byte installation is not available in this build")
        }
        let revision=s.child?.transcriptRevision ?? session.transcriptRevision
        guard p.revision==revision, p.messages.count<=120, p.before==(p.messages.first?.ordinal ?? 0),
              !p.hasMore || !p.messages.isEmpty && p.before>0,
              s.before==0 || !p.hasMore || p.before<s.before,
              p.messages.map(\.ordinal)==p.messages.map(\.ordinal).sorted(),
              Set(p.messages.map(\.id)).count==p.messages.count, Set(p.messages.map(\.ordinal)).count==p.messages.count,
              p.messages.allSatisfy({ m in
                  !m.id.isEmpty && m.id.utf8.count<=256 && m.ordinal>=0 && (s.before==0 || m.ordinal<s.before) &&
                  !m.removed && m.revision>0 && m.revision<=revision && ["user","assistant","system","tool"].contains(m.role) &&
                  m.text.utf8.count<=1<<20 && (m.model?.utf8.count ?? 0)<=200 && (m.effort?.utf8.count ?? 0)<=32 && (m.speed?.utf8.count ?? 0)<=32
              }) else { throw CampCloudError.invalidResponse }
    }
    @discardableResult func advance() async throws -> TransferHistoryJournal? {
        guard !busy else { throw CampError.message("History is already being downloaded") }
        busy=true; defer {busy=false}
        guard var j=try await current() else { return nil }
        try await checkSource(j.session)
        guard let index=j.streams.firstIndex(where:{!$0.complete}) else { return j }
        let stream=j.streams[index], revision=j.session.transcriptRevision
        let page: CloudTranscript
        if let child=stream.child {
            let result=try await get(CloudChildTranscript.self,base(j.session)+"/children/\(child.id)/transcript?revision=\(revision)&before=\(stream.before)&limit=120")
            guard result.valid(parent:j.session.id,before:stream.before), result.child==child,
                  result.parentRevision==revision, result.permissionRevision==j.session.permissionRevision else { throw CampCloudError.invalidResponse }
            page=result.transcript
        } else {
            page=try await get(CloudTranscript.self,base(j.session)+"/transcript?revision=\(revision)&before=\(stream.before)&limit=120")
        }
        try validatePage(page,stream:stream,session:j.session)
        let raw=try CampWire.encoder().encode(page), oldRevision=j.revision
        guard raw.count<=2<<20 else { throw CampCloudError.invalidResponse }
        j.storedBytes += raw.count
        j.streams[index].textBytes += page.messages.reduce(0){$0+$1.text.utf8.count}
        j.streams[index].messageCount += page.messages.count
        guard j.storedBytes<=Self.maxBytes, j.streams[index].textBytes<=8<<20, j.streams[index].messageCount<=2000 else {
            throw CampError.message("This history exceeds the transfer limit. No messages were truncated")
        }
        j.streams[index].hashes.append(PublishMessage.hash(raw)); j.streams[index].pages += 1
        j.streams[index].before=page.before; j.streams[index].complete = !page.hasMore; j.revision += 1
        try await checkSource(j.session)
        try await cache.saveTransferHistory(j,scope:scope,expectedEpoch:epoch,previousRevision:oldRevision,page:raw,streamID:stream.id,pageIndex:stream.pages)
        return j
    }
    func archive() async throws -> TransferHistoryArchive? {
        guard !busy else { throw CampError.message("History is already being downloaded") }
        busy=true; defer {busy=false}
        guard let j=try await current(), j.complete else { return nil }
        try await checkSource(j.session)
        let catalog=try await get(CloudChildHistoryList.self,base(j.session)+"/children?revision=\(j.session.transcriptRevision)")
        guard catalog.valid(parent:j.session.id), catalog.parentRevision==j.session.transcriptRevision,
              catalog.permissionRevision==j.session.permissionRevision,
              catalog.children==j.streams.dropFirst().compactMap(\.child) else { throw CampCloudError.invalidResponse }
        var result=TransferHistoryArchive(origin:scope.origin,session:j.session,conversations:[]), total=0
        for stream in j.streams {
            var messages:[CloudMessage]=[], cursor: Int64=0, textBytes=0
            for index in 0..<stream.pages {
                guard let raw=try await cache.transferHistoryPage(scope:scope,id:j.id,stream:stream.id,index:index),
                      PublishMessage.hash(raw)==stream.hashes[index] else { throw CampCloudError.invalidResponse }
                total += raw.count
                let page=try CampWire.decoder().decode(CloudTranscript.self,from:raw)
                var expected=stream; expected.before=cursor
                try validatePage(page,stream:expected,session:j.session)
                guard page.hasMore == (index+1<stream.pages) else { throw CampCloudError.invalidResponse }
                messages=page.messages+messages; cursor=page.before
                textBytes += page.messages.reduce(0){$0+$1.text.utf8.count}
            }
            guard messages.count==stream.messageCount, textBytes==stream.textBytes, cursor==stream.before,
                  Set(messages.map(\.id)).count==messages.count else { throw CampCloudError.invalidResponse }
            result.conversations.append(.init(id:stream.id,child:stream.child,messages:messages))
        }
        guard total==j.storedBytes else { throw CampCloudError.invalidResponse }
        try await checkSource(j.session)
        guard let current=try await current(), current.id==j.id, current.revision==j.revision else { throw CancellationError() }
        return result
    }
    func verifySource(_ session: CloudSession) async throws {
        guard let j=try await current(), j.complete, j.session==session else { throw CampCloudError.invalidResponse }
        try await checkSource(session)
        guard let after=try await current(), after.id==j.id, after.revision==j.revision else { throw CancellationError() }
    }
    func discard() async throws {
        guard let j=try await current() else { return }
        try await cache.discardTransferHistory(scope:scope,id:j.id,expectedEpoch:epoch)
    }
}
