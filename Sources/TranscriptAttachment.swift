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

    // Matches the descriptor contract enforced by internal/attachments.
    var validForTransfer: Bool {
        guard id.utf8.count <= 100, id.range(of: "^a:[0-9]+(?:/[0-9]+){0,8}$", options: .regularExpression) != nil,
              sourceDigest.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil,
              ["image", "audio", "document"].contains(kind),
              ["inline", "local", "remote", "provider", "unknown"].contains(source) else { return false }
        let media = mediaType ?? ""
        let allowed: Bool
        switch kind {
        case "image": allowed = ["image/png", "image/jpeg", "image/gif", "image/webp"].contains(media)
        case "audio": allowed = ["audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/ogg", "audio/webm"].contains(media)
        default: allowed = ["application/pdf", "text/plain", "application/octet-stream"].contains(media)
        }
        if state == "embedded" {
            return source == "inline" && allowed && (1...(16 << 20)).contains(size ?? 0) &&
                (sha256 ?? "").range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil
        }
        guard (sha256 ?? "").isEmpty, (size ?? 0) == 0 else { return false }
        switch state {
        case "reference": return media.isEmpty && ["local", "remote", "provider"].contains(source)
        case "unsupported": return media.isEmpty && ["unknown", "inline"].contains(source)
        case "invalid", "too_large": return source == "inline" && (media.isEmpty ? state == "invalid" : allowed)
        default: return false
        }
    }

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
    var access: AttachmentAccess? = nil
    @State private var selected: TranscriptAttachment?
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(attachments) { attachment in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: attachment.symbol).frame(width: 16)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(attachment.label).font(.system(size: 12, weight: .medium))
                        Text(attachment.availability).font(.system(size: 11)).foregroundStyle(campMuted)
                    }
                    Spacer(minLength: 8)
                    if access != nil, attachment.state == "embedded", AttachmentPreviewModel.fileExtension(attachment) != nil {
                        CampPlainButton { selected = attachment } label: {
                            Image(systemName: "eye").frame(width: 32, height: 32).contentShape(Rectangle())
                        }.accessibilityLabel("Preview \(attachment.label.lowercased())").help("Preview attachment")
                    }
                }.foregroundStyle(campInk).padding(9).frame(maxWidth: .infinity, alignment: .leading)
                    .background(campMuted.opacity(0.06)).clipShape(RoundedRectangle(cornerRadius: 7))
                    .accessibilityElement(children: .contain)
            }
        }
        .sheet(item: $selected) { attachment in
            if let access { AttachmentPreview(attachment: attachment, access: access) }
        }
        .onChange(of: attachments) { _, values in
            if let selected, !values.contains(selected) { self.selected = nil }
        }
        .onChange(of: access?.id) { _, _ in selected = nil }
        .onDisappear { selected = nil }
    }
}
