import Foundation

struct RestoredHistoryReference: Codable, Equatable, Sendable {
    var scope: String
    var transferID: String
    var receiptSHA256: String
}

struct TransferInstallationTarget: Codable, Equatable, Sendable {
    var profileID: String
    var sourceID: String
    var provider: String
    var root: String
    var sessionID: String
    var purpose: String
}
struct TransferInstallationReceipt: Codable, Equatable, Sendable {
    struct Files: Codable, Equatable, Sendable {
        var reviewSHA256: String; var baseCommit: String; var branch: String; var files: Int
        enum CodingKeys: String, CodingKey {
            case reviewSHA256 = "review_sha256", baseCommit = "base_commit", branch, files
        }
    }
    var version: Int; var scope: String; var transferID: String; var exportID: String
    var destination: String; var target: TransferInstallationTarget
    var payloadSHA256: String; var historySHA256: String; var sourceID: String
    var transcriptRevision: Int64; var files: Files
}
struct TransferInstallationResult: Codable, Equatable, Sendable {
    var receipt: TransferInstallationReceipt
    var receiptSHA256: String
}

// Installs a reviewed snapshot into an existing isolated checkout. The caller
// still needs a committed cloud handoff before publishing or running it locally.
enum CampTransferInstallation {
    static func install(scope: CloudScope, transferID: String, destination: String,
                        target: TransferInstallationTarget, export: CloudWorkspaceExport,
                        payload: Data, review: TransferCapture, history: TransferHistoryArchive,
                        readAttachment: (@Sendable (TransferHistoryAttachment) async throws -> Data)? = nil,
                        callLocal: @Sendable (String,Data) async throws -> Data = { try await Bridge.call($0,input:$1) }) async throws -> TransferInstallationResult {
        let session=history.session
        guard [1, 2].contains(history.version), history.origin==scope.origin, session.ownerUserId==scope.userId, session.campId==scope.campId,
              session.provider==target.provider, session.executionLocation=="cloud", !session.isHistoryOnly,
              export.campId==scope.campId, export.ownerUserId==scope.userId, export.sessionId==session.id,
              export.workspaceId==session.executionWorkspaceId, export.sourceEpoch==session.executionEpoch,
              export.sourceRevision==session.revision, export.transcriptRevision==session.transcriptRevision,
              export.permissionRevision==session.permissionRevision, export.kind=="capture", export.state=="ready",
              export.expiresAt>Date(), export.resultBytes==payload.count, export.resultSha256==PublishMessage.hash(payload),
              review.valid, review.payloadSha256==export.resultSha256,
              UUID(uuidString:transferID) != nil, UUID(uuidString:target.profileID) != nil,
              UUID(uuidString:target.sessionID) != nil, ["planning","implementation"].contains(target.purpose),
              destination.hasPrefix("/"), target.root.hasPrefix("/") else { throw CampCloudError.invalidResponse }
        let archived=try CampWire.encoder().encode(history), historyHash=PublishMessage.hash(archived)
        let directory = try await stageAttachments(history.attachments ?? [], read: readAttachment)
        defer { if let directory { try? FileManager.default.removeItem(at: directory) } }
        var input: [String: Any] = [
            "scope":scope.key,"transferID":transferID,"destination":destination,"branch":review.branch,
            "target":try JSONSerialization.jsonObject(with:JSONEncoder().encode(target)),
            "export":try JSONSerialization.jsonObject(with:CampWire.encoder().encode(export)),
            "payload":payload.base64EncodedString(),"payloadSHA256":review.payloadSha256,"reviewSHA256":review.reviewSha256,
            "history":archived.base64EncodedString(),"historySHA256":historyHash
        ]
        if let directory { input["attachmentDirectory"] = directory.path }
        let body=try CampWire.body(input)
        try Task.checkCancellation()
        let raw=try await callLocal("transfer-install",body)
        try Task.checkCancellation()
        let result=try JSONDecoder().decode(TransferInstallationResult.self,from:raw), receipt=result.receipt
        let expected=TransferInstallationReceipt(version:1,scope:scope.key,transferID:transferID,exportID:export.id,
            destination:destination,target:target,payloadSHA256:review.payloadSha256,historySHA256:historyHash,
            sourceID:session.id,transcriptRevision:session.transcriptRevision,
            files:.init(reviewSHA256:review.reviewSha256,baseCommit:review.baseCommit,branch:review.branch,files:review.files.count))
        guard receipt==expected, result.receiptSHA256.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil else { throw CampCloudError.invalidResponse }
        return result
    }

    // Bodies stay out of the JSON envelope. The helper copies and hashes each
    // private file before issuing the receipt bound to the complete manifest.
    static func stageAttachments(_ items: [TransferHistoryAttachment],
                                 read: (@Sendable (TransferHistoryAttachment) async throws -> Data)?,
                                 root: URL = FileManager.default.temporaryDirectory) async throws -> URL? {
        guard !items.isEmpty else { return nil }
        guard let read, items.count <= 1024, Set(items.map(\.key)).count == items.count,
              items.allSatisfy({ UUID(uuidString: $0.streamId) != nil && $0.attachment.validForTransfer && $0.attachment.state == "embedded" }),
              items.reduce(0, { $0 + ($1.attachment.size ?? 0) }) <= CampTransferHistory.maxAttachmentBytes else {
            throw CampCloudError.invalidResponse
        }
        let directory = root.appendingPathComponent("CapybaraTransfer-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        do {
            for item in items {
                try Task.checkCancellation()
                let bytes = try await read(item)
                try Task.checkCancellation()
                guard bytes.count == item.attachment.size, PublishMessage.hash(bytes) == item.attachment.sha256 else {
                    throw CampCloudError.invalidResponse
                }
                let file = directory.appendingPathComponent("attachment-" + item.streamId + "-" + item.attachment.sha256!)
                try bytes.write(to: file, options: .withoutOverwriting)
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
            }
            return directory
        } catch {
            try? FileManager.default.removeItem(at: directory)
            throw error
        }
    }
}
