import Foundation

enum CampWire {
    static func decoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer().decode(String.self)
            if let date = try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(value) { return date }
            if let date = try? Date.ISO8601FormatStyle().parse(value) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Invalid Camp date"))
        }
        return decoder
    }
    static func encoder() -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = .sortedKeys
        return encoder
    }
    static func body(_ value: [String: Any]) throws -> Data { try JSONSerialization.data(withJSONObject: value, options: .sortedKeys) }
}

struct CloudActor: Codable, Equatable, Sendable {
    var userId: String
    var deviceId: String
    var email: String
    var name: String
}
struct CloudCredential: Codable, Sendable {
    var origin: String
    var actor: CloudActor
    var token: String
    var expiresAt: Date
}
struct CloudLogin: Codable, Sendable { var actor: CloudActor; var token: String; var expiresAt: Date }
struct CloudCamp: Codable, Identifiable, Sendable { var id: String; var name: String; var role: String; var cursor: Int64 }
struct CloudProject: Codable, Identifiable, Sendable { var id: String; var name: String }
struct CloudMember: Codable, Identifiable, Sendable {
    var userId: String; var name: String; var role: String
    var id: String { userId }
}
struct CloudSnapshot: Codable, Sendable { var camp: CloudCamp; var projects: [CloudProject]; var members: [CloudMember] }
struct CloudProfile: Codable, Identifiable, Sendable {
    var id: String; var ownerUserId: String; var ownerDeviceId: String; var provider: String; var localId: String; var label: String
}
struct CloudSession: Codable, Identifiable, Equatable, Sendable {
    var id: String; var campId: String; var projectId: String; var profileId: String
    var provider: String; var profileLabel: String; var ownerUserId: String; var ownerDeviceId: String
    var localSessionId: String; var title: String; var model: String; var state: String; var stage: String
    var shared: Bool; var allowRequests: Bool; var externalOwner: Bool
    var revision: Int64; var transcriptRevision: Int64; var permissionRevision: Int64; var updatedAt: Date
}
struct CloudSessionPage: Codable, Sendable { var sessions: [CloudSession]; var next: String }
struct CloudMessage: Codable, Identifiable, Equatable, Sendable {
    var id: String; var ordinal: Int64; var role: String; var text: String; var removed: Bool
    var model: String?; var effort: String?; var speed: String?; var revision: Int64
}
struct CloudTranscript: Codable, Sendable { var revision: Int64; var messages: [CloudMessage]; var before: Int64; var hasMore: Bool }
struct CloudSourceRef: Codable, Equatable, Sendable { var sessionId: String; var transcriptRevision: Int64; var permissionRevision: Int64 = 0 }
struct CloudRequest: Codable, Identifiable, Equatable, Sendable {
    var id: String; var campId: String; var sessionId: String; var requesterUserId: String; var ownerUserId: String; var ownerDeviceId: String
    var permissionRevision: Int64; var revision: Int64; var prompt: String; var model: String; var effort: String; var speed: String; var state: String
    var leaseToken: String?; var leaseUntil: Date?; var createdAt: Date; var evidence: String?; var reason: String?; var sources: [CloudSourceRef]
}
struct CloudEvent: Codable, Sendable {
    struct Payload: Codable, Sendable {
        var sessionId: String?; var requestId: String?; var revision: Int64?; var transcriptRevision: Int64?; var state: String?; var automatic: Bool?
    }
    var cursor: Int64; var kind: String; var actorId: String; var payload: Payload
}
struct CloudInvitation: Codable, Sendable { var id: String; var token: String; var expiresAt: Date }

struct CampEndpoint: Equatable, Sendable {
    let origin: String
    let url: URL
    init(_ input: String, allowLoopback: Bool = false) throws {
        guard var parts = URLComponents(string: input.trimmingCharacters(in: .whitespacesAndNewlines)),
              let host = parts.host?.lowercased(), parts.user == nil, parts.password == nil,
              parts.query == nil, parts.fragment == nil, parts.path.isEmpty || parts.path == "/",
              parts.scheme == "https" || (allowLoopback && parts.scheme == "http" && ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host)) else {
            throw CampCloudError.invalidEndpoint
        }
        parts.host = host; parts.path = ""
        guard let value = parts.url else { throw CampCloudError.invalidEndpoint }
        origin = value.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        url = value
    }
    func requestURL(_ path: String) throws -> URL {
        guard let relative = URLComponents(string: path), relative.scheme == nil, relative.host == nil,
              relative.fragment == nil, path.hasPrefix("/"), !path.hasPrefix("//"),
              !relative.path.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }),
              !path.contains("\\"), var target = URLComponents(url: url, resolvingAgainstBaseURL: false) else { throw CampCloudError.invalidEndpoint }
        target.path = relative.path; target.percentEncodedQuery = relative.percentEncodedQuery
        guard let value = target.url else { throw CampCloudError.invalidEndpoint }; return value
    }
}

enum CampCloudError: LocalizedError {
    case invalidEndpoint, invalidResponse, responseTooLarge, cacheUnavailable, credentialsUnavailable
    case server(Int, String)
    var errorDescription: String? {
        switch self {
        case .invalidEndpoint: return "Use the HTTPS address of your Camp server"
        case .invalidResponse: return "The Camp response could not be read"
        case .responseTooLarge: return "The Camp response exceeded its size limit"
        case .cacheUnavailable: return "The local Camp cache is unavailable"
        case .credentialsUnavailable: return "Camp credentials could not be stored securely"
        case .server(let status, let code):
            if status == 401 { return "Sign in to your Camp again" }
            if status == 409 && code == "revision_conflict" { return "This item changed on another device. Review the latest version" }
            if status == 404 { return "This item is no longer shared with you" }
            if status == 429 { return "Camp is busy. Try again shortly" }
            return "Camp could not complete this request (\(status))"
        }
    }
}
