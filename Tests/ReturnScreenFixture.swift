import Foundation

@MainActor final class ReturnScreenFixture {
    let cache: CampCache
    let scope: CloudScope
    let auth: CloudCredential
    let session: CloudSession
    let source: CampSource
    let transport: ReturnHandoffFixture
    let bridge: DestinationBridgeFixture
    let epoch: Int64
    init(directory: URL, provider: String, unpublishedCommits: Bool = false) async throws {
        scope = CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        auth = CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture"),token:"fixture",expiresAt:Date().addingTimeInterval(3600))
        session = CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,
            provider:provider,profileLabel:"Cloud test account",ownerUserId:scope.userId,ownerDeviceId:UUID().uuidString,
            localSessionId:UUID().uuidString,title:"Bring the project back to this Mac",model:"fixture",state:"idle",stage:"Ready",
            shared:false,allowRequests:false,externalOwner:false,revision:4,transcriptRevision:3,permissionRevision:2,updatedAt:Date(),
            executionLocation:"cloud",executionEpoch:2,executionWorkspaceId:UUID().uuidString)
        source = CampSource(id:"fixture-profile",provider:provider,root:"/fixture/profile",label:"Personal test account",count:0)
        let text = Data("Reviewed notes from the cloud workspace\n".utf8)
        var manifest: [String:Any] = ["version":1,"base_commit":String(repeating:"c",count:40),"branch":"main",
            "files":[["path":"notes.txt","data":text.base64EncodedString(),"sha256":PublishMessage.hash(text)]],"excluded":[]]
        if unpublishedCommits { manifest["commits"] = ["fixture":"synthetic archive; Go tests verify real objects"] }
        let payload = try CampWire.body(manifest)
        let exported = CloudWorkspaceExport(id:UUID().uuidString,campId:scope.campId,sessionId:session.id,ownerUserId:scope.userId,
            deviceId:auth.actor.deviceId,workspaceId:session.executionWorkspaceId!,workspaceRevision:3,sourceEpoch:2,sourceRevision:4,
            transcriptRevision:3,permissionRevision:2,kind:"capture",paths:["notes.txt"],state:"ready",resultSha256:PublishMessage.hash(payload),
            resultBytes:payload.count,expiresAt:Date().addingTimeInterval(3600))
        var review = TransferCapture(payloadSha256:exported.resultSha256,reviewSha256:String(repeating:"b",count:64),
            baseCommit:String(repeating:"c",count:40),branch:"main",files:[.init(path:"notes.txt",bytes:text.count,deleted:false)],excluded:[])
        if unpublishedCommits {
            review.commits = .init(baseCommit:String(repeating:"a",count:40),headCommit:review.baseCommit,reviewSha256:String(repeating:"d",count:64),commits:2,objects:6,bytes:500)
        }
        let files = ExportFixture(exported,payload:payload)
        await files.checkout(device:auth.actor.deviceId,remoteBase:review.commits?.baseCommit);await files.history(session)
        transport = ReturnHandoffFixture(session:session,device:auth.actor.deviceId,files:files,payload:payload,review:review)
        bridge = DestinationBridgeFixture(review)
        cache = try CampCache(directory:directory);epoch = try await cache.epoch(scope)
        let capture = try CampWorkspaceExport(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch)
        try await capture.begin(session:session,kind:"capture",paths:["notes.txt"])
        do { _ = try await capture.advance();preconditionFailure("Missing fixture loss") } catch is URLError {}
        _ = try await capture.advance()
    }
    func screen(automatic: Bool = false) -> CampReturnTransfer {
        let screen = CampReturnTransfer(cache:cache,automatic:automatic),bridge = bridge
        screen.callLocal = { try await bridge.call($0,$1) }
        screen.attach(scope:scope,credential:auth,transport:transport)
        return screen
    }
}
