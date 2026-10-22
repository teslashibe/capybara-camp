import Foundation

private actor ExportFixture: CampAccountTransport {
    var value: CloudWorkspaceExport
    let payload: Data
    var creates = Set<String>()
    var reads: [Int:Int] = [:]
    var loseCreate = true
    var rejectBytes = false
    var wrongOwner = false
    var corruptChunk = false
    var loseCancel = false
    var delayChunk = false
    init(_ value: CloudWorkspaceExport, payload: Data) { self.value = value; self.payload = payload }
    func configure(reject: Bool = false, owner: Bool = false, corrupt: Bool = false, cancel: Bool = false) {
        rejectBytes=reject; wrongOwner=owner; corruptChunk=corrupt; loseCancel=cancel
    }
    func counts() -> (Int,[Int:Int]) { (creates.count,reads) }
    func delay() { delayChunk = true }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        precondition(token == "fixture")
        if path.hasSuffix("/exports") {
            precondition(method == "POST" && key != nil)
            creates.insert(key!)
            if loseCreate { loseCreate=false; throw URLError(.networkConnectionLost) }
        } else if path.hasSuffix("/cancel") {
            precondition(method == "POST")
            value.state="cancelled"
            if loseCancel { loseCancel=false; throw URLError(.networkConnectionLost) }
        } else if path.contains("/chunks/") {
            precondition(method == "GET")
            if rejectBytes { throw CampCloudError.server(409,"revision_conflict") }
            let index=Int(path.split(separator:"/").last!)!
            reads[index,default:0] += 1
            if delayChunk { try await Task.sleep(for:.milliseconds(150)) }
            var part=Data(payload[(index*(256<<10))..<min(payload.count,(index+1)*(256<<10))])
            let hash=PublishMessage.hash(part)
            if corruptChunk { part[0] ^= 1 }
            return try CampWire.body(["data":part.base64EncodedString(),"sha256":hash])
        } else { precondition(method == "GET") }
        var result=value
        if wrongOwner { result.ownerUserId=UUID().uuidString }
        let encoder=CampWire.encoder()
        encoder.dateEncodingStrategy = .custom { date,encoder in
            var container=encoder.singleValueContainer()
            try container.encode(Date.ISO8601FormatStyle(includingFractionalSeconds:true).format(date))
        }
        return try encoder.encode(result)
    }
}

