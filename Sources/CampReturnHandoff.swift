import Foundation

struct ReturnHandoffJournal: Codable, Sendable {
    var id: String
    var revision: Int64 = 1
    var destination: LocalDestinationJournal
    var payloadBytes: Int
    var createKey: String; var holdKey: String; var commitKey: String
    var phase = "uploading"
    var cancelling = false
    var transfer: CloudTransfer?
    var handoff: CloudHandoff?
    var commitRevision: Int64?
    var permissions: [String:String]?
    var executionApproved: Bool?
    var session: CloudSession?
}

// Owns one durable return-to-Mac handoff. Preparing/confirming never starts a
// provider. A committed receipt still requires the local continuation mapping.
actor CampReturnHandoff {
    private let cache: CampCache
    private let scope: CloudScope
    private let credential: CloudCredential
    private let transport: any CampAccountTransport
    private let epoch: Int64
    private let destination: CampLocalDestination
    private let files: CampWorkspaceExport
    private var busy=false

    init(cache:CampCache,scope:CloudScope,credential:CloudCredential,transport:any CampAccountTransport,epoch:Int64,
         callLocal:@escaping @Sendable (String,Data)async throws->Data = {try await Bridge.call($0,input:$1)})throws {
        self.cache=cache;self.scope=scope;self.credential=credential;self.transport=transport;self.epoch=epoch
        destination=try CampLocalDestination(cache:cache,scope:scope,credential:credential,transport:transport,epoch:epoch,callLocal:callLocal)
        files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:transport,epoch:epoch)
    }
    private func authority()async throws {
        try Task.checkCancellation()
        guard try await cache.epoch(scope)==epoch else{throw CancellationError()}
    }
    func current()async throws->ReturnHandoffJournal? {
        try await authority()
        guard let raw=try await cache.metadata("return-handoff:"+scope.key) else{return nil}
        guard raw.count<=256<<10 else{throw CampCloudError.invalidResponse}
        let j=try JSONDecoder().decode(ReturnHandoffJournal.self,from:raw)
        guard j.revision>0,[j.id,j.createKey,j.holdKey,j.commitKey].allSatisfy({UUID(uuidString:$0) != nil}),
              j.destination.deviceId==credential.actor.deviceId,j.destination.session.ownerUserId==scope.userId,
              j.destination.session.campId==scope.campId,j.destination.phase=="installed",!j.destination.cancelling,
              j.destination.installation != nil,(2...(46<<20)).contains(j.payloadBytes),
              ["uploading","holding","confirm","committing","complete","cancelled"].contains(j.phase) else{throw CampCloudError.invalidResponse}
        if let t=j.transfer{try validate(t,j)}
        if let h=j.handoff{try validate(h,j)}
        try await authority();return j
    }
    private func save(_ value:ReturnHandoffJournal,previous:Int64?)async throws {
        try await authority()
        try await cache.saveReturnHandoff(value,scope:scope,expectedEpoch:epoch,previousRevision:previous)
        try await authority()
    }
    private func base(_ j:ReturnHandoffJournal)->String {"/v1/camps/\(scope.campId)/sessions/\(j.destination.session.id)"}
    private func request<T:Decodable>(_ type:T.Type,_ method:String,_ path:String,body:Data?=nil,key:String?=nil)async throws->T {
        try await authority()
        let raw=try await transport.data(method,path:path,body:body,key:key,token:credential.token)
        try await authority();return try CampWire.decoder().decode(type,from:raw)
    }
    private func local(_ j:ReturnHandoffJournal)throws->CloudLocalDestination {
        guard let result=j.destination.installation else{throw CampCloudError.invalidResponse}
        return CloudLocalDestination(profileId:j.destination.target.profileID,sessionId:j.destination.target.sessionID,
            purpose:j.destination.target.purpose,receiptSha256:result.receiptSHA256,transcriptRevision:j.destination.session.transcriptRevision)
    }
    private func validate(_ t:CloudTransfer,_ j:ReturnHandoffJournal)throws {
        let d=j.destination,s=d.session
        guard let r=d.installation?.receipt,UUID(uuidString:t.id) != nil,t.campId==scope.campId,t.sessionId==s.id,t.ownerUserId==scope.userId,
              t.sourceLocation=="cloud",t.sourceWorkspaceId==s.executionWorkspaceId,t.sourceEpoch==s.executionEpoch,
              t.sourceRevision==s.revision,t.sourceTranscriptRevision==s.transcriptRevision,t.sourcePermissionRevision==s.permissionRevision,
              t.targetLocation=="local",t.targetDeviceId==credential.actor.deviceId,t.targetAccountId==nil,
              t.payloadSha256==r.payloadSHA256,t.reviewSha256==d.reviewedSha256,t.payloadBytes==j.payloadBytes,
              t.chunkBytes==256<<10,t.uploadedBytes>=0,t.uploadedBytes<=t.payloadBytes,t.revision>0,
              ["uploading","ready","completed","cancelled","expired"].contains(t.state),
              j.transfer==nil || t.id==j.transfer?.id else{throw CampCloudError.invalidResponse}
        if t.state=="ready" || t.state=="completed" {guard t.baseCommit==r.files.baseCommit,t.fileCount==r.files.files else{throw CampCloudError.invalidResponse}}
    }
    private func validate(_ h:CloudHandoff,_ j:ReturnHandoffJournal)throws {
        let s=j.destination.session
        let expected=try local(j)
        guard UUID(uuidString:h.id) != nil,h.transferId==j.transfer?.id,h.campId==scope.campId,h.sessionId==s.id,h.ownerUserId==scope.userId,
              h.sourceDeviceId==s.ownerDeviceId,h.sourceLocation=="cloud",h.sourceWorkspaceId==s.executionWorkspaceId,h.sourceEpoch==s.executionEpoch,
              h.targetLocation=="local",h.targetDeviceId==credential.actor.deviceId,h.localDestination==expected,h.workspaceId.isEmpty,h.workspaceRevision==0,
              j.handoff==nil || h.id==j.handoff?.id,
              h.revision>0,["pending","claimed","acknowledged","committed","released","invalidated","expired"].contains(h.state) else{throw CampCloudError.invalidResponse}
        if h.state=="committed" {guard h.executionEpoch==(s.executionEpoch ?? 0)+1,UUID(uuidString:h.commitNonce ?? "") != nil else{throw CampCloudError.invalidResponse}}
    }
    func begin()async throws {
        guard !busy else{throw CampError.message("The move is already being updated")};busy=true;defer{busy=false}
        guard try await current()==nil,let d=try await destination.advance(),d.phase=="installed",!d.cancelling,
              let bytes=try await files.payload(),PublishMessage.hash(bytes)==d.installation?.receipt.payloadSHA256 else{throw CampCloudError.invalidResponse}
        try await save(ReturnHandoffJournal(id:UUID().uuidString.lowercased(),destination:d,payloadBytes:bytes.count,
            createKey:UUID().uuidString.lowercased(),holdKey:UUID().uuidString.lowercased(),commitKey:UUID().uuidString.lowercased()),previous:nil)
    }
    private func verifyInstallation(_ j:ReturnHandoffJournal)async throws {
        guard let d=try await destination.advance(),d.phase=="installed",!d.cancelling,d.id==j.destination.id,
              d.installation==j.destination.installation,d.target==j.destination.target else{throw CampCloudError.invalidResponse}
    }
    // Approval is stored before delivery. A lost response replays the same
    // permissions and revision with the same key, never a fresh confirmation.
    func confirm(contextContinuationApproved:Bool,executionApproved:Bool,permissions:PermissionSettings)async throws {
        guard !busy else{throw CampError.message("The move is already being updated")};busy=true;defer{busy=false}
        guard var j=try await current(),j.phase=="confirm",!j.cancelling,contextContinuationApproved,let h=j.handoff,h.state=="acknowledged" else{throw CampCloudError.invalidResponse}
        let implementation=j.destination.target.purpose=="implementation"
        guard !implementation || executionApproved && permissions.valid(for:j.destination.session.provider) else{throw CampCloudError.invalidResponse}
        try await verifyInstallation(j)
        let revision=j.revision;j.revision+=1;j.phase="committing";j.commitRevision=h.revision;j.executionApproved=executionApproved
        j.permissions=implementation ? ["profile":permissions.profile,"sandbox":permissions.sandbox,"approval":permissions.approval,"reviewer":permissions.reviewer,"claudeMode":permissions.claudeMode].filter{!$0.value.isEmpty} : [:]
        try await save(j,previous:revision)
    }
    func requestCancellation()async throws {
        guard var j=try await current(),!["complete","cancelled"].contains(j.phase),!j.cancelling else{return}
        let previous=j.revision;j.revision+=1;j.cancelling=true
        try await save(j,previous:previous)
    }
    func discardCancelled()async throws {
        guard !busy else{throw CampError.message("The move is already being updated")};busy=true;defer{busy=false}
        guard let j=try await current(),j.phase=="cancelled" else{throw CampCloudError.invalidResponse}
        if let d=try await destination.current() {
            guard d.id==j.destination.id,d.phase=="cancelled" else{throw CampCloudError.invalidResponse}
            try await destination.discardCancelled()
        }
        try await cache.discardReturnHandoff(j,scope:scope,expectedEpoch:epoch)
        try await authority()
    }
    // Recheck the committed server assignment before adopting it. No provider
    // starts here; the worker must separately reserve an authorized request.
    func adoptCompleted() async throws -> LocalContinuationBinding? {
        guard !busy else { throw CampError.message("The move is already being updated") }
        busy = true; defer { busy = false }
        guard var j = try await current() else { return nil }
        guard j.phase == "complete" else { throw CampCloudError.invalidResponse }
        guard let savedSession = j.session, let installation = j.destination.installation, let handoff = j.handoff else { throw CampCloudError.invalidResponse }
        let before = LocalContinuationBinding(session:savedSession,handoffID:handoff.id,installation:installation)
        try await refreshHandoff(&j)
        guard j.phase == "complete", let current = j.session, before.matches(current) else { throw CampCloudError.invalidResponse }
        let previous = j.revision
        j.revision += 1
        try await save(j,previous:previous)
        let binding = try await cache.adoptReturnHandoff(j, scope: scope, expectedEpoch: epoch)
        try await authority()
        return binding
    }
    @discardableResult func advance()async throws->ReturnHandoffJournal? {
        guard !busy else{throw CampError.message("The move is already being updated")};busy=true;defer{busy=false}
        guard var j=try await current() else{return nil}
        if ["complete","cancelled"].contains(j.phase){return j}
        let previous=j.revision,path=base(j)
        if j.transfer==nil {
            guard let r=j.destination.installation?.receipt else{throw CampCloudError.invalidResponse}
            let s=j.destination.session
            let t=try await request(CloudTransfer.self,"POST",path+"/transfers",body:CampWire.body([
                "target_location":"local","expected_epoch":s.executionEpoch ?? 0,"expected_revision":s.revision,
                "expected_transcript_revision":s.transcriptRevision,"expected_permission_revision":s.permissionRevision,
                "payload_sha256":r.payloadSHA256,"review_sha256":j.destination.reviewedSha256,"payload_bytes":j.payloadBytes]),key:j.createKey)
            try validate(t,j);j.transfer=t
        } else if j.cancelling {
            let t=try await request(CloudTransfer.self,"GET",path+"/transfers/"+j.transfer!.id);try validate(t,j);j.transfer=t
            if t.state=="completed" { try await refreshHandoff(&j) }
            else {
                let cancelled=try await request(CloudTransfer.self,"POST",path+"/transfers/"+t.id+"/cancel",body:Data("{}".utf8))
                try validate(cancelled,j);guard ["cancelled","expired"].contains(cancelled.state) else{throw CampCloudError.invalidResponse}
                j.transfer=cancelled
                try await destination.requestCancellation();_ = try await destination.advance()
                j.phase="cancelled"
            }
        } else if j.phase=="uploading" {
            var t=try await request(CloudTransfer.self,"GET",path+"/transfers/"+j.transfer!.id);try validate(t,j)
            guard ["uploading","ready"].contains(t.state),let bytes=try await files.payload(),bytes.count==j.payloadBytes,
                  PublishMessage.hash(bytes)==t.payloadSha256 else{throw CampCloudError.invalidResponse}
            if t.state=="uploading" {
                let count=(bytes.count+t.chunkBytes-1)/t.chunkBytes,uploaded=t.uploadedChunks ?? []
                guard Set(uploaded).count==uploaded.count,uploaded.allSatisfy({(0..<count).contains($0)}) else{throw CampCloudError.invalidResponse}
                if let index=(0..<count).first(where:{!uploaded.contains($0)}) {
                    let data=bytes.subdata(in:(index*t.chunkBytes)..<min(bytes.count,(index+1)*t.chunkBytes))
                    t=try await request(CloudTransfer.self,"PUT",path+"/transfers/\(t.id)/chunks/\(index)",body:CampWire.body(["data":data.base64EncodedString(),"sha256":PublishMessage.hash(data)]))
                } else { t=try await request(CloudTransfer.self,"POST",path+"/transfers/\(t.id)/complete",body:Data("{}".utf8)) }
            }
            try validate(t,j);j.transfer=t
            if t.state=="ready"{j.phase="holding"}
        } else if j.phase=="holding",j.handoff==nil {
            try await verifyInstallation(j)
            let h=try await request(CloudHandoff.self,"POST",path+"/transfers/"+j.transfer!.id+"/handoffs",
                body:CampWire.body(["expected_transfer_revision":j.transfer!.revision,"local_destination":JSONSerialization.jsonObject(with:CampWire.encoder().encode(local(j)))]),key:j.holdKey)
            try validate(h,j);j.handoff=h
        } else if j.phase=="committing",j.handoff?.commitNonce==nil {
            guard let h=j.handoff,let revision=j.commitRevision,let approved=j.executionApproved,let permissions=j.permissions else{throw CampCloudError.invalidResponse}
            // Reconcile a response lost after the source moved before reading
            // the old export, which is correctly inaccessible after commit.
            try await refreshHandoff(&j)
            if j.phase != "complete",j.handoff?.commitNonce==nil {
                try await verifyInstallation(j)
                let confirmed=try await request(CloudHandoff.self,"POST",path+"/handoffs/"+h.id+"/commit",
                    body:CampWire.body(["expected_handoff_revision":revision,"context_continuation_approved":true,"execution_approved":approved,"permissions":permissions]),key:j.commitKey)
                try validate(confirmed,j);j.handoff=confirmed
            }
        } else {try await refreshHandoff(&j)}
        j.revision+=1;try await save(j,previous:previous);return j
    }
    private func refreshHandoff(_ j:inout ReturnHandoffJournal)async throws {
        guard let id=j.handoff?.id else{throw CampCloudError.invalidResponse}
        let h=try await request(CloudHandoff.self,"GET",base(j)+"/handoffs/"+id);try validate(h,j)
        guard h.id==id else{throw CampCloudError.invalidResponse};j.handoff=h
        if h.state=="committed" {
            let s=try await request(CloudSession.self,"GET",base(j)),before=j.destination.session
            guard s.id==before.id,s.campId==scope.campId,s.ownerUserId==scope.userId,s.ownerDeviceId==credential.actor.deviceId,
                  s.executionLocation=="local",s.executionWorkspaceId==nil,s.executionEpoch==h.executionEpoch,
                  s.profileId==j.destination.target.profileID,s.localSessionId==j.destination.target.sessionID,
                  s.provider==before.provider,s.transcriptRevision==before.transcriptRevision,s.revision>before.revision else{throw CampCloudError.invalidResponse}
            j.session=s;j.phase="complete"
        } else if ["released","invalidated","expired"].contains(h.state){throw CampError.message("The source hold ended. Cancel this move and review it again")}
        else if h.state=="acknowledged" && j.phase=="holding"{j.phase="confirm"}
    }
}
