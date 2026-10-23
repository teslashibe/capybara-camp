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
    var checkoutDevice = ""
    var checkoutMismatch = ""
    var checkoutRequests = 0
    func checkout(device:String,mismatch:String="") { checkoutDevice=device; checkoutMismatch=mismatch }
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

private actor DestinationBridgeFixture {
    let review: TransferCapture
    var checkoutCalls=0
    var installCalls=0
    var loseCheckout=false
    var loseInstall=false
    var delayInstall=false
    var installStarted=false
    init(_ review:TransferCapture) { self.review=review }
    func lose(_ phase:String) { loseCheckout=phase=="checkout";loseInstall=phase=="install" }
    func delay() { delayInstall=true }
    func started() -> Bool { installStarted }
    func counts() -> (Int,Int) { (checkoutCalls,installCalls) }
    func call(_ action:String,_ data:Data) async throws -> Data {
        if action=="transfer-review" { return try CampWire.encoder().encode(review) }
        let input=try JSONSerialization.jsonObject(with:data) as! [String:Any]
        if action=="transfer-checkout" {
            checkoutCalls+=1
            let scope=input["scope"] as! String,id=input["transferID"] as! String
            let receipt=TransferCheckoutReceipt(version:1,scope:scope,transferID:id,repository:"fixture/repo",baseCommit:review.baseCommit,branch:review.branch,destination:"/fixture/workspace-\(scope)-\(id)")
            if loseCheckout { loseCheckout=false;throw URLError(.networkConnectionLost) }
            return try JSONEncoder().encode(receipt)
        }
        precondition(action=="transfer-install")
        installCalls+=1;installStarted=true
        if delayInstall { try await Task.sleep(for:.milliseconds(180)) }
        let target=try JSONDecoder().decode(TransferInstallationTarget.self,from:JSONSerialization.data(withJSONObject:input["target"]!))
        let exported=try CampWire.decoder().decode(CloudWorkspaceExport.self,from:JSONSerialization.data(withJSONObject:input["export"]!))
        let receipt=TransferInstallationReceipt(version:1,scope:input["scope"] as! String,transferID:input["transferID"] as! String,
            exportID:exported.id,destination:input["destination"] as! String,target:target,
            payloadSHA256:input["payloadSHA256"] as! String,historySHA256:input["historySHA256"] as! String,
            sourceID:exported.sessionId,transcriptRevision:exported.transcriptRevision,
            files:.init(reviewSHA256:review.reviewSha256,baseCommit:review.baseCommit,branch:review.branch,files:review.files.count))
        let encoder=JSONEncoder();encoder.outputFormatting = .sortedKeys
        let result=TransferInstallationResult(receipt:receipt,receiptSHA256:PublishMessage.hash(try encoder.encode(receipt)))
        if loseInstall { loseInstall=false;throw URLError(.networkConnectionLost) }
        return try JSONEncoder().encode(result)
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
        for changedAfterInstall in [false,true] {
            try await cache.clear(scope)
            let installationFixture=ExportFixture(value,payload:payload)
            await installationFixture.history(session)
            let installEpoch=try await cache.epoch(scope)
            let files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:installationFixture,epoch:installEpoch)
            let history=try CampTransferHistory(cache:cache,scope:scope,credential:credential,transport:installationFixture,epoch:installEpoch)
            try await files.begin(session:session,kind:"capture",paths:["notes.txt"])
            await rejects("Expected lost export create") { try await files.advance() }
            try await files.advance();try await files.advance()
            try await history.begin(session:session);try await history.advance()
            let archive=try await history.archive()!
            let target=TransferInstallationTarget(profileID:UUID().uuidString,sourceID:"fixture-source",provider:"Codex",root:"/fixture/profile",sessionID:UUID().uuidString,purpose:"planning")
            let transferID=UUID().uuidString
            let review=TransferCapture(payloadSha256:value.resultSha256,reviewSha256:String(repeating:"b",count:64),baseCommit:String(repeating:"c",count:40),branch:"main",files:[.init(path:"notes.txt",bytes:payload.count,deleted:false)],excluded:[])
            let receipt=TransferInstallationReceipt(version:1,scope:scope.key,transferID:transferID,exportID:value.id,destination:"/fixture/checkout",target:target,payloadSHA256:value.resultSha256,historySHA256:PublishMessage.hash(try CampWire.encoder().encode(archive)),sourceID:session.id,transcriptRevision:session.transcriptRevision,files:.init(reviewSHA256:review.reviewSha256,baseCommit:review.baseCommit,branch:review.branch,files:1))
            let result=TransferInstallationResult(receipt:receipt,receiptSHA256:String(repeating:"d",count:64))
            let bridge: @Sendable (String,Data) async throws -> Data = { action,body in
                if action=="transfer-review" { precondition(body==payload);return try CampWire.encoder().encode(review) }
                precondition(action=="transfer-install")
                let input=try JSONSerialization.jsonObject(with:body) as! [String:Any]
                precondition(input["historySHA256"] as? String==receipt.historySHA256)
                if changedAfterInstall { await installationFixture.revokeHistory() }
                return try JSONEncoder().encode(result)
            }
            if changedAfterInstall {
                await rejects("Installer returned a receipt after source permission changed") {
                    _ = try await files.installLocally(history:history,transferID:transferID,destination:"/fixture/checkout",target:target,reviewedSHA256:review.reviewSha256,callLocal:bridge)
                }
            } else {
                let installed=try await files.installLocally(history:history,transferID:transferID,destination:"/fixture/checkout",target:target,reviewedSHA256:review.reviewSha256,callLocal:bridge)
                precondition(installed==result)
                await rejects("Installer accepted a different reviewed digest") {
                    _ = try await files.installLocally(history:history,transferID:transferID,destination:"/fixture/checkout",target:target,reviewedSHA256:String(repeating:"e",count:64),callLocal:bridge)
                }
            }
        }
        for mismatch in ["", "device_id", "workspace_id", "sha256", "base_commit", "repository", "changed-after-checkout", "cancel-during-checkout", "erase-during-checkout", "receipt"] {
            try await cache.clear(scope)
            let checkoutFixture=ExportFixture(value,payload:payload)
            await checkoutFixture.checkout(device:credential.actor.deviceId,mismatch:["changed-after-checkout","cancel-during-checkout","erase-during-checkout","receipt"].contains(mismatch) ? "" : mismatch)
            let files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:checkoutFixture,epoch:await cache.epoch(scope))
            try await files.begin(session:session,kind:"capture",paths:["notes.txt"])
            await rejects("Expected lost create") { try await files.advance() }
            try await files.advance();try await files.advance()
            let transferID=UUID().uuidString.lowercased()
            let review=TransferCapture(payloadSha256:value.resultSha256,reviewSha256:String(repeating:"b",count:64),baseCommit:String(repeating:"c",count:40),branch:"main",files:[.init(path:"notes.txt",bytes:payload.count,deleted:false)],excluded:[])
            let receipt=TransferCheckoutReceipt(version:1,scope:scope.key,transferID:transferID,repository:"fixture/repo",baseCommit:review.baseCommit,branch:review.branch,destination:"/fixture/workspace-\(scope.key)-\(transferID)")
            let bridge: @Sendable (String,Data) async throws -> Data = { action,body in
                if action=="transfer-review" { precondition(body==payload);return try CampWire.encoder().encode(review) }
                precondition(["","changed-after-checkout","cancel-during-checkout","erase-during-checkout","receipt"].contains(mismatch),"Invalid grant reached the helper")
                precondition(action=="transfer-checkout")
                let input=try JSONSerialization.jsonObject(with:body) as! [String:Any]
                let secret=input["credential"] as! [String:String]
                precondition(secret["url"]=="https://github.com/fixture/repo.git" && secret["token"]=="ghu_"+String(repeating:"x",count:36))
                precondition(input["scope"] as? String==scope.key && input["transferID"] as? String==transferID)
                if mismatch=="changed-after-checkout" { await checkoutFixture.configure(reject:true) }
                if mismatch=="cancel-during-checkout" { try await files.requestCancellation() }
                if mismatch=="erase-during-checkout" { try await cache.clear(scope) }
                var result=receipt
                if mismatch=="receipt" { result.baseCommit=String(repeating:"f",count:40) }
                return try JSONEncoder().encode(result)
            }
            await rejects("Unreviewed checkout requested a credential") {
                _ = try await files.prepareLocalCheckout(transferID:transferID,reviewedSHA256:"wrong",callLocal:bridge)
            }
            let unreviewedRequests=await checkoutFixture.tokenRequests();precondition(unreviewedRequests==0)
            if mismatch.isEmpty {
                let prepared=try await files.prepareLocalCheckout(transferID:transferID,reviewedSHA256:review.reviewSha256,callLocal:bridge)
                precondition(prepared==receipt)
                let journal=try await cache.metadata("workspace-export:"+scope.key)!
                precondition(!String(decoding:journal,as:UTF8.self).contains("ghu_"),"Credential cached in export journal")
            } else {
                await rejects("Checkout returned a receipt after \(mismatch)") {
                    _ = try await files.prepareLocalCheckout(transferID:transferID,reviewedSHA256:review.reviewSha256,callLocal:bridge)
                }
            }
        }
        for (provider,loss) in ["Codex","Claude"].flatMap({ provider in ["none","checkout","install","cancel","erase"].map { (provider,$0) } }) {
            var scenarioSession=session;scenarioSession.provider=provider
            try await cache.clear(scope)
            let fixture=ExportFixture(value,payload:payload)
            await fixture.checkout(device:credential.actor.deviceId)
            await fixture.history(scenarioSession)
            let destinationEpoch=try await cache.epoch(scope)
            let files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:fixture,epoch:destinationEpoch)
            let history=try CampTransferHistory(cache:cache,scope:scope,credential:credential,transport:fixture,epoch:destinationEpoch)
            try await files.begin(session:scenarioSession,kind:"capture",paths:["notes.txt"])
            await rejects("Expected lost create") { try await files.advance() }
            try await files.advance();try await files.advance()
            try await history.begin(session:scenarioSession);try await history.advance()
            let review=TransferCapture(payloadSha256:value.resultSha256,reviewSha256:String(repeating:"b",count:64),baseCommit:String(repeating:"c",count:40),branch:"main",files:[.init(path:"notes.txt",bytes:payload.count,deleted:false)],excluded:[])
            let bridge=DestinationBridgeFixture(review)
            await bridge.lose(loss)
            let target=TransferInstallationTarget(profileID:UUID().uuidString.lowercased(),sourceID:"fixture-source",provider:provider,root:"/fixture/profile",sessionID:UUID().uuidString.lowercased(),purpose:"implementation")
            func destination(_ login:CloudCredential=credential) throws -> CampLocalDestination {
                try CampLocalDestination(cache:cache,scope:scope,credential:login,transport:fixture,epoch:destinationEpoch,callLocal:{try await bridge.call($0,$1)})
            }
            let first=try destination()
            try await first.begin(target:target,reviewedSHA256:review.reviewSha256)
            let initial=try await first.current()!
            precondition(initial.phase=="checkout" && initial.target==target)
            await rejects("Duplicate preparation replaced the saved target") { try await destination().begin(target:target,reviewedSHA256:review.reviewSha256) }
            var foreign=credential;foreign.actor.deviceId=UUID().uuidString
            await rejects("Another device adopted local preparation") { _=try await destination(foreign).current() }
            if loss=="checkout" { await rejects("Lost checkout did not throw") { _=try await first.advance() } }
            let restarted=try destination()
            let checkout=try await restarted.advance()!
            precondition(checkout.id==initial.id && checkout.phase=="installing" && checkout.checkout?.transferID==initial.id)
            if loss=="install" { await rejects("Lost installer did not throw") { _=try await restarted.advance() } }
            if ["cancel","erase"].contains(loss) {
                await bridge.delay()
                let running=Task {try await restarted.advance()}
                for _ in 0..<500 { if await bridge.started(){break};try await Task.sleep(for:.milliseconds(2)) }
                let started=await bridge.started();precondition(started)
                if loss=="cancel" { try await first.requestCancellation() } else { try await cache.clear(scope) }
                await rejects("Late helper receipt survived \(loss)") { _=try await running.value }
                if loss=="cancel" {
                    let cancelled=try await destination().advance()!
                    precondition(cancelled.phase=="cancelled" && cancelled.installation==nil)
                    try await destination().discardCancelled()
                    let discarded=try await destination().current()
                    let discardedExport=try await files.current()
                    let discardedHistory=try await history.current()
                    precondition(discarded==nil && discardedExport==nil && discardedHistory==nil)
                    await rejects("A discarded preparation was recreated") { try await cache.saveLocalDestination(cancelled,scope:scope,expectedEpoch:destinationEpoch,previousRevision:cancelled.revision-1) }

                    await rejects("Stale saved progress removed cancellation") { try await cache.saveLocalDestination(checkout,scope:scope,expectedEpoch:destinationEpoch,previousRevision:initial.revision) }
                } else { let cleared=try await cache.metadata("local-destination:"+scope.key);precondition(cleared==nil) }
            } else {
                let installed=try await destination().advance()!
                precondition(installed.phase=="installed" && installed.target==target && installed.installation?.receipt.transferID==initial.id)
                let verified=try await destination().advance()!
                precondition(verified.installation==installed.installation)
                let counts=await bridge.counts()
                precondition(counts.0==(loss=="checkout" ? 2 : 1),"Restart reset an installed checkout")
                precondition(counts.1==(loss=="install" ? 3 : 2))
                let raw=try await cache.metadata("local-destination:"+scope.key)!
                precondition(!String(decoding:raw,as:UTF8.self).contains("ghu_"),"Destination journal contains a Git token")
                var changed=installed;changed.target.root="/another/profile";changed.revision+=1
                await rejects("Preparation target was rebound") { try await cache.saveLocalDestination(changed,scope:scope,expectedEpoch:destinationEpoch,previousRevision:installed.revision) }
                await fixture.revokeHistory()
                await rejects("Installed receipt returned after source permissions changed") { _=try await destination().advance() }
            }
        }
        print("PASS: export, checkout/install restart composition, profile binding, cancellation, erasure and integrity")



    }
}
