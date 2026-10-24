import Foundation

@main struct CampTransferTests {
    @MainActor static func until(_ label:String,_ body:()async->Bool)async {
        for _ in 0..<1000 {if await body(){return};try? await Task.sleep(for:.milliseconds(5))}
        preconditionFailure(label)
    }
    @MainActor static func main()async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("camp-transfer-"+UUID().uuidString)
        defer{try? FileManager.default.removeItem(at:root)}
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture"),token:"fixture",expiresAt:Date().addingTimeInterval(3600))
        let local=CampSession(id:"local",sessionID:UUID().uuidString,provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:root.path,title:"Fixture",cwd:root.path,updated:1,path:"",accountID:"",archived:false)
        let session=CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Codex",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:credential.actor.deviceId,localSessionId:local.sessionID,title:"Fixture",model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:1,permissionRevision:1,updatedAt:Date(),executionLocation:"local",executionEpoch:1)
        let input=CloudWorkspaceInput(providerAccountId:UUID().uuidString,gitConnectionId:UUID().uuidString,githubInstallationId:7,repositoryId:5,installationPage:1,repositoryPage:1,ref:"main",purpose:"planning",expectedSessionRevision:1,expectedProjectRevision:1)
        let history=LocalHistoryBinding(parentId:String(repeating:"c",count:64),mainRevision:String(repeating:"d",count:64),children:[],transcriptRevision:session.transcriptRevision)
        let payload=try CampWire.body(["version":1,"base_commit":String(repeating:"a",count:40),"files":[],"excluded":[],"history":JSONSerialization.jsonObject(with:CampWire.encoder().encode(history))])
        let capture=TransferCapture(payload:payload,payloadSha256:PublishMessage.hash(payload),reviewSha256:String(repeating:"b",count:64),baseCommit:String(repeating:"a",count:40),branch:"main",files:[],excluded:[],history:history)
        for scenario in ["valid","changed-head","changed-ancestor","already-remote"] {
            let ancestor=scenario=="already-remote" ? capture.baseCommit : String(repeating:"c",count:40)
            var result=capture
            if scenario != "already-remote" {
                var manifest=try JSONSerialization.jsonObject(with:payload) as! [String:Any]
                manifest["commits"]=["base_commit":ancestor,"fixture":"synthetic objects; Go tests verify Git identity"]
                result.payload=try CampWire.body(manifest);result.payloadSha256=PublishMessage.hash(result.payload!)
                result.commits = .init(baseCommit:scenario=="changed-ancestor" ? String(repeating:"d",count:40) : ancestor,headCommit:capture.baseCommit,reviewSha256:String(repeating:"e",count:64),commits:1,objects:3,bytes:500)
            }
            if scenario=="changed-head" {result.baseCommit=String(repeating:"f",count:40);result.commits?.headCommit=result.baseCommit}
            let returned=result
            let archiveCache=try CampCache(directory:root.appendingPathComponent("ancestor-"+scenario))
            let archiveModel=CampTransfer(cache:archiveCache,interval:0.01),archiveTransport=TransferFixture(session)
            archiveModel.callLocal={action,raw in
                let body=try JSONSerialization.jsonObject(with:raw) as! [String:Any]
                if action=="transfer-changes" {
                    precondition(body["repository"] as? String=="fixture/repo" && body["ref"] as? String=="main")
                    return try JSONEncoder().encode(TransferChanges(baseCommit:capture.baseCommit,branch:"main",files:[],remoteBaseCommit:ancestor))
                }
                precondition(action=="transfer-capture" && body["expectedHead"] as? String==capture.baseCommit)
                precondition(scenario=="already-remote" ? body["remoteBaseCommit"]==nil : body["remoteBaseCommit"] as? String==ancestor)
                return try JSONEncoder().encode(returned)
            }
            archiveModel.attach(scope:scope,credential:credential,transport:archiveTransport)
            await archiveModel.inspect(local,repository:"fixture/repo",ref:"main")
            precondition(archiveModel.changes?.remoteBaseCommit==ancestor)
            await archiveModel.capture(local,session:session,input:input,paths:[],history:history,ancestor:ancestor,expectedHead:capture.baseCommit)
            if scenario.hasPrefix("changed") {precondition(archiveModel.job==nil && !archiveModel.error.isEmpty)}
            else {
                precondition(archiveModel.job?.phase=="review",archiveModel.error)
                await archiveModel.prepare();await until("Archive destination did not match reviewed head"){archiveModel.job?.phase=="ready"}
                precondition(archiveModel.job?.workspace?.remoteBaseCommit==returned.commits?.baseCommit)
            }
            archiveModel.attach(scope:nil,credential:nil,transport:nil)
        }
        func setup(_ folder:String,_ fixture:TransferFixture)->CampTransfer {
            let cache=try! CampCache(directory:root.appendingPathComponent(folder))
            let model=CampTransfer(cache:cache,interval:0.01)
            model.callLocal={action,_ in precondition(action=="transfer-capture");return try JSONEncoder().encode(capture)}
            model.attach(scope:scope,credential:credential,transport:fixture)
            return model
        }
        let fixture=TransferFixture(session),model=setup("complete",fixture)
        let attachmentReview=TransferAttachmentReview(version:1,sha256:String(repeating:"e",count:64),files:1,bytes:32,unavailable:[.init(kind:"image",state:"reference",count:2)])
        precondition(attachmentReview.valid)
        await fixture.reviewAttachments(attachmentReview)
        await model.capture(local,session:session,input:input,paths:[],history:history)
        precondition(model.job?.phase=="review" && model.job?.capture.payload==nil,"Capture was not saved separately")
        await model.prepare();await until("Destination not ready"){model.job?.phase=="ready"}
        await model.hold();await until("Source did not acknowledge"){model.job?.phase=="confirm"}
        precondition(model.job?.transfer?.attachmentReview == attachmentReview)
        await model.confirm(permissions:PermissionSettings(),approved:false)
        await until("Transfer did not commit"){model.job?.phase=="complete"}
        let completedCreates=await fixture.counts();precondition(completedCreates==1)
        model.attach(scope:nil,credential:nil,transport:nil)
        for ended in ["invalidated","expired","released"] {
            let source=TransferFixture(session),review=setup("hold-"+ended,source)
            await review.capture(local,session:session,input:input,paths:[],history:history)
            await review.prepare();await until("Destination not ready"){review.job?.phase=="ready"}
            await review.hold();await until("Source did not acknowledge"){review.job?.phase=="confirm" && !review.working}
            await source.endHold(ended)
            await until("Ended source hold stayed verified during permission review"){review.job?.phase=="blocked"}
            precondition(review.error.contains("source hold ended") && review.job?.handoff?.commitNonce==nil)
            await review.confirm(permissions:PermissionSettings(),approved:false)
            precondition(review.job?.phase=="blocked","An ended hold accepted confirmation")
            await review.cancel();await until("Ended hold could not be cancelled"){review.job?.phase=="cancelled"}
            review.attach(scope:nil,credential:nil,transport:nil)
        }
        let reject=TransferFixture(session);await reject.configure(reject:true)
        let blocked=setup("blocked",reject)
        await blocked.capture(local,session:session,input:input,paths:[],history:history);await blocked.prepare()
        await until("Rejected transfer did not pause"){blocked.job?.phase=="blocked"}
        let original=await reject.counts();try await Task.sleep(for:.milliseconds(100));let later=await reject.counts();precondition(later==original)
        blocked.retry();await until("Explicit retry did not recover"){blocked.job?.phase=="ready"}
        await blocked.cancel();await until("Cancellation did not finish"){blocked.job?.phase=="cancelled"}
        blocked.attach(scope:nil,credential:nil,transport:nil)
        let lost=TransferFixture(session);await lost.configure(lose:true)
        let interrupted=setup("restart",lost)
        await interrupted.capture(local,session:session,input:input,paths:[],history:history);await interrupted.prepare()
        await until("Lost create did not surface"){!interrupted.error.isEmpty}
        interrupted.attach(scope:nil,credential:nil,transport:nil)
        let recovered=setup("restart",lost)
        await until("Upload did not recover after restart"){recovered.job?.phase=="ready"}
        let creates=await lost.counts();precondition(creates==1,"Restart duplicated the transfer")
        let cache=try CampCache(directory:root.appendingPathComponent("restart"))
        await lost.configure(cancel:true)
        await recovered.cancel();await until("Lost cancellation did not surface"){!recovered.error.isEmpty}
        let jobID=recovered.job!.id
        recovered.attach(scope:nil,credential:nil,transport:nil)
        let cancelled=setup("restart",lost)
        await until("Saved cancellation did not recover"){cancelled.job?.phase=="cancelled"}
        let removed=try await cache.metadata("transfer-payload:"+scope.key+":"+jobID)
        precondition(removed==nil,"Cancelled payload remained cached")
        await cancelled.resetReview()
        precondition(cancelled.job==nil,"A terminal review could not be reopened")
        await cancelled.capture(local,session:session,input:input,paths:[],history:history)
        precondition(cancelled.job?.phase=="review","A cancelled move blocked a fresh review")
        await cancelled.resetReview()
        cancelled.attach(scope:nil,credential:nil,transport:nil)
        let invalid=setup("wrong-source",TransferFixture(session))
        var wrongSource=session;wrongSource.ownerDeviceId=UUID().uuidString
        await invalid.capture(local,session:wrongSource,input:input,paths:[],history:history)
        precondition(invalid.job==nil && !invalid.error.isEmpty,"Another Mac captured the source")
        invalid.attach(scope:nil,credential:nil,transport:nil)
        let unbound=setup("unbound",TransferFixture(session))
        await unbound.capture(local,session:session,input:input,paths:[])
        precondition(unbound.job==nil && !unbound.error.isEmpty,"Capture accepted a missing published history binding")
        var staleHistory=history;staleHistory.transcriptRevision += 1
        await unbound.capture(local,session:session,input:input,paths:[],history:staleHistory)
        precondition(unbound.job==nil && !unbound.error.isEmpty,"Capture accepted another cloud history revision")
        unbound.attach(scope:nil,credential:nil,transport:nil)
        print("PASS: native transfer capture, claimed source, commit, rejection, lost-response restart, durable cancellation and source ownership")
    }
}
