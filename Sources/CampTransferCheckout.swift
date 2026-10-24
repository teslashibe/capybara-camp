import Foundation
import CryptoKit

// Credentials exist only for this request and the helper's temporary input.
// They never enter the export journal, shared state or checkout receipt.
struct CloudExportCheckoutToken: Decodable, Sendable {
    var token: String; var expiresAt: Date; var exportId: String; var deviceId: String
    var workspaceId: String; var repository: String; var baseCommit: String; var branch: String; var sha256: String
    var remoteBaseCommit: String? = nil
}
struct TransferCheckoutReceipt: Codable, Equatable, Sendable {
    var version: Int; var scope: String; var transferID: String
    var repository: String; var baseCommit: String; var branch: String; var destination: String
    var remoteBaseCommit: String? = nil
    var commitsSha256: String? = nil
}

enum CampTransferCheckout {
    static func prepare(scope: CloudScope, deviceID: String, transferID: String,
                        export: CloudWorkspaceExport, review: TransferCapture, grant: CloudExportCheckoutToken,
                        payload: Data? = nil,
                        callLocal: @Sendable (String,Data) async throws -> Data) async throws -> TransferCheckoutReceipt {
        guard UUID(uuidString:transferID)?.uuidString.lowercased()==transferID,
              export.campId==scope.campId, export.ownerUserId==scope.userId,
              export.kind=="capture", export.state=="ready", export.expiresAt>Date(),
              grant.exportId==export.id, grant.deviceId==deviceID, grant.workspaceId==export.workspaceId,
              grant.sha256==export.resultSha256, grant.sha256==review.payloadSha256,
              review.valid, grant.baseCommit==review.baseCommit, grant.branch==review.branch,
              grant.expiresAt>Date(), grant.expiresAt<=Date().addingTimeInterval(3600),
              grant.repository.range(of:"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$",options:.regularExpression) != nil,
              grant.baseCommit.range(of:"^[a-f0-9]{40}$",options:.regularExpression) != nil,
              grant.token.range(of:"^ghu_[A-Za-z0-9_]{8,508}$",options:.regularExpression) != nil else { throw CampCloudError.invalidResponse }
        var input: [String:Any] = [
            "scope":scope.key,"transferID":transferID,"repository":grant.repository,
            "baseCommit":grant.baseCommit,"branch":grant.branch,
            "credential":["url":"https://github.com/\(grant.repository).git","token":grant.token,
                          "expires_at":ISO8601DateFormatter().string(from:grant.expiresAt)]
        ]
        if let commits = review.commits {
            guard grant.remoteBaseCommit == commits.baseCommit,
                  let payload, payload.count <= 46*1024*1024,
                  SHA256.hash(data:payload).map({String(format:"%02x",$0)}).joined() == review.payloadSha256,
                  let manifest = try JSONSerialization.jsonObject(with:payload) as? [String:Any],
                  let archive = manifest["commits"] as? [String:Any] else { throw CampCloudError.invalidResponse }
            input["remoteBaseCommit"] = commits.baseCommit
            input["commits"] = archive
        } else if grant.remoteBaseCommit != nil { throw CampCloudError.invalidResponse }
        let body=try CampWire.body(input)
        try Task.checkCancellation()
        let raw=try await callLocal("transfer-checkout",body)
        try Task.checkCancellation()
        let receipt=try JSONDecoder().decode(TransferCheckoutReceipt.self,from:raw)
        guard receipt.version==1,receipt.scope==scope.key,receipt.transferID==transferID,
              receipt.repository==grant.repository,receipt.baseCommit==grant.baseCommit,receipt.branch==grant.branch,
              receipt.remoteBaseCommit==review.commits?.baseCommit,receipt.commitsSha256==review.commits?.reviewSha256,
              receipt.destination.hasPrefix("/"),
              URL(fileURLWithPath:receipt.destination).lastPathComponent=="workspace-\(scope.key)-\(transferID)" else { throw CampCloudError.invalidResponse }
        return receipt
    }
}
