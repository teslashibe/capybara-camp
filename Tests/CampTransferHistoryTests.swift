import Foundation

private actor HistoryDownloadFixture: CampAccountTransport {
    var session: CloudSession
    var child: CloudChildHistory
    var mode = ""
    var pageReads: [String:Int] = [:]
    var waiting = false
    init(session: CloudSession) {
        self.session=session
        child=CloudChildHistory(id:UUID().uuidString,parentId:session.id,sourceKey:String(repeating:"a",count:64),sourceRevision:String(repeating:"b",count:64),name:"Review helper",phase:"review",workflow:"Plan",state:"done",model:"child-model",complete:true,removed:false,revision:2,transcriptRevision:1)
    }
    func configure(_ value: String) { mode=value }
    func reads() -> [String:Int] { pageReads }
    func isWaiting() -> Bool { waiting }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        precondition(method=="GET" && token=="fixture" && body==nil && key==nil)
        let parts=URLComponents(string:"https://fixture.example"+path)!
        let values=Dictionary(uniqueKeysWithValues:(parts.queryItems ?? []).map{($0.name,$0.value ?? "")})
        let encoder=CampWire.encoder()
        if parts.path.hasSuffix("/children") {
            precondition(values["revision"]=="3")
            var c=child
            if mode=="incomplete" { c.complete=false }
            return try encoder.encode(CloudChildHistoryList(parentRevision:3,permissionRevision:2,children:[c]))
        }
        if parts.path.hasSuffix("/transcript") {
            precondition(values["revision"]=="3" && values["limit"]=="120")
            let isChild=parts.path.contains("/children/"), before=Int64(values["before"]!)!
            let id=(isChild ? "child" : "main")+":"+String(before)
            pageReads[id,default:0] += 1
            if mode=="lost" { mode=""; throw URLError(.networkConnectionLost) }
            if mode=="delay" { waiting=true; try await Task.sleep(for:.milliseconds(150)) }
            if isChild {
                let message=CloudMessage(id:"child-answer",ordinal:0,role:"assistant",text:"Full child review",removed:false,model:"recorded",effort:"high",speed:"fast",revision:1)
                var c=child
                if mode=="wrong-child" { c.sourceRevision=String(repeating:"c",count:64) }
                return try encoder.encode(CloudChildTranscript(parentRevision:3,permissionRevision:2,child:c,transcript:CloudTranscript(revision:1,messages:[message],before:0,hasMore:false)))
            }
            let positions: [Int64]=before==0 ? [1,2] : [0]
            var messages=positions.map { CloudMessage(id:"main-\($0)",ordinal:$0,role:$0==0 ? "user" : "assistant",text:"Full original message \($0)",removed:false,model:"recorded",effort:"high",speed:"ultrafast",revision:1) }
            var page=CloudTranscript(revision:3,messages:messages,before:positions[0],hasMore:before==0)
            if mode=="wrong-revision" { page.revision=4 }
            if mode=="bad-cursor" { page.before=99 }
            if mode=="removed" { page.messages[0].removed=true }
            if mode=="duplicate" && before>0 { messages[0].id="main-1"; page.messages=messages }
            return try encoder.encode(page)
        }
        precondition(parts.path.hasSuffix(session.id))
        if mode=="revoked" { throw CampCloudError.server(403,"denied") }
        var result=session
        if mode=="changed" { result.transcriptRevision += 1 }
        if mode=="foreign" { result.ownerUserId=UUID().uuidString }
        return try encoder.encode(result)
    }
}

