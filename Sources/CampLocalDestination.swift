import Foundation

struct LocalDestinationJournal: Codable, Sendable {
    var id: String
    var revision: Int64 = 1
    var deviceId: String
    var session: CloudSession
    var exportId: String
    var reviewedSha256: String
    var target: TransferInstallationTarget
    var phase = "checkout"
    var cancelling = false
    var checkout: TransferCheckoutReceipt?
    var installation: TransferInstallationResult?
}

// Prepares the destination only. The caller must complete the source-stop
// handoff and install a continuation mapping before this becomes runnable.
// Each advance performs one restartable phase; the actor owns no background timer.
actor CampLocalDestination {
    private let cache: CampCache
    private let scope: CloudScope
    private let credential: CloudCredential
    private let epoch: Int64
    private let files: CampWorkspaceExport
    private let history: CampTransferHistory
    private let callLocal: @Sendable (String,Data) async throws -> Data
    private var busy = false

    init(cache: CampCache, scope: CloudScope, credential: CloudCredential,
         transport: any CampAccountTransport, epoch: Int64,
         callLocal: @escaping @Sendable (String,Data) async throws -> Data = { try await Bridge.call($0,input:$1) }) throws {
        self.cache=cache;self.scope=scope;self.credential=credential;self.epoch=epoch;self.callLocal=callLocal
        files=try CampWorkspaceExport(cache:cache,scope:scope,credential:credential,transport:transport,epoch:epoch)
        history=try CampTransferHistory(cache:cache,scope:scope,credential:credential,transport:transport,epoch:epoch)
    }
    private func authority() async throws {
        try Task.checkCancellation()
        guard try await cache.epoch(scope)==epoch else { throw CancellationError() }
    }
    func current() async throws -> LocalDestinationJournal? {
        try await authority()
        guard let raw=try await cache.metadata("local-destination:"+scope.key) else { return nil }
        guard raw.count<=128<<10 else { throw CampCloudError.invalidResponse }
        let j=try JSONDecoder().decode(LocalDestinationJournal.self,from:raw)
        try validate(j)
        try await authority()
        return j
    }
    private func validate(_ j: LocalDestinationJournal) throws {
        guard j.revision>0,UUID(uuidString:j.id)?.uuidString.lowercased()==j.id,
              [j.exportId,j.deviceId,j.target.profileID].allSatisfy({UUID(uuidString:$0) != nil}),
              UUID(uuidString:j.target.sessionID)?.uuidString.lowercased()==j.target.sessionID,
              j.deviceId==credential.actor.deviceId,j.session.ownerUserId==scope.userId,j.session.campId==scope.campId,
              j.session.executionLocation=="cloud",j.session.executionWorkspaceId != nil,!j.session.isHistoryOnly,
              j.session.provider==j.target.provider,["Codex","Claude"].contains(j.target.provider),
              ["planning","implementation"].contains(j.target.purpose),
              j.target.sourceID.utf8.count<=512,!j.target.sourceID.isEmpty,
              j.target.root.hasPrefix("/"),j.target.root.utf8.count<=4096,
              j.reviewedSha256.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil,
              ["checkout","installing","installed","cancelled"].contains(j.phase),
              j.phase != "cancelled" || j.cancelling else { throw CampCloudError.invalidResponse }
        if let c=j.checkout {
            guard c.version==1,c.scope==scope.key,c.transferID==j.id,c.destination.hasPrefix("/"),
                  URL(fileURLWithPath:c.destination).lastPathComponent=="workspace-\(scope.key)-\(j.id)",
                  c.baseCommit.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil else { throw CampCloudError.invalidResponse }
        }
        if ["installing","installed"].contains(j.phase) { guard j.checkout != nil else { throw CampCloudError.invalidResponse } }
        if let i=j.installation,let c=j.checkout {
            let r=i.receipt
            guard r.version==1,r.scope==scope.key,r.transferID==j.id,r.exportID==j.exportId,
                  r.target==j.target,r.destination==c.destination,r.sourceID==j.session.id,
                  r.transcriptRevision==j.session.transcriptRevision,r.files.baseCommit==c.baseCommit,
                  r.files.branch==c.branch,r.files.reviewSHA256==j.reviewedSha256,
                  [r.payloadSHA256,r.historySHA256,i.receiptSHA256].allSatisfy({$0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil}) else { throw CampCloudError.invalidResponse }
        } else if j.installation != nil || j.phase=="installed" { throw CampCloudError.invalidResponse }
    }
    private func save(_ j: LocalDestinationJournal, previous: Int64?) async throws {
        try validate(j);try await authority()
        try await cache.saveLocalDestination(j,scope:scope,expectedEpoch:epoch,previousRevision:previous)
        try await authority()
    }
    func begin(target: TransferInstallationTarget, reviewedSHA256: String) async throws {
        guard !busy else { throw CampError.message("The destination is already being prepared") }
        busy=true;defer{busy=false}
        guard try await current()==nil,let export=try await files.current(),!export.cancelling,
              let e=export.export,e.kind=="capture",e.state=="ready",
              let review=try await files.review(callLocal:callLocal),review.reviewSha256==reviewedSHA256,
              let archived=try await history.archive(),archived.session==export.session else { throw CampCloudError.invalidResponse }
        let value=LocalDestinationJournal(id:UUID().uuidString.lowercased(),deviceId:credential.actor.deviceId,
            session:export.session,exportId:e.id,reviewedSha256:reviewedSHA256,target:target)
        try await save(value,previous:nil)
    }
    @discardableResult func advance() async throws -> LocalDestinationJournal? {
        guard !busy else { throw CampError.message("The destination is already being prepared") }
        busy=true;defer{busy=false}
        guard var j=try await current() else { return nil }
        if j.phase=="cancelled" { return j }
        let previous=j.revision
        if j.cancelling {
            guard let export=try await files.current(),export.export?.id==j.exportId else { throw CampCloudError.invalidResponse }
            try await files.requestCancellation()
            let cancelled=try await files.advance()
            guard let state=cancelled?.export?.state,["cancelled","expired","failed"].contains(state) else { throw CampCloudError.invalidResponse }
            j.phase="cancelled"
        } else {
            guard let export=try await files.current(),export.session==j.session,export.export?.id==j.exportId,!export.cancelling else { throw CampCloudError.invalidResponse }
            try await history.verifySource(j.session)
            if j.phase=="checkout" {
                j.checkout=try await files.prepareLocalCheckout(transferID:j.id,reviewedSHA256:j.reviewedSha256,callLocal:callLocal)
                j.phase="installing"
            } else {
                guard let checkout=j.checkout else { throw CampCloudError.invalidResponse }
                let installed=try await files.installLocally(history:history,transferID:j.id,destination:checkout.destination,
                    target:j.target,reviewedSHA256:j.reviewedSha256,callLocal:callLocal)
                if let previous=j.installation,previous != installed { throw CampCloudError.invalidResponse }
                j.installation=installed;j.phase="installed"
            }
        }
        j.revision+=1
        try await save(j,previous:previous)
        return j
    }
    // Persist immediately even while a helper is running. Its late receipt
    // cannot overwrite cancellation because every save checks the revision.
    func requestCancellation() async throws {
        guard var j=try await current(),!j.cancelling else { return }
        let previous=j.revision;j.revision+=1;j.cancelling=true
        try await save(j,previous:previous)
        try await files.requestCancellation()
    }
    func discardCancelled() async throws {
        guard !busy else { throw CampError.message("The destination is already being prepared") }
        busy=true;defer{busy=false}
        guard let j=try await current(),j.phase=="cancelled" else { throw CampCloudError.invalidResponse }
        try await cache.discardLocalDestination(j,scope:scope,expectedEpoch:epoch)
        try await authority()
    }
}
