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

private actor LostContinuationEvent: CampPublishingTransport {
    let api: CampHTTP
    let token: String
    var lost = false
    init(api:CampHTTP,token:String) { self.api=api;self.token=token }
    func perform(_ operation:CampPublication) async throws -> Data {
        let data = try await api.data(operation.method,path:operation.path,body:operation.body,key:operation.key,token:token)
        if operation.path.hasSuffix("/continuation-events") && !lost {
            lost=true;throw URLError(.networkConnectionLost)
        }
        return data
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
    @MainActor static func main()async throws {
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
            let binding = try await handoff().adoptCompleted()!
            precondition(binding.session == finished.session && binding.installation == finished.destination.installation)
            let adopted = try await cache.localContinuation(sessionID:session.id,executionEpoch:binding.session.executionEpoch!,scope:scope,expectedEpoch:epoch)
            precondition(adopted == binding)
            let transient = try await handoff().current(); precondition(transient == nil)
            let replay = try await cache.adoptReturnHandoff(finished,scope:scope,expectedEpoch:epoch)
            precondition(replay == binding)
            let providerID = UUID().uuidString.lowercased()
            let live = CloudLiveConversation()
            for turn in 0..<2 {
                let queued = try CampWire.decoder().decode(CloudRequest.self,from:await api.data("POST",path:base+"/requests",
                    body:CampWire.body(["prompt":"Synthetic continuation admission", "model":"", "effort":"high", "speed":"standard", "sources":[]]),key:UUID().uuidString,token:token))
                let requestBase="/v1/camps/\(scope.campId)/requests"
                let claimed = try CampWire.decoder().decode(CloudRequest.self,from:await api.data("POST",path:requestBase+"/claim",
                    body:CampWire.body(["session_ids":[session.id]]),key:UUID().uuidString,token:token))
                precondition(claimed.id == queued.id && claimed.executionEpoch == binding.session.executionEpoch)
                var started = try CampWire.decoder().decode(CloudRequest.self,from:await api.data("POST",path:requestBase+"/\(queued.id)/actions",
                    body:CampWire.body(["action":"start","lease_token":claimed.leaseToken!]),key:UUID().uuidString,token:token))
                started.leaseToken = claimed.leaseToken
                precondition(started.localContinuation == true && (turn == 0 || started.providerSessionId == providerID))
                let attempt = CloudAttempt(id:started.id,scope:scope.key,request:started,localSessionId:binding.session.localSessionId,
                    state:"running",startKey:UUID().uuidString,finishKey:UUID().uuidString)
                try await cache.saveWorkerAttempt(attempt,scope:scope,expectedEpoch:epoch)
                let current = try await api.get(CloudSession.self,path:base,token:token)
                let launch = try await cache.reserveContinuation(session:current,requestID:started.id,scope:scope,expectedEpoch:epoch)
                precondition(launch.newSession == (turn == 0))
                if turn == 0 {
                    try await cache.bindContinuation(sessionID:session.id,executionEpoch:current.executionEpoch!,requestID:started.id,
                        providerSessionID:providerID,scope:scope,expectedEpoch:epoch)
                } else { precondition(launch.providerSessionID == providerID) }
                await rejects("Real request admitted twice") { _ = try await cache.reserveContinuation(session:current,requestID:started.id,scope:scope,expectedEpoch:epoch) }
                let stream = CampLocalImplementationStream(request:started,scope:scope,epoch:epoch,cache:cache,transport:LostContinuationEvent(api:api,token:token))
                if turn == 0 { try await stream.emit(CampWire.body(["type":"session","sessionID":providerID])) }
                try await stream.emit(CampWire.body(["type":"started"]))
                try await stream.emit(CampWire.body(["type":"settings","model":"fixture-actual-model","effort":"high","speed":"fast"]))
                try await stream.emit(CampWire.body(["type":"text","text":"Synthetic continuation answer \(turn)"]))
                if turn == 0 {
                    let approvalID=UUID().uuidString.lowercased()
                    try await stream.emit(CampWire.body(["type":"approval","id":approvalID,"kind":"file","title":"Synthetic edit","detail":"No file is edited by this test","questions":[],"canApprove":true,"acceptLabel":"Allow edit"]))
                    live.watch(session:current,scope:scope,client:api,credential:auth)
                    for _ in 0..<200 { if live.approvals.count == 1 { break }; try await Task.sleep(for:.milliseconds(10)) }
                    precondition(live.head?.localContinuation == true && live.approvals.first?.id == approvalID && live.messages.last?.text == "Synthetic continuation answer 0")
                    let waiting = try await api.get(CloudRequest.self,path:requestBase+"/"+started.id,token:token)
                    let control = try CampWire.body(["id":UUID().uuidString.lowercased(),"approval_seq":waiting.streamSeq!,"control":["type":"approval","id":approvalID,"allow":true]])
                    await denied("Teammate approved owner continuation") { _ = try await api.data("POST",path:requestBase+"/\(started.id)/controls",body:control,token:teammate.token) }
                    _ = try await api.data("POST",path:requestBase+"/\(started.id)/controls",body:control,token:token)
                    let controls = try CampWire.decoder().decode([LocalWorkerControl].self,from:await api.data("POST",path:requestBase+"/\(started.id)/local-controls",body:CampWire.body(["lease_token":claimed.leaseToken!]),token:token))
                    precondition(controls.count == 1 && controls[0].control.id == approvalID)
                    try await stream.emit(CampWire.body(["type":"approvalResolved","requestID":approvalID]))
                }
                try await stream.emit(CampWire.body(["type":"done"]))
                // The stream is synthetic; no live model or project command runs.
                let finishKey=UUID().uuidString
                for _ in 0..<2 {
                _ = try await api.data("POST",path:requestBase+"/\(queued.id)/actions",
                    body:CampWire.body(["action":"finish","lease_token":claimed.leaseToken!,"outcome":"completed","evidence":"Synthetic provider stream; no live model was started"]),key:finishKey,token:token)
                }
            }
            var retained:[CloudMessage]=[],before:Int64=0
            for _ in 0..<4 {
                let page=try await api.get(CloudTranscript.self,path:base+"/transcript?limit=200&before=\(before)",token:token)
                retained=page.messages+retained
                if !page.hasMore { break };before=page.before
            }
            precondition(retained.count == 264 && Set(retained.map(\.id)).count == 264)
            precondition(retained.suffix(4).map(\.text) == ["Synthetic continuation admission","Synthetic continuation answer 0","Synthetic continuation admission","Synthetic continuation answer 1"])
            let original=archive.conversations[0].messages
            precondition(Array(retained.prefix(260)) == original)
            live.acknowledge(retained)
            precondition(live.messages.isEmpty && live.approvals.isEmpty)
            live.stop()
            try await cache.clear(scope)
            print("PASS: real API return for \(session.provider), independent destination device, 510 history messages, lost-response recovery and retained permissions")
        }
    }
}
