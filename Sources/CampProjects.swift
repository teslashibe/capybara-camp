import Foundation
import SwiftUI

enum ProjectRepositories {
    static func normalize(_ values: [String]) throws -> [String] {
        guard values.count <= 50 else { throw CampError.message("Use at most 50 repository links") }
        var result = Set<String>()
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
        for value in values {
            guard !value.contains(where:{ "?#%".contains($0) }), let url = URLComponents(string: value.trimmingCharacters(in:.whitespacesAndNewlines)), url.scheme == "https", url.host?.lowercased() == "github.com", url.port == nil, url.user == nil, url.password == nil, url.query == nil, url.fragment == nil else { throw CampError.message("Use a GitHub repository URL such as https://github.com/owner/repository") }
            let parts = url.path.trimmingCharacters(in:CharacterSet(charactersIn:"/")).components(separatedBy:"/")
            guard parts.count == 2, parts.allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." && $0.unicodeScalars.allSatisfy(allowed.contains) }) else { throw CampError.message("Use repository URLs without branches or credentials") }
            let name = parts[1].hasSuffix(".git") ? String(parts[1].dropLast(4)) : parts[1]
            guard !name.isEmpty else { throw CampCloudError.invalidResponse }
            result.insert("https://github.com/" + parts[0].lowercased() + "/" + name.lowercased())
        }
        return result.sorted()
    }
}
struct LocalProjectState: Sendable { var revision: Int64; var projects: [CampProject]; var assignments: [String:String] }

@MainActor final class LocalProjects: ObservableObject {
    @Published private(set) var projects: [CampProject] = []
    @Published private(set) var assignments: [String:String] = [:]
    @Published private(set) var ready = false
    @Published private(set) var error = ""
    private(set) var byID: [String:CampProject] = [:]
    var changed: (() -> Void)?
    private let cache: CampCache?
    private var revision: Int64 = -1
    init(cache: CampCache?) {
        self.cache=cache
        Task { [weak self] in
            do { guard let cache else { throw CampCloudError.cacheUnavailable }; let state=try await cache.localProjectState();self?.apply(state);self?.ready=true }
            catch { self?.error=error.localizedDescription }
        }
    }
    private func apply(_ state: LocalProjectState) { guard state.revision >= revision else { return }; revision=state.revision;projects=state.projects;byID=Dictionary(uniqueKeysWithValues:projects.map{($0.id,$0)});assignments=state.assignments;error="";changed?() }
    func save(_ value: CampProject, expectedRevision: Int64) async throws {
        guard ready,let cache else { throw CampCloudError.cacheUnavailable }
        do { apply(try await cache.saveLocalProject(value,expectedRevision:expectedRevision)) } catch { self.error=error.localizedDescription;throw error }
    }
    func assign(_ ticket: String, to project: String?) async throws {
        guard ready,let cache else { throw CampCloudError.cacheUnavailable }
        do { apply(try await cache.assignLocalProject(ticketID:ticket,projectID:project)) } catch { self.error=error.localizedDescription;throw error }
    }
}

extension CampStore {
    var canManageSharedProjects: Bool { ["owner","editor"].contains(cloud.camps.first { $0.id == cloud.scope?.campId }?.role ?? "") }
    func sharedProjectSession(_ s: CampSession) -> CloudSession? {
        if let remote = cloudSessions[s.id] { return remote }
        guard let binding = sharing.binding(for: s), binding.shared || binding.syncToCloud == true else { return nil }
        return cloudSessions["camp:" + binding.id]
    }
    func projectKey(_ s: CampSession) -> String {
        if s.isSharedPlan {
            if let project = sharedPlanning.draft(s.sessionID)?.input.projectId ?? sharedPlanning.tickets.first(where:{$0.id==s.sessionID})?.projectId { return "cloud:"+project }
        }
        if let remote = sharedProjectSession(s) { return "cloud:"+remote.projectId }
        return projects.assignments[s.id].map { "local:"+$0 } ?? ""
    }
    func assignedProject(_ s: CampSession) -> CampProject? {
        let key=projectKey(s)
        if key.hasPrefix("cloud:") { return cloudProjectIndex[String(key.dropFirst(6))] }
        return key.hasPrefix("local:") ? projects.byID[String(key.dropFirst(6))] : nil
    }
    func projectLabel(_ s: CampSession) -> String { assignedProject(s)?.name ?? "Inbox" }
    func projectSummary(_ s: CampSession) -> String {
        let name=projectLabel(s)
        return !s.isCloud && !s.project.isEmpty ? name + " · " + s.project : name
    }
    func projectMatches(_ s: CampSession) -> Bool {
        if projectFilter=="all" { return true }
        if projectFilter=="inbox" { return projectKey(s).isEmpty || assignedProject(s)?.isInbox == true }
        return projectKey(s)==projectFilter
    }
    func assignProject(_ s: CampSession, to id: String?) {
        Task {
            do {
                if s.isSharedPlan {
                    if let draft=sharedPlanning.draft(s.sessionID) {
                        guard !sharedPlanning.pending(draft.id),let target=id ?? cloud.projects.first(where: \.isInbox)?.id else { throw CampError.message("Choose a shared project") }
                        sharedPlanning.edit(draft.id) { $0.projectId=target }
                    } else if let plan=sharedPlanning.detail(s.sessionID) { try await cloud.queuePlanningProject(plan,projectID:id ?? "") }
                    else { sharedPlanning.open(s.sessionID);throw CampError.message("Open this plan once before changing its project") }
                } else if let remote=sharedProjectSession(s) { try await cloud.queueSessionProject(remote,projectID:id ?? "") }
                else { try await projects.assign(s.id,to:id) }
            } catch { banner=error.localizedDescription }
        }
    }
}