@main struct CampWorkspaceExportTests {
    static func rejects(_ label:String,_ action:()async throws->Void)async {
        do { try await action(); preconditionFailure(label) } catch {}
    }
    static func main()async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("workspace-export-"+UUID().uuidString)
        defer { try? FileManager.default.removeItem(at:root) }
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture"),token:"fixture",expiresAt:Date().addingTimeInterval(3600))
        let session=CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Codex",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:credential.actor.deviceId,localSessionId:UUID().uuidString,title:"Fixture",model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:4,transcriptRevision:3,permissionRevision:2,updatedAt:Date(),executionLocation:"cloud",executionEpoch:2,executionWorkspaceId:UUID().uuidString)
        let payload=Data(repeating:65,count:(256<<10)+29)
        let value=CloudWorkspaceExport(id:UUID().uuidString,campId:scope.campId,sessionId:session.id,ownerUserId:scope.userId,deviceId:credential.actor.deviceId,workspaceId:session.executionWorkspaceId!,workspaceRevision:3,sourceEpoch:2,sourceRevision:4,transcriptRevision:3,permissionRevision:2,kind:"capture",paths:["notes.txt"],state:"ready",resultSha256:PublishMessage.hash(payload),resultBytes:payload.count,expiresAt:Date().addingTimeInterval(600))
        let cache=try CampCache(directory:root),fixture=ExportFixture(value,payload:payload)
        let epoch=try await cache.epoch(scope)
        func client() throws -> CampWorkspaceExport { try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:fixture,epoch:epoch) }
        let first=try client()
        await rejects("Accepted traversal") { try await first.begin(session:session,kind:"capture",paths:["../secret"]) }
        var foreign=session; foreign.ownerUserId=UUID().uuidString
        await rejects("Accepted foreign session") { try await first.begin(session:foreign,kind:"capture",paths:[]) }
        try await first.begin(session:session,kind:"capture",paths:["notes.txt"])
        await rejects("Lost creation did not throw") { try await first.advance() }
        let restarted=try client()
        try await restarted.advance()
        let firstProgress=try await restarted.downloadedBytes()
        precondition(firstProgress == 256 << 10)
        let partial=try await restarted.payload(); precondition(partial == nil)
        var renewed=credential
        renewed.actor.deviceId=UUID().uuidString
        let resumed=try CampWorkspaceExport(cache:cache,scope:scope,credential:renewed,transport:fixture,epoch:epoch)
        try await resumed.advance()
        let finalProgress=try await resumed.downloadedBytes()
        precondition(finalProgress == payload.count)
        let complete=try await resumed.payload(); precondition(complete == payload)
        let (creates,reads)=await fixture.counts()
        precondition(creates == 1 && reads[1] == 1 && reads[0] == 2,"Restart repeated capture or download did not revalidate access")
        await fixture.configure(reject:true)
        await rejects("Cached bytes survived revoked authority") { _ = try await resumed.payload() }
        await fixture.configure(owner:true)
        await rejects("Accepted foreign response") { try await resumed.advance() }
        await fixture.configure(cancel:true)
        try await resumed.requestCancellation()
        let cancelledBytes=try await cache.workspaceExportChunk(scope:scope,id:value.id,index:0)
        precondition(cancelledBytes == nil,"Cancellation retained file bytes")
        await rejects("Lost cancel did not throw") { try await resumed.advance() }
        let cancelling=try client()
        try await cancelling.advance()
        let cancelledState=try await cancelling.current()?.export?.state
        precondition(cancelledState == "cancelled")
        let ended=try await cancelling.current()!
        try await cancelling.discard()
        let discarded=try await cancelling.current()
        precondition(discarded == nil)
        await rejects("A stale response recreated a discarded export") {
            try await cache.saveWorkspaceExport(ended,scope:scope,expectedEpoch:epoch)
        }
        try await cancelling.begin(session:session,kind:"capture",paths:["notes.txt"])
        await rejects("A stale view discarded a new export") {
            try await cache.saveWorkspaceExport(nil,scope:scope,expectedEpoch:epoch,discardingKey:ended.key)
        }
        // An in-flight save must not undo a persisted cancellation request.
        var stale=try await cancelling.current()!
        try await cancelling.requestCancellation()
        stale.cancelling=false
        try await cache.saveWorkspaceExport(stale,scope:scope,expectedEpoch:epoch)
        let stickyCancellation=try await cancelling.current()?.cancelling
        precondition(stickyCancellation == true)
        try await cache.clear(scope)
        await rejects("A cleared scope recreated its journal") { try await cancelling.advance() }
        let cleared=try await cache.metadata("workspace-export:"+scope.key)
        precondition(cleared == nil)

        let freshEpoch=try await cache.epoch(scope)
        let corruptFixture=ExportFixture(value,payload:payload)
        let corrupt=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:corruptFixture,epoch:freshEpoch)
        try await corrupt.begin(session:session,kind:"capture",paths:["notes.txt"])
        await rejects("Expected first lost create") { try await corrupt.advance() }
        await corruptFixture.configure(corrupt:true)
        await rejects("Accepted corrupt chunk") { try await corrupt.advance() }
        let rejectedChunk=try await cache.workspaceExportChunk(scope:scope,id:value.id,index:0)
        precondition(rejectedChunk == nil)
        await corruptFixture.configure()
        try await corrupt.advance(); try await corrupt.advance()
        // Simulate corrupt local storage. Full payload hash must reject it.
        try await cache.setMetadata("workspace-export-chunk:"+scope.key+":"+value.id+":0",Data(repeating:66,count:256<<10))
        await rejects("Accepted corrupt cached payload") { _ = try await corrupt.payload() }
        try await cache.clear(scope)
        let delayedFixture=ExportFixture(value,payload:payload)
        let delayed=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:delayedFixture,epoch:await cache.epoch(scope))
        try await delayed.begin(session:session,kind:"capture",paths:["notes.txt"])
        await rejects("Expected initial response loss") { try await delayed.advance() }
        await delayedFixture.delay()
        let inFlight=Task { try await delayed.advance() }
        for _ in 0..<500 {
            if await delayedFixture.counts().1[0] != nil { break }
            try await Task.sleep(for:.milliseconds(2))
        }
        let started=await delayedFixture.counts().1[0]
        precondition(started != nil,"Chunk request never started")
        try await cache.clear(scope)
        await rejects("In-flight download survived account erasure") { _ = try await inFlight.value }
        let erased=try await cache.workspaceExportChunk(scope:scope,id:value.id,index:0)
        precondition(erased == nil,"In-flight response recreated private bytes")
        print("PASS: cloud export lost-create recovery, chunk resume, authority, cancellation, scope erasure and integrity")
    }
}
