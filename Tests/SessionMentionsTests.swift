import Foundation

@main struct SessionMentionsTests {
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-context-tests-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let store = CampStore(stateDirectory: root)
        let source = CampSession(id: "local-source", sessionID: "native-source", provider: "Claude", sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Synthetic source", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
        let target = CampSession(id: "local-target", sessionID: "native-target", provider: "Codex", sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Synthetic target", cwd: root.path, updated: 2, path: "", accountID: "", archived: false)
        store.sessions = [source, target]
        let messages = [Message(id: "not-selected", role: "user", text: "Private excluded text"), Message(id: "picked", role: "assistant", text: "Selected answer")]
        let preview = ContextPreview(messages: messages, total: 2)
        let reference = try store.makeReference(source, preview: preview, range: 1...1)
        precondition(reference.messages?.map(\.id) == ["picked"])
        try store.mentions.add(reference, to: target.id)
        try store.mentions.record([reference])
        let contexts = try await store.resolveContexts([reference], destination: target)
        precondition(contexts.count == 1 && contexts[0].messages.map(\.text) == ["Selected answer"])
        let contextJSON = try JSONEncoder().encode(contexts)
        precondition(!String(data: contextJSON, encoding: .utf8)!.contains("Private excluded text"))
        precondition(contexts[0].id.hasPrefix("source-") && contexts[0].id != source.id)
        try store.mentions.clear([reference], for: target.id)
        let restored = SessionMentions(directory: root)
        precondition(restored.references(for: target.id).isEmpty)
        precondition(restored.source(id: source.id, revision: reference.digest!, message: "picked") == reference)
        precondition(restored.source(id: reference.contextID, revision: reference.digest!, message: "picked") == reference)
        precondition(restored.source(id: source.id, revision: reference.digest!, message: "not-selected") == nil)
        let attributes = try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("context-drafts.json").path)
        precondition((attributes[.posixPermissions] as? Int) == 0o600)
        var tampered = reference; tampered.messages = [Message(id: "picked", role: "assistant", text: "Changed")]
        do { _ = try await store.resolveContexts([tampered], destination: target); preconditionFailure("Tampered source accepted") } catch {}
        let remoteTarget = CampSession(id: "camp:remote", sessionID: "remote", provider: "Codex", sourceID: "remote", sourceLabel: "Remote", root: "", title: "Remote", cwd: "", updated: 1, path: "", accountID: "", archived: false)
        do { _ = try await store.resolveContexts([reference], destination: remoteTarget); preconditionFailure("Private local context sent to shared target") } catch {}
        store.sessions = [target]
        do { _ = try await store.resolveContexts([reference], destination: target); preconditionFailure("Removed source accepted") } catch {}
        let old = Data("{\"session_id\":\"fixture\",\"transcript_revision\":1,\"permission_revision\":1}".utf8)
        let legacy = try CampWire.decoder().decode(CloudSourceRef.self, from: old)
        precondition(legacy.messageIds == nil)
        let queued = QueuedPrompt(id: "q", sessionID: target.id, text: "Use context", model: "fixture", effort: "low", speed: "standard", sources: [reference])
        let decoded = try JSONDecoder().decode(QueuedPrompt.self, from: JSONEncoder().encode(queued))
        precondition(decoded.sources == [reference])
        print("PASS: pinned source selection, private persistence, provenance, tamper checks, scope and queue round trips")
    }
}
