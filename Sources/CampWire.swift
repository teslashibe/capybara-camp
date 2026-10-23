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
struct CampProject: Codable, Identifiable, Equatable, Sendable {
    var id: String; var name: String
    var revision: Int64? = nil
    var archived: Bool? = nil
    var inbox: Bool? = nil
    var repositories: [String]? = nil
    var version: Int64 { revision ?? 1 }
    var isArchived: Bool { archived ?? false }
    var isInbox: Bool { inbox ?? false }
    var repositoryURLs: [String] { repositories ?? [] }
}
typealias CloudProject = CampProject
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
    var activityRevision: Int64? = nil
    var executionLocation: String? = nil
    var executionEpoch: Int64? = nil
    var executionWorkspaceId: String? = nil
    var historyOnly: Bool? = nil
    var isHistoryOnly: Bool { historyOnly == true }
    func canReplace(_ old: CloudSession) -> Bool {
        revision > old.revision || (revision == old.revision && transcriptRevision >= old.transcriptRevision && (activityRevision ?? 0) >= (old.activityRevision ?? 0))
    }
}
struct CloudSessionPage: Codable, Sendable { var sessions: [CloudSession]; var next: String }
struct CloudMessage: Codable, Identifiable, Equatable, Sendable {
    var id: String; var ordinal: Int64; var role: String; var text: String; var removed: Bool
    var model: String?; var effort: String?; var speed: String?; var revision: Int64
    var transcriptMessage: Message { Message(id: id, role: role, text: text, model: model, effort: effort, speed: speed) }
}
struct CloudTranscript: Codable, Sendable { var revision: Int64; var messages: [CloudMessage]; var before: Int64; var hasMore: Bool }
struct CloudSourceRef: Codable, Equatable, Sendable {
    var sessionId: String; var transcriptRevision: Int64; var permissionRevision: Int64 = 0
    var messageIds: [String]? = nil
}
struct CloudRequest: Codable, Identifiable, Equatable, Sendable {
    var contentRestricted: Bool? = nil
    var promptLabel: String { contentRestricted == true ? "Context access changed" : prompt }
    var id: String; var campId: String; var sessionId: String; var requesterUserId: String; var ownerUserId: String; var ownerDeviceId: String
    var permissionRevision: Int64; var revision: Int64; var prompt: String; var model: String; var effort: String; var speed: String; var state: String
    var leaseToken: String?; var leaseUntil: Date?; var createdAt: Date; var evidence: String?; var reason: String?; var sources: [CloudSourceRef]
    var kind: String? = nil
    var planId: String? = nil
    var planVersion: Int? = nil
    var planStep: Int? = nil
    var executionLocation: String? = nil
    var streamSeq: Int64? = nil
    var executionEpoch: Int64? = nil
    var quotedSources: [ImportedPlanningSource]? = nil
    var quotedContextDigest: String? = nil
    var isPlanning: Bool { kind == "planning" }
    var implementationId: String? = nil
    var providerSessionId: String? = nil
    var executionContext: String? = nil
    var permissions: [String: String]? = nil
    var mode: String? = nil
    var isImplementation: Bool { kind == "implementation" }
    var isIsolatedWork: Bool { isPlanning || isImplementation }
    var permissionSettings: PermissionSettings? {
        guard let p = permissions, Set(p.keys).isSubset(of: ["profile", "sandbox", "approval", "reviewer", "claudeMode"]) else { return nil }
        return PermissionSettings(profile: p["profile"] ?? "", sandbox: p["sandbox"] ?? "", approval: p["approval"] ?? "", reviewer: p["reviewer"] ?? "", claudeMode: p["claudeMode"] ?? "")
    }
    func merging(_ previous: CloudRequest?) -> CloudRequest {
        guard let previous, previous.id == id, previous.campId == campId else { return contentRestricted == true ? restrictingContent() : self }
        let latest = previous.revision > revision ? previous : self
        return contentRestricted == true || previous.contentRestricted == true ? latest.restrictingContent() : latest
    }
    func restrictingContent() -> CloudRequest {
        var value = self
        value.contentRestricted = true; value.prompt = ""; value.sources = []
        value.quotedSources = nil; value.quotedContextDigest = nil; value.evidence = nil
        value.providerSessionId = nil; value.executionContext = nil
        return value
    }
    var supportedWork: Bool {
        guard contentRestricted != true else { return false }
        let quoted = quotedSources ?? []
        let validQuotes = validPlanningQuotes(quoted, sharedCount: sources.count) &&
            (quoted.isEmpty ? quotedContextDigest == nil || quotedContextDigest == "" : quotedContextDigest?.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil)
        if isPlanning { return validQuotes && planId.flatMap(UUID.init(uuidString:)) != nil && (planVersion ?? 0) > 0 && (0..<5).contains(planStep ?? -1) }
        if isImplementation {
            return validQuotes && planId.flatMap(UUID.init(uuidString:)) != nil && (planVersion ?? 0) > 0 &&
                implementationId.flatMap(UUID.init(uuidString:)) != nil && mode == "implementation" && permissionSettings != nil &&
                (executionLocation != "local" || executionContext?.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil) &&
                (providerSessionId == nil || providerSessionId == "" || providerSessionId!.utf8.count <= 200 && !providerSessionId!.contains(where: { "/\\\r\n\0".contains($0) }))
        }
        return quoted.isEmpty && (kind == nil || kind == "chat") && planId == nil
    }
}
struct CloudEvent: Codable, Sendable {
    struct Payload: Codable, Sendable {
        var sessionId: String?; var requestId: String?; var revision: Int64?; var transcriptRevision: Int64?; var state: String?; var automatic: Bool?; var planId: String?
    }
    var cursor: Int64; var kind: String; var actorId: String; var payload: Payload
}
struct CloudNotification: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var kind: String
    var title: String
    var sessionId: String?
    var requestId: String?
    var planId: String?
    var planVersion: Int?
    var revision: Int64
    var read: Bool
    var createdAt: Date
    var valid: Bool {
        UUID(uuidString: id) != nil && revision > 0 && title.utf8.count <= 2000 && ["finished", "attention", "plan"].contains(kind) &&
        ((sessionId.flatMap(UUID.init(uuidString:)) != nil && requestId.flatMap(UUID.init(uuidString:)) != nil && planId == nil && planVersion == nil && kind != "plan") ||
         (planId.flatMap(UUID.init(uuidString:)) != nil && (planVersion ?? 0) > 0 && sessionId == nil && requestId == nil && kind != "finished"))
    }
    func key(scope: CloudScope) -> String {
        if let requestId { return "request:\(scope.key):\(requestId)" }
        return "cloud-plan:\(scope.key):\(planId ?? ""):\(planVersion ?? 0)"
    }
}
struct CloudNotificationPage: Codable, Sendable { var notifications: [CloudNotification]; var next: String; var unread: Int }
struct CloudNotificationAction: Codable, Sendable { var ids: [String]; var action: String }
struct CloudPlanningExecution: Codable, Equatable, Sendable {
    var location: String; var epoch: Int64; var workspaceId: String? = nil
    var valid: Bool { epoch > 0 && (location == "local" && workspaceId == nil || location == "cloud" && workspaceId.flatMap(UUID.init(uuidString:)) != nil) }
    var label: String { location == "cloud" ? "Cloud" : "Owner’s Mac" }
}
extension CloudPlanningExecution {
    init?(session: CloudSession) {
        guard !session.isHistoryOnly else { return nil }
        guard let location = session.executionLocation, let epoch = session.executionEpoch else { return nil }
        self.init(location: location, epoch: epoch, workspaceId: session.executionWorkspaceId)
        guard valid else { return nil }
    }
}
struct CloudPlanningResult: Codable, Equatable, Sendable {
    struct Planner: Codable, Equatable, Sendable { var slot: Int; var sessionId: String; var provider: String; var model: String; var effort: String; var speed: String; var execution: CloudPlanningExecution? = nil }
    struct Step: Codable, Identifiable, Equatable, Sendable { var ordinal: Int; var slot: Int; var phase: String; var state: String; var output: String; var usage: ConversationUsage? = nil; var id: Int { ordinal } }
    struct Version: Codable, Equatable, Sendable {
        var version: Int; var state: String; var brief: String; var planners: [Planner]; var steps: [Step]; var sources: [CloudSourceRef]; var mergeSlot: Int = 0; var budgetPolicy: String = "calls"
        var mode: PlanningMode? = nil
        var imported: ImportedPlanningSnapshot? = nil
        var quotedSources: [ImportedPlanningSource]? = nil
        var allQuotedSources: [ImportedPlanningSource] { (quotedSources ?? []) + (imported?.localSources ?? []) }
        var workflow: PlanningMode { mode ?? .comparison }
    }
    var id: String; var title: String; var projectId: String; var version: Version
    var campId: String? = nil; var creatorUserId: String? = nil; var shared: Bool? = nil; var revision: Int64? = nil; var activeVersion: Int? = nil; var updatedAt: Date? = nil
    var implementation: SharedImplementationSummary? = nil
    var referencedSessions: Set<String> { Set(version.planners.map(\.sessionId) + version.sources.map(\.sessionId)) }
    var valid: Bool {
        UUID(uuidString: id) != nil && UUID(uuidString: projectId) != nil && title.utf8.count <= 2000 &&
        (campId == nil || UUID(uuidString: campId!) != nil) && (creatorUserId == nil || UUID(uuidString: creatorUserId!) != nil) &&
        (revision == nil || revision! > 0) && (activeVersion == nil || activeVersion! >= version.version) &&
        (implementation == nil || implementation?.valid == true && implementation?.planVersion == version.version) &&
        version.version > 0 && version.brief.utf8.count <= 8192 && version.workflow.accepts(version.planners.map(\.provider)) &&
        Set(version.planners.map(\.slot)) == Set(0..<version.workflow.plannerCount) &&
        (0..<version.workflow.plannerCount).contains(version.mergeSlot) &&
        version.planners.allSatisfy { $0.model.utf8.count <= 200 && UUID(uuidString: $0.sessionId) != nil && ($0.execution?.valid ?? true) } &&
        version.sources.count <= 20 && version.sources.allSatisfy { UUID(uuidString: $0.sessionId) != nil } && validPlanningQuotes(version.allQuotedSources, sharedCount: version.sources.count) && (version.imported?.valid(for: version) ?? true) && version.steps.count <= version.workflow.turnLimit &&
        Set(version.steps.map(\.id)).count == version.steps.count && version.steps.allSatisfy {
            $0.output.utf8.count <= 8192 && (0..<version.workflow.plannerCount).contains($0.slot) && (0..<version.workflow.turnLimit).contains($0.ordinal) &&
            $0.slot == ($0.ordinal == 4 ? version.mergeSlot : $0.ordinal % 2) && $0.phase == ($0.ordinal < 2 ? "draft" : $0.ordinal < 4 ? "review" : "merge")
        }
    }
}
struct CloudPlannerInput: Codable, Equatable, Sendable {
    var sessionId: String; var model: String; var effort: String; var speed: String
    var execution: CloudPlanningExecution? = nil
}
struct CloudPlanningInput: Codable, Equatable, Sendable {
    var title: String; var projectId: String; var brief: String; var shared: Bool
    var planners: [CloudPlannerInput]; var sources: [CloudSourceRef] = []
    var budgetPolicy = "strict_tokens"; var mergeSlot = 0; var start = true
    var mode: PlanningMode? = nil
    var quotedSourceIds: [String]? = nil
    var quotedSourceVersion: Int? = nil
    var workflow: PlanningMode { mode ?? .comparison }
}
struct CloudPlanDraft: Codable, Equatable, Identifiable, Sendable {
    var id = UUID().uuidString.lowercased(); var input: CloudPlanningInput
    var updatedAt = Date(); var cancelRequested = false
    var restartPlanID: String? = nil; var baseRevision: Int64? = nil
    private enum CodingKeys: String, CodingKey {
        case id, input, updatedAt, cancelRequested, baseRevision, tuningHistory, quotedSources
        case restartPlanID = "restartPlanId"
    }
    var tuningHistory: [String: ModelTuning] = [:]
    var quotedSources: [ImportedPlanningSource]? = nil
}
struct CloudPlanSummary: Codable, Equatable, Identifiable, Sendable {
    var id: String; var projectId: String; var creatorUserId: String; var title: String; var shared: Bool
    var revision: Int64; var activeVersion: Int; var state: String; var planners: [CloudPlanningResult.Planner]; var updatedAt: Date; var completed: Int
    var implementation: SharedImplementationSummary? = nil
    var mode: PlanningMode? = nil
    var workflow: PlanningMode { mode ?? .comparison }
    var valid: Bool { UUID(uuidString: id) != nil && UUID(uuidString: projectId) != nil && UUID(uuidString: creatorUserId) != nil && revision > 0 && activeVersion > 0 && title.utf8.count <= 2000 && workflow.accepts(planners.map(\.provider)) && Set(planners.map(\.slot)) == Set(0..<workflow.plannerCount) && planners.allSatisfy { UUID(uuidString: $0.sessionId) != nil && ($0.execution?.valid ?? true) } && (0...workflow.turnLimit).contains(completed) && (implementation == nil || implementation?.valid == true && implementation?.planVersion == activeVersion) }
    var stage: Stage { implementation?.stage ?? (state == "running" ? .progress : ["ready", "failed", "needs_review"].contains(state) ? .attention : .ready) }
    init(_ result: CloudPlanningResult) throws {
        guard result.valid, let owner = result.creatorUserId, let revision = result.revision, let activeVersion = result.activeVersion, let date = result.updatedAt, let shared = result.shared else { throw CampCloudError.invalidResponse }
        id = result.id; projectId = result.projectId; creatorUserId = owner; title = result.title; self.shared = shared
        self.implementation = result.version.version == activeVersion ? result.implementation : nil
        self.revision = revision; self.activeVersion = activeVersion; state = result.version.state; planners = result.version.planners; updatedAt = date; mode = result.version.mode
        completed = result.version.steps.filter { $0.state == "completed" }.count
        guard valid else { throw CampCloudError.invalidResponse }
    }
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
