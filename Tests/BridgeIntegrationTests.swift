import Foundation

@main struct BridgeIntegrationTests {
    @MainActor static func attachments(root: URL) async throws {
        let bytes = Data(repeating: 0x5a, count: (1 << 20) + 13)
        for provider in ["Codex", "Claude"] {
            let file = root.appendingPathComponent("attachments-" + provider + ".jsonl")
            let content: [[String: Any]] = provider == "Claude"
                ? [["type": "image", "source": ["type": "base64", "media_type": "image/png", "data": bytes.base64EncodedString()]]]
                : [["type": "input_image", "image_url": "data:image/png;base64," + bytes.base64EncodedString()]]
            let record: [String: Any] = provider == "Claude"
                ? ["uuid": "attachment-only", "type": "user", "message": ["content": content]]
                : ["type": "response_item", "payload": ["type": "message", "id": "attachment-only", "role": "user", "content": content]]
            var wire = try JSONSerialization.data(withJSONObject: record)
            wire.append(0x0a)
            try wire.write(to: file)
            let session = CampSession(id: "fixture", sessionID: "attachment-fixture", provider: provider, sourceID: "fixture",
                sourceLabel: "Fixture", root: root.path, title: "Attachment fixture", cwd: root.path, updated: 1,
                path: file.path, accountID: "", archived: false)
            struct Input: Encodable { var session: CampSession }
            let input = try JSONEncoder().encode(Input(session: session))
            let transcript = try JSONDecoder().decode(Transcript.self, from: await Bridge.call("transcript", input: input))
            precondition(transcript.total == 1 && transcript.messages.count == 1 && transcript.messages[0].text.isEmpty)
            let message = transcript.messages[0]
            let ref = message.attachments!.first!
            precondition(ref.state == "embedded" && ref.size == bytes.count && ref.sha256 == PublishMessage.hash(bytes))
            precondition(ref.label == "Image" && ref.availability == "Saved in transcript")
            let read = try await TranscriptAttachmentReader.read(session: session, messageID: message.id, attachment: ref)
            precondition(read == bytes, "The bundled helper did not return exact embedded bytes above the text-page limit")
            precondition(message.contextMessage.attachments == nil, "Text-only provenance falsely claimed to include image bytes")
            precondition(!HistorySourceSnapshot(messages: [message]).valid, "Embedded history needs its original source")
            let saved = HistorySourceSnapshot(messages: [message], attachmentSource: session)
            precondition(saved.valid)
            let savedPage = try saved.page(after: -1)
            precondition(savedPage.messages.first?.attachments == [ref])
            let savedBytes = try await TranscriptAttachmentReader.read(session: saved.attachmentSource!, messageID: message.id, attachment: ref)
            precondition(savedBytes == bytes)
            let exported = try JSONDecoder().decode(TranscriptExport.self, from: await Bridge.call("export-page", input: CampWire.body(["session": JSONSerialization.jsonObject(with: JSONEncoder().encode(session)), "after": -1, "limit": 200])))
            precondition(exported.messages.count == 1 && exported.messages[0].attachments == [ref], "Export dropped attachment metadata")
            if provider == "Claude" {
                let childFile=file.deletingPathExtension().appendingPathComponent("subagents/agent-image.jsonl")
                try FileManager.default.createDirectory(at:childFile.deletingLastPathComponent(),withIntermediateDirectories:true)
                try wire.write(to:childFile)
                let catalog=try await ChildHistoryBridge.catalog(session)
                precondition(catalog.sources.count==1)
                let source=catalog.sources[0]
                let page=try await ChildHistoryBridge.page(session,source,-1)
                precondition(page.page.messages.first?.attachments==[ref])
                let childBytes=try await TranscriptAttachmentReader.read(session:session,child:source,messageID:message.id,attachment:ref)
                precondition(childBytes==bytes,"Child bridge lost exact attachment bytes")
                try Data().write(to:childFile)
                do {_ = try await TranscriptAttachmentReader.read(session:session,child:source,messageID:message.id,attachment:ref);preconditionFailure("Removed child attachment read succeeded")} catch {}
            }
            try Data().write(to: file)
            do { _ = try await TranscriptAttachmentReader.read(session: session, messageID: message.id, attachment: ref); preconditionFailure("Read an attachment removed from its source") } catch {}
        }
        // Saved text-only histories remain decodable after adding optional metadata.
        let legacy = try JSONDecoder().decode(Message.self, from: Data(#"{"id":"old","role":"user","text":"hello"}"#.utf8))
        precondition(legacy.attachments == nil && HistorySourceSnapshot(messages: [legacy]).valid)
    }
    @MainActor static func installation(root: URL, repository: URL, capture: TransferCapture) async throws {
        for provider in ["Codex","Claude"] {
            func id() -> String { UUID().uuidString.lowercased() }
            let profile=root.deletingLastPathComponent().appendingPathComponent("install-"+provider)
            try FileManager.default.createDirectory(at:profile.appendingPathComponent(provider=="Codex" ? "sessions" : "projects"),withIntermediateDirectories:true)
            _ = try await Bridge.call("add-source",input:Data(profile.path.utf8))
            struct Sources: Decodable { var sources: [Source] }
            struct Source: Decodable { var id:String;var root:String;var provider:String }
            let discovered=try JSONDecoder().decode(Sources.self,from:await Bridge.call("discover"))
            let source=discovered.sources.first{$0.root==profile.resolvingSymlinksInPath().path && $0.provider==provider}!
            let scope=CloudScope(origin:"https://fixture.example",userId:id(),campId:id())
            let parentID=id(),childID=id(),profileID=id(),deviceID=id(),workspaceID=id()
            let session=try CampWire.decoder().decode(CloudSession.self,from:CampWire.body([
                "id":parentID,"camp_id":scope.campId,"project_id":"","profile_id":id(),"provider":provider,"profile_label":"Fixture",
                "owner_user_id":scope.userId,"owner_device_id":deviceID,"local_session_id":"source","title":"Transferred history",
                "model":"fixture-model","state":"idle","stage":"ready","shared":false,"allow_requests":false,"external_owner":false,
                "revision":3,"transcript_revision":4,"permission_revision":2,"updated_at":"2026-10-22T00:00:00Z",
                "execution_location":"cloud","execution_epoch":2,"execution_workspace_id":workspaceID
            ]))
            let child=CloudChildHistory(id:childID,parentId:parentID,sourceKey:String(repeating:"a",count:64),sourceRevision:String(repeating:"b",count:64),name:"Research",phase:"",workflow:"",state:"done",model:"fixture-model",complete:true,removed:false,revision:3,transcriptRevision:2)
            let history=TransferHistoryArchive(origin:scope.origin,session:session,conversations:[
                .init(id:parentID,messages:[CloudMessage(id:"parent",ordinal:0,role:"user",text:"Original parent 🦫",removed:false,revision:1)]),
                .init(id:childID,child:child,messages:[CloudMessage(id:"child",ordinal:0,role:"assistant",text:"Original child",removed:false,model:"recorded-model",effort:"high",speed:"fast",revision:1)])
            ])
            let exported=CloudWorkspaceExport(id:id(),campId:scope.campId,sessionId:parentID,ownerUserId:scope.userId,deviceId:deviceID,
                workspaceId:workspaceID,workspaceRevision:1,sourceEpoch:2,sourceRevision:3,transcriptRevision:4,permissionRevision:2,
                kind:"capture",paths:["file.txt"],state:"ready",resultSha256:capture.payloadSha256,resultBytes:capture.payload!.count,expiresAt:Date().addingTimeInterval(3600))
            let checkout=root.appendingPathComponent("installed-"+provider)
            let clone=Process();clone.executableURL=URL(fileURLWithPath:"/usr/bin/git")
            clone.arguments=["clone","--no-local","--no-hardlinks","--",repository.path,checkout.path]
            clone.standardOutput=FileHandle.nullDevice;clone.standardError=FileHandle.nullDevice
            try clone.run();clone.waitUntilExit();precondition(clone.terminationStatus==0)
            try FileManager.default.setAttributes([.posixPermissions:0o700],ofItemAtPath:checkout.path)
            let target=TransferInstallationTarget(profileID:profileID,sourceID:source.id,provider:provider,root:source.root,sessionID:id(),purpose:"implementation")
            let transferID=id()
            func install() async throws -> TransferInstallationResult {
                try await CampTransferInstallation.install(scope:scope,transferID:transferID,destination:checkout.path,target:target,
                    export:exported,payload:capture.payload!,review:capture,history:history)
            }
            let result=try await install(), restarted=try await install()
            precondition(result==restarted,"A new helper process did not recover the installation")
            let state=root.appendingPathComponent("restore-"+scope.key+"-"+transferID)
            let saved=try Data(contentsOf:state.appendingPathComponent("history.json"))
            let expected=try CampWire.encoder().encode(history)
            precondition(saved==expected,"Installed history lost source metadata")
            let receipt=try Data(contentsOf:state.appendingPathComponent("installation.json"))
            precondition(PublishMessage.hash(receipt)==result.receiptSHA256)
            precondition(result.receipt.target==target && result.receipt.sourceID==parentID)
            try Data("Changed after installation".utf8).write(to:checkout.appendingPathComponent("file.txt"))
            do { _=try await install();preconditionFailure("Installer overwrote newer local work") } catch {}
            let profileFiles=try FileManager.default.contentsOfDirectory(atPath:profile.path)
            precondition(profileFiles.count==1,"Installer wrote provider-native state")
        }
    }
    @MainActor static func main() async throws {

        guard let path = ProcessInfo.processInfo.environment["CAPY_SUPPORT"], path.contains("/.camp-tests."), path.hasSuffix("/bridge-support") else {
            preconditionFailure("Bridge tests require an isolated support directory")
        }
        let root = URL(fileURLWithPath:path), id = UUID().uuidString.lowercased()
        try await attachments(root: root)
        let key = String(repeating:"a",count:64)
        let file = root.appendingPathComponent("run-"+key+".lock")
        try Data(("capybara-transfer-v1:"+id+"\n").utf8).write(to:file)
        try FileManager.default.setAttributes([.posixPermissions:0o600],ofItemAtPath:file.path)

        let helper = try Bridge.process("transfer-fences")
        precondition(helper.executableURL == Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local"))
        let coordinator = CampLocalHandoff(cache:nil)
        let values = try await coordinator.readFences()
        precondition(values == [LocalTransferFence(key:key,handoffID:id)],"The app did not reach its bundled recovery helper")
        try await coordinator.resolveRecoveredFence(values[0])
        let remaining = try Data(contentsOf:file)
        precondition(remaining.isEmpty,"The bundled helper did not resolve its marker")
        let empty = try JSONDecoder().decode([LocalTransferFence].self,from:await Bridge.call("transfer-fences"))
        precondition(empty.isEmpty)
        let repository=root.appendingPathComponent("repository")
        try FileManager.default.createDirectory(at:repository,withIntermediateDirectories:true)
        func git(_ arguments:[String]) throws {
            let process=Process();process.executableURL=URL(fileURLWithPath:"/usr/bin/git")
            process.arguments=["-C",repository.path]+arguments;process.standardOutput=FileHandle.nullDevice;process.standardError=FileHandle.nullDevice
            try process.run();process.waitUntilExit();precondition(process.terminationStatus==0)
        }
        try git(["init","-q"])
        try Data("before\n".utf8).write(to:repository.appendingPathComponent("file.txt"))
        try git(["add","file.txt"]);try git(["-c","user.name=Fixture","-c","user.email=fixture@example.com","commit","-qm","Fixture"])
        try Data("after\n".utf8).write(to:repository.appendingPathComponent("file.txt"))
        let session=CampSession(id:"fixture",sessionID:UUID().uuidString,provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:root.path,title:"Fixture",cwd:repository.path,updated:1,path:"",accountID:"",archived:false)
        let request=try CampWire.body(["session":JSONSerialization.jsonObject(with:JSONEncoder().encode(session)),"paths":["file.txt"]])
        let changes=try JSONDecoder().decode(TransferChanges.self,from:await Bridge.call("transfer-changes",input:request))
        precondition(changes.files.count==1 && changes.files[0].path=="file.txt")
        let capture=try JSONDecoder().decode(TransferCapture.self,from:await Bridge.call("transfer-capture",input:request))
        precondition(capture.valid && capture.files.count==1 && capture.payload.map(PublishMessage.hash)==capture.payloadSha256,"The bundled capture helper was not used")
        let downloadedReview=try CampWire.decoder().decode(TransferCapture.self,from:await Bridge.call("transfer-review",input:capture.payload!))
        precondition(downloadedReview.valid && downloadedReview.payload == nil && downloadedReview.reviewSha256 == capture.reviewSha256 && downloadedReview.payloadSha256 == capture.payloadSha256 && downloadedReview.files.count == 1,"The bundled helper did not validate downloaded bytes")
        let destination=root.appendingPathComponent("restored-checkout")
        try git(["-c","core.hooksPath=/dev/null","clone","--no-local","--no-hardlinks","--",repository.path,destination.path])
        try FileManager.default.setAttributes([.posixPermissions:0o700],ofItemAtPath:destination.path)
        let restoreID=UUID().uuidString.lowercased(), restoreScope=PublishMessage.hash(Data("synthetic-camp-owner".utf8))
        let restore=try CampWire.body(["scope":restoreScope,"transferID":restoreID,"destination":destination.path,"branch":capture.branch,
            "payload":capture.payload!.base64EncodedString(),"payloadSHA256":capture.payloadSha256,"reviewSHA256":capture.reviewSha256])
        let staged=try await Bridge.call("transfer-stage",input:restore)
        let receipt=try JSONSerialization.jsonObject(with:staged) as! [String:Any]
        precondition(receipt["transferID"] as? String == restoreID && receipt["scope"] as? String == restoreScope)
        precondition((receipt["receipt"] as? [String:Any])?["review_sha256"] as? String == capture.reviewSha256)
        let stagedAgain=try await Bridge.call("transfer-stage",input:restore)
        precondition(staged==stagedAgain,"Restarted helper changed the staging receipt")
        try await installation(root:root,repository:repository,capture:capture)
        let restored=try String(contentsOf:destination.appendingPathComponent("file.txt"),encoding:.utf8)
        precondition(restored=="after\n","The bundled helper did not restore selected changes")
        try Data("new local work\n".utf8).write(to:destination.appendingPathComponent("file.txt"))
        do {_=try await Bridge.call("transfer-stage",input:restore);preconditionFailure("Staging overwrote local work")}catch{}
        let retained=try String(contentsOf:destination.appendingPathComponent("file.txt"),encoding:.utf8)
        precondition(retained=="new local work\n")
        let profile=root.appendingPathComponent("claude"), parentPath=profile.appendingPathComponent("projects/fixture/parent.jsonl")
        let childPath=profile.appendingPathComponent("projects/fixture/parent/subagents/agent-one.jsonl")
        try FileManager.default.createDirectory(at:childPath.deletingLastPathComponent(),withIntermediateDirectories:true)
        try Data().write(to:parentPath)
        try Data("{\"type\":\"assistant\",\"uuid\":\"child-message\",\"isSidechain\":true,\"message\":{\"model\":\"fixture-model\",\"content\":\"Child history\"}}\n".utf8).write(to:childPath)
        let parent=CampSession(id:"parent",sessionID:UUID().uuidString,provider:"Claude",sourceID:"fixture",sourceLabel:"Fixture",root:profile.path,title:"Parent",cwd:repository.path,updated:1,path:parentPath.path,accountID:"",archived:false)
        let encodedParent=try JSONSerialization.jsonObject(with:JSONEncoder().encode(parent))
        let catalog=try JSONDecoder().decode(LocalChildHistoryCatalog.self,from:await Bridge.call("child-history-catalog",input:CampWire.body(["session":encodedParent])))
        precondition(catalog.sources.count==1)
        struct ChildPage:Decodable {var page:TranscriptExport}
        let childPage=try JSONDecoder().decode(ChildPage.self,from:await Bridge.call("child-export-page",input:CampWire.body(["session":encodedParent,"sourceID":catalog.sources[0].id,"revision":catalog.sources[0].revision,"after":-1,"limit":200])))
        precondition(childPage.page.messages.count==1 && childPage.page.messages[0].text=="Child history")
        let parentPage=try JSONDecoder().decode(TranscriptExport.self,from:await Bridge.call("export-page",input:CampWire.body(["session":encodedParent,"limit":1])))
        let history=LocalHistoryBinding(parentId:catalog.parentID,mainRevision:parentPage.snapshotRevision,children:catalog.sources.map{.init(sourceId:$0.id,revision:$0.revision)},transcriptRevision:3)
        let boundRequest=try CampWire.body(["session":encodedParent,"paths":["file.txt"],"history":JSONSerialization.jsonObject(with:CampWire.encoder().encode(history))])
        let bound=try CampWire.decoder().decode(TransferCapture.self,from:await Bridge.call("transfer-capture",input:boundRequest))
        precondition(bound.history==history,"The packaged helper did not bind parent and child history")
        try Data("{\"type\":\"assistant\",\"uuid\":\"changed\",\"message\":{\"content\":\"Changed child history\"}}\n".utf8).write(to:childPath)
        do {_=try await Bridge.call("transfer-capture",input:boundRequest);preconditionFailure("Packaged capture accepted stale child history")}catch{}
        do { _ = try Bridge.process("unrecognized-action"); preconditionFailure("The bridge accepted an unsupported command") }
        catch let error as CampError { precondition(error.localizedDescription == "Unsupported local session action") }
        print("PASS: app-bundled Go recovery, change review, exact capture, restartable local staging, pinned child export and action validation")
    }
}
