import Foundation

actor PublishingFixture: CampPublishingTransport {
    let scope: CloudScope
    let actor: CloudActor
    var profile: CloudProfile?
    var remote: CloudSession?
    var receipts: [String: Data] = [:]
    var keys: [String] = []
    var messages: [String: PublishMessage] = [:]
    var lostResponse = true
    var transcriptWrites = 0
    var holdSession = false
    var sessionHeld = false
    var releaseGate: CheckedContinuation<Void, Never>?
    var childHistories: [String:CloudChildHistory] = [:]
    var childMessages: [String:PublishMessage] = [:]
    var childWrites = 0
    var lostChildResponse = false
    init(scope: CloudScope, actor: CloudActor) { self.scope = scope; self.actor = actor }
    func holdNextSession() { holdSession = true; lostResponse = false }
    func isSessionHeld() -> Bool { sessionHeld }
    func moveProject(_ id:String) { remote?.projectId=id;remote?.revision += 1 }
    func moveToCloud() { remote?.executionLocation = "cloud"; remote?.state = "running"; remote?.model = "cloud-actual"; remote?.stage = "In progress"; remote?.title = "Cloud title" }
    func releaseSession() { releaseGate?.resume(); releaseGate = nil; holdSession = false }
    func loseChildResponse() {lostChildResponse=true;lostResponse=false}
    func childSnapshot()->([CloudChildHistory],[String:PublishMessage],Int){(Array(childHistories.values),childMessages,childWrites)}
    func perform(_ operation: CampPublication) async throws -> Data {
        if operation.method == "GET" {
            guard let remote else { throw CampCloudError.server(404, "not_found") }
            if operation.path.hasSuffix("/children/publication") {
                return try CampWire.encoder().encode(CloudChildHistoryList(parentRevision:remote.transcriptRevision,permissionRevision:remote.permissionRevision,children:Array(childHistories.values)))
            }
            return try CampWire.encoder().encode(remote)
        }
        keys.append(operation.key)
        if let receipt = receipts[operation.key] { return receipt }
        if operation.kind == "session" && holdSession && !sessionHeld {
            sessionHeld = true
            await withCheckedContinuation { releaseGate = $0 }
            if let receipt = receipts[operation.key] { return receipt }
        }
        let object = try JSONSerialization.jsonObject(with: operation.body) as! [String: Any]
        let result: Data
        switch operation.kind {
        case "profile":
            let value = CloudProfile(id: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, provider: object["provider"] as! String, localId: object["local_id"] as! String, label: object["label"] as! String)
            profile = value; result = try CampWire.encoder().encode(value)
        case "session":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.revision ?? 0 else { throw CampCloudError.server(409, "revision_conflict") }
            let value = CloudSession(id: object["id"] as! String, campId: scope.campId, projectId: object["project_id"] as! String, profileId: object["profile_id"] as! String, provider: profile!.provider, profileLabel: profile!.label, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: object["local_session_id"] as! String, title: (object["title"] as! String).trimmingCharacters(in: .whitespacesAndNewlines), model: object["model"] as! String, state: object["state"] as! String, stage: object["stage"] as! String, shared: object["shared"] as! Bool, allowRequests: object["allow_requests"] as! Bool, externalOwner: object["external_owner"] as! Bool, revision: expected + 1, transcriptRevision: remote?.transcriptRevision ?? 0, permissionRevision: 1, updatedAt: Date(), executionLocation: remote?.executionLocation)
            remote = value; result = try CampWire.encoder().encode(value)
        case "transcript":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.transcriptRevision else { throw CampCloudError.server(409, "revision_conflict") }
            let encoded = try JSONSerialization.data(withJSONObject: object["messages"]!)
            let values = try CampWire.decoder().decode([PublishMessage].self, from: encoded)
            for value in values { messages[value.id] = value }
            remote?.transcriptRevision += 1; transcriptWrites += 1
            result = try CampWire.body(["revision": remote!.transcriptRevision])
        case "child-transcript":
            let id=operation.publishedSessionID!,old=childHistories[id]
            guard (object["expected_parent_revision"] as! NSNumber).int64Value==remote?.transcriptRevision,
                  (object["expected_transcript_revision"] as! NSNumber).int64Value==(old?.transcriptRevision ?? 0) else{throw CampCloudError.server(409,"revision_conflict")}
            let values=try CampWire.decoder().decode([PublishMessage].self,from:JSONSerialization.data(withJSONObject:object["messages"]!))
            for value in values {childMessages[value.id]=value}
            remote?.transcriptRevision += 1;childWrites += 1
            let child=CloudChildHistory(id:id,parentId:remote!.id,sourceKey:object["source_key"] as! String,sourceRevision:object["source_revision"] as! String,name:object["name"] as! String,phase:object["phase"] as! String,workflow:object["workflow"] as! String,state:object["state"] as! String,model:object["model"] as! String,complete:object["complete"] as! Bool,removed:object["removed"] as! Bool,revision:remote!.transcriptRevision,transcriptRevision:(old?.transcriptRevision ?? 0)+(values.isEmpty ? 0 : 1))
            childHistories[id]=child;result=try CampWire.encoder().encode(child)
        default: throw CampCloudError.invalidResponse
        }
        receipts[operation.key] = result
        if operation.kind == "transcript" && lostResponse { lostResponse = false; throw URLError(.networkConnectionLost) }
        if operation.kind == "child-transcript" && lostChildResponse {lostChildResponse=false;throw URLError(.networkConnectionLost)}
        return result
    }
    func snapshot() -> (CloudSession?, Int, [String], [String: PublishMessage]) { (remote, transcriptWrites, keys, messages) }
}

