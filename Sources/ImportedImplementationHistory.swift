import Foundation
import SwiftUI

private func historyDigest(_ value: String) -> Bool { value.range(of: "^[a-f0-9]{64}$", options: .regularExpression) != nil }
private func historyPermissions(_ value: PermissionSettings?) -> Bool {
    guard let value else { return true }
    return [value.profile,value.sandbox,value.approval,value.reviewer,value.claudeMode].allSatisfy { $0.utf8.count <= 160 }
}

struct ImportedImplementationRecord: Codable, Equatable, Identifiable, Sendable {
    var id: String; var ordinal: Int; var attemptCount: Int
    var sourceId: String; var planVersion: Int; var sessionId: String; var transcriptRevision: Int64; var messageCount: Int
    var provider: String; var state: String; var model: String; var effort: String; var speed: String
    var planDigest: String? = nil; var contextFingerprint: String? = nil; var initialPrompt: String? = nil
    var permissions: PermissionSettings? = nil; var permissionSummary: String? = nil
    var valid: Bool {
        UUID(uuidString: id) != nil && ordinal > 0 && ordinal <= 100 && (0...200).contains(attemptCount) &&
        historyDigest(sourceId) && planVersion > 0 && UUID(uuidString: sessionId) != nil && transcriptRevision >= 0 &&
        (0...2000).contains(messageCount) && ["Codex", "Claude"].contains(provider) &&
        ["approved", "idle", "complete", "needs_review"].contains(state) && model.utf8.count <= 200 && effort.utf8.count <= 64 && speed.utf8.count <= 64
    }
    var validDetail: Bool {
        valid && planDigest.map(historyDigest) == true && contextFingerprint.map(historyDigest) == true &&
        initialPrompt != nil && initialPrompt!.utf8.count <= 65536 && (permissionSummary?.utf8.count ?? 0) <= 2048 && historyPermissions(permissions)
    }
    func matches(_ other: Self) -> Bool {
        id == other.id && ordinal == other.ordinal && attemptCount == other.attemptCount && sourceId == other.sourceId &&
        planVersion == other.planVersion && sessionId == other.sessionId && transcriptRevision == other.transcriptRevision &&
        messageCount == other.messageCount && provider == other.provider && state == other.state && model == other.model && effort == other.effort && speed == other.speed
    }
}
struct ImportedImplementationPage: Codable, Sendable { var implementations: [ImportedImplementationRecord]; var next: Int }
struct ImportedAttempt: Codable, Equatable, Identifiable, Sendable {
    var ordinal: Int; var id: String; var prompt: String; var model: String; var effort: String; var speed: String
    var state: String; var error: String; var usage: ConversationUsage? = nil
    var permissions: PermissionSettings? = nil; var permissionFingerprint: String? = nil
    var valid: Bool {
        (1...200).contains(ordinal) && UUID(uuidString: id) != nil && prompt.utf8.count <= 65536 && model.utf8.count <= 200 &&
        effort.utf8.count <= 64 && speed.utf8.count <= 64 && ["completed", "cancelled", "failed", "uncertain"].contains(state) &&
        error.utf8.count <= 65536 && (usage?.inputTokens ?? 0) >= 0 && (usage?.outputTokens ?? 0) >= 0 &&
        (permissionFingerprint == nil || permissionFingerprint == "" || historyDigest(permissionFingerprint!)) && historyPermissions(permissions)
    }
}
struct ImportedAttemptPage: Codable, Sendable { var attempts: [ImportedAttempt]; var next: Int }

