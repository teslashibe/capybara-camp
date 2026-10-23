import Foundation

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
        for (provider,scenario) in ["Codex","Claude"].flatMap({ p in ["complete","losses","cancel","early-cancel","finished-cancel","wrong-destination"].map{(p,$0)}}) {
            try await cache.clear(scope)
            var source=session;source.provider=provider
            let fixture=ExportFixture(value,payload:payload);await fixture.checkout(device:credential.actor.deviceId);await fixture.history(source)
            let review=TransferCapture(payloadSha256:value.resultSha256,reviewSha256:String(repeating:"b",count:64),baseCommit:String(repeating:"c",count:40),branch:"main",files:[.init(path:"notes.txt",bytes:payload.count,deleted:false)],excluded:[])
            let transport=ReturnHandoffFixture(session:source,device:credential.actor.deviceId,files:fixture,payload:payload,review:review)
            if scenario=="losses"{await transport.losses()}
            let bridge=DestinationBridgeFixture(review),localEpoch=try await cache.epoch(scope)
            let files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:transport,epoch:localEpoch)
            let history=try CampTransferHistory(cache:cache,scope:scope,credential:credential,transport:transport,epoch:localEpoch)
            try await files.begin(session:source,kind:"capture",paths:["notes.txt"])
            await rejects("Expected lost export create"){try await files.advance()}
            try await files.advance();try await files.advance();try await history.begin(session:source);try await history.advance()
            let target=TransferInstallationTarget(profileID:UUID().uuidString.lowercased(),sourceID:"fixture-source",provider:provider,root:"/fixture/profile",sessionID:UUID().uuidString.lowercased(),purpose:"implementation")
            let local=try CampLocalDestination(cache:cache,scope:scope,credential:credential,transport:transport,epoch:localEpoch,callLocal:{try await bridge.call($0,$1)})
            try await local.begin(target:target,reviewedSHA256:review.reviewSha256)
            _=try await local.advance();_=try await local.advance()
            func handoff()throws->CampReturnHandoff{try CampReturnHandoff(cache:cache,scope:scope,credential:credential,transport:transport,epoch:localEpoch,callLocal:{try await bridge.call($0,$1)})}
            try await handoff().begin()
            if scenario=="early-cancel" {
                try await handoff().requestCancellation()
                for _ in 0..<5 { _=try await handoff().advance();if try await handoff().current()?.phase=="cancelled"{break} }
                let ended=try await handoff().current()!,counts=await transport.counts()
                precondition(ended.phase=="cancelled" && counts.0==1 && counts.1==0 && counts.2==0)
                try await handoff().discardCancelled()
                let cleared=try await handoff().current();precondition(cleared==nil)
                continue
            }
            for _ in 0..<30 {
                if try await handoff().current()?.phase=="confirm"{break}
                do{_=try await handoff().advance()}catch is URLError{}
            }
            let ready=try await handoff().current()!
            precondition(ready.phase=="confirm" && ready.handoff?.localDestination?.sessionId==target.sessionID)
            let permissions=provider=="Codex" ? PermissionSettings(sandbox:"workspace-write",approval:"on-request",reviewer:"user") : PermissionSettings(claudeMode:"manual")
            await rejects("Implicit continuation approved"){try await handoff().confirm(contextContinuationApproved:false,executionApproved:true,permissions:permissions)}
            await rejects("Implicit implementation approved"){try await handoff().confirm(contextContinuationApproved:true,executionApproved:false,permissions:permissions)}
            if scenario=="wrong-destination" {
                await transport.corrupt()
                await rejects("Foreign destination accepted"){_=try await handoff().advance()}
                continue
            }
            if scenario=="cancel" {try await handoff().requestCancellation()}
            else {
                try await handoff().confirm(contextContinuationApproved:true,executionApproved:true,permissions:permissions)
                if scenario=="finished-cancel" {
                    _=try await handoff().advance()
                    await transport.finishIssued()
                    try await handoff().requestCancellation()
                }
            }
            for _ in 0..<20 {
                if let phase=try await handoff().current()?.phase,["complete","cancelled"].contains(phase){break}
                do{_=try await handoff().advance()}catch is URLError{}
            }
            let finished=try await handoff().current()!,counts=await transport.counts()
            precondition(counts.0==1 && counts.1==1)
            if scenario=="cancel" {
                precondition(finished.phase=="cancelled" && counts.2==0)
                try await handoff().discardCancelled()
                let cleared=try await handoff().current();precondition(cleared==nil)
                await rejects("Discarded handoff journal recreated"){try await cache.saveReturnHandoff(finished,scope:scope,expectedEpoch:localEpoch,previousRevision:finished.revision-1)}
            }
            else {
                precondition(finished.phase=="complete" && counts.2==1)
                precondition(finished.session?.id==source.id && finished.session?.ownerDeviceId==credential.actor.deviceId && finished.session?.profileId==target.profileID && finished.session?.transcriptRevision==source.transcriptRevision)
                precondition(finished.session?.executionEpoch==(source.executionEpoch ?? 0)+1)
                var stale = finished; stale.revision -= 1
                await rejects("Stale journal adopted") { _ = try await cache.adoptReturnHandoff(stale,scope:scope,expectedEpoch:localEpoch) }
                let adopted = try await handoff().adoptCompleted()!
                let assigned = finished.session!
                precondition(adopted.session == assigned && adopted.target == target && adopted.firstRequestID == nil && adopted.providerSessionID == nil)
                precondition(adopted.history.transferID == finished.destination.id)
                for prefix in ["return-handoff:","local-destination:","workspace-export:","transfer-history:"] {
                    let value = try await cache.metadata(prefix+scope.key); precondition(value == nil)
                }
                var request = CloudRequest(id:UUID().uuidString.lowercased(),campId:scope.campId,sessionId:assigned.id,
                    requesterUserId:scope.userId,ownerUserId:scope.userId,ownerDeviceId:assigned.ownerDeviceId,
                    permissionRevision:assigned.permissionRevision,revision:1,prompt:"Continue",model:"",effort:"",speed:"standard",state:"started",
                    leaseToken:UUID().uuidString,leaseUntil:Date().addingTimeInterval(120),createdAt:Date(),sources:[])
                request.kind="chat";request.localContinuation=true;request.executionLocation="local";request.executionEpoch=assigned.executionEpoch
                request.mode="implementation";request.permissions=finished.permissions
                let first = CloudAttempt(id:request.id,scope:scope.key,request:request,localSessionId:assigned.localSessionId,
                    state:"running",startKey:UUID().uuidString,finishKey:UUID().uuidString)
                try await cache.saveWorkerAttempt(first,scope:scope,expectedEpoch:localEpoch)
                var moved = assigned; moved.executionEpoch! += 1
                await rejects("Old execution epoch launched") { _ = try await cache.reserveContinuation(session:moved,requestID:request.id,scope:scope,expectedEpoch:localEpoch) }
                moved=assigned;moved.ownerDeviceId=UUID().uuidString
                await rejects("Other device launched") { _ = try await cache.reserveContinuation(session:moved,requestID:request.id,scope:scope,expectedEpoch:localEpoch) }
                for violation in ["lease","permissions","revision","epoch","restricted","identity","continuation"] {
                    var invalid=first
                    switch violation {
                    case "lease": invalid.request.leaseUntil = Date().addingTimeInterval(-1)
                    case "permissions": invalid.request.permissions = ["sandbox":"unsupported"]
                    case "revision": invalid.request.permissionRevision += 1
                    case "epoch": invalid.request.executionEpoch! += 1
                    case "identity": invalid.request.providerSessionId = UUID().uuidString
                    case "continuation": invalid.request.localContinuation = false
                    default: invalid.request.contentRestricted = true
                    }
                    try await cache.saveWorkerAttempt(invalid,scope:scope,expectedEpoch:localEpoch)
                    await rejects("Invalid continuation admitted: "+violation) { _ = try await cache.reserveContinuation(session:assigned,requestID:request.id,scope:scope,expectedEpoch:localEpoch) }
                }
                try await cache.saveWorkerAttempt(first,scope:scope,expectedEpoch:localEpoch)
                let launch = try await cache.reserveContinuation(session:assigned,requestID:request.id,scope:scope,expectedEpoch:localEpoch)
                precondition(launch.newSession && launch.providerSessionID == target.sessionID)
                await rejects("Reserved request launched twice") { _ = try await cache.reserveContinuation(session:assigned,requestID:request.id,scope:scope,expectedEpoch:localEpoch) }
                // A stale worker write must not clear the launch reservation.
                try await cache.saveWorkerAttempt(first,scope:scope,expectedEpoch:localEpoch)
                await rejects("Stale worker write permitted relaunch") { _ = try await cache.reserveContinuation(session:assigned,requestID:request.id,scope:scope,expectedEpoch:localEpoch) }
                var next = first; next.id=UUID().uuidString.lowercased();next.request.id=next.id
                try await cache.saveWorkerAttempt(next,scope:scope,expectedEpoch:localEpoch)
                await rejects("Unknown first conversation created another") { _ = try await cache.reserveContinuation(session:assigned,requestID:next.id,scope:scope,expectedEpoch:localEpoch) }
                let replay = try await cache.adoptReturnHandoff(finished,scope:scope,expectedEpoch:localEpoch)
                precondition(replay.firstRequestID == request.id && replay.providerSessionID == nil)
                if scenario == "losses" {
                    let restarted = try CampCache(directory:root)
                    let unknown = try await restarted.localContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,scope:scope,expectedEpoch:localEpoch)
                    precondition(unknown?.firstRequestID == request.id && unknown?.providerSessionID == nil)
                    await rejects("Restart created a replacement for an unknown submission") { _ = try await restarted.reserveContinuation(session:assigned,requestID:next.id,scope:scope,expectedEpoch:localEpoch) }
                    await rejects("Restart accepted an unowned late identity") { try await restarted.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:request.id,providerSessionID:UUID().uuidString,scope:scope,expectedEpoch:localEpoch) }
                    continue
                }
                let providerID = UUID().uuidString.lowercased()
                await rejects("Other request bound the conversation") { try await cache.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:next.id,providerSessionID:providerID,scope:scope,expectedEpoch:localEpoch) }
                try await cache.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:request.id,providerSessionID:providerID,scope:scope,expectedEpoch:localEpoch)
                try await cache.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:request.id,providerSessionID:providerID,scope:scope,expectedEpoch:localEpoch)
                await rejects("Conversation identity overwritten") { try await cache.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:request.id,providerSessionID:UUID().uuidString,scope:scope,expectedEpoch:localEpoch) }
                await rejects("Local provider identity accepted without server binding") { _ = try await cache.reserveContinuation(session:assigned,requestID:next.id,scope:scope,expectedEpoch:localEpoch) }
                next.request.providerSessionId=providerID
                try await cache.saveWorkerAttempt(next,scope:scope,expectedEpoch:localEpoch)
                let followup = try await cache.reserveContinuation(session:assigned,requestID:next.id,scope:scope,expectedEpoch:localEpoch)
                precondition(!followup.newSession && followup.providerSessionID == providerID && followup.binding.session.localSessionId == target.sessionID)
                let reopened = try CampCache(directory:root)
                let persisted = try await reopened.localContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,scope:scope,expectedEpoch:localEpoch)
                precondition(persisted?.providerSessionID == providerID && persisted?.session.transcriptRevision == source.transcriptRevision)
                await rejects("Restart repeated submitted request") { _ = try await reopened.reserveContinuation(session:assigned,requestID:next.id,scope:scope,expectedEpoch:localEpoch) }
                try await cache.clear(scope)
                let newEpoch=try await cache.epoch(scope)
                let erased=try await cache.localContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,scope:scope,expectedEpoch:newEpoch)
                precondition(erased == nil)
                await rejects("Late provider callback restored cleared binding") { try await cache.bindContinuation(sessionID:assigned.id,executionEpoch:assigned.executionEpoch!,requestID:request.id,providerSessionID:providerID,scope:scope,expectedEpoch:localEpoch) }
            }
        }
        print("PASS: export, destination restart, approved return handoff, lost responses, cancellation and integrity")




    }
}
