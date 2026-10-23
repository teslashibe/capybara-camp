import Foundation

struct CampAttachmentSelection: Sendable {
    var session: CloudSession
    var childId: String? = nil
    var revision: Int64
    var messageId: String
    var attachment: TranscriptAttachment

    var valid: Bool {
        UUID(uuidString: session.id) != nil && UUID(uuidString: session.campId) != nil &&
        (childId == nil || UUID(uuidString: childId!) != nil) && revision > 0 && revision <= session.transcriptRevision &&
        session.permissionRevision > 0 && !messageId.isEmpty && messageId.utf8.count <= 256 &&
        attachment.state == "embedded" && attachment.source == "inline" &&
        attachment.id.utf8.count <= 100 && attachment.id.range(of: "^a:[0-9]+(?:/[0-9]+){0,8}$", options: .regularExpression) != nil &&
        [attachment.sourceDigest, attachment.sha256 ?? ""].allSatisfy { $0.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil } &&
        (1...(16 << 20)).contains(attachment.size ?? 0)
    }
}

enum CampAttachmentReader {
    private struct Input: Encodable {
        var childId: String?; var revision: Int64; var messageId: String; var attachmentId: String; var sourceDigest: String
    }
    private struct Body: Decodable {
        var sessionId: String; var messageId: String; var revision: Int64; var permissionRevision: Int64
        var attachment: TranscriptAttachment; var data: Data
    }
    // No local byte cache: every read passes through current server authority.
    static func read(_ selection: CampAttachmentSelection, scope: CloudScope, credential: CloudCredential,
                     transport: any CampAccountTransport) async throws -> Data {
        guard selection.valid, selection.session.campId == scope.campId,
              credential.origin == scope.origin, credential.actor.userId == scope.userId else { throw CampCloudError.invalidResponse }
        try Task.checkCancellation()
        let input = Input(childId: selection.childId, revision: selection.revision, messageId: selection.messageId,
                          attachmentId: selection.attachment.id, sourceDigest: selection.attachment.sourceDigest)
        let raw = try await transport.data("POST", path: "/v1/camps/\(scope.campId)/sessions/\(selection.session.id)/attachment",
                                           body: CampWire.encoder().encode(input), key: nil, token: credential.token)
        try Task.checkCancellation()
        guard raw.count <= 23 << 20 else { throw CampCloudError.responseTooLarge }
        let body = try CampWire.decoder().decode(Body.self, from: raw)
        guard body.sessionId == (selection.childId ?? selection.session.id), body.messageId == selection.messageId,
              body.revision == selection.revision, body.permissionRevision == selection.session.permissionRevision,
              body.attachment == selection.attachment, body.data.count == selection.attachment.size,
              PublishMessage.hash(body.data) == selection.attachment.sha256 else { throw CampCloudError.invalidResponse }
        try Task.checkCancellation()
        return body.data
    }
}
