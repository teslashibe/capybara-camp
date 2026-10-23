import Foundation

// Lose responses only after the real API has persisted each operation. The
// journal must reuse its key when a newly constructed coordinator retries.
private actor LostReturnResponses: CampAccountTransport {
    let api: CampHTTP
    var lost=Set<String>()
    var keys: [String:Set<String>]=[:]
    init(_ api:CampHTTP) { self.api=api }
    func counts()->[String:Int] { keys.mapValues(\.count) }
    func data(_ method:String,path:String,body:Data?,key:String?,token:String?)async throws->Data {
        let result=try await api.data(method,path:path,body:body,key:key,token:token)
        let operation = method=="POST" ? ["/transfers","/handoffs","/commit"].first(where:{path.hasSuffix($0)}) : nil
        if let operation {
            guard let key else{preconditionFailure("Missing durable operation key")}
            keys[operation,default:[]].insert(key)
            if lost.insert(operation).inserted { throw URLError(.networkConnectionLost) }
        }
        return result
    }
}

@main struct NativeReturnHandoffAPITests {
    struct Fixture:Decodable {var url:String;var campId:String}
    struct Driver:Decodable {var token:String}
    struct Profile:Decodable {var id:String}
    struct ClaimedExport:Decodable {var workspace:CloudWorkspace}
    static func rejects(_ label:String,_ action:()async throws->Void)async {
        do{try await action();preconditionFailure(label)}catch{}
    }
    static func denied(_ label:String,_ action:()async throws->Void)async {
        do{try await action();preconditionFailure(label)}
        catch CampCloudError.server(let status,let code){precondition(status==404 && code=="not_available",label)}
        catch{preconditionFailure("Unexpected authorization failure: \(error)")}
    }
    static func main()async throws {
        guard let directory=ProcessInfo.processInfo.environment["CAMP_NATIVE_API_FIXTURE"],
              ProcessInfo.processInfo.environment["CAMP_NATIVE_FIXTURE_RETURN"]=="1" else {
            print("SKIP: run CAMP_NATIVE_FIXTURE_HISTORY=1 CAMP_NATIVE_FIXTURE_RETURN=1 tools/test-native-api.sh NativeReturnHandoffAPITests");return
        }
        let root=URL(fileURLWithPath:directory)
        precondition(directory.hasPrefix("/") && FileManager.default.fileExists(atPath:root.appendingPathComponent(".capybara-api-fixture").path))
        let fixture=try CampWire.decoder().decode(Fixture.self,from:Data(contentsOf:root.appendingPathComponent("fixture.json")))
        precondition(fixture.url.hasPrefix("http://127.0.0.1:"))
        let driver=try CampWire.decoder().decode(Driver.self,from:Data(contentsOf:root.appendingPathComponent("driver.json")))
        let sessions=try CampWire.decoder().decode([CloudSession].self,from:Data(contentsOf:root.appendingPathComponent("history-sessions.json")))
        let runners=try JSONDecoder().decode([String:String].self,from:Data(contentsOf:root.appendingPathComponent("return-runner-tokens.json")))
        precondition(Set(sessions.map(\.provider))==Set(["Codex","Claude"]))
        let api=CampHTTP(endpoint:try CampEndpoint(fixture.url,allowLoopback:true))
        let sourceActor=try await api.get(CloudActor.self,path:"/v1/me",token:driver.token)
        func login(_ email:String,_ name:String)async throws->CloudLogin {
            _=try await api.data("POST",path:"/v1/auth/code",body:CampWire.body(["email":email]))
            let code=try String(contentsOf:root.appendingPathComponent(name+"-code.txt"),encoding:.utf8)
            return try CampWire.decoder().decode(CloudLogin.self,from:await api.data("POST",path:"/v1/auth/verify",body:CampWire.body(["email":email,"code":code,"device_name":"Return destination Mac"])))
        }
        let destinationLogin=try await login("owner@example.com","owner"),teammate=try await login("k@example.com","k")
        let actor=destinationLogin.actor,token=destinationLogin.token
        precondition(actor.userId==sourceActor.userId && actor.deviceId != sourceActor.deviceId && teammate.actor.userId != actor.userId)
        for session in sessions {
            let scope=CloudScope(origin:fixture.url,userId:actor.userId,campId:fixture.campId)
            let cache=try CampCache(directory:root.appendingPathComponent("return-"+session.provider)),epoch=try await cache.epoch(scope)
            let auth=CloudCredential(origin:fixture.url,actor:actor,token:token,expiresAt:Date().addingTimeInterval(3600))
            let transport=LostReturnResponses(api),runner=runners[session.provider]!
            let files=try CampWorkspaceExport(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch)
            let history=try CampTransferHistory(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch)
            try await files.begin(session:session,kind:"capture",paths:["notes.txt"])
            let exported=try await files.advance()!.export!
            let claim=UUID().uuidString.lowercased(),exportBase="/v1/runner/exports/"+exported.id
            let claimed=try CampWire.decoder().decode(ClaimedExport.self,from:await api.data("POST",path:exportBase+"/claim",body:CampWire.body(["claim_id":claim]),token:runner))
            let content=Data("Reviewed cloud notes\n".utf8)
            let payload=try CampWire.body(["version":1,"base_commit":claimed.workspace.baseCommit,"branch":claimed.workspace.branch,
                "files":[["path":"notes.txt","sha256":PublishMessage.hash(content),"data":content.base64EncodedString()]],"excluded":[]])
            _=try await api.data("POST",path:exportBase+"/result",body:CampWire.body(["claim_id":claim,"sha256":PublishMessage.hash(payload),"bytes":payload.count]),token:runner)
            _=try await api.data("PUT",path:exportBase+"/chunks/0",body:CampWire.body(["claim_id":claim,"sha256":PublishMessage.hash(payload),"data":payload.base64EncodedString()]),token:runner)
            _=try await api.data("POST",path:exportBase+"/complete",body:CampWire.body(["claim_id":claim]),token:runner)
            _=try await files.advance()
            try await history.begin(session:session)
            for _ in 0..<12 {if try await history.current()?.complete==true{break};try await history.advance()}
            let archive=try await history.archive()!
            precondition(archive.conversations.count==2 && archive.conversations[0].messages.count==260 && archive.conversations[1].messages.count==250)
            // Use the production Go manifest reviewer. Only filesystem checkout
            // and installation receipts are synthetic in this API contract test.
            let process=Process(),input=Pipe(),output=Pipe()
            process.executableURL=URL(fileURLWithPath:FileManager.default.currentDirectoryPath).appendingPathComponent("build/camp-local")
            process.arguments=["transfer-review"];process.standardInput=input;process.standardOutput=output
            try process.run();try input.fileHandleForWriting.write(contentsOf:payload);try input.fileHandleForWriting.close()
            let reviewed=output.fileHandleForReading.readDataToEndOfFile();process.waitUntilExit();precondition(process.terminationStatus==0)
            let review=try CampWire.decoder().decode(TransferCapture.self,from:reviewed),bridge=DestinationBridgeFixture(review)
            let profile=try CampWire.decoder().decode(Profile.self,from:await api.data("POST",path:"/v1/profiles",body:CampWire.body([
                "local_id":PublishMessage.hash(Data(("return-"+session.provider).utf8)),"provider":session.provider,"label":"Return test profile"]),key:UUID().uuidString,token:token))
            let target=TransferInstallationTarget(profileID:profile.id,sourceID:"fixture-source",provider:session.provider,root:"/fixture/profile",sessionID:UUID().uuidString.lowercased(),purpose:"implementation")
            let local=try CampLocalDestination(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch,callLocal:{try await bridge.call($0,$1)})
            try await local.begin(target:target,reviewedSHA256:review.reviewSha256)
            _=try await local.advance();_=try await local.advance()
            func handoff()throws->CampReturnHandoff {try CampReturnHandoff(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch,callLocal:{try await bridge.call($0,$1)})}
            try await handoff().begin()
            let base="/v1/camps/\(scope.campId)/sessions/\(session.id)",stopClaim=UUID().uuidString.lowercased()
            for _ in 0..<24 {
                if try await handoff().current()?.phase=="confirm"{break}
                do{_=try await handoff().advance()}catch is URLError{}
                if let held=try await handoff().current()?.handoff,held.state=="pending" {
                    let runnerBase="/v1/runner/handoffs/"+held.id
                    _=try await api.data("POST",path:runnerBase+"/claim",body:CampWire.body(["claim_id":stopClaim]),token:runner)
                    _=try await api.data("POST",path:runnerBase+"/ack",body:CampWire.body(["claim_id":stopClaim,"stopped":true,"payload_sha256":review.payloadSha256,"review_sha256":review.reviewSha256]),token:runner)
                }
            }
            let ready=try await handoff().current()!
            precondition(ready.phase=="confirm")
            let permissions=session.provider=="Codex" ? PermissionSettings(sandbox:"workspace-write",approval:"on-request",reviewer:"user") : PermissionSettings(claudeMode:"manual")
            let held=ready.handoff!,commitPath=base+"/handoffs/"+held.id+"/commit"
            let approval=try CampWire.body(["expected_handoff_revision":held.revision,"context_continuation_approved":true,"execution_approved":true,"permissions":session.provider=="Codex" ? ["sandbox":"workspace-write","approval":"on-request","reviewer":"user"] : ["claudeMode":"manual"]])
            await denied("Other device confirmed destination"){_=try await api.data("POST",path:commitPath,body:approval,key:UUID().uuidString,token:driver.token)}
            await denied("Teammate confirmed private move"){_=try await api.data("POST",path:commitPath,body:approval,key:UUID().uuidString,token:teammate.token)}
            try await handoff().confirm(contextContinuationApproved:true,executionApproved:true,permissions:permissions)
            await rejects("Expected lost commit response"){_=try await handoff().advance()}
            let committed=try await api.get(CloudHandoff.self,path:base+"/handoffs/"+held.id,token:token)
            precondition(committed.commitNonce != nil)
            // The real runner's fresh stop verification is tested in Go. This
            // fixture submits the proof to exercise the actual server commit.
            _=try await api.data("POST",path:"/v1/runner/handoffs/"+held.id+"/finish",body:CampWire.body([
                "claim_id":stopClaim,"stopped":true,"payload_sha256":review.payloadSha256,"review_sha256":review.reviewSha256,"commit_nonce":committed.commitNonce!]),token:runner)
            _=try await handoff().advance()
            let finished=try await handoff().current()!,counts=await transport.counts()
            precondition(finished.phase=="complete" && finished.session?.id==session.id && finished.session?.profileId==profile.id)
            precondition(finished.session?.ownerDeviceId==actor.deviceId && finished.session?.executionEpoch==session.executionEpoch!+1 && finished.session?.transcriptRevision==session.transcriptRevision)
            precondition(counts==["/transfers":1,"/handoffs":1,"/commit":1])
            let execution=try await api.get(CloudExecutionTarget.self,path:base+"/execution",token:token)
            precondition(execution.location=="local" && execution.mode=="implementation" && execution.permissions==finished.permissions)
            let child=try await api.get(CloudChildHistoryList.self,path:base+"/children",token:token)
            precondition(child.children.count==1)
            await rejects("Completed move retained access to source export"){_=try await files.payload()}
            try await cache.clear(scope)
            print("PASS: real API return for \(session.provider), independent destination device, 510 history messages, lost-response recovery and retained permissions")
        }
    }
}
