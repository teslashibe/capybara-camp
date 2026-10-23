import Foundation

private actor AttachmentAPITransport: CampPublishingTransport {
    let publisher: AuthenticatedCampPublisher
    var lost = Set<String>()
    init(api:CampHTTP,token:String) {publisher=AuthenticatedCampPublisher(client:api,token:token)}
    func perform(_ operation:CampPublication) async throws -> Data {
        let response=try await publisher.perform(operation)
        if ["attachment","transcript"].contains(operation.kind),lost.insert(operation.kind).inserted {
            throw URLError(.networkConnectionLost)
        }
        return response
    }
}

@main struct NativeAttachmentAPITests {
    struct Fixture:Decodable {var url:String;var campId:String;var projectId:String}
    struct Driver:Decodable {var token:String}
    @MainActor static func until(_ condition:() async->Bool) async throws {
        for _ in 0..<600 {if await condition(){return};try await Task.sleep(for:.milliseconds(50))}
        preconditionFailure("Attachment publication did not finish through real API")
    }
    @MainActor static func main() async throws {
        guard let directory=ProcessInfo.processInfo.environment["CAMP_NATIVE_API_FIXTURE"] else {
            print("SKIP: run tools/test-native-api.sh NativeAttachmentAPITests");return
        }
        let root=URL(fileURLWithPath:directory)
        precondition(directory.hasPrefix("/") && FileManager.default.fileExists(atPath:root.appendingPathComponent(".capybara-api-fixture").path))
        let fixture=try CampWire.decoder().decode(Fixture.self,from:Data(contentsOf:root.appendingPathComponent("fixture.json")))
        precondition(fixture.url.hasPrefix("http://127.0.0.1:"))
        let driver=try CampWire.decoder().decode(Driver.self,from:Data(contentsOf:root.appendingPathComponent("driver.json")))
        let api=CampHTTP(endpoint:try CampEndpoint(fixture.url,allowLoopback:true))
        let actor=try await api.get(CloudActor.self,path:"/v1/me",token:driver.token)
        _ = try await api.data("POST",path:"/v1/auth/code",body:CampWire.body(["email":"k@example.com"]))
        let code=try String(contentsOf:root.appendingPathComponent("k-code.txt"),encoding:.utf8)
        let teammate=try CampWire.decoder().decode(CloudLogin.self,from:await api.data("POST",path:"/v1/auth/verify",body:CampWire.body(["email":"k@example.com","code":code,"device_name":"Attachment reader Mac"])))
        precondition(teammate.actor.userId != actor.userId && teammate.actor.deviceId != actor.deviceId)
        let vault=MemoryCampCredentials()
        try vault.save(CloudCredential(origin:fixture.url,actor:teammate.actor,token:teammate.token,expiresAt:teammate.expiresAt))
        let reader=CampConnection(directory:root.appendingPathComponent("attachment-reader"),vault:vault,allowLoopback:true)
        reader.server=fixture.url
        await reader.restore()
        precondition(reader.scope?.campId==fixture.campId && reader.signedIn)
        for provider in ["Codex","Claude"] {
            let scope=CloudScope(origin:fixture.url,userId:actor.userId,campId:fixture.campId)
            let cache=try CampCache(directory:root.appendingPathComponent("attachment-cache-"+provider))
            let local=CampSession(id:"fixture-"+provider,sessionID:UUID().uuidString,provider:provider,sourceID:provider+":fixture",sourceLabel:"Attachment fixture",root:root.path,title:"Attachment "+provider,cwd:root.path,updated:1,path:"",accountID:"",archived:false,model:"fixture")
            let bytes=Data(repeating:0xff,count:16<<20)
            let ref=TranscriptAttachment(id:"a:0",kind:"image",source:"inline",sourceDigest:String(repeating:"e",count:64),state:"embedded",mediaType:"image/png",sha256:PublishMessage.hash(bytes),size:bytes.count)
            let message=PublishMessage(id:"image",ordinal:0,role:"user",text:"",removed:false,attachments:[ref])
            let revision=String(repeating:"f",count:64)
            let page=TranscriptExport(messages:[message],after:0,hasMore:false,fingerprint:revision)
            let source=LocalChildHistorySource(id:String(repeating:"c",count:64),localID:"child",name:"Image reviewer",phase:"review",workflow:"",status:"done",model:"fixture",revision:revision)
            let catalog=LocalChildHistoryCatalog(parentID:String(repeating:"a",count:64),revision:String(repeating:"b",count:64),sources:[source])
            let sharing=CampSharing(cache:cache,export:{_,_ in page})
            sharing.localSession={_ in local}
            sharing.childCatalog={_ in catalog}
            sharing.childPage={_,_,_ in LocalChildHistoryPage(parentID:catalog.parentID,source:source,page:page)}
            sharing.attachmentBytes={_,_,_,_ in bytes}
            sharing.attach(scope:scope,actor:actor,transport:AttachmentAPITransport(api:api,token:driver.token))
            try await until {sharing.ready}
            try await sharing.configure(local:local,projectId:fixture.projectId,shared:true,allowRequests:false)
            try await until {sharing.shares.first?.state=="Shared"}
            let published=sharing.shares[0],session=published.remote!,child=published.childHistories!.first!
            precondition(child.complete && published.publishedHistory != nil)
            let base="/v1/camps/\(scope.campId)/sessions/\(session.id)"
            let transcript=try await api.get(CloudTranscript.self,path:base+"/transcript?limit=120",token:teammate.token)
            precondition(transcript.messages.count==1 && transcript.messages[0].attachments==[ref])
            let childTranscript=try await api.get(CloudChildTranscript.self,path:base+"/children/\(child.id)/transcript?limit=120",token:teammate.token)
            precondition(childTranscript.transcript.messages.count==1 && childTranscript.transcript.messages[0].attachments==[ref])
            await reader.refreshSession(session.id)
            for childID in [nil,Optional(child.id)] {
                let selection=CampAttachmentSelection(session:session,childId:childID,revision:session.transcriptRevision,messageId:"image",attachment:ref)
                let body=try await reader.attachmentBytes(selection)
                precondition(body==bytes,"Maximum-size attachment did not round-trip through native connection")
            }
            let selection=CampAttachmentSelection(session:session,revision:session.transcriptRevision,messageId:"image",attachment:ref)
            try await reader.cache!.clear(reader.scope!)
            do {_ = try await reader.attachmentBytes(selection);preconditionFailure("Cleared cache scope returned bytes")}catch is CancellationError{}
            await reader.selectCamp(fixture.campId)
            let restored=try await reader.attachmentBytes(selection)
            precondition(restored==bytes,"Attachment reader did not recover after selecting the Camp again")
            try await sharing.configure(local:local,projectId:fixture.projectId,shared:false,allowRequests:false)
            try await until {sharing.shares.first?.state=="Private"}
            do {
                _ = try await api.data("POST",path:base+"/attachment",body:CampWire.body(["revision":session.transcriptRevision,"message_id":"image","attachment_id":ref.id,"source_digest":ref.sourceDigest]),token:teammate.token)
                preconditionFailure("Unshared attachment remained readable")
            } catch CampCloudError.server(let status,_) {precondition([403,404].contains(status))}
            sharing.attach(scope:nil,actor:nil,transport:nil)
            print("Real API attachment publication passed for \(provider): 16 MiB parent/child bytes through the native reader, cache-clear fencing/recovery, lost upload and transcript responses, independent teammate read and revoked sharing")
        }
        await reader.signOut()
        precondition(!reader.signedIn)
        await api.cancel()
    }
}
