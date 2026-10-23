import Foundation

actor ExportFixture: CampAccountTransport {
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
    var checkoutDevice = ""
    var checkoutMismatch = ""
    var checkoutRequests = 0
    var checkoutRemoteBase: String?
    func checkout(device:String,mismatch:String="",remoteBase:String?=nil) { checkoutDevice=device; checkoutMismatch=mismatch; checkoutRemoteBase=remoteBase }
    func tokenRequests() -> Int { checkoutRequests }
    var historySession: CloudSession?
    func history(_ session:CloudSession) { historySession=session }
    func revokeHistory() { historySession?.permissionRevision += 1 }
    init(_ value: CloudWorkspaceExport, payload: Data) { self.value = value; self.payload = payload }
    func configure(reject: Bool = false, owner: Bool = false, corrupt: Bool = false, cancel: Bool = false) {
        rejectBytes=reject; wrongOwner=owner; corruptChunk=corrupt; loseCancel=cancel
    }
    func counts() -> (Int,[Int:Int]) { (creates.count,reads) }
    func delay() { delayChunk = true }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        precondition(token == "fixture")
        if let session=historySession, !path.contains("/exports") {
            if path.contains("/children?") {
                return try CampWire.encoder().encode(CloudChildHistoryList(parentRevision:session.transcriptRevision,permissionRevision:session.permissionRevision,children:[]))
            }
            if path.contains("/transcript?") {
                return try CampWire.encoder().encode(CloudTranscript(revision:session.transcriptRevision,messages:[CloudMessage(id:"history",ordinal:0,role:"user",text:"Original history",removed:false,revision:1)],before:0,hasMore:false))
            }
            return try CampWire.encoder().encode(session)
        }

        if path.hasSuffix("/checkout-token") {
            precondition(method=="POST" && key==nil)
            let input=try JSONSerialization.jsonObject(with:body!) as! [String:Any]
            precondition(input["sha256"] as? String==value.resultSha256)
            checkoutRequests += 1
            var result:[String:Any] = ["token":"ghu_"+String(repeating:"x",count:36),"expires_at":ISO8601DateFormatter().string(from:Date().addingTimeInterval(180)),
                "export_id":value.id,"device_id":checkoutDevice,"workspace_id":value.workspaceId,
                "repository":"fixture/repo","base_commit":String(repeating:"c",count:40),"branch":"main","sha256":value.resultSha256]
            if let checkoutRemoteBase {result["remote_base_commit"]=checkoutRemoteBase}
            if !checkoutMismatch.isEmpty { result[checkoutMismatch]="wrong" }
            return try CampWire.body(result)
        }
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

actor ReturnHandoffFixture: CampAccountTransport {
    let original: CloudSession
    let device: String
    let files: ExportFixture
    let payload: Data
    let review: TransferCapture
    var transfer: CloudTransfer?
    var handoff: CloudHandoff?
    var createKeys=Set<String>(),holdKeys=Set<String>(),commitKeys=Set<String>()
    var dropped=Set<String>()
    var dropAll=false

    var malformed=false
    init(session:CloudSession,device:String,files:ExportFixture,payload:Data,review:TransferCapture) {
        original=session;self.device=device;self.files=files;self.payload=payload;self.review=review
    }
    func losses(){dropAll=true}
func corrupt(){malformed=true}
    func finishIssued()async {
        precondition(handoff?.commitNonce != nil)
        handoff!.state="committed";handoff!.executionEpoch=original.executionEpoch!+1;transfer!.state="completed"
        await files.configure(reject:true);await files.history(projected())
    }
    func counts()->(Int,Int,Int){(createKeys.count,holdKeys.count,commitKeys.count)}
    private func deliver<T:Encodable>(_ value:T,_ stage:String)throws->Data {
        if dropAll && !dropped.contains(stage) {dropped.insert(stage);throw URLError(.networkConnectionLost)}
        return try CampWire.encoder().encode(value)
    }
    private func projected()->CloudSession {
        var session=original
        if let h=handoff,h.state=="committed",let d=h.localDestination {
            session.ownerDeviceId=device;session.profileId=d.profileId;session.localSessionId=d.sessionId
            session.executionLocation="local";session.executionWorkspaceId=nil;session.executionEpoch=h.executionEpoch
            session.revision+=1;session.permissionRevision+=1
        }
        return session
    }
    func data(_ method:String,path:String,body:Data?,key:String?,token:String?)async throws->Data {
        if path == "/v1/profiles" {
            precondition(method == "POST")
            let input = try JSONSerialization.jsonObject(with: body!) as! [String:String]
            return try CampWire.encoder().encode(CloudProfile(id:UUID().uuidString,ownerUserId:original.ownerUserId,
                ownerDeviceId:device,provider:input["provider"]!,localId:input["local_id"]!,label:input["label"]!))
        }
        if path.contains("/transfers") {
            if path.hasSuffix("/transfers") {
                precondition(method=="POST" && key != nil)
                let input=try JSONSerialization.jsonObject(with:body!) as! [String:Any]
                precondition(input["target_location"] as? String=="local" && input["payload_sha256"] as? String==review.payloadSha256)
                createKeys.insert(key!)
                if transfer==nil {
                    transfer=CloudTransfer(id:UUID().uuidString.lowercased(),campId:original.campId,sessionId:original.id,ownerUserId:original.ownerUserId,
                        sourceLocation:"cloud",sourceEpoch:original.executionEpoch!,sourceRevision:original.revision,sourceTranscriptRevision:original.transcriptRevision,
                        sourcePermissionRevision:original.permissionRevision,targetLocation:"local",targetAccountId:nil,payloadSha256:review.payloadSha256,
                        reviewSha256:review.reviewSha256,payloadBytes:payload.count,uploadedBytes:0,uploadedChunks:[],chunkBytes:256<<10,
                        state:"uploading",revision:1,expiresAt:Date().addingTimeInterval(600),baseCommit:nil,fileCount:0,
                        sourceWorkspaceId:original.executionWorkspaceId,targetDeviceId:device)
                }
                return try deliver(transfer!,"create")
            }
            if path.contains("/chunks/") {
                precondition(method=="PUT")
                let index=Int(path.split(separator:"/").last!)!
                let input=try JSONSerialization.jsonObject(with:body!) as! [String:String]
                let part=Data(base64Encoded:input["data"]!)!
                precondition(part==payload.subdata(in:(index*(256<<10))..<min(payload.count,(index+1)*(256<<10))))
                precondition(input["sha256"]==PublishMessage.hash(part))
                if !transfer!.uploadedChunks!.contains(index){transfer!.uploadedChunks!.append(index);transfer!.uploadedBytes+=part.count;transfer!.revision+=1}
                return try deliver(transfer!,"chunk-\(index)")
            }
            if path.hasSuffix("/complete") {
                precondition(transfer?.uploadedBytes==payload.count)
                if transfer!.state=="uploading"{transfer!.state="ready";transfer!.baseCommit=review.baseCommit;transfer!.fileCount=review.files.count;transfer!.revision+=1}
                return try deliver(transfer!,"complete")
            }
            if path.hasSuffix("/handoffs") {
                precondition(method=="POST" && key != nil);holdKeys.insert(key!)
                let input=try JSONSerialization.jsonObject(with:body!) as! [String:Any]
                let local=try CampWire.decoder().decode(CloudLocalDestination.self,from:JSONSerialization.data(withJSONObject:input["local_destination"]!))
                if handoff==nil {handoff=CloudHandoff(id:UUID().uuidString.lowercased(),transferId:transfer!.id,campId:original.campId,sessionId:original.id,ownerUserId:original.ownerUserId,
                    sourceDeviceId:original.ownerDeviceId,sourceLocation:"cloud",sourceWorkspaceId:original.executionWorkspaceId,sourceEpoch:original.executionEpoch!,
                    workspaceId:"",workspaceRevision:0,state:"pending",claimId:nil,revision:1,expiresAt:Date().addingTimeInterval(300),commitNonce:nil,executionEpoch:nil,
                    targetLocation:"local",targetDeviceId:device,localDestination:local)}
                return try deliver(handoff!,"hold")
            }
            if path.hasSuffix("/cancel") {
                guard transfer!.state != "completed" else {throw CampCloudError.server(409,"transition")}
                transfer!.state="cancelled";transfer!.revision+=1
                if handoff != nil{handoff!.state="released";handoff!.revision+=1}
                return try deliver(transfer!,"cancel")
            }
            precondition(method=="GET");return try CampWire.encoder().encode(transfer!)
        }
        if path.contains("/handoffs/") {
            if path.hasSuffix("/commit") {
                precondition(method=="POST" && key != nil);commitKeys.insert(key!)
                let input=try JSONSerialization.jsonObject(with:body!) as! [String:Any]
                precondition(input["context_continuation_approved"] as? Bool==true)
                if handoff!.localDestination!.purpose=="implementation" {precondition(input["execution_approved"] as? Bool==true)}
                if handoff!.commitNonce==nil{handoff!.commitNonce=UUID().uuidString.lowercased();handoff!.revision+=1}
                return try deliver(handoff!,"commit")
            }
            if handoff!.commitNonce != nil {
                handoff!.state="committed";handoff!.executionEpoch=original.executionEpoch!+1;transfer!.state="completed"
                await files.configure(reject:true)
                await files.history(projected())
            } else if handoff!.state=="pending" {handoff!.state="acknowledged";handoff!.revision+=1}
            var response=handoff!
            if malformed{response.localDestination!.profileId=UUID().uuidString.lowercased()}
            return try CampWire.encoder().encode(response)
        }
        if handoff?.state=="committed",path.hasSuffix("/sessions/"+original.id) {return try CampWire.encoder().encode(projected())}
        return try await files.data(method,path:path,body:body,key:key,token:token)
    }
}
