import SwiftUI
import AppKit

@MainActor final class TransferAuditState: ObservableObject {
    let model: CampTransfer
    let local: CampSession
    let session: CloudSession
    let input: CloudWorkspaceInput
    let history:LocalHistoryBinding
    let worker:CampLocalHandoff
    let transport:TransferFixture
    init() throws {
        let environment=ProcessInfo.processInfo.environment
        guard let path=environment["CAMP_TRANSFER_AUDIT"],path.contains("/.transfer-ui."),
              FileManager.default.fileExists(atPath:path+"/.capybara-ui-fixtures"),
              environment["CAPY_SUPPORT"]==path+"/state" else{throw CampCloudError.invalidResponse}
        let root=URL(fileURLWithPath:path)
        let scope=CloudScope(origin:"https://fixture.invalid",userId:"a0000000-0000-4000-8000-000000000001",campId:"a0000000-0000-4000-8000-000000000002")
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:"a0000000-0000-4000-8000-000000000003",email:"fixture@example.com",name:"Fixture"),token:"synthetic-only",expiresAt:Date().addingTimeInterval(3600))
        let profile=root.appendingPathComponent("claude")
        let transcript=profile.appendingPathComponent("projects/fixture/parent.jsonl")
        let children=profile.appendingPathComponent("projects/fixture/parent/subagents")
        try FileManager.default.createDirectory(at:children,withIntermediateDirectories:true)
        try Data("{\"type\":\"assistant\",\"uuid\":\"parent-message\",\"message\":{\"model\":\"fixture-model\",\"content\":\"Synthetic main conversation\"}}\n".utf8).write(to:transcript)
        for name in ["review","layout"] {
            try Data("{\"type\":\"assistant\",\"uuid\":\"\(name)-message\",\"isSidechain\":true,\"message\":{\"model\":\"fixture-model\",\"content\":\"Synthetic \(name) history\"}}\n".utf8).write(to:children.appendingPathComponent("agent-\(name).jsonl"))
        }
        local=CampSession(id:"fixture",sessionID:"a0000000-0000-4000-8000-000000000004",provider:"Claude",sourceID:"fixture",sourceLabel:"Fixture",root:profile.path,title:"Review a move to cloud",cwd:root.appendingPathComponent("repository").path,updated:1,path:transcript.path,accountID:"",archived:false)
        let encodedLocal=try JSONSerialization.jsonObject(with:JSONEncoder().encode(local))
        func read(_ action:String)throws->Data {
            let process=Process(),input=Pipe(),output=Pipe()
            process.executableURL=Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local");process.arguments=[action]
            process.standardInput=input;process.standardOutput=output;process.standardError=FileHandle.nullDevice
            try process.run();try input.fileHandleForWriting.write(contentsOf:CampWire.body(["session":encodedLocal,"limit":1]));try input.fileHandleForWriting.close()
            let data=output.fileHandleForReading.readDataToEndOfFile();process.waitUntilExit()
            guard process.terminationStatus==0 else{throw CampCloudError.invalidResponse};return data
        }
        let exported=try JSONDecoder().decode(TranscriptExport.self,from:read("export-page")),catalog=try JSONDecoder().decode(LocalChildHistoryCatalog.self,from:read("child-history-catalog"))
        precondition(catalog.sources.count==2)
        history=LocalHistoryBinding(parentId:catalog.parentID,mainRevision:exported.snapshotRevision,children:catalog.sources.map{.init(sourceId:$0.id,revision:$0.revision)},transcriptRevision:1)
        session=CloudSession(id:"a0000000-0000-4000-8000-000000000005",campId:scope.campId,projectId:"a0000000-0000-4000-8000-000000000006",profileId:"a0000000-0000-4000-8000-000000000007",provider:local.provider,profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:credential.actor.deviceId,localSessionId:local.sessionID,title:local.title,model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:1,permissionRevision:1,updatedAt:Date(),executionLocation:"local",executionEpoch:1)
        input=CloudWorkspaceInput(providerAccountId:"a0000000-0000-4000-8000-000000000008",gitConnectionId:"a0000000-0000-4000-8000-000000000009",githubInstallationId:7,repositoryId:5,installationPage:1,repositoryPage:1,ref:"main",purpose:"implementation",expectedSessionRevision:1,expectedProjectRevision:1)
        let cache=try CampCache(directory:root.appendingPathComponent("state"))
        model=CampTransfer(cache:cache,interval:0.3)
        transport=TransferFixture(session)
        worker=CampLocalHandoff(cache:cache)
        let native=local
        worker.nativeSession={_ in native};worker.available={_ in true}
        worker.acceptSession={value,_,_ in precondition(value.executionLocation=="cloud")}
        worker.attach(scope:scope,actor:credential.actor,transport:transport)
        model.attach(scope:scope,credential:credential,transport:transport)
    }
    func runWorker() async {
        await transport.enableSourceWorker()
        while !Task.isCancelled {
            worker.wake()
            do {_ = try await worker.poll()} catch { /* The production review renders the rejected handoff. */ }
            do {try await Task.sleep(for:.milliseconds(300))} catch {break}
        }
        worker.attach(scope:nil,actor:nil,transport:nil)
    }
}

@main struct TransferReviewAudit: App {
    @StateObject private var state=try! TransferAuditState()
    @State private var permissions=PermissionSettings(claudeMode:"manual")
    @State private var revision=0
    private let catalog=PermissionCatalog(provider:"Claude",modes:["manual","acceptEdits","plan"],profiles:[],sandboxes:[],approvals:[],reviewers:[],inherited:"Choose explicit cloud permissions",note:"")
    var body:some Scene {
        Window("Synthetic transfer review · No cloud connection",id:"transfer") {
            VStack(alignment:.leading,spacing:16) {
                HStack {Text("Move to cloud").font(.title2);Spacer();Button("Done"){NSApplication.shared.terminate(nil)}}
                Text(state.local.title).font(.system(size:14,weight:.medium))
                ScrollView {
                    TransferReviewView(transfer:state.model,local:state.local,session:state.session,input:state.input,busy:false,permissions:$permissions,catalog:catalog,accountLabel:"Claude test account",history:state.history) {
                        Task {await state.model.resetReview();revision+=1}
                    }.id(revision).frame(maxWidth:.infinity,alignment:.leading)
                }.frame(maxHeight:480)
            }.padding(24).frame(width:540).background(campCream).foregroundStyle(campInk).tint(campGreen).preferredColorScheme(.light)
                .task {await state.runWorker()}
        }.windowResizability(.contentSize)
    }
}