actor ChildSourceFixture {
    var visible=true
    let source=LocalChildHistorySource(id:String(repeating:"c",count:64),localID:"child",name:"Review helper",phase:"review",workflow:"Feature",status:"done",model:String(repeating:"m",count:100),revision:String(repeating:"d",count:64))
    func show(_ visible:Bool){self.visible=visible}
    func catalog()->LocalChildHistoryCatalog {LocalChildHistoryCatalog(parentID:String(repeating:"a",count:64),revision:String(repeating:visible ? "b" : "e",count:64),sources:visible ? [source] : [])}
    func page(_ after:Int64)->LocalChildHistoryPage {
        let start=after<0 ? 0 : 200,end=after<0 ? 200 : 230
        let values=(start..<end).map{PublishMessage(id:$0==0 ? "first" : "child-\($0)",ordinal:Int64($0),role:"assistant",text:"Child message \($0)",removed:false,model:"child-model")}
        return LocalChildHistoryPage(parentID:String(repeating:"a",count:64),source:source,page:TranscriptExport(messages:values,after:Int64(end-1),hasMore:end<230,fingerprint:source.revision))
    }
}

actor ChangingHistoryFixture {
    var stable=false
    var reads=0
    func settle(){stable=true}
    func count()->Int{reads}
    func page(_ after:Int64)->TranscriptExport {
        reads += 1
        let newer=stable || after>=0
        let message=PublishMessage(id:after<0 ? "first" : "second",ordinal:after<0 ? 0 : 1,role:"assistant",text:after<0 ? (newer ? "Updated first" : "Original first") : "Second",removed:false)
        return TranscriptExport(messages:[message],after:message.ordinal,hasMore:after<0,fingerprint:"shared-profile-file",contentRevision:String(repeating:newer ? "b" : "a",count:64))
    }
}