@MainActor final class ImportedImplementationHistory: ObservableObject {
    @Published private(set) var implementations: [ImportedImplementationRecord] = []
    @Published private(set) var selected: ImportedImplementationRecord?
    @Published private(set) var messages: [Message] = []
    @Published private(set) var attempts: [ImportedAttempt] = []
    @Published private(set) var loading = false
    @Published private(set) var error = ""
    @Published private(set) var implementationNext = 0
    @Published private(set) var attemptNext = 0
    @Published private(set) var hasEarlier = false
    private var before: Int64 = 0
    private let planning: CampPlanning
    private var plan: CloudPlanningResult?
    private var work: Task<Void, Never>?
    private var generation = UUID()
    init(planning: CampPlanning) { self.planning = planning }
    deinit { work?.cancel() }
    func clear() {
        generation = UUID(); work?.cancel(); work = nil; plan = nil; loading = false; error = ""
        implementations = []; selected = nil; messages = []; attempts = []; implementationNext = 0; attemptNext = 0; hasEarlier = false; before = 0
    }
    func bind(_ ticket: CloudPlanningResult) { clear(); plan = ticket; loadImplementations() }
    func cancel() { clear() }
    private func start(_ operation: @escaping (UUID, CloudPlanningResult) async throws -> Void) {
        guard let plan else { return }
        generation = UUID(); let token = generation; work?.cancel(); loading = true; error = ""
        work = Task { [weak self] in
            guard let self else { return }
            defer { if self.generation == token { self.loading = false; self.work = nil } }
            do { try await operation(token, plan) }
            catch is CancellationError {}
            catch {
                guard self.generation == token else { return }
                self.error = error.localizedDescription
                if case CampCloudError.server(let status, _) = error, [401,403,404].contains(status) {
                    self.implementations = []; self.selected = nil; self.messages = []; self.attempts = []
                    self.implementationNext = 0; self.attemptNext = 0; self.hasEarlier = false
                }
            }
        }
    }
    private func check(_ token: UUID) throws { if generation != token || Task.isCancelled { throw CancellationError() } }
    func loadImplementations() {
        guard !loading else { return }
        let after = implementationNext, base = implementations
        start { [weak self] token, plan in
            guard let self else { return }
            let data = try await self.planning.readImplementationResource(plan, suffix: "?version=\(plan.version.version)&after=\(after)&limit=20")
            let page = try CampWire.decoder().decode(ImportedImplementationPage.self, from: data)
            try self.check(token)
            guard page.implementations.count <= 20, page.implementations.allSatisfy({ $0.valid && $0.planVersion == plan.version.version && $0.ordinal > after && self.planning.implementationSourceVisible($0.sessionId) }),
                  page.implementations.map(\.ordinal) == page.implementations.map(\.ordinal).sorted(),
                  Set(page.implementations.map(\.ordinal)).count == page.implementations.count,
                  page.next == 0 || page.next == page.implementations.last?.ordinal && page.next > after,
                  base.count + page.implementations.count <= 100,
                  Set((base + page.implementations).map(\.id)).count == base.count + page.implementations.count else { throw CampCloudError.invalidResponse }
            self.implementations = base + page.implementations; self.implementationNext = page.next
        }
    }
    func select(_ record: ImportedImplementationRecord) {
        guard implementations.contains(where: { $0 == record }) else { return }
        selected = record; messages = []; attempts = []; hasEarlier = false; attemptNext = 0; before = 0
        start { [weak self] token, plan in
            guard let self else { return }
            let data = try await self.planning.readImplementationResource(plan, suffix: "/" + record.id, sourceID: record.sessionId)
            let detail = try CampWire.decoder().decode(ImportedImplementationRecord.self, from: data)
            try self.check(token)
            guard detail.validDetail, detail.matches(record) else { throw CampCloudError.invalidResponse }
            self.selected = detail
            try await self.transcript(token, plan: plan, record: detail, older: false)
        }
    }
    private func transcript(_ token: UUID, plan: CloudPlanningResult, record: ImportedImplementationRecord, older: Bool) async throws {
        let cursor = older ? before : 0, base = older ? messages : []
        let data = try await planning.readImplementationResource(plan, suffix: "/\(record.id)/transcript?before=\(cursor)&limit=200", sourceID: record.sessionId)
        let page = try CampWire.decoder().decode(CloudTranscript.self, from: data)
        try check(token)
        let fresh = page.messages.map(\.transcriptMessage)
        guard page.revision == record.transcriptRevision, page.messages.count <= 200,
              page.messages.allSatisfy({ !$0.removed && $0.revision <= page.revision && $0.ordinal >= 0 && (cursor == 0 || $0.ordinal < cursor) && ["user","assistant","system","tool"].contains($0.role) && !$0.id.isEmpty && $0.id.utf8.count <= 256 }),
              page.messages.map(\.ordinal) == page.messages.map(\.ordinal).sorted(),
              Set(page.messages.map(\.ordinal)).count == page.messages.count,
              page.before == (page.messages.first?.ordinal ?? 0), !page.hasMore || !fresh.isEmpty && page.before > 0 && (cursor == 0 || page.before < cursor),
              fresh.count + base.count <= record.messageCount,
              Set((fresh + base).map(\.id)).count == fresh.count + base.count,
              (fresh + base).reduce(0, { $0 + $1.text.utf8.count }) <= 8 << 20 else { throw CampCloudError.invalidResponse }
        if !page.hasMore, fresh.count + base.count != record.messageCount { throw CampCloudError.invalidResponse }
        messages = fresh + base; before = page.before; hasEarlier = page.hasMore
    }
    func loadEarlier() {
        guard !loading, hasEarlier, let selected else { return }
        start { [weak self] token, plan in try await self?.transcript(token, plan: plan, record: selected, older: true) }
    }
    func loadAttempts() {
        guard !loading, let selected, attempts.isEmpty || attemptNext > 0 else { return }
        let after = attemptNext, base = attempts
        start { [weak self] token, plan in
            guard let self else { return }
            let data = try await self.planning.readImplementationResource(plan, suffix: "/\(selected.id)/attempts?after=\(after)&limit=10", sourceID: selected.sessionId)
            let page = try CampWire.decoder().decode(ImportedAttemptPage.self, from: data)
            try self.check(token)
            guard page.attempts.count <= 10, page.attempts.allSatisfy({ $0.valid && $0.ordinal > after }),
                  page.attempts.map(\.ordinal) == page.attempts.map(\.ordinal).sorted(),
                  Set(page.attempts.map(\.ordinal)).count == page.attempts.count,
                  page.next == 0 || page.next == page.attempts.last?.ordinal && page.next > after,
                  base.count + page.attempts.count <= selected.attemptCount,
                  Set((base + page.attempts).map(\.id)).count == base.count + page.attempts.count,
                  (base + page.attempts).reduce(0, { $0 + $1.prompt.utf8.count + $1.error.utf8.count }) <= 16 << 20 else { throw CampCloudError.invalidResponse }
            if page.next == 0, base.count + page.attempts.count != selected.attemptCount { throw CampCloudError.invalidResponse }
            self.attempts = base + page.attempts; self.attemptNext = page.next
        }
    }
}
