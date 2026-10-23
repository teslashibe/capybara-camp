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
    var attachmentBodies: [String:Data] = [:]
    var attachmentAttempts = 0
    var lostAttachmentResponse = false
    var invalidAttachmentReceipt = false
    var attachmentCapacity = false
    func fillAttachmentStorage() { attachmentCapacity = true }
    func loseAttachmentResponse() { lostAttachmentResponse = true; lostResponse = false }
    func invalidateAttachmentReceipt() { invalidAttachmentReceipt = true; lostResponse = false }
    func attachmentSnapshot() -> ([String:Data],Int) { (attachmentBodies,attachmentAttempts) }
    func verifyAttachments(_ values:[PublishMessage], conversation:String) {
        for value in values {
            for ref in value.attachments ?? [] where ref.state == "embedded" {
                precondition(attachmentBodies[conversation+":"+ref.sha256!]?.count == ref.size, "Transcript published before attachment bytes")
            }
        }
    }
    var lostChildResponse = false
    init(scope: CloudScope, actor: CloudActor) { self.scope = scope; self.actor = actor }
    func holdNextSession() { holdSession = true; lostResponse = false }
    func isSessionHeld() -> Bool { sessionHeld }
    func moveProject(_ id:String) { remote?.projectId=id;remote?.revision += 1 }
    func moveToCloud() { remote?.executionLocation = "cloud"; remote?.state = "running"; remote?.model = "cloud-actual"; remote?.stage = "In progress"; remote?.title = "Cloud title" }
    func releaseSession() { releaseGate?.resume(); releaseGate = nil; holdSession = false }
    func loseChildResponse() {lostChildResponse=true;lostResponse=false}
    func corruptHistory() { if let key = messages.keys.sorted().first { messages[key]?.text = "Unexpected saved content" } }
    func childSnapshot()->([CloudChildHistory],[String:PublishMessage],Int){(Array(childHistories.values),childMessages,childWrites)}
    func perform(_ operation: CampPublication) async throws -> Data {
        if operation.method == "GET" {
            guard let remote else { throw CampCloudError.server(404, "not_found") }
            if operation.path.contains("/transcript?") {
                let parts = URLComponents(string: "https://fixture.example" + operation.path)!
                let before = Int64(parts.queryItems?.first { $0.name == "before" }?.value ?? "0") ?? 0
                let all = messages.values.filter { before == 0 || $0.ordinal < before }.sorted { $0.ordinal < $1.ordinal }
                let page = Array(all.suffix(200)).map { CloudMessage(id: $0.id, ordinal: $0.ordinal, role: $0.role, text: $0.text, removed: $0.removed,
                    model: $0.model, effort: $0.effort, speed: $0.speed, revision: remote.transcriptRevision, attachments: $0.attachments) }
                return try CampWire.encoder().encode(CloudTranscript(revision: remote.transcriptRevision, messages: page,
                    before: all.count > 200 ? page.first!.ordinal : 0, hasMore: all.count > 200))
            }
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
        case "attachment":
            attachmentAttempts += 1
            if attachmentCapacity {throw CampCloudError.server(409,"attachment_storage_limit")}
            let hash = String(operation.path.split(separator:"/").last!)
            let body = Data(base64Encoded:object["data"] as! String)!
            let conversation = object["child_id"] as? String ?? remote!.id
            precondition(operation.method == "PUT" && PublishMessage.hash(body) == hash)
            precondition(operation.body.count <= ((body.count+2)/3)*4+128,"Escaped base64 exceeded the bounded upload envelope")
            if conversation != remote!.id { precondition(childHistories[conversation] != nil, "Upload before child registration") }
            attachmentBodies[conversation+":"+hash] = body
            result = try CampWire.body(["session_id":invalidAttachmentReceipt ? UUID().uuidString : conversation,"sha256":hash,"bytes":body.count])
            if lostAttachmentResponse {lostAttachmentResponse=false;throw URLError(.networkConnectionLost)}
        case "profile":
            let value = CloudProfile(id: UUID().uuidString, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, provider: object["provider"] as! String, localId: object["local_id"] as! String, label: object["label"] as! String)
            profile = value; result = try CampWire.encoder().encode(value)
        case "session":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.revision ?? 0 else { throw CampCloudError.server(409, "revision_conflict") }
            let value = CloudSession(id: object["id"] as! String, campId: scope.campId, projectId: object["project_id"] as! String, profileId: object["profile_id"] as! String, provider: profile!.provider, profileLabel: profile!.label, ownerUserId: actor.userId, ownerDeviceId: actor.deviceId, localSessionId: object["local_session_id"] as! String, title: (object["title"] as! String).trimmingCharacters(in: .whitespacesAndNewlines), model: object["model"] as! String, state: object["state"] as! String, stage: object["stage"] as! String, shared: object["shared"] as! Bool, allowRequests: object["allow_requests"] as! Bool, externalOwner: object["external_owner"] as! Bool, revision: expected + 1, transcriptRevision: remote?.transcriptRevision ?? 0, permissionRevision: 1, updatedAt: Date(), executionLocation: object["history_only"] as? Bool == true ? "local" : remote?.executionLocation, executionEpoch: object["history_only"] as? Bool == true ? 1 : nil, historyOnly: object["history_only"] as? Bool)
            remote = value; result = try CampWire.encoder().encode(value)
        case "transcript":
            let expected = (object["expected_revision"] as! NSNumber).int64Value
            guard expected == remote?.transcriptRevision else { throw CampCloudError.server(409, "revision_conflict") }
            let encoded = try JSONSerialization.data(withJSONObject: object["messages"]!)
            let values = try CampWire.decoder().decode([PublishMessage].self, from: encoded)
            verifyAttachments(values, conversation: remote!.id)
            for value in values { messages[value.id] = value }
            remote?.transcriptRevision += 1; transcriptWrites += 1
            result = try CampWire.body(["revision": remote!.transcriptRevision])
        case "child-transcript":
            let id=operation.publishedSessionID!,old=childHistories[id]
            guard (object["expected_parent_revision"] as! NSNumber).int64Value==remote?.transcriptRevision,
                  (object["expected_transcript_revision"] as! NSNumber).int64Value==(old?.transcriptRevision ?? 0) else{throw CampCloudError.server(409,"revision_conflict")}
            let values=try CampWire.decoder().decode([PublishMessage].self,from:JSONSerialization.data(withJSONObject:object["messages"]!))
            verifyAttachments(values, conversation: id)
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
        try await attachmentPublication(root:root,scope:scope,actor:actor,native:native)
        try await savedAttachmentPublication(root:root,scope:scope,actor:actor,native:native)
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
        precondition(changing.shares.first?.publishedHistory?.mainRevision==String(repeating:"b",count:64) && changing.shares.first?.publishedHistory?.transcriptRevision==settled.0?.transcriptRevision,"Published history was not bound to its cloud revision")
        changing.attach(scope:nil,actor:nil,transport:nil)
        let server = PublishingFixture(scope: scope, actor: actor)
        let export: @Sendable (CampSession, Int64) async throws -> TranscriptExport = { _, after in
            let message = PublishMessage(id: after < 0 ? "first" : "second", ordinal: after < 0 ? 0 : 1, role: "assistant", text: after < 0 ? "First message" : "Second message", removed: false)
            return TranscriptExport(messages: [message], after: message.ordinal, hasMore: after < 0, fingerprint: "fixture",contentRevision:String(repeating:"f",count:64))
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
        precondition(childSharing.shares.first?.publishedHistory?.children==[.init(sourceId:String(repeating:"c",count:64),revision:String(repeating:"d",count:64))],"Published history omitted the child revision")
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
        let historyMessages = (0..<230).map { Message(id: "saved-\($0)", role: "assistant", text: $0 == 17 ? String(repeating: "界", count: 100_000) : "Saved message \($0)", model: "historical-model", effort: "ultra", speed: "fast") }
        let history = HistorySourceSnapshot(messages: historyMessages)
        precondition(history.valid)
        precondition(!HistorySourceSnapshot(messages: [historyMessages[0], historyMessages[0]]).valid)
        do { _ = try history.page(after: -2); preconditionFailure("Negative history cursor was accepted") } catch {}
        var pageAfter: Int64 = -1, all: [PublishMessage] = []
        repeat {
            let page = try history.page(after: pageAfter)
            precondition(page.messages.count <= 200 && page.messages.reduce(0, { $0 + $1.text.utf8.count }) <= 1 << 20)
            all += page.messages; pageAfter = page.after
            if !page.hasMore { break }
        } while true
        precondition(all.count == 230 && all[17].text == historyMessages[17].text)
        let historyID = HistorySourceSnapshot.localID(key: "implementation-fixture", messages: historyMessages)
        precondition(historyID == HistorySourceSnapshot.localID(key: "implementation-fixture", messages: historyMessages))
        precondition(HistorySourceSnapshot.limited("界界界", bytes: 7) == "界界")
        let historyLocal = CampSession(id: historyID, sessionID: historyID, provider: "Claude", sourceID: native.sourceID,
            sourceLabel: native.sourceLabel, root: native.root, title: "Saved implementation", cwd: native.cwd, updated: 1,
            path: "", accountID: "", archived: true, model: "historical-model")
        let historyCache = try CampCache(directory: root.appendingPathComponent("frozen-history"))
        let historyServer = PublishingFixture(scope: scope, actor: actor)
        func frozenPublisher() -> CampSharing {
            let publisher = CampSharing(cache: historyCache, export: { _, _ in preconditionFailure("Frozen history reread a live CLI session") })
            publisher.localSession = { _ in nil }
            publisher.childCatalog = { _ in preconditionFailure("Frozen history probed live subagents") }
            publisher.attach(scope: scope, actor: actor, transport: historyServer)
            return publisher
        }
        let frozen = frozenPublisher()
        await eventually("Frozen publisher did not restore") { frozen.ready }
        try await frozen.configureHistory(local: historyLocal, snapshot: history, projectId: project)
        await eventually("Frozen history did not publish") { frozen.shares.first?.state == "Saved to Camp" }
        let historyUploaded = await historyServer.snapshot()
        precondition(historyUploaded.0?.isHistoryOnly == true && historyUploaded.0?.shared == false && historyUploaded.0?.allowRequests == false)
        precondition(historyUploaded.3.count == 230 && historyUploaded.1 == 2, "Lost response duplicated a history page")
        precondition(frozen.shares.first?.publishedHistory?.mainRevision == history.revision)
        var altered = history; altered.messages[0] = Message(id: "saved-0", role: "assistant", text: "Changed")
        do { try await frozen.configureHistory(local: historyLocal, snapshot: altered, projectId: project); preconditionFailure("Replaced a saved snapshot identity") } catch {}
        do { try await frozen.configure(local: historyLocal, projectId: project, shared: true, allowRequests: true); preconditionFailure("History became executable") } catch {}
        frozen.attach(scope: nil, actor: nil, transport: nil)
        try await historyCache.clear(scope)
        let recovered = frozenPublisher()
        await eventually("Recovered publisher did not restore") { recovered.ready }
        try await recovered.configureHistory(local: historyLocal, snapshot: history, projectId: project)
        await eventually("Recovered history did not reconcile") { recovered.shares.first?.state == "Saved to Camp" }
        let recoveredUpload = await historyServer.snapshot()
        precondition(recoveredUpload.0?.id == historyUploaded.0?.id && recoveredUpload.1 == historyUploaded.1 && recoveredUpload.0?.transcriptRevision == historyUploaded.0?.transcriptRevision, "Cache recovery republished a complete snapshot")
        recovered.attach(scope: nil, actor: nil, transport: nil)
        await historyServer.corruptHistory()
        let corrupted = frozenPublisher()
        await eventually("Corrupt-history publisher did not restore") { corrupted.ready }
        await eventually("Unexpected remote content was not rejected") { corrupted.shares.first?.state == "Needs review" }
        let corruptResult = await historyServer.snapshot()
        precondition(corruptResult.1 == recoveredUpload.1, "Conflicting remote history was overwritten")
        corrupted.attach(scope: nil, actor: nil, transport: nil)
        let partialCache = try CampCache(directory: root.appendingPathComponent("partial-history"))
        let partialServer = PublishingFixture(scope: scope, actor: actor)
        let partial = CampSharing(cache: partialCache, export: { _, _ in preconditionFailure("Historical upload launched a CLI") })
        partial.attach(scope: scope, actor: actor, transport: partialServer)
        await eventually("Partial publisher did not restore") { partial.ready }
        try await partial.configureHistory(local: historyLocal, snapshot: history, projectId: project)
        await eventually("The interrupted history page did not reach the server") {
            let state = await partialServer.snapshot()
            return state.1 == 1 && partial.shares.first?.state == "Waiting to sync"
        }
        partial.attach(scope: nil, actor: nil, transport: nil)
        try await partialCache.clear(scope)
        var otherDevice = actor; otherDevice.deviceId = UUID().uuidString
        let continued = CampSharing(cache: partialCache, export: { _, _ in preconditionFailure("History recovery launched a CLI") })
        continued.attach(scope: scope, actor: otherDevice, transport: partialServer)
        await eventually("Recovery publisher did not restore") { continued.ready }
        try await continued.configureHistory(local: historyLocal, snapshot: history, projectId: project)
        await eventually("Another device could not finish the owner's history copy") { continued.shares.first?.state == "Saved to Camp" }
        let resumedHistory = await partialServer.snapshot()
        precondition(resumedHistory.1 == 2 && resumedHistory.3.count == 230 && resumedHistory.0?.ownerDeviceId == actor.deviceId, "History recovery duplicated data or changed execution ownership")
        continued.attach(scope: nil, actor: nil, transport: nil)
        print("Native sharing, parent/child pagination, lost-response recovery, tombstones, privacy and hash isolation passed")
    }
}

actor AttachmentReadGate {
    var started = false
    var continuation: CheckedContinuation<Void,Never>?
    func read(_ data:Data) async -> Data {
        started=true
        await withCheckedContinuation {continuation=$0}
        return data
    }
    func isStarted()->Bool {started}
    func markRead(){started=true}
    func release(){continuation?.resume();continuation=nil}
}

extension CampSharingTests {
    @MainActor static func attachmentPublication(root:URL,scope:CloudScope,actor:CloudActor,native:CampSession) async throws {
        let data = Data(repeating:0xff,count:(1<<20)+13)
        let ref = TranscriptAttachment(id:"a:0",kind:"image",source:"inline",sourceDigest:String(repeating:"e",count:64),state:"embedded",mediaType:"image/png",sha256:PublishMessage.hash(data),size:data.count)
        var duplicate=ref;duplicate.id="a:1"
        let message=PublishMessage(id:"image",ordinal:0,role:"user",text:"",removed:false,attachments:[ref,duplicate])
        let revision=String(repeating:"f",count:64)
        let export: @Sendable (CampSession,Int64) async throws -> TranscriptExport = {_,_ in TranscriptExport(messages:[message],after:0,hasMore:false,fingerprint:revision)}
        let source=LocalChildHistorySource(id:String(repeating:"c",count:64),localID:"child",name:"Helper",phase:"review",workflow:"",status:"done",model:"fixture",revision:revision)
        let catalog=LocalChildHistoryCatalog(parentID:String(repeating:"a",count:64),revision:String(repeating:"b",count:64),sources:[source])
        let cache=try CampCache(directory:root.appendingPathComponent("attachments"))
        let server=PublishingFixture(scope:scope,actor:actor)
        await server.loseAttachmentResponse()
        let sharing=CampSharing(cache:cache,export:export)
        sharing.localSession={_ in native}
        sharing.childCatalog={_ in catalog}
        sharing.childPage={_,_,_ in LocalChildHistoryPage(parentID:catalog.parentID,source:source,page:try await export(native,-1))}
        sharing.attachmentBytes={local,child,id,attachment in
            precondition(local.id==native.id && id=="image" && attachment==ref)
            precondition(child == nil || child?.id==source.id)
            return data
        }
        sharing.attach(scope:scope,actor:actor,transport:server)
        await eventually("Attachment publisher did not initialize"){sharing.ready}
        try await sharing.configure(local:native,projectId:UUID().uuidString,shared:true,allowRequests:false)
        await eventually("Attachment publication did not recover lost upload response"){sharing.shares.first?.state=="Shared"}
        let blobs=await server.attachmentSnapshot(),published=await server.snapshot(),children=await server.childSnapshot()
        precondition(blobs.0.count==2 && blobs.1==3 && blobs.0.values.allSatisfy{$0==data},"Expected one body per conversation and one safe upload retry")
        precondition(published.3["image"]?.attachments==[ref,duplicate] && children.1["image"]?.attachments==[ref,duplicate])
        let saved=try await cache.shares(scope)
        precondition(saved.first?.pending==nil)
        let savedWire=try CampWire.encoder().encode(saved)
        precondition(savedWire.range(of:data.base64EncodedData()) == nil,"Raw bytes leaked into publication journal")
        sharing.attach(scope:nil,actor:nil,transport:nil)
        let resumed=CampSharing(cache:cache,export:export)
        resumed.localSession={_ in native}
        resumed.childCatalog=sharing.childCatalog;resumed.childPage=sharing.childPage
        resumed.attachmentBytes={_,_,_,_ in preconditionFailure("Restart unnecessarily reuploaded committed bytes")}
        var resumedSync=false
        resumed.changed={if resumed.shares.first?.state=="Syncing transcript" {resumedSync=true}}
        resumed.attach(scope:scope,actor:actor,transport:server)
        await eventually("Attachment publisher did not restore"){resumedSync && resumed.shares.first?.state=="Shared"}
        resumed.attach(scope:nil,actor:nil,transport:nil)
        let pendingCache=try CampCache(directory:root.appendingPathComponent("attachment-pending"))
        let pendingServer=PublishingFixture(scope:scope,actor:actor)
        let pending=CampSharing(cache:pendingCache,export:export)
        pending.localSession={_ in native}
        pending.childCatalog={_ in LocalChildHistoryCatalog(parentID:catalog.parentID,revision:catalog.revision,sources:[])}
        pending.attachmentBytes={_,_,_,_ in data}
        pending.attach(scope:scope,actor:actor,transport:pendingServer)
        await eventually("Pending attachment fixture did not initialize"){pending.ready}
        try await pending.configure(local:native,projectId:UUID().uuidString,shared:true,allowRequests:false)
        await eventually("Lost transcript receipt did not retain its command"){pending.shares.first?.state=="Waiting to sync" && pending.shares.first?.pending?.kind=="transcript"}
        pending.attach(scope:nil,actor:nil,transport:nil)
        let replay=CampSharing(cache:pendingCache,export:export)
        replay.localSession={_ in native}
        replay.childCatalog=pending.childCatalog
        replay.attachmentBytes={_,_,_,_ in preconditionFailure("Pending transcript recovery reuploaded attachment bytes")}
        replay.attach(scope:scope,actor:actor,transport:pendingServer)
        await eventually("Pending attachment transcript did not recover after restart"){replay.shares.first?.state=="Shared"}
        let recovered=await pendingServer.snapshot(),recoveredBodies=await pendingServer.attachmentSnapshot()
        precondition(recovered.1==1 && recoveredBodies.1==1 && recovered.3["image"]?.attachments==[ref,duplicate],"Restart duplicated the attachment or transcript")
        replay.attach(scope:nil,actor:nil,transport:nil)
        // Failed verification cannot publish a transcript or durable command.
        for failure in ["source","receipt","detach","erase","capacity"] {
            let localCache=try CampCache(directory:root.appendingPathComponent("attachment-"+failure))
            let target=PublishingFixture(scope:scope,actor:actor),gate=AttachmentReadGate()
            if failure=="receipt" {await target.invalidateAttachmentReceipt()}
            if failure=="capacity" {await target.fillAttachmentStorage()}
            let publisher=CampSharing(cache:localCache,export:export)
            publisher.localSession={_ in native}
            publisher.childCatalog={_ in LocalChildHistoryCatalog(parentID:catalog.parentID,revision:catalog.revision,sources:[])}
            publisher.attachmentBytes={_,_,_,_ in
                if failure=="detach" || failure=="erase" {return await gate.read(data)}
                await gate.markRead()
                if failure=="source" {return Data("changed".utf8)}
                return data
            }
            publisher.attach(scope:scope,actor:actor,transport:target)
            await eventually("Attachment failure fixture did not initialize"){publisher.ready}
            try await publisher.configure(local:native,projectId:UUID().uuidString,shared:true,allowRequests:false)
            if failure=="detach" || failure=="erase" {
                await eventually("Attachment read did not start"){await gate.isStarted()}
                if failure=="erase" {try await localCache.clear(scope)} else {publisher.attach(scope:nil,actor:nil,transport:nil)}
                await gate.release()
                try await Task.sleep(for:.milliseconds(80))
            } else {await eventually("Attachment verification did not refuse publication"){await gate.isStarted() && publisher.shares.first?.state==(failure=="capacity" ? "Attachment storage full" : "Waiting to sync")}}
            if failure=="capacity" {try await Task.sleep(for:.milliseconds(1200))}
            let result=await target.snapshot(),uploads=await target.attachmentSnapshot(),journal=try await localCache.shares(scope)
            precondition(result.1==0 && result.3.isEmpty && journal.allSatisfy{$0.pending==nil},"Failed attachment published transcript")
            precondition(uploads.1 == (["receipt","capacity"].contains(failure) ? 1 : 0),"Cancelled/invalid read uploaded bytes")
            publisher.attach(scope:nil,actor:nil,transport:nil)
        }
        print("Attachment publication: parent/child ordering, retry, dedupe, restart, verification and scope cancellation passed")
    }
}

extension CampSharingTests {
    @MainActor static func savedAttachmentPublication(root: URL, scope: CloudScope, actor: CloudActor, native: CampSession) async throws {
        let data = Data(repeating: 0x41, count: (1 << 20) + 1)
        let embedded = TranscriptAttachment(id:"a:0",kind:"document",source:"inline",sourceDigest:String(repeating:"d",count:64),state:"embedded",mediaType:"text/plain",sha256:PublishMessage.hash(data),size:data.count)
        let unavailable = TranscriptAttachment(id:"a:1",kind:"image",source:"remote",sourceDigest:String(repeating:"e",count:64),state:"reference")
        let messages = [Message(id:"saved-file",role:"user",text:"",attachments:[embedded,unavailable])]
        let snapshot = HistorySourceSnapshot(messages:messages,attachmentSource:native)
        precondition(snapshot.valid && !HistorySourceSnapshot(messages:messages).valid)
        let local = LocalPlanImport.historySession(native,id:HistorySourceSnapshot.localID(key:"saved-file",messages:messages),title:"Saved file",model:"fixture")
        for loss in ["body","transcript"] {
            let cache = try CampCache(directory:root.appendingPathComponent("saved-attachment-"+loss))
            let server = PublishingFixture(scope:scope,actor:actor)
            if loss == "body" { await server.loseAttachmentResponse() }
            let sharing = CampSharing(cache:cache,export:{_,_ in preconditionFailure("Saved history read live text")})
            sharing.attachmentBytes={source,child,message,ref in
                precondition(source == native && child == nil && message == "saved-file" && ref == embedded)
                return data
            }
            sharing.attach(scope:scope,actor:actor,transport:server)
            await eventually("Saved attachments did not initialize"){sharing.ready}
            try await sharing.configureHistory(local:local,snapshot:snapshot,projectId:UUID().uuidString)
            await eventually("Saved attachments did not publish"){sharing.shares.first?.state == "Saved to Camp"}
            let uploaded = await server.snapshot(), blobs = await server.attachmentSnapshot()
            precondition(uploaded.1 == 1 && uploaded.3["saved-file"]?.attachments == [embedded,unavailable])
            precondition(blobs.0.count == 1 && blobs.0.values.first == data)
            let copied = try await sharing.historyTranscript(for:local.id)
            precondition(copied.messages.first?.attachments == [embedded,unavailable])
            let journal = try CampWire.encoder().encode(await cache.shares(scope))
            precondition(journal.range(of:data.base64EncodedData()) == nil)
            sharing.attach(scope:nil,actor:nil,transport:nil)
            let restored = CampSharing(cache:cache,export:{_,_ in preconditionFailure("Recovery read live text")})
            restored.attachmentBytes={_,_,_,_ in preconditionFailure("Committed saved bytes were read again")}
            restored.attach(scope:scope,actor:actor,transport:server)
            await eventually("Saved attachment recovery failed"){restored.ready && restored.shares.first?.publishedHistory != nil}
            _ = try await restored.historyTranscript(for:local.id)
            restored.attach(scope:nil,actor:nil,transport:nil)
        }
        let page = try snapshot.page(after:-1)
        var copied = page.messages.map { CloudMessage(id:$0.id,ordinal:$0.ordinal,role:$0.role,text:$0.text,removed:false,revision:1,attachments:$0.attachments) }
        precondition(snapshot.matchesPrefix(copied))
        copied[0].attachments = nil
        precondition(!snapshot.matchesPrefix(copied),"Missing attachment descriptors matched saved history")
    }
}
