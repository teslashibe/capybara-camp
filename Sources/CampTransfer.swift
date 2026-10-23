import Foundation
import SwiftUI

struct TransferChange: Codable, Identifiable, Sendable {
    var path: String; var kind: String; var excluded: String?
    var id: String { path }
}
struct TransferChanges: Codable, Sendable {
    var baseCommit: String; var branch: String; var files: [TransferChange]
}
struct TransferCapture: Codable, Sendable {
    struct CommitSummary: Codable, Sendable {
        var baseCommit: String; var headCommit: String; var reviewSha256: String
        var commits: Int; var objects: Int; var bytes: Int
        var valid: Bool {
            [baseCommit,headCommit].allSatisfy { $0.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil } &&
            baseCommit != headCommit && reviewSha256.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil &&
            (1...256).contains(commits) && (1...4096).contains(objects) && (1...32*1024*1024).contains(bytes)
        }
    }
    struct File: Codable, Identifiable, Sendable {
        var path: String; var bytes: Int; var deleted: Bool; var beforeSha256: String?; var sha256: String?
        var id: String { path }
    }
    struct Exclusion: Codable, Identifiable, Sendable { var path: String; var reason: String; var id: String { path } }
    var payload: Data?; var payloadSha256: String; var reviewSha256: String
    var baseCommit: String; var branch: String; var files: [File]; var excluded: [Exclusion]
    var history:LocalHistoryBinding? = nil
    var commits: CommitSummary? = nil
    var valid: Bool {
        [payloadSha256,reviewSha256].allSatisfy { $0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil } &&
        baseCommit.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil && files.count+excluded.count <= 256 &&
        (commits.map { $0.valid && $0.headCommit == baseCommit } ?? true)
    }
}
struct CloudTransfer: Codable, Sendable {
    var id: String; var campId: String; var sessionId: String; var ownerUserId: String
    var sourceLocation: String; var sourceEpoch: Int64; var sourceRevision: Int64; var sourceTranscriptRevision: Int64; var sourcePermissionRevision: Int64
    var targetLocation: String; var targetAccountId: String?; var payloadSha256: String; var reviewSha256: String
    var payloadBytes: Int; var uploadedBytes: Int; var uploadedChunks: [Int]?; var chunkBytes: Int
    var state: String; var revision: Int64; var expiresAt: Date; var baseCommit: String?; var fileCount: Int
    var sourceWorkspaceId: String? = nil
    var targetDeviceId: String? = nil
}
struct TransferReviewJob: Codable, Sendable {
    var id: String; var session: CloudSession; var input: CloudWorkspaceInput; var capture: TransferCapture
    var payloadBytes: Int; var phase: String; var createKey: String; var workspaceKey: String; var holdKey: String; var commitKey: String
    var transfer: CloudTransfer?; var workspace: CloudWorkspace?; var handoff: CloudHandoff?
    var commitRevision: Int64?; var permissions: [String:String]?; var approved: Bool?
    var resumePhase: String?; var failure: String?
}

extension TransferCapture {
    static func preview(_ path:String, payload:Data) throws -> String {
        struct Snapshot:Decodable {struct File:Decodable {var path:String;var data:Data?;var deleted:Bool?};var files:[File]}
        guard let file=try JSONDecoder().decode(Snapshot.self,from:payload).files.first(where:{$0.path==path}) else{throw CampCloudError.invalidResponse}
        if file.deleted==true{return "This file will be deleted"}
        let bytes=file.data ?? Data()
        guard !bytes.contains(0),String(data:bytes,encoding:.utf8) != nil else{return "Binary file · \(bytes.count) bytes"}
        var prefix=Data(bytes.prefix(65536))
        while !prefix.isEmpty,String(data:prefix,encoding:.utf8)==nil {prefix.removeLast()}
        let text=String(decoding:prefix,as:UTF8.self)
        return text+(bytes.count>65536 ? "\n\nPreview limited to 64 KB" : "")
    }
}

