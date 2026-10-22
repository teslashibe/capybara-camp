import Foundation

actor TransferFixture: CampAccountTransport {
    let session: CloudSession
    var transfer: CloudTransfer?
    var workspace: CloudWorkspace?
    var handoff: CloudHandoff?
    var receipts: [String:Data] = [:]
    var creates = 0
    var loseCreate = false
    var rejectCreate = false
    var loseCancel = false
    var finish = false
    var chunks: [Int:Data] = [:]
    init(_ session:CloudSession){self.session=session}
    func configure(lose:Bool=false,reject:Bool=false,cancel:Bool=false){loseCreate=lose;rejectCreate=reject;loseCancel=cancel}
    func complete(){finish=true}
    func counts()->Int{creates}
    func data(_ method:String,path:String,body:Data?,key:String?,token:String?) async throws ->Data {
        if let key,let saved=receipts[key]{return saved}
        if method=="GET" {
            if path.contains("/handoffs/") {
                if finish {handoff?.state="committed";handoff?.executionEpoch=(session.executionEpoch ?? 1)+1}
                else if handoff?.state=="pending" {handoff?.state="claimed"}
                else if handoff?.state=="claimed" {handoff?.state="acknowledged"}
                return try CampWire.encoder().encode(handoff!)
            }
            if path.contains("/workspaces/"){workspace?.state="ready";return try CampWire.encoder().encode(workspace!)}
            return try CampWire.encoder().encode(transfer!)
        }
        let object=try JSONSerialization.jsonObject(with:body ?? Data("{}".utf8)) as! [String:Any]
        let result:Data
        if path.hasSuffix("/transfers") {
            if rejectCreate {rejectCreate=false;throw CampCloudError.server(409,"changed")}
            creates+=1
            transfer=CloudTransfer(id:UUID().uuidString.lowercased(),campId:session.campId,sessionId:session.id,ownerUserId:session.ownerUserId,sourceLocation:"local",sourceEpoch:session.executionEpoch ?? 1,sourceRevision:session.revision,sourceTranscriptRevision:session.transcriptRevision,sourcePermissionRevision:session.permissionRevision,targetLocation:"cloud",targetAccountId:object["target_account_id"] as? String,payloadSha256:object["payload_sha256"] as! String,reviewSha256:object["review_sha256"] as! String,payloadBytes:object["payload_bytes"] as! Int,uploadedBytes:0,uploadedChunks:[],chunkBytes:256<<10,state:"uploading",revision:1,expiresAt:Date().addingTimeInterval(3600),fileCount:0)
            result=try CampWire.encoder().encode(transfer!)
        } else if path.contains("/chunks/") {
            let bytes=Data(base64Encoded:object["data"] as! String)!
            precondition(PublishMessage.hash(bytes)==object["sha256"] as? String)
            let ordinal=Int(path.split(separator:"/").last!)!
            if !(transfer?.uploadedChunks?.contains(ordinal) ?? false) {transfer?.uploadedBytes+=bytes.count;transfer?.uploadedChunks?.append(ordinal)}
            chunks[ordinal]=bytes
            result=try CampWire.encoder().encode(transfer!)
        } else if path.hasSuffix("/complete") {
            let payload=chunks.keys.sorted().reduce(into:Data()){$0.append(chunks[$1]!)}
            let manifest=try JSONSerialization.jsonObject(with:payload) as! [String:Any]
            transfer?.state="ready";transfer?.baseCommit=manifest["base_commit"] as? String
            transfer?.fileCount=(manifest["files"] as? [Any])?.count ?? 0
            result=try CampWire.encoder().encode(transfer!)
        } else if path.hasSuffix("/workspaces") {
            let input=try CampWire.decoder().decode(CloudWorkspaceInput.self,from:body!)
            let id=UUID().uuidString.lowercased()
            workspace=CloudWorkspace(id:id,campId:session.campId,sessionId:session.id,projectId:session.projectId,ownerUserId:session.ownerUserId,providerAccountId:input.providerAccountId,providerInstallationId:UUID().uuidString,providerEpoch:1,gitConnectionId:input.gitConnectionId,gitEpoch:1,githubUserId:123,githubLogin:"fixture",githubInstallationId:7,repositoryId:5,repositoryName:"fixture/repo",requestedRef:input.ref,baseCommit:input.ref,branch:"camp/\(session.id)/\(id)",purpose:input.purpose,sourceRevision:session.revision,projectRevision:1,state:"queued",revision:1,createdAt:Date(),updatedAt:Date(),transferId:transfer!.id)
            result=try CampWire.encoder().encode(workspace!)
        } else if path.hasSuffix("/handoffs") {
            handoff=CloudHandoff(id:UUID().uuidString.lowercased(),transferId:transfer!.id,campId:session.campId,sessionId:session.id,ownerUserId:session.ownerUserId,sourceDeviceId:session.ownerDeviceId,sourceLocation:"local",sourceEpoch:session.executionEpoch ?? 1,workspaceId:workspace!.id,workspaceRevision:workspace!.revision,state:"pending",claimId:UUID().uuidString,revision:1,expiresAt:Date().addingTimeInterval(300))
            result=try CampWire.encoder().encode(handoff!)
        } else if path.hasSuffix("/commit") {
            handoff?.commitNonce=UUID().uuidString.lowercased();finish=true
            result=try CampWire.encoder().encode(handoff!)
        } else if path.hasSuffix("/cancel") {
            transfer?.state="cancelled";result=try CampWire.encoder().encode(transfer!)
        } else {throw CampCloudError.invalidResponse}
        if let key {receipts[key]=result}
        if path.hasSuffix("/transfers"),loseCreate {loseCreate=false;throw URLError(.networkConnectionLost)}
        if path.hasSuffix("/cancel"),loseCancel {loseCancel=false;throw URLError(.networkConnectionLost)}
        return result
    }
}
