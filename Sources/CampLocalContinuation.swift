import Foundation

// The Camp ticket keeps its imported identity and history. Only the physical
// provider conversation changes when an approved context continuation starts.
struct LocalContinuationBinding: Codable, Equatable, Sendable {
    var session: CloudSession
    var handoffID: String
    var installation: TransferInstallationResult
    var firstRequestID: String?
    var providerSessionID: String?

    var history: RestoredHistoryReference {
        .init(scope: installation.receipt.scope, transferID: installation.receipt.transferID,
              receiptSHA256: installation.receiptSHA256)
    }
    var target: TransferInstallationTarget { installation.receipt.target }
    var nativeSession: CampSession {
        let id = providerSessionID ?? target.sessionID
        return CampSession(id: target.sourceID + ":" + id, sessionID: id, provider: target.provider,
                           sourceID: target.sourceID, sourceLabel: session.profileLabel, root: target.root,
                           title: session.title, cwd: installation.receipt.destination,
                           updated: session.updatedAt.timeIntervalSince1970, path: "", accountID: "",
                           archived: false, model: session.model)
    }
    var key: String { "local-continuation:\(installation.receipt.scope):\(session.id):\(session.executionEpoch ?? 0)" }

    static func validProviderID(_ id: String) -> Bool {
        !id.isEmpty && id.utf8.count <= 200 && !id.contains(where: { "/\\\r\n\0".contains($0) })
    }
    func validate(scope: CloudScope) throws {
        let receipt = installation.receipt
        guard session.campId == scope.campId, session.ownerUserId == scope.userId,
              session.executionLocation == "local", session.executionWorkspaceId == nil,
              (session.executionEpoch ?? 0) > 0, !session.isHistoryOnly, !session.externalOwner,
              [session.id, session.ownerDeviceId, handoffID, target.profileID, target.sessionID,
               receipt.transferID, receipt.exportID].allSatisfy({ UUID(uuidString: $0) != nil }),
              session.profileId == target.profileID, session.localSessionId == target.sessionID,
              session.provider == target.provider, ["Claude", "Codex"].contains(target.provider),
              ["planning", "implementation"].contains(target.purpose),
              !target.sourceID.isEmpty, target.sourceID.utf8.count <= 512, target.root.hasPrefix("/"),
              receipt.version == 1, receipt.scope == scope.key, receipt.sourceID == session.id,
              receipt.transcriptRevision == session.transcriptRevision, receipt.destination.hasPrefix("/"),
              [receipt.payloadSHA256, receipt.historySHA256, installation.receiptSHA256].allSatisfy({
                  $0.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil
              }),
              firstRequestID == nil || UUID(uuidString: firstRequestID!) != nil,
              providerSessionID == nil || firstRequestID != nil && Self.validProviderID(providerSessionID!)
        else { throw CampCloudError.invalidResponse }
    }
    // Compare execution identity, allowing later activity and transcript updates.
    func matches(_ current: CloudSession) -> Bool {
        current.id == session.id && current.campId == session.campId &&
        current.ownerUserId == session.ownerUserId && current.ownerDeviceId == session.ownerDeviceId &&
        current.provider == session.provider && current.profileId == session.profileId &&
        current.localSessionId == session.localSessionId && current.executionEpoch == session.executionEpoch &&
        current.executionLocation == "local" && current.executionWorkspaceId == nil &&
        !current.externalOwner && !current.isHistoryOnly && current.revision >= session.revision &&
        current.transcriptRevision >= session.transcriptRevision && current.permissionRevision >= session.permissionRevision
    }
}

struct LocalContinuationLaunch: Sendable {
    var binding: LocalContinuationBinding
    var requestID: String
    var newSession: Bool
    var providerSessionID: String { binding.providerSessionID ?? binding.target.sessionID }
}
