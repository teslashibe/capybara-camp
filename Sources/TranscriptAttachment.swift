import Foundation
import SwiftUI

struct TranscriptAttachment: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var kind: String
    var source: String
    var sourceDigest: String
    var state: String
    var mediaType: String? = nil
    var sha256: String? = nil
    var size: Int? = nil

    var symbol: String {
        switch kind { case "image": return "photo"; case "audio": return "waveform"; default: return "doc" }
    }
    var label: String {
        switch kind { case "image": return "Image"; case "audio": return "Audio"; default: return "Document" }
    }
    var availability: String {
        switch state {
        case "embedded": return "Saved in transcript"
        case "reference":
            switch source { case "local": return "Local file reference"; case "remote": return "Remote reference"; default: return "Provider file reference" }
        case "too_large": return "Exceeds 16 MB attachment limit"
        case "invalid": return "Attachment data is incomplete"
        default: return "Attachment format not supported yet"
        }
    }
}

// The bridge reads only bytes already present in a provider transcript. It
// never asks the helper to follow the attachment's original path or URL.
enum TranscriptAttachmentReader {
    struct Body: Decodable {
        var messageID: String
        var attachment: TranscriptAttachment
        var data: Data
    }
    static func read(session: CampSession, child: LocalChildHistorySource? = nil, messageID: String, attachment: TranscriptAttachment) async throws -> Data {
        guard attachment.state == "embedded", let size = attachment.size, size > 0, size <= 16 << 20,
              let hash = attachment.sha256, hash.count == 64 else { throw CampError.message("The transcript does not contain readable attachment bytes") }
        if let child, !child.valid { throw CampCloudError.invalidResponse }
        struct Input: Encodable {
            var session: CampSession; var messageID: String; var attachmentID: String; var sourceDigest: String
            var sourceID: String?; var revision: String?
        }
        let input = Input(session: session, messageID: messageID, attachmentID: attachment.id, sourceDigest: attachment.sourceDigest,
                          sourceID: child?.id, revision: child?.revision)
        let result = try JSONDecoder().decode(Body.self, from: await Bridge.call(child == nil ? "transcript-attachment" : "child-transcript-attachment", input: JSONEncoder().encode(input)))
        guard result.messageID == messageID, result.attachment == attachment, result.data.count == size,
              PublishMessage.hash(result.data) == hash else { throw CampError.message("The attachment changed; reload the session before reading it") }
        return result.data
    }
}

struct TranscriptAttachmentList: View {
    let attachments: [TranscriptAttachment]
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(attachments) { attachment in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: attachment.symbol).frame(width: 16)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(attachment.label).font(.system(size: 12, weight: .medium))
                        Text(attachment.availability).font(.system(size: 11)).foregroundStyle(campMuted)
                    }
                }.foregroundStyle(campInk).padding(9).frame(maxWidth: .infinity, alignment: .leading)
                    .background(campMuted.opacity(0.06)).clipShape(RoundedRectangle(cornerRadius: 7))
                    .accessibilityElement(children: .combine)
            }
        }
    }
}
