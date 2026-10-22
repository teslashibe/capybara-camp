import Foundation
import Darwin

actor ChildReadGate {
    var waiter:CheckedContinuation<CloudChildHistoryList,Never>?
    func read()async->CloudChildHistoryList {await withCheckedContinuation{waiter=$0}}
    func waiting()->Bool {waiter != nil}
    func finish(_ value:CloudChildHistoryList){waiter?.resume(returning:value);waiter=nil}
}

@main struct CampChildHistoryTests {
    static func check(_ value:Bool,_ message:String){precondition(value,message)}
    @MainActor static func offlineConnectionChecks(root:URL)async throws {
        // Reserve a non-listening loopback port so no real service receives the fixture token.
        let socket=Darwin.socket(AF_INET,SOCK_STREAM,0)
        guard socket>=0 else{preconditionFailure("Could not reserve fixture port")}
        defer{Darwin.close(socket)}
        var address=sockaddr_in();address.sin_len=UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family=sa_family_t(AF_INET);address.sin_addr.s_addr=inet_addr("127.0.0.1")
        let bound=withUnsafePointer(to:&address){$0.withMemoryRebound(to:sockaddr.self,capacity:1){Darwin.bind(socket,$0,socklen_t(MemoryLayout<sockaddr_in>.size))}}
        check(bound==0,"Could not bind fixture port")
        var length=socklen_t(MemoryLayout<sockaddr_in>.size)
        let located=withUnsafeMutablePointer(to:&address){$0.withMemoryRebound(to:sockaddr.self,capacity:1){getsockname(socket,$0,&length)}}
        check(located==0,"Could not locate fixture port")
        let scope=CloudScope(origin:"http://127.0.0.1:\(UInt16(bigEndian:address.sin_port))",userId:UUID().uuidString,campId:UUID().uuidString)
        let directory=root.appendingPathComponent("offline-connection"),vault=MemoryCampCredentials()
        let connection=CampConnection(directory:directory,vault:vault,allowLoopback:true)
        let credential=CloudCredential(origin:scope.origin,actor:CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture"),token:"camp_synthetic_fixture",expiresAt:Date().addingTimeInterval(86400))
        try vault.save(credential)
        let cache=connection.cache!,parent=session(scope),catalog=list(parent),child=catalog.children[0]
        let historyEpoch:Int64=0
        try await cache.setMetadata("server",Data(scope.origin.utf8))
        try await cache.setMetadata("selected:"+scope.userId,Data(scope.campId.utf8))
        try await cache.ingest(scope:scope,session:parent)
        try await cache.saveChildCatalog(catalog,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        let message=CloudMessage(id:"offline",ordinal:0,role:"assistant",text:"Saved before disconnection",removed:false,revision:1)
        let page=CloudChildTranscript(parentRevision:2,permissionRevision:1,child:child,transcript:CloudTranscript(revision:1,messages:[message],before:0,hasMore:false))
        try await cache.saveChildPage(page,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        await connection.restore()
        let savedCatalog=try await connection.childHistories(session:parent)
        check(savedCatalog.cachedAt != nil && savedCatalog.displayChildren.first?.status=="saved","Offline catalog reported live activity")
        let savedPage=try await connection.childTranscript(session:parent,childID:child.id)
        check(savedPage.cachedAt != nil && savedPage.transcript.messages.first?.id=="offline","Connection failure did not return the saved page")
        do {_=try await connection.childMessage(session:parent,childID:child.id,revision:2,messageID:"offline");preconditionFailure("Exact citation silently fell back to a cached page")}catch{}
        print("PASS: real connection failure restores saved child history without claiming live activity")
    }
    static func cacheChecks(root:URL)async throws {
        let directory=root.appendingPathComponent("cache")
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let parent=session(scope),catalog=list(parent),child=catalog.children[0]
        let cache=try CampCache(directory:directory)
        var historyEpoch:Int64=0
        try await cache.ingest(scope:scope,session:parent)
        try await cache.saveChildCatalog(catalog,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        let message=CloudMessage(id:"latest",ordinal:1,role:"assistant",text:"Saved child answer",removed:false,model:"recorded-model",effort:"high",speed:"fast",revision:1)
        let latest=CloudChildTranscript(parentRevision:2,permissionRevision:1,child:child,transcript:CloudTranscript(revision:1,messages:[message],before:1,hasMore:true))
        try await cache.saveChildPage(latest,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        var earlier=latest;earlier.transcript.messages[0].id="earlier";earlier.transcript.messages[0].ordinal=0;earlier.transcript.before=0;earlier.transcript.hasMore=false
        try await cache.saveChildPage(earlier,scope:scope,parent:parent.id,before:1,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        let reopened=try CampCache(directory:directory)
        let saved=try await reopened.childPage(scope,parent:parent.id,child:child.id,permission:1,before:0,revision:0)
        check(saved?.cachedAt != nil && saved?.transcript.messages.first?.text==message.text,"Child page did not persist with an offline timestamp")
        check(try await reopened.childPage(scope,parent:parent.id,child:child.id,permission:1,before:1,revision:2)?.transcript.messages.first?.id=="earlier","Earlier cached page was lost")
        check(try await reopened.childPage(scope,parent:parent.id,child:UUID().uuidString,permission:1,before:0,revision:0)==nil,"Foreign child cache entry was returned")
        check(try await reopened.childPage(scope,parent:parent.id,child:child.id,permission:1,before:0,revision:1)==nil,"Wrong parent revision was returned")
        let foreign=CloudScope(origin:scope.origin,userId:UUID().uuidString,campId:scope.campId)
        do {_=try await reopened.childCatalog(foreign,parent:parent.id,permission:1);preconditionFailure("Child cache crossed accounts")}catch{}
        var invalid=latest;invalid.child.parentId=UUID().uuidString
        do {try await cache.saveChildPage(invalid,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch);preconditionFailure("Foreign parent page was cached")}catch{}
        try await cache.ingest(scope:scope,cursor:10,childrenChanged:parent.id)
        do {try await cache.saveChildCatalog(catalog,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch);preconditionFailure("Late catalog restored an invalidated child")}catch{}
        do {try await cache.saveChildPage(latest,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch);preconditionFailure("Late page restored an invalidated child")}catch{}
        historyEpoch=try await cache.childEpoch(scope,parent:parent.id)
        check(try await cache.childCatalog(scope,parent:parent.id,permission:1)==nil,"Observed child change retained stale offline history")
        try await cache.saveChildCatalog(catalog,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        try await cache.saveChildPage(latest,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        var newer=catalog;newer.parentRevision=3;newer.children[0].revision=3;newer.children[0].removed=true
        try await cache.saveChildCatalog(newer,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        try await cache.saveChildCatalog(catalog,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        check(try await cache.childPage(scope,parent:parent.id,child:child.id,permission:1,before:0,revision:0)==nil,"Removed child or late catalog restored cached history")
        var revoked=parent;revoked.permissionRevision=2;revoked.revision=2
        try await cache.ingest(scope:scope,session:revoked)
        check(try await cache.childCatalog(scope,parent:parent.id,permission:2)==nil,"Permission change retained child cache")
        do {try await cache.saveChildPage(latest,scope:scope,parent:parent.id,before:0,expectedEpoch:0,expectedChildEpoch:historyEpoch);preconditionFailure("Revoked response repopulated child cache")}catch{}
        var current=catalog;current.permissionRevision=2
        try await cache.saveChildCatalog(current,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        var page=latest;page.permissionRevision=2;page.transcript.hasMore=false;page.transcript.before=0
        for index in 2...132 {
            page.transcript.messages[0].ordinal=Int64(index-1)
            try await cache.saveChildPage(page,scope:scope,parent:parent.id,before:Int64(index),expectedEpoch:0,expectedChildEpoch:historyEpoch)
        }
        check(try await cache.childPage(scope,parent:parent.id,child:child.id,permission:2,before:2,revision:0)==nil,"Child cache exceeded its row ceiling")
        check(try await cache.childPage(scope,parent:parent.id,child:child.id,permission:2,before:132,revision:0) != nil,"Row eviction lost the current child catalog")
        page.transcript.messages[0].text=String(repeating:"x",count:1_200_000)
        for index in 200...229 {
            page.transcript.messages[0].ordinal=Int64(index-1)
            try await cache.saveChildPage(page,scope:scope,parent:parent.id,before:Int64(index),expectedEpoch:0,expectedChildEpoch:historyEpoch)
        }
        check(try await cache.childPage(scope,parent:parent.id,child:child.id,permission:2,before:200,revision:0)==nil,"Child cache exceeded its byte ceiling")
        check(try await cache.childPage(scope,parent:parent.id,child:child.id,permission:2,before:229,revision:0) != nil,"Byte eviction lost the newest page")
        try await cache.invalidateChildHistory(scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        historyEpoch=try await cache.childEpoch(scope,parent:parent.id)
        check(try await cache.childCatalog(scope,parent:parent.id,permission:2)==nil,"Explicit access denial retained cached history")
        try await cache.saveChildCatalog(current,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch)
        try await cache.clear(scope)
        do {try await cache.saveChildCatalog(current,scope:scope,parent:parent.id,expectedEpoch:0,expectedChildEpoch:historyEpoch);preconditionFailure("Late response repopulated erased cache")}catch{}
        check(childHistoryTransportUnavailable(URLError(.notConnectedToInternet)),"Offline transport was not eligible for saved history")
        for error:Error in [URLError(.cancelled),URLError(.serverCertificateUntrusted),CampCloudError.server(403,"denied"),CampCloudError.server(500,"failed"),CampCloudError.invalidResponse] {
            check(!childHistoryTransportUnavailable(error),"Security or response failure used offline fallback")
        }
        print("PASS: bounded durable child cache, scope/revision isolation, revocation and erased-cache fencing")
    }
    @MainActor static func until(_ body:()async->Bool)async {
        for _ in 0..<300 {if await body(){return};try? await Task.sleep(for:.milliseconds(5))}
        preconditionFailure("Child reader timed out")
    }
    static func session(_ scope:CloudScope)->CloudSession {
        CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Claude",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:UUID().uuidString,localSessionId:UUID().uuidString,title:"Parent",model:"fixture",state:"idle",stage:"Ready",shared:true,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:2,permissionRevision:1,updatedAt:Date())
    }
    static func list(_ parent:CloudSession)->CloudChildHistoryList {
        CloudChildHistoryList(parentRevision:2,permissionRevision:1,children:[CloudChildHistory(id:UUID().uuidString,parentId:parent.id,sourceKey:String(repeating:"a",count:64),sourceRevision:String(repeating:"b",count:64),name:"Review helper",phase:"review",workflow:"Feature",state:"done",model:"recorded-model",complete:true,removed:false,revision:2,transcriptRevision:1)])
    }
    @MainActor static func main()async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("camp-child-read-"+UUID().uuidString)
        defer{try? FileManager.default.removeItem(at:root)}
        try await cacheChecks(root:root)
        try await offlineConnectionChecks(root:root)
        let store=CampStore(stateDirectory:root)
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let parent=session(scope),other=session(scope),first=list(parent),second=list(other)
        precondition(first.valid(parent:parent.id) && !first.valid(parent:other.id))
        precondition(first.children[0].childSession.model=="recorded-model")
        let cited=CloudMessage(id:"cited",ordinal:0,role:"assistant",text:"Original child answer",removed:false,model:"recorded-model",effort:"high",speed:"standard",revision:1)
        let snapshot=CloudChildTranscript(parentRevision:2,permissionRevision:1,child:first.children[0],transcript:CloudTranscript(revision:1,messages:[cited],before:0,hasMore:false))
        let inspection=try inspectChildHistoryMessage(parent:parent,childID:first.children[0].id,scope:scope.key,revision:2,messageID:"cited",snapshot:snapshot)
        precondition(inspection.sourceID=="camp:"+parent.id && inspection.childID==first.children[0].id && inspection.messages[0].text==cited.text)
        do {_=try inspectChildHistoryMessage(parent:other,childID:first.children[0].id,scope:scope.key,revision:2,messageID:"cited",snapshot:snapshot);preconditionFailure("Child citation crossed parents")}catch{}
        store.applyCloud([parent,other],projects:[])
        let local=store.sessions.first{$0.id=="camp:"+parent.id}!,otherLocal=store.sessions.first{$0.id=="camp:"+other.id}!
        store.selectedID=local.id;store.cloudChildReader={_ in first}
        store.loadChildren(local);await until{!store.childrenLoading}
        precondition(store.children.count==1 && store.children[0].id==first.children[0].id)
        store.selectedChildID=first.children[0].id
        let gate=ChildReadGate();store.cloudChildReader={_ in await gate.read()}
        store.loadChildren(local);await until{await gate.waiting()}
        precondition(store.children.count==1 && store.selectedChildID==first.children[0].id,"Refresh dropped the selected child")
        await gate.finish(first);await until{!store.childrenLoading}
        let race=ChildReadGate()
        store.cloudChildReader={value in if value.id==parent.id{return await race.read()};return second}
        store.loadChildren(local);await until{await race.waiting()}
        store.selectedID=otherLocal.id;store.loadChildren(otherLocal);await until{!store.childrenLoading}
        await race.finish(first);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.first?.id==second.children.first?.id,"Late response replaced another parent's children")
        let navigation=ChildReadGate();store.cloudChildReader={_ in await navigation.read()}
        store.loadChildren(otherLocal);await until{await navigation.waiting()}
        let plan=CampSession(id:"plan:"+UUID().uuidString,sessionID:UUID().uuidString,provider:"Planning",sourceID:"fixture",sourceLabel:"Fixture",root:"",title:"Plan",cwd:"",updated:1,path:"",accountID:"",archived:false)
        store.open(plan)
        await navigation.finish(second);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.isEmpty && store.subagentsParent==nil,"Late child response returned after opening a plan")
        let revoked=ChildReadGate();store.cloudChildReader={_ in await revoked.read()}
        store.selectedID=otherLocal.id
        store.loadChildren(otherLocal);await until{await revoked.waiting()}
        store.applyCloud([],projects:[])
        await revoked.finish(second);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.isEmpty && store.selectedChildID==nil && store.selectedID==nil,"Revoked children returned after a delayed response")
        print("PASS: cloud child projection, preserved selection, parent-switch fencing and revoked-response rejection")
    }
}