@main struct CampTransferHistoryTests {
    static func rejects(_ label: String, _ action: () async throws -> Void) async {
        do { try await action(); preconditionFailure(label) } catch {}
    }
    static func main() async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("history-transfer-"+UUID().uuidString)
        defer { try? FileManager.default.removeItem(at:root) }
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture"),token:"fixture",expiresAt:Date().addingTimeInterval(3600))
        let session=CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Codex",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:credential.actor.deviceId,localSessionId:UUID().uuidString,title:"Original plan",model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:4,transcriptRevision:3,permissionRevision:2,updatedAt:Date(),executionLocation:"cloud",executionEpoch:2,executionWorkspaceId:UUID().uuidString)
        let cache=try CampCache(directory:root)
        let fixture=HistoryDownloadFixture(session:session)
        func client(_ auth: CloudCredential? = nil) async throws -> CampTransferHistory {
            try CampTransferHistory(cache:cache,scope:scope,credential:auth ?? credential,transport:fixture,epoch:await cache.epoch(scope))
        }
        let initial=try await client()
        try await initial.begin(session:session)
        await fixture.configure("lost")
        await rejects("Lost reply unexpectedly succeeded") { try await initial.advance() }
        let afterLoss=try await initial.current()!
        precondition(afterLoss.streams[0].pages==0)
        try await initial.advance()
        let partial=try await initial.archive(); precondition(partial==nil)
        var renewed=credential; renewed.actor.deviceId=UUID().uuidString
        let resumed=try await client(renewed)
        try await resumed.advance(); try await resumed.advance()
        let archive=try await resumed.archive()!
        precondition(archive.session.id==session.id && archive.conversations.count==2)
        precondition(archive.conversations[0].messages.map(\.id)==["main-0","main-1","main-2"])
        precondition(archive.conversations[1].child?.sourceKey==String(repeating:"a",count:64))
        precondition(archive.conversations[1].messages[0].effort=="high" && archive.conversations[1].messages[0].speed=="fast")
        let counts=await fixture.reads()
        precondition(counts["main:0"]==2 && counts["main:1"]==1 && counts["child:0"]==1,"Restart redownloaded saved pages")
        for mode in ["revoked","changed","foreign"] {
            await fixture.configure(mode)
            await rejects("Cached archive bypassed current authority") { _ = try await resumed.archive() }
        }
        await fixture.configure("")
        let completed=try await resumed.current()!
        var missingChild=completed
        let childStream=missingChild.streams.removeLast()
        for index in 0..<childStream.pages {
            let raw=try await cache.transferHistoryPage(scope:scope,id:completed.id,stream:childStream.id,index:index)!
            missingChild.storedBytes -= raw.count
        }
        try await cache.setMetadata("transfer-history:"+scope.key,CampWire.encoder().encode(missingChild))
        await rejects("Cached journal silently omitted a child") { _ = try await resumed.archive() }
        try await cache.setMetadata("transfer-history:"+scope.key,CampWire.encoder().encode(completed))
        let pageKey="transfer-history-page:"+scope.key+":"+completed.id+":"+session.id+":0"
        try await cache.setMetadata(pageKey,Data("corrupt".utf8))
        await rejects("Corrupt cached history escaped") { _ = try await resumed.archive() }
        try await resumed.discard()
        let discarded=try await cache.metadata(pageKey); precondition(discarded==nil)
        await rejects("Stale write resurrected discarded history") {
            var stale=completed; stale.revision += 1
            try await cache.saveTransferHistory(stale,scope:scope,expectedEpoch:await cache.epoch(scope),previousRevision:completed.revision,page:Data("{}".utf8),streamID:session.id,pageIndex:2)
        }
        for mode in ["wrong-revision","bad-cursor","removed","incomplete","wrong-child","duplicate"] {
            await fixture.configure("")
            let c=try await client()
            if mode=="incomplete" {
                await fixture.configure(mode)
                await rejects("Incomplete child catalog accepted") { try await c.begin(session:session) }
                continue
            }
            try await c.begin(session:session)
            await fixture.configure(mode)
            if mode=="wrong-child" || mode=="duplicate" {
                try await c.advance(); try await c.advance()
                if mode=="wrong-child" { await rejects("Wrong child snapshot accepted") { try await c.advance() } }
                else { try await c.advance(); await rejects("Duplicate message across pages accepted") { _ = try await c.archive() } }
            } else { await rejects("Invalid history page accepted") { try await c.advance() } }
            try await c.discard()
        }
        await fixture.configure("")
        let delayed=try await client()
        try await delayed.begin(session:session)
        let pending=try await delayed.current()!
        await fixture.configure("delay")
        let task=Task { try await delayed.advance() }
        for _ in 0..<500 {
            if await fixture.isWaiting() { break }
            try await Task.sleep(for:.milliseconds(1))
        }
        let waiting=await fixture.isWaiting(); precondition(waiting)
        try await cache.clear(scope)
        await rejects("Cleared account accepted in-flight history") { _ = try await task.value }
        let missing=try await cache.metadata("transfer-history:"+scope.key)
        let missingPage=try await cache.transferHistoryPage(scope:scope,id:pending.id,stream:session.id,index:0)
        precondition(missing==nil && missingPage==nil)
        print("Pinned history download, restart, identity, cursor, corruption and erasure checks passed")
    }
}
