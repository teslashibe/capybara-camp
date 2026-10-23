import SwiftUI

struct TransferAttachmentReview: Codable, Equatable, Sendable {
    struct Group: Codable, Equatable, Identifiable, Sendable {
        var kind: String
        var state: String
        var count: Int
        var id: String { kind + ":" + state }
    }
    var version: Int
    var sha256: String
    var files: Int
    var bytes: Int64
    var unavailable: [Group]
    var valid: Bool {
        version == 1 && sha256.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil &&
        (0...1024).contains(files) && (0...Int64(128 << 20)).contains(bytes) &&
        (files == 0 ? bytes == 0 : bytes >= Int64(files)) && unavailable.count <= 12 &&
        Set(unavailable.map(\.id)).count == unavailable.count && unavailable.allSatisfy {
            ["image", "audio", "document"].contains($0.kind) &&
            ["reference", "unsupported", "invalid", "too_large"].contains($0.state) && (1...1_000_000).contains($0.count)
        } && (files > 0 || !unavailable.isEmpty)
    }
}

struct TransferAttachmentReviewView: View {
    let review: TransferAttachmentReview
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Attachments").font(.system(size: 12, weight: .medium))
            Text("\(review.files) files with contents · \(ByteCountFormatter.string(fromByteCount: review.bytes, countStyle: .file))")
            ForEach(review.unavailable) { group in
                Text("\(group.count) \(group.kind) attachments · \(reason(group.state))")
                    .foregroundStyle(.orange)
            }
            if !review.unavailable.isEmpty {
                Text("Unavailable attachments keep their references in the conversation, without file contents")
                    .foregroundStyle(campMuted)
            }
        }.font(.caption)
    }
    private func reason(_ state: String) -> String {
        switch state {
        case "reference": return "External references only"
        case "too_large": return "Contents exceed the size limit"
        case "invalid": return "Contents are invalid"
        default: return "Format not supported"
        }
    }
}
