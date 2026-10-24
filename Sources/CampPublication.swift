import Foundation
import CryptoKit

struct PublishMessage: Codable, Sendable, Equatable {
    var id: String; var ordinal: Int64; var role: String; var text: String; var removed: Bool
    var model: String?
    var effort: String?
    var speed: String?
    var attachments: [TranscriptAttachment]? = nil
    var digest: String { (try? CampWire.encoder().encode(self)).map(Self.hash) ?? "" }
    static func hash(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
}
struct TranscriptExport: Decodable, Sendable {
    var messages: [PublishMessage]; var after: Int64; var hasMore: Bool; var fingerprint: String
    var contentRevision:String? = nil
    var snapshotRevision:String {contentRevision ?? fingerprint}
    // The local bridge uses camel case; the Camp API uses snake case.
}
struct PublishedDigest: Codable, Sendable { var id: String; var digest: String }
struct LocalHistoryBinding:Codable,Equatable,Sendable {
    struct Child:Codable,Equatable,Sendable {var sourceId:String;var revision:String}
    var version:Int=1;var parentId:String;var mainRevision:String;var children:[Child];var transcriptRevision:Int64
    var valid:Bool {
        let values=[parentId,mainRevision]+children.flatMap{[$0.sourceId,$0.revision]}
        return version==1 && transcriptRevision>=0 && children.count<=512 && Set(children.map(\.sourceId)).count==children.count && values.allSatisfy{$0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil}
    }
}
struct CampPublication: Codable, Sendable {
    var key: String = UUID().uuidString.lowercased()
    var kind: String; var method: String; var path: String; var body: Data
    var hashes: [PublishedDigest] = []
    var sent: Bool?
    var publishedSessionID: String?
    private enum CodingKeys: String, CodingKey {
        case key, kind, method, path, body, hashes, sent
        case publishedSessionID = "publishedSessionId"
    }
}
struct LocalShare: Codable, Identifiable, Sendable {
    var id: String
    var local: CampSession
    var projectId: String
    var shared: Bool
    var allowRequests: Bool
    var profile: CloudProfile?
    var remote: CloudSession?
    var pending: CampPublication?
    var adoptRequested: Bool?
    var deviceId: String?
    var needsWithdrawalFence: Bool?
    var projectChangeRequested: Bool?
    var syncToCloud: Bool?
    var childHistories: [CloudChildHistory]?
    var publishedHistory:LocalHistoryBinding?
    var historySnapshot: HistorySourceSnapshot?
    var state: String = "Waiting to sync"
}
protocol CampPublishingTransport: Sendable {
    func perform(_ operation: CampPublication) async throws -> Data
}
struct AuthenticatedCampPublisher: CampPublishingTransport {
    let client: CampHTTP
    let token: String
    func perform(_ operation: CampPublication) async throws -> Data {
        try await client.data(operation.method, path: operation.path, body: operation.method == "GET" ? nil : operation.body, key: operation.method == "GET" ? nil : operation.key, token: token)
    }
}