@MainActor final class CampTransfer: ObservableObject {
    private struct Connection { var scope: CloudScope; var credential: CloudCredential; var transport: any CampAccountTransport; var generation = UUID(); var epoch: Int64? }
    @Published private(set) var job: TransferReviewJob?
    @Published private(set) var changes: TransferChanges?
    @Published private(set) var inspectionID: String?
    @Published private(set) var working = false
    @Published private(set) var error = ""
    @Published private(set) var uploadedBytes = 0
    @Published private(set) var cancelling = false
    var changed: ((String)->Void)?
    var callLocal: (String,Data) async throws -> Data = { try await Bridge.call($0,input:$1) }
    private let cache: CampCache?
    private var connection: Connection?
    private var loaded = false
    private var task: Task<Void,Never>?
    private let interval: Double
    init(cache: CampCache?, interval: Double = 2) { self.cache = cache;self.interval=interval }
    deinit { task?.cancel() }
    func attach(scope: CloudScope?, credential: CloudCredential?, transport: (any CampAccountTransport)?) {
        if let c=connection,c.scope==scope,c.credential.token==credential?.token { return }
        task?.cancel(); task=nil; connection=nil; loaded=false; job=nil; changes=nil; inspectionID=nil; working=false; error=""; uploadedBytes=0;cancelling=false
        if let scope,let credential,let transport { connection=Connection(scope:scope,credential:credential,transport:transport); wake() }
    }
    private func check(_ c: Connection) throws {
        guard connection?.generation==c.generation,!Task.isCancelled else {throw CancellationError()}
    }
    private func authority(_ c: Connection) async throws {
        try check(c)
        guard let epoch=connection?.epoch,try await cache?.epoch(c.scope)==epoch else {throw CancellationError()}
        try check(c)
    }
    private func load(_ c: Connection) async throws {
        guard !loaded,let cache else{return}
        let epoch=try await cache.epoch(c.scope)
        let raw=try await cache.metadata("transfer-review:"+c.scope.key);try check(c)
        var value=try raw.map {try CampWire.decoder().decode(TransferReviewJob?.self,from:$0)} ?? nil
        if let value {
            guard UUID(uuidString:value.id) != nil,value.session.campId==c.scope.campId,value.session.ownerUserId==c.credential.actor.userId,
                  [value.session.id,value.input.providerAccountId,value.input.gitConnectionId].allSatisfy({UUID(uuidString:$0) != nil}),
                  value.input.githubInstallationId>0,value.input.repositoryId>0,value.input.ref==value.capture.baseCommit,
                  value.capture.valid,value.capture.payload==nil,(2...(46<<20)).contains(value.payloadBytes),
                  ["review","uploading","preparing","ready","holding","confirm","committing","cancelling","complete","cancelled","blocked"].contains(value.phase),
                  [value.createKey,value.workspaceKey,value.holdKey,value.commitKey].allSatisfy({UUID(uuidString:$0) != nil}) else {throw CampCloudError.invalidResponse}
        }
        if let pending=value,!["complete","cancelled"].contains(pending.phase),
           let cancel=try await cache.metadata("transfer-cancel:"+c.scope.key),String(decoding:cancel,as:UTF8.self)==pending.id {value?.phase="cancelling";cancelling=true}
        guard try await cache.epoch(c.scope)==epoch else {throw CancellationError()}
        try check(c);connection?.epoch=epoch;job=value;error=value?.failure ?? "";loaded=true
    }
    private func save(_ value: TransferReviewJob?,_ c: Connection,payload:Data?=nil,discard:Bool=false) async throws {
        try await authority(c)
        guard let cache,let epoch=connection?.epoch,let id=value?.id ?? job?.id else {throw CampCloudError.cacheUnavailable}
        try await cache.saveTransferJournal(CampWire.encoder().encode(value),payload:payload,id:id,scope:c.scope,expectedEpoch:epoch,discardPayload:discard)
        try check(c);job=value
    }
    private func request<T:Decodable>(_ type:T.Type,_ method:String,_ path:String,_ c:Connection,body:Data?=nil,key:String?=nil) async throws ->T {
        try await authority(c)
        let raw=try await c.transport.data(method,path:path,body:body,key:key,token:c.credential.token)
        try await authority(c);return try CampWire.decoder().decode(type,from:raw)
    }
    private func base(_ j:TransferReviewJob,_ c:Connection)->String {"/v1/camps/\(c.scope.campId)/sessions/\(j.session.id)"}
    private func valid(_ t:CloudTransfer,_ j:TransferReviewJob,_ c:Connection)->Bool {
        UUID(uuidString:t.id) != nil && t.campId==c.scope.campId && t.sessionId==j.session.id && t.ownerUserId==c.credential.actor.userId &&
        t.sourceLocation=="local" && t.sourceEpoch==(j.session.executionEpoch ?? 1) && t.sourceRevision==j.session.revision &&
        t.sourceTranscriptRevision==j.session.transcriptRevision && t.sourcePermissionRevision==j.session.permissionRevision &&
        t.targetLocation=="cloud" && t.targetAccountId==j.input.providerAccountId && t.payloadSha256==j.capture.payloadSha256 && t.reviewSha256==j.capture.reviewSha256 &&
        t.payloadBytes==j.payloadBytes && t.chunkBytes==256<<10 && t.uploadedBytes>=0 && t.uploadedBytes<=t.payloadBytes && t.revision>0 &&
        ["uploading","ready","completed","cancelled","expired"].contains(t.state)
    }
    private func fail(_ failure:Error,_ c:Connection) {
        guard connection?.generation==c.generation,!(failure is CancellationError) else{return}
        switch failure {
        case CampCloudError.server(409,_):error="The session or workspace changed. Cancel this transfer and review it again"
        case CampCloudError.server(401,_):error="Sign in to Camp again to resume this transfer"
        case CampCloudError.server(403,_),CampCloudError.server(404,_):error="This transfer or account is no longer available to you"
        default:error=failure.localizedDescription
        }
    }
    func inspect(_ local:CampSession) async {
        guard !working,let c=connection else{return};working=true
        defer{if connection?.generation==c.generation{working=false}}
        do {
            let raw=try await callLocal("transfer-changes",try JSONEncoder().encode(["session":local]))
            try check(c)
            let value=try JSONDecoder().decode(TransferChanges.self,from:raw)
            guard value.baseCommit.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil,
                  value.files.count<=4096,Set(value.files.map(\.path)).count==value.files.count else{throw CampCloudError.invalidResponse}
            changes=value;inspectionID=local.id;error=""
        } catch {fail(error,c)}
    }
    func capture(_ local:CampSession,session:CloudSession,input:CloudWorkspaceInput,paths:[String],history:LocalHistoryBinding?=nil) async {
        guard !working,let c=connection else{return};working=true
        defer{if connection?.generation==c.generation{working=false}}
        do {
            try await load(c);try await authority(c)
            guard let history,history.valid,history.transcriptRevision==session.transcriptRevision else{throw CampError.message("Finish syncing this session before reviewing its move")}
            guard job==nil || ["complete","cancelled"].contains(job!.phase),session.ownerUserId==c.credential.actor.userId,session.campId==c.scope.campId,
                  session.ownerDeviceId==c.credential.actor.deviceId,session.provider==local.provider,session.localSessionId==local.sessionID,
                  session.executionLocation != "cloud",paths.count<=256 else {throw CampCloudError.invalidResponse}
            let body=try CampWire.body(["session":JSONSerialization.jsonObject(with:JSONEncoder().encode(local)),"paths":paths,"history":JSONSerialization.jsonObject(with:CampWire.encoder().encode(history))])
            let raw=try await callLocal("transfer-capture",body);try await authority(c)
            var capture=try await Task.detached {
                let value=try CampWire.decoder().decode(TransferCapture.self,from:raw)
                guard value.valid,value.history==history,let bytes=value.payload,(2...(46<<20)).contains(bytes.count),PublishMessage.hash(bytes)==value.payloadSha256 else{throw CampCloudError.invalidResponse}
                return value
            }.value
            guard let payload=capture.payload else{throw CampCloudError.invalidResponse}
            capture.payload=nil
            var choice=input;choice.ref=capture.baseCommit
            let value=TransferReviewJob(id:UUID().uuidString.lowercased(),session:session,input:choice,capture:capture,payloadBytes:payload.count,phase:"review",createKey:UUID().uuidString.lowercased(),workspaceKey:UUID().uuidString.lowercased(),holdKey:UUID().uuidString.lowercased(),commitKey:UUID().uuidString.lowercased())
            try await save(value,c,payload:payload);uploadedBytes=0;error=""
        } catch {fail(error,c)}
    }
    func restore() async {guard let c=connection else{return};do{try await load(c)}catch{fail(error,c)}}
    func preview(_ path:String) async throws -> String {
        guard let c=connection,let j=job,j.capture.files.contains(where:{$0.path==path}),let data=try await cache?.metadata("transfer-payload:"+c.scope.key+":"+j.id) else{throw CampCloudError.invalidResponse}
        try await authority(c)
        let text=try await Task.detached { try TransferCapture.preview(path, payload:data) }.value
        try await authority(c);return text
    }
    func prepare() async {await mutate {j,c in guard j.phase=="review" else{throw CampCloudError.invalidResponse};var j=j;j.phase="uploading";try await self.save(j,c)}}
    func hold() async {await mutate {j,c in guard j.phase=="ready" else{throw CampCloudError.invalidResponse};var j=j;j.phase="holding";try await self.save(j,c)}}
    func confirm(permissions:PermissionSettings,approved:Bool) async {
        await mutate {j,c in
            guard j.phase=="confirm",let h=j.handoff else{throw CampCloudError.invalidResponse}
            if j.input.purpose=="implementation" {guard approved,permissions.valid(for:j.session.provider) else{throw CampCloudError.invalidResponse}}
            var j=j;j.phase="committing";j.commitRevision=h.revision;j.approved=approved
            j.permissions=j.input.purpose=="implementation" ? ["profile":permissions.profile,"sandbox":permissions.sandbox,"approval":permissions.approval,"reviewer":permissions.reviewer,"claudeMode":permissions.claudeMode].filter{!$0.value.isEmpty} : [:]
            try await self.save(j,c)
        }
    }
    func cancel() async {
        guard let c=connection,let j=job,let cache,let epoch=connection?.epoch,!cancelling else{return}
        do {
            try await cache.requestTransferCancellation(j.id,scope:c.scope,expectedEpoch:epoch);try check(c);cancelling=true
            let activeTask=task;activeTask?.cancel();await activeTask?.value
            try check(c)
            await mutate {j,c in var j=j;j.phase="cancelling";try await self.save(j,c)}
        } catch {fail(error,c)}
    }
    func resetReview() async {
        guard !working,let c=connection,let phase=job?.phase,["review","complete","cancelled"].contains(phase) else{return}
        do{try await save(nil,c,discard:true)}catch{fail(error,c)}
    }
    func retry() {
        Task {await mutate {j,c in
            guard j.phase=="blocked",let phase=j.resumePhase else{return}
            var j=j;j.phase=phase;j.resumePhase=nil;j.failure=nil;try await self.save(j,c)
        }}
    }
    private func mutate(_ operation:(TransferReviewJob,Connection)async throws->Void) async {
        guard !working,let c=connection else{return};working=true
        do {try await load(c);guard let job else{throw CampCloudError.invalidResponse};try await operation(job,c);error=""}
        catch{fail(error,c)}
        if connection?.generation==c.generation{working=false;wake()}
    }
    private var active:Bool {job.map{["uploading","preparing","holding","confirm","committing","cancelling"].contains($0.phase)} ?? false}
    private func wake() {
        guard task==nil,let c=connection else{return}
        task=Task{[weak self] in
            guard let self else{return}
            do{try await self.load(c)}catch{self.fail(error,c)}
            while self.connection?.generation==c.generation,!Task.isCancelled,self.active {
                if !self.working {
                    self.working=true
                    do{try await self.advance(c);self.error=""}catch{
                        if error is CancellationError {if self.connection?.generation==c.generation{self.working=false};break}
                        self.fail(error,c)
                        let fatal:Bool
                        if case CampCloudError.server(let code,_) = error {fatal=[400,401,403,404,409,422].contains(code)}
                        else {fatal = !(error is URLError)}
                        if fatal,var value=self.job,value.phase != "blocked" {
                            value.resumePhase=value.phase;value.phase="blocked";value.failure=self.error
                            do{try await self.save(value,c)}catch{self.fail(error,c)}
                        }
                    }
                    if self.connection?.generation==c.generation{self.working=false}
                }
                do{try await Task.sleep(for:.seconds(self.error.isEmpty ? self.interval : max(self.interval,5)))}catch{break}
            }
            if self.connection?.generation==c.generation{self.task=nil}
        }
    }
    private func advance(_ c:Connection) async throws {
        guard var j=job else{return};try await authority(c)
        if let cancel=try await cache?.metadata("transfer-cancel:"+c.scope.key),String(decoding:cancel,as:UTF8.self)==j.id,!["complete","cancelled"].contains(j.phase){j.phase="cancelling"}
        let path=base(j,c)
        if j.phase=="uploading" {
            if j.transfer==nil {
                let body=try CampWire.body(["target_location":"cloud","target_account_id":j.input.providerAccountId,"expected_epoch":j.session.executionEpoch ?? 1,"expected_revision":j.session.revision,"expected_transcript_revision":j.session.transcriptRevision,"expected_permission_revision":j.session.permissionRevision,"payload_sha256":j.capture.payloadSha256,"review_sha256":j.capture.reviewSha256,"payload_bytes":j.payloadBytes])
                let created=try await request(CloudTransfer.self,"POST",path+"/transfers",c,body:body,key:j.createKey)
                guard valid(created,j,c) else{throw CampCloudError.invalidResponse}
                j.transfer=created;try await save(j,c)
            }
            guard let transfer=j.transfer else{throw CampCloudError.invalidResponse}
            var t=try await request(CloudTransfer.self,"GET",path+"/transfers/"+transfer.id,c)
            guard valid(t,j,c),["uploading","ready"].contains(t.state) else{throw CampCloudError.invalidResponse}
            guard let payload=try await cache?.metadata("transfer-payload:"+c.scope.key+":"+j.id),payload.count==j.payloadBytes else{throw CampCloudError.invalidResponse}
            let payloadHash=await Task.detached {PublishMessage.hash(payload)}.value
            try await authority(c)
            guard payloadHash==j.capture.payloadSha256 else{throw CampCloudError.invalidResponse}
            if t.state=="uploading" {
                let count=(payload.count+t.chunkBytes-1)/t.chunkBytes
                let uploaded=t.uploadedChunks ?? []
                guard Set(uploaded).count==uploaded.count,uploaded.allSatisfy({(0..<count).contains($0)}) else{throw CampCloudError.invalidResponse}
                for ordinal in 0..<count where !uploaded.contains(ordinal) {
                    let data=payload.subdata(in:(ordinal*t.chunkBytes)..<min(payload.count,(ordinal+1)*t.chunkBytes))
                    t=try await request(CloudTransfer.self,"PUT",path+"/transfers/\(t.id)/chunks/\(ordinal)",c,body:CampWire.body(["data":data.base64EncodedString(),"sha256":PublishMessage.hash(data)]))
                    guard valid(t,j,c) else{throw CampCloudError.invalidResponse};uploadedBytes=t.uploadedBytes
                }
                t=try await request(CloudTransfer.self,"POST",path+"/transfers/\(t.id)/complete",c,body:Data("{}".utf8))
            }
            guard t.state=="ready",t.baseCommit==j.capture.baseCommit,t.fileCount==j.capture.files.count else{throw CampCloudError.invalidResponse}
            j.transfer=t;try await save(j,c)
            let workspace=try await request(CloudWorkspace.self,"POST",path+"/transfers/\(t.id)/workspaces",c,body:CampWire.encoder().encode(j.input),key:j.workspaceKey)
            guard workspace.valid(session:j.session,owner:c.credential.actor.userId),workspace.transferId==t.id else{throw CampCloudError.invalidResponse}
            j.workspace=workspace;j.phase="preparing";try await save(j,c)
        } else if j.phase=="preparing" {
            guard let old=j.workspace else{throw CampCloudError.invalidResponse}
            let workspace=try await request(CloudWorkspace.self,"GET","/v1/camps/\(c.scope.campId)/workspaces/\(old.id)",c)
            guard workspace.id==old.id,workspace.valid(session:j.session,owner:c.credential.actor.userId),workspace.transferId==j.transfer?.id else{throw CampCloudError.invalidResponse}
            j.workspace=workspace
            if workspace.state=="ready"{j.phase="ready"}
            if ["failed","cancelled"].contains(workspace.state){throw CampError.message("Workspace preparation failed. Cancel this transfer and review it again")}
            try await save(j,c)
        } else if j.phase=="holding" {
            guard let t=j.transfer,let w=j.workspace else{throw CampCloudError.invalidResponse}
            if j.handoff==nil {
                let created=try await request(CloudHandoff.self,"POST",path+"/transfers/\(t.id)/handoffs",c,body:CampWire.body(["workspace_id":w.id,"expected_workspace_revision":w.revision,"expected_transfer_revision":t.revision]),key:j.holdKey)
                guard valid(created,j,c) else{throw CampCloudError.invalidResponse}
                j.handoff=created
                try await save(j,c)
            }
            try await updateHandoff(&j,c)
            if j.handoff?.state=="acknowledged"{j.phase="confirm";try await save(j,c)}
        } else if j.phase=="confirm" {
            // The source can change or its hold can expire while permissions
            // are being reviewed. Keep the verified state current until commit.
            try await updateHandoff(&j,c)
        } else if j.phase=="committing" {
            guard let h=j.handoff,let revision=j.commitRevision else{throw CampCloudError.invalidResponse}
            if h.commitNonce==nil {
                let confirmed=try await request(CloudHandoff.self,"POST",path+"/handoffs/\(h.id)/commit",c,body:CampWire.body(["expected_handoff_revision":revision,"execution_approved":j.approved ?? false,"permissions":j.permissions ?? [:]]),key:j.commitKey)
                guard valid(confirmed,j,c) else{throw CampCloudError.invalidResponse}
                j.handoff=confirmed
                try await save(j,c)
            }
            try await updateHandoff(&j,c)
        } else if j.phase=="cancelling" {
            if let t=j.transfer {
                let current=try await request(CloudTransfer.self,"GET",path+"/transfers/\(t.id)",c)
                guard valid(current,j,c) else{throw CampCloudError.invalidResponse}
                if current.state=="completed" {try await updateHandoff(&j,c);return}
                let value=try await request(CloudTransfer.self,"POST",path+"/transfers/\(t.id)/cancel",c,body:Data("{}".utf8))
                guard valid(value,j,c),["cancelled","expired"].contains(value.state) else{throw CampCloudError.invalidResponse}
            }
            j.phase="cancelled";try await save(j,c,discard:true);cancelling=false
        }
    }
    private func updateHandoff(_ value:inout TransferReviewJob,_ c:Connection) async throws {
        guard let old=value.handoff,let w=value.workspace else{throw CampCloudError.invalidResponse}
        let h=try await request(CloudHandoff.self,"GET",base(value,c)+"/handoffs/\(old.id)",c)
        guard h.id==old.id,valid(h,value,c),h.workspaceId==w.id else{throw CampCloudError.invalidResponse}
        value.handoff=h
        if h.state=="committed" {
            guard h.executionEpoch==(value.session.executionEpoch ?? 1)+1 else{throw CampCloudError.invalidResponse}
            value.phase="complete";try await save(value,c,discard:true);changed?(value.session.id)
            cancelling=false
        } else if ["released","invalidated","expired"].contains(h.state){throw CampError.message("The source hold ended. Cancel this transfer and review it again")}
        else{try await save(value,c)}
    }
    private func valid(_ h:CloudHandoff,_ j:TransferReviewJob,_ c:Connection)->Bool {
        UUID(uuidString:h.id) != nil && h.transferId==j.transfer?.id && h.campId==c.scope.campId && h.sessionId==j.session.id &&
        h.ownerUserId==c.credential.actor.userId && h.sourceDeviceId==j.session.ownerDeviceId && h.sourceLocation=="local" &&
        h.sourceEpoch==(j.session.executionEpoch ?? 1) && h.workspaceId==j.workspace?.id && h.workspaceRevision>0 && h.revision>0 &&
        ["pending","claimed","acknowledged","committed","released","invalidated","expired"].contains(h.state) &&
        (h.state != "committed" || UUID(uuidString:h.commitNonce ?? "") != nil)
    }
}
