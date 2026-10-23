import Foundation

final class AttachmentHTTPFixture: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    private static var started = 0
    private static var stopped = 0
    static func counts()->(Int,Int) {lock.lock();defer{lock.unlock()};return(started,stopped)}
    override class func canInit(with request:URLRequest)->Bool {true}
    override class func canonicalRequest(for request:URLRequest)->URLRequest {request}
    override func startLoading() {
        Self.lock.lock();Self.started += 1;Self.lock.unlock()
        let mode=request.value(forHTTPHeaderField:"X-Fixture") ?? (request.url?.lastPathComponent=="attachment" ? "large" : "normal")
        let status=mode=="denied" ? 403 : 200
        let headers=mode=="declared" ? ["Content-Length":"99999999"] : [String:String]()
        client?.urlProtocol(self,didReceive:HTTPURLResponse(url:request.url!,statusCode:status,httpVersion:nil,headerFields:headers)!,cacheStoragePolicy:.notAllowed)
        if mode=="hold" {return}
        if mode=="denied" {client?.urlProtocol(self,didLoad:Data(#"{"error":"forbidden"}"#.utf8))}
        else {
            let count=mode=="large" ? 20 : mode=="oversize" ? 5 : 4
            for _ in 0..<count {client?.urlProtocol(self,didLoad:Data(repeating:0xff,count:256<<10))}
        }
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {Self.lock.lock();Self.stopped += 1;Self.lock.unlock()}
}

actor AttachmentResponseFixture: CampAccountTransport {
    var response:Data
    var held=false
    var started=false
    var continuation:CheckedContinuation<Void,Never>?
    var calls=0
    init(_ response:Data){self.response=response}
    func hold(){held=true}
    func isStarted()->Bool{started}
    func release(){continuation?.resume();continuation=nil}
    func count()->Int{calls}
    func data(_ method:String,path:String,body:Data?,key:String?,token:String?) async throws->Data {
        calls += 1;started=true
        precondition(method=="POST" && path.hasSuffix("/attachment") && key==nil && token=="synthetic")
        let input=try JSONSerialization.jsonObject(with:body!) as! [String:Any]
        precondition(input["source_digest"] != nil && input["attachment_id"] != nil && input["revision"] != nil)
        if held {await withCheckedContinuation{continuation=$0}}
        return response
    }
}

@main struct CampAttachmentReadTests {
    static func rejects(_ label:String,_ action:()async throws->Void)async {
        do {try await action();preconditionFailure(label)}catch{}
    }
    static func until(_ condition:()async->Bool)async throws {
        for _ in 0..<200 {if await condition(){return};try await Task.sleep(for:.milliseconds(10))}
        preconditionFailure("Attachment test did not settle")
    }
    static func main()async throws {
        let configuration=URLSessionConfiguration.ephemeral;configuration.protocolClasses=[AttachmentHTTPFixture.self]
        let transport=CampBoundedHTTP(configuration:configuration)
        let url=URL(string:"https://attachment.example/read")!
        let normal=try await transport.data(URLRequest(url:url),limit:1<<20)
        precondition(normal.0==Data(repeating:0xff,count:1<<20))
        for mode in ["oversize","declared"] {
            var request=URLRequest(url:url);request.setValue(mode,forHTTPHeaderField:"X-Fixture")
            do {_ = try await transport.data(request,limit:1<<20);preconditionFailure("Accepted oversized attachment")}
            catch CampCloudError.responseTooLarge {}
        }
        let previous=AttachmentHTTPFixture.counts()
        var held=URLRequest(url:url);held.setValue("hold",forHTTPHeaderField:"X-Fixture")
        let pending=Task {try await transport.data(held,limit:1<<20)}
        try await until {AttachmentHTTPFixture.counts().0>previous.0}
        pending.cancel()
        do {_ = try await pending.value;preconditionFailure("Cancelled download returned data")}catch is CancellationError{}
        try await until {AttachmentHTTPFixture.counts().1>previous.1}
        // Per-request cancellation does not close the reusable session.
        let recovered=try await transport.data(URLRequest(url:url),limit:1<<20)
        precondition(recovered.0.count==1<<20)
        let admission=AttachmentHTTPFixture.counts()
        let heldOne=Task {try await transport.data(held,limit:1<<20)}
        let heldTwo=Task {try await transport.data(held,limit:1<<20)}
        try await until {AttachmentHTTPFixture.counts().0>=admission.0+2}
        do {_ = try await transport.data(URLRequest(url:url),limit:1<<20);preconditionFailure("Third large download was admitted")}
        catch CampCloudError.server(429,"attachment_busy") {}
        precondition(AttachmentHTTPFixture.counts().0==admission.0+2,"Rejected download reached the network")
        heldOne.cancel();heldTwo.cancel()
        await rejects("First held download survived cancellation"){_ = try await heldOne.value}
        await rejects("Second held download survived cancellation"){_ = try await heldTwo.value}
        let stopped=AttachmentHTTPFixture.counts()
        let closing=Task {try await transport.data(held,limit:1<<20)}
        try await until {AttachmentHTTPFixture.counts().0>stopped.0}
        transport.cancel()
        await rejects("Closing client left a download alive"){_ = try await closing.value}

        let api=CampHTTP(endpoint:try CampEndpoint("https://attachment.example"),configuration:configuration)
        let path="/v1/camps/\(UUID().uuidString)/sessions/\(UUID().uuidString)/attachment"
        let large=try await api.data("POST",path:path,body:Data("{}".utf8))
        precondition(large.count==5<<20)
        for (method,target) in [("GET",path),("POST",path+"?extra=1"),("POST","/v1/camps/invalid/sessions/invalid/attachment")] {
            do {_ = try await api.data(method,path:target);preconditionFailure("Large response allowance escaped the exact attachment route")}
            catch CampCloudError.responseTooLarge {}
        }
        await api.cancel()

        let scope=CloudScope(origin:"https://camp.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let actor=CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture")
        let credential=CloudCredential(origin:scope.origin,actor:actor,token:"synthetic",expiresAt:Date().addingTimeInterval(3600))
        let session=CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Claude",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:actor.deviceId,localSessionId:"fixture",title:"Fixture",model:"fixture",state:"idle",stage:"Ready",shared:true,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:3,permissionRevision:2,updatedAt:Date())
        let bytes=Data("embedded image fixture".utf8)
        let ref=TranscriptAttachment(id:"a:0",kind:"image",source:"inline",sourceDigest:String(repeating:"a",count:64),state:"embedded",mediaType:"image/png",sha256:PublishMessage.hash(bytes),size:bytes.count)
        let selection=CampAttachmentSelection(session:session,revision:3,messageId:"image",attachment:ref)
        let object:[String:Any]=["session_id":session.id,"message_id":"image","revision":3,"permission_revision":2,"attachment":try JSONSerialization.jsonObject(with:CampWire.encoder().encode(ref)),"data":bytes.base64EncodedString()]
        let fixture=AttachmentResponseFixture(try CampWire.body(object))
        let actual=try await CampAttachmentReader.read(selection,scope:scope,credential:credential,transport:fixture)
        precondition(actual==bytes)
        for (key,value) in [("session_id",UUID().uuidString as Any),("message_id","other" as Any),("revision",2 as Any),("permission_revision",1 as Any),("data",Data("wrong".utf8).base64EncodedString() as Any),("data",Data(repeating:0,count:bytes.count).base64EncodedString() as Any)] {
            var changed=object;changed[key]=value
            let wrong=AttachmentResponseFixture(try CampWire.body(changed))
            await rejects("Accepted mismatched attachment response"){_ = try await CampAttachmentReader.read(selection,scope:scope,credential:credential,transport:wrong)}
        }
        var child=selection;child.childId=UUID().uuidString
        await rejects("Parent response accepted for child"){_ = try await CampAttachmentReader.read(child,scope:scope,credential:credential,transport:fixture)}
        var childObject=object;childObject["session_id"]=child.childId!
        let childFixture=AttachmentResponseFixture(try CampWire.body(childObject))
        let childBytes=try await CampAttachmentReader.read(child,scope:scope,credential:credential,transport:childFixture)
        precondition(childBytes==bytes)
        let heldResponse=AttachmentResponseFixture(try CampWire.body(object));await heldResponse.hold()
        let read=Task {try await CampAttachmentReader.read(selection,scope:scope,credential:credential,transport:heldResponse)}
        try await until {await heldResponse.isStarted()}
        read.cancel();await heldResponse.release()
        do {_ = try await read.value;preconditionFailure("Late cancelled response exposed bytes")}catch is CancellationError{}
        for mode in ["origin","user","reference","revision","hash"] {
            var invalid=selection,auth=credential
            if mode=="origin" {auth.origin="https://other.example"}
            if mode=="user" {auth.actor.userId=UUID().uuidString}
            if mode=="reference" {invalid.attachment.state="reference"}
            if mode=="revision" {invalid.revision=4}
            if mode=="hash" {invalid.attachment.sha256="not-a-hash"}
            let before=await fixture.count()
            await rejects("Invalid attachment selection sent a request"){_ = try await CampAttachmentReader.read(invalid,scope:scope,credential:auth,transport:fixture)}
            let after=await fixture.count();precondition(before==after)
        }
        print("Attachment reads: chunk bounds, cancellation, reusable session, exact selection/hash/revision and origin isolation passed")
    }
}
