import SwiftUI
import AppKit

@MainActor final class TransferAuditState: ObservableObject {
    let model: CampTransfer
    let local: CampSession
    let session: CloudSession
    let input: CloudWorkspaceInput
    let history:LocalHistoryBinding
    init() throws {
        let environment=ProcessInfo.processInfo.environment
        guard let path=environment["CAMP_TRANSFER_AUDIT"],path.contains("/.transfer-ui."),
              FileManager.default.fileExists(atPath:path+"/.capybara-ui-fixtures"),
              environment["CAPY_SUPPORT"]==path+"/state" else{throw CampCloudError.invalidResponse}
        let root=URL(fileURLWithPath:path)
        let scope=CloudScope(origin:"https://fixture.invalid",userId:"a0000000-0000-4000-8000-000000000001",campId:"a0000000-0000-4000-8000-000000000002")
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:"a0000000-0000-4000-8000-000000000003",email:"fixture@example.com",name:"Fixture"),token:"synthetic-only",expiresAt:Date().addingTimeInterval(3600))
        let transcript=root.appendingPathComponent("session.jsonl");try Data().write(to:transcript)
        local=CampSession(id:"fixture",sessionID:"a0000000-0000-4000-8000-000000000004",provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:path,title:"Review a move to cloud",cwd:root.appendingPathComponent("repository").path,updated:1,path:transcript.path,accountID:"",archived:false)
        let encodedLocal=try JSONSerialization.jsonObject(with:JSONEncoder().encode(local))
        func read(_ action:String)throws->Data {
            let process=Process(),input=Pipe(),output=Pipe()
            process.executableURL=Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local");process.arguments=[action]
            process.standardInput=input;process.standardOutput=output;process.standardError=FileHandle.nullDevice
            try process.run();try input.fileHandleForWriting.write(contentsOf:CampWire.body(["session":encodedLocal,"limit":1]));try input.fileHandleForWriting.close()
            let data=output.fileHandleForReading.readDataToEndOfFile();process.waitUntilExit()
            guard process.terminationStatus==0 else{throw CampCloudError.invalidResponse};return data
        }
        let exported=try JSONDecoder().decode(TranscriptExport.self,from:read("export-page")),children=try JSONDecoder().decode(LocalChildHistoryCatalog.self,from:read("child-history-catalog"))
        history=LocalHistoryBinding(parentId:children.parentID,mainRevision:exported.snapshotRevision,children:children.sources.map{.init(sourceId:$0.id,revision:$0.revision)},transcriptRevision:1)
        session=CloudSession(id:"a0000000-0000-4000-8000-000000000005",campId:scope.campId,projectId:"a0000000-0000-4000-8000-000000000006",profileId:"a0000000-0000-4000-8000-000000000007",provider:"Codex",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:credential.actor.deviceId,localSessionId:local.sessionID,title:local.title,model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:1,permissionRevision:1,updatedAt:Date(),executionLocation:"local",executionEpoch:1)
        input=CloudWorkspaceInput(providerAccountId:"a0000000-0000-4000-8000-000000000008",gitConnectionId:"a0000000-0000-4000-8000-000000000009",githubInstallationId:7,repositoryId:5,installationPage:1,repositoryPage:1,ref:"main",purpose:"implementation",expectedSessionRevision:1,expectedProjectRevision:1)
        model=CampTransfer(cache:try CampCache(directory:root.appendingPathComponent("state")),interval:0.3)
        model.attach(scope:scope,credential:credential,transport:TransferFixture(session))
    }
}

@main struct TransferReviewAudit: App {
    @StateObject private var state=try! TransferAuditState()
    @State private var permissions=PermissionSettings(sandbox:"workspace-write",approval:"on-request",reviewer:"user")
    @State private var revision=0
    private let catalog=PermissionCatalog(provider:"Codex",modes:[],profiles:[],sandboxes:["read-only","workspace-write","danger-full-access"],approvals:["on-request","never"],reviewers:["user","auto_review"],inherited:"Choose explicit cloud permissions",note:"")
    var body:some Scene {
        Window("Synthetic transfer review · No cloud connection",id:"transfer") {
            VStack(alignment:.leading,spacing:16) {
                HStack {Text("Move to cloud").font(.title2);Spacer();Button("Done"){NSApplication.shared.terminate(nil)}}
                Text(state.local.title).font(.system(size:14,weight:.medium))
                ScrollView {
                    TransferReviewView(transfer:state.model,local:state.local,session:state.session,input:state.input,busy:false,permissions:$permissions,catalog:catalog,accountLabel:"Codex test account",history:state.history) {
                        Task {await state.model.resetReview();revision+=1}
                    }.id(revision).frame(maxWidth:.infinity,alignment:.leading)
                }.frame(maxHeight:480)
            }.padding(24).frame(width:540).background(campCream).foregroundStyle(campInk).tint(campGreen).preferredColorScheme(.light)
        }.windowResizability(.contentSize)
    }
}