@main struct CampSharingTests {
    @MainActor static func eventually(_ label: String, _ condition: @MainActor () async -> Bool) async {
        for _ in 0..<150 {
            if await condition() { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
        preconditionFailure(label)
    }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-sharing-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = CloudScope(origin: "https://fixture.example", userId: UUID().uuidString, campId: UUID().uuidString)
        let actor = CloudActor(userId: scope.userId, deviceId: UUID().uuidString, email: "fixture@example.com", name: "Fixture")
        let cache = try CampCache(directory: root)
        let native = CampSession(id: "Claude:/local:fixture", sessionID: UUID().uuidString, provider: "Claude", sourceID: "Claude:/local", sourceLabel: "Fixture account", root: "/local", title: "  A synthetic session  ", cwd: "/local/project", updated: 1, path: "/local/session.jsonl", accountID: "", archived: false, model: "fixture-model")
        let changingSource=ChangingHistoryFixture(),changingServer=PublishingFixture(scope:scope,actor:actor)
        let changing=CampSharing(cache:try CampCache(directory:root.appendingPathComponent("changing-history")),export:{_,after in await changingSource.page(after)})
        changing.localSession={_ in native}
        changing.childCatalog={_ in LocalChildHistoryCatalog(parentID:String(repeating:"a",count:64),revision:String(repeating:"b",count:64),sources:[])}
        changing.attach(scope:scope,actor:actor,transport:changingServer)
        try await changing.configure(local:native,projectId:UUID().uuidString,shared:true,allowRequests:false)
        await eventually("Changed pages were not rejected") {await changingSource.count()>1 && changing.shares.first?.state=="Waiting to sync"}
        precondition(changing.shares.first?.state != "Shared","Mixed history was reported as fully shared")
        await changingSource.settle();changing.retry()
        await eventually("Stable history did not recover after retry"){changing.shares.first?.state=="Shared"}
        let settled=await changingServer.snapshot()
        precondition(settled.3["first"]?.text=="Updated first" && settled.3["second"]?.text=="Second","Retry did not replace the incomplete snapshot")
        changing.attach(scope:nil,actor:nil,transport:nil)
        let server = PublishingFixture(scope: scope, actor: actor)
        let export: @Sendable (CampSession, Int64) async throws -> TranscriptExport = { _, after in
            let message = PublishMessage(id: after < 0 ? "first" : "second", ordinal: after < 0 ? 0 : 1, role: "assistant", text: after < 0 ? "First message" : "Second message", removed: false)
            return TranscriptExport(messages: [message], after: message.ordinal, hasMore: after < 0, fingerprint: "fixture")
        }
        let sharing = CampSharing(cache: cache, export: export)
        sharing.childCatalog={_ in LocalChildHistoryCatalog(parentID:String(repeating:"a",count:64),revision:String(repeating:"b",count:64),sources:[])}
        sharing.localSession = { id in id == native.id ? native : nil }
        sharing.attach(scope: scope, actor: actor, transport: server)
        let project = UUID().uuidString
        try await sharing.configure(local: native, projectId: project, shared: true, allowRequests: true)
        await eventually("sharing did not recover lost response") { sharing.shares.first?.state == "Shared" }
        let first = await server.snapshot()
        precondition(first.0?.shared == true && first.0?.allowRequests == true)
        precondition(first.1 == 2 && first.3.count == 2, "lost response repeated provider persistence")
        precondition(first.2.count > Set(first.2).count, "transport did not reuse the command key")
        let saved = try await cache.shares(scope)
        precondition(saved.first?.local == native && saved.first?.pending == nil, "native identifiers did not round-trip")
        precondition(saved.first?.profile?.localId.count == 64, "raw local path was transmitted as profile id")
        let id = saved[0].id
        sharing.attach(scope: nil, actor: nil, transport: nil)
        let resumed = CampSharing(cache: cache, export: export)
        resumed.childCatalog=sharing.childCatalog
        resumed.localSession = { _ in native }
        resumed.attach(scope: scope, actor: actor, transport: server)
        await eventually("sharing did not restore") { resumed.shares.first?.state == "Shared" }
        resumed.retry()
        try? await Task.sleep(for: .milliseconds(100))
        let unchanged = await server.snapshot()
        precondition(unchanged.1 == 2, "unchanged transcript was uploaded again")
        precondition(unchanged.0?.revision == first.0?.revision, "Title whitespace repeatedly republished metadata")
        let reassigned=UUID().uuidString
        await server.moveProject(reassigned);resumed.retry()
        await eventually("Publisher reverted the remote project assignment") { resumed.shares.first?.projectId==reassigned }
        let afterAssignment=await server.snapshot()
        precondition(afterAssignment.0?.projectId==reassigned)
        precondition(resumed.shares.first?.id == id, "reconnect changed shared session identity")
        try await resumed.configure(local: native, projectId: project, shared: false, allowRequests: false)
        await eventually("unshare did not complete") { resumed.shares.first?.state == "Private" }
        let withdrawn = await server.snapshot()
        precondition(withdrawn.0?.shared == false)
        resumed.attach(scope: nil, actor: nil, transport: nil)
        let racingServer = PublishingFixture(scope: scope, actor: actor)
        await racingServer.holdNextSession()
        let racing = CampSharing(cache: try CampCache(directory: root.appendingPathComponent("race")), export: export)
        racing.childCatalog=sharing.childCatalog
        racing.localSession = { _ in native }
        racing.attach(scope: scope, actor: actor, transport: racingServer)
        try await racing.configure(local: native, projectId: project, shared: true, allowRequests: true)
        await eventually("first publication did not start") { await racingServer.isSessionHeld() }
        try await racing.configure(local: native, projectId: project, shared: false, allowRequests: false)
        await eventually("withdrawal raced with first publication") { racing.shares.first?.state == "Private" }
        await racingServer.releaseSession()
        try? await Task.sleep(for: .milliseconds(40))
        let raceResult = await racingServer.snapshot()
        precondition(raceResult.0?.shared == false, "late publication reopened withdrawn sharing")
        racing.attach(scope: nil, actor: nil, transport: nil)
        let privateServer = PublishingFixture(scope: scope, actor: actor)
        let privateSharing = CampSharing(cache: try CampCache(directory: root.appendingPathComponent("private")), export: export)
        privateSharing.childCatalog=sharing.childCatalog
        privateSharing.localSession = { _ in native }
        privateSharing.attach(scope: scope, actor: actor, transport: privateServer)
        try await privateSharing.configure(local: native, projectId: project, shared: false, allowRequests: false, syncToCloud: true)
        await eventually("private cloud copy did not finish") { privateSharing.shares.first?.state == "Saved to Camp" }
        let privateResult = await privateServer.snapshot()
        precondition(privateResult.0?.shared == false && privateResult.0?.allowRequests == false && privateResult.3.count == 2, "Private copy changed sharing permissions or omitted transcript")
        await privateServer.moveToCloud(); privateSharing.retry()
        await eventually("cloud-owned session kept uploading local history") { privateSharing.shares.first?.state == "Runs in Cloud" }
        let cloudResult = await privateServer.snapshot()
        precondition(cloudResult.0?.state == "running" && cloudResult.0?.model == "cloud-actual" && cloudResult.0?.title == "Cloud title" && cloudResult.1 == privateResult.1, "Local publisher overwrote cloud state or transcript")
        precondition(privateSharing.shares.first?.remote?.executionLocation == "cloud", "Cloud ownership was not persisted on the local binding")
        privateSharing.attach(scope: nil, actor: nil, transport: nil)
        let childServer=PublishingFixture(scope:scope,actor:actor),childSource=ChildSourceFixture()
        await childServer.loseChildResponse()
        let childCache=try CampCache(directory:root.appendingPathComponent("children"))
        let childSharing=CampSharing(cache:childCache,export:export)
        childSharing.localSession={_ in native}
        childSharing.childCatalog={_ in await childSource.catalog()}
        childSharing.childPage={_,_,after in await childSource.page(after)}
        childSharing.attach(scope:scope,actor:actor,transport:childServer)
        try await childSharing.configure(local:native,projectId:project,shared:true,allowRequests:false)
        await eventually("child upload did not recover a lost response"){childSharing.shares.first?.state=="Shared"}
        let uploaded=await childServer.childSnapshot()
        precondition(uploaded.0.count==1 && uploaded.0[0].complete && uploaded.1.count==230 && uploaded.2==3,"Child pages were lost or repeated")
        precondition(uploaded.0[0].model==String(repeating:"m",count:100),"Child model identity was truncated")
        let parentUpload=await childServer.snapshot()
        precondition(parentUpload.3["first"]?.text=="First message" && uploaded.1["first"]?.text=="Child message 0","Child transcript replaced its parent")
        let childID=uploaded.0[0].id,childFirst=uploaded.1["first"]!
        let childUnpublished=try await childCache.unpublished([childFirst],scope:scope,sessionId:childID)
        let parentUnpublished=try await childCache.unpublished([childFirst],scope:scope,sessionId:childSharing.shares[0].id)
        precondition(childUnpublished.isEmpty && parentUnpublished.count==1,"Child hashes shared the parent namespace")
        await childSource.show(false);childSharing.retry()
        await eventually("missing child was not removed"){let values=await childServer.childSnapshot();return values.0.first?.removed==true && childSharing.shares.first?.state=="Shared"}
        await childSource.show(true);childSharing.retry()
        await eventually("removed child could not return"){let values=await childServer.childSnapshot();return values.0.first?.removed==false && values.0.first?.complete==true && childSharing.shares.first?.state=="Shared"}
        let returned=await childServer.childSnapshot()
        precondition(returned.0[0].id==childID && returned.1.count==230,"Returning child lost its stable identity")
        childSharing.attach(scope:nil,actor:nil,transport:nil)
        let epoch = try await cache.epoch(scope)
        try await cache.clear(scope)
        do {
            try await cache.saveShare(saved[0], scope: scope, expectedEpoch: epoch)
            preconditionFailure("revoked publisher restored a local sharing grant")
        } catch {}
        let purged = try await cache.shares(scope)
        precondition(purged.isEmpty)
        print("Native sharing, parent/child pagination, lost-response recovery, tombstones, privacy and hash isolation passed")
    }
}
