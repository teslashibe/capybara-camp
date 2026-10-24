import Foundation

struct HistorySourceSnapshot: Codable, Equatable, Sendable {
    var messages: [Message]
    var attachmentSource: CampSession? = nil
    var revision: String { (try? CampWire.encoder().encode(messages)).map(PublishMessage.hash) ?? "" }
    var bytes: Int { messages.reduce(0) { $0 + $1.text.utf8.count + ((try? CampWire.encoder().encode($1.attachments ?? []).count) ?? (2 << 20)) } }
    var valid: Bool {
        attachmentSourceValid && messages.count <= 2000 && bytes <= 8 << 20 && Set(messages.map(\.id)).count == messages.count && messages.allSatisfy {
            ($0.attachments ?? []).count <= 64 && ($0.attachments ?? []).allSatisfy(\.validForTransfer) && !$0.id.isEmpty && $0.id.utf8.count <= 256 && ["user", "assistant", "system", "tool"].contains($0.role) &&
            $0.text.utf8.count <= 1 << 20 && ($0.model?.utf8.count ?? 0) <= 200 && ($0.effort?.utf8.count ?? 0) <= 32 && ($0.speed?.utf8.count ?? 0) <= 32 &&
            ((try? CampWire.encoder().encode($0).count) ?? Int.max) <= (2 << 20) - 4096
        }
    }
    private var attachmentSourceValid: Bool {
        guard messages.contains(where: { ($0.attachments ?? []).contains(where: { $0.state == "embedded" }) }) else { return true }
        guard let source = attachmentSource else { return false }
        return ["Codex", "Claude"].contains(source.provider) && source.root.hasPrefix("/") &&
            !source.sourceID.isEmpty && !source.sessionID.isEmpty && source.sessionID.utf8.count <= 200 &&
            !source.sessionID.contains("/") && !source.sessionID.contains("\\") && !source.sessionID.hasPrefix("history:")
    }
    func page(after: Int64) throws -> TranscriptExport {
        guard valid, after >= -1, after <= Int64(messages.count) else { throw CampCloudError.invalidResponse }
        let start = min(Int(after + 1), messages.count)
        var values: [PublishMessage] = [], size = 0, encodedSize = 2
        for index in start..<min(start + 200, messages.count) {
            let message = messages[index]
            if size + message.text.utf8.count > 1 << 20 && !values.isEmpty { break }
            let value = PublishMessage(id: message.id, ordinal: Int64(index), role: message.role, text: message.text,
                removed: false, model: message.model, effort: message.effort, speed: message.speed, attachments: message.attachments)
            let bytes = try CampWire.encoder().encode(value).count + (values.isEmpty ? 0 : 1)
            if encodedSize + bytes > (2 << 20) - 4096 {
                guard !values.isEmpty else { throw CampCloudError.responseTooLarge }
                break
            }
            values.append(value); size += message.text.utf8.count; encodedSize += bytes
        }
        let end = start + values.count
        return TranscriptExport(messages: values, after: values.last?.ordinal ?? after, hasMore: end < messages.count,
            fingerprint: revision, contentRevision: revision)
    }
    func matchesPrefix(_ copied: [CloudMessage]) -> Bool {
        copied.count <= messages.count && copied.enumerated().allSatisfy { index, value in
            let expected = messages[index]
            return value.ordinal == Int64(index) && !value.removed && value.id == expected.id && value.role == expected.role && value.text == expected.text &&
                (value.model ?? "") == (expected.model ?? "") && (value.effort ?? "") == (expected.effort ?? "") && (value.speed ?? "") == (expected.speed ?? "") && (value.attachments ?? []) == (expected.attachments ?? [])
        }
    }
    static func localID(key: String, messages: [Message]) -> String {
        let snapshot = Self(messages: messages)
        return "history:" + PublishMessage.hash(Data((key + "\n" + snapshot.revision).utf8))
    }
    static func cloudID(localID: String, scope: CloudScope) -> String {
        var hex = Array(PublishMessage.hash(Data((scope.key + "\n" + localID).utf8)).prefix(32))
        hex[12] = "8"; hex[16] = "8"
        return "\(String(hex[0..<8]))-\(String(hex[8..<12]))-\(String(hex[12..<16]))-\(String(hex[16..<20]))-\(String(hex[20..<32]))"
    }
    static func limited(_ value: String, bytes: Int) -> String {
        var result = "", count = 0
        for scalar in value.unicodeScalars {
            let size = String(scalar).utf8.count
            guard count + size <= bytes else { break }
            result.unicodeScalars.append(scalar); count += size
        }
        return result
    }
}
