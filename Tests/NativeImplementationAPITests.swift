import Foundation

// The real native coordinator/worker talk to a disposable Go API/PostgreSQL
// fixture. Only the provider process and its local permission inspection are
// synthetic. Never run this suite with a personal profile or production API.
actor LostNativeAPIReply: CampPublishingTransport {
    let base: AuthenticatedCampPublisher
    var lost: Set<String>
    var keys: [String: [String]] = [:]
    init(_ base: AuthenticatedCampPublisher, losing: Set<String>) { self.base = base; lost = losing }
    func perform(_ operation: CampPublication) async throws -> Data {
        keys[operation.kind, default: []].append(operation.key)
        let data: Data
        do { data = try await base.perform(operation) }
        catch { FileHandle.standardError.write(Data(("Native API operation \(operation.kind): \(error)\n").utf8)); throw error }
        if lost.remove(operation.kind) != nil { throw URLError(.networkConnectionLost) }
        return data
    }
    func replayed(_ kind: String) -> Bool {
        let values = keys[kind] ?? []
        return values.count > 1 && Set(values).count < values.count
    }
    func reconciledWithoutReplay(_ kind: String) -> Bool { !lost.contains(kind) && keys[kind]?.count == 1 }
}

@main struct NativeImplementationAPITests {
    struct Fixture: Decodable { var url: String; var campId: String; var projectId: String }
    struct Driver: Decodable { var token: String }
    static func check(_ value: Bool, _ message: String) { precondition(value, message) }
    @MainActor static func until(_ label: String, _ condition: () async throws -> Bool) async throws {
        for _ in 0..<600 {
            if try await condition() { return }
            try await Task.sleep(for: .milliseconds(25))
        }
        throw CampError.message("Native API test timed out: " + label)
    }
    static func post<T: Decodable>(_ type: T.Type, _ api: CampHTTP, _ token: String, _ path: String, _ body: [String: Any], method: String = "POST") async throws -> T {
        let data: Data
        do { data = try await api.data(method, path: path, body: CampWire.body(body), key: UUID().uuidString, token: token) }
        catch { throw CampError.message("Native API fixture \(method) \(path): \(error)") }
        return try CampWire.decoder().decode(type, from: data)
    }
    @MainActor static func main() async throws {
        guard let directory = ProcessInfo.processInfo.environment["CAMP_NATIVE_API_FIXTURE"] else {
            print("SKIP: real native/API integration requires tools/test-native-api.sh"); return
        }
        let root = URL(fileURLWithPath: directory)
        check(directory.hasPrefix("/") && FileManager.default.fileExists(atPath: root.appendingPathComponent(".capybara-api-fixture").path), "Expected an explicit disposable API fixture")
        let fixture = try CampWire.decoder().decode(Fixture.self, from: Data(contentsOf: root.appendingPathComponent("fixture.json")))
        check(fixture.url.hasPrefix("http://127.0.0.1:"), "Native integration must use the loopback fixture")
        let api = CampHTTP(endpoint: try CampEndpoint(fixture.url, allowLoopback: true))
        let driver = try CampWire.decoder().decode(Driver.self, from: Data(contentsOf: root.appendingPathComponent("driver.json")))
        let creator = try await api.get(CloudActor.self, path: "/v1/me", token: driver.token)
        _ = try await api.data("POST", path: "/v1/auth/code", body: CampWire.body(["email": "k@example.com"]))
        let code = try String(contentsOf: root.appendingPathComponent("k-code.txt"), encoding: .utf8)
        let login: CloudLogin = try await post(CloudLogin.self, api, "", "/v1/auth/verify", ["email": "k@example.com", "code": code, "device_name": "Native integration executor"])
        check(creator.userId != login.actor.userId && creator.deviceId != login.actor.deviceId, "Two users/devices are required")
        let base = "/v1/camps/" + fixture.campId
        let creatorScope = CloudScope(origin: fixture.url, userId: creator.userId, campId: fixture.campId)
        let ownerScope = CloudScope(origin: fixture.url, userId: login.actor.userId, campId: fixture.campId)
        for provider in ["Codex", "Claude"] {
            FileHandle.standardError.write(Data(("Native API journey: " + provider + "\n").utf8))
            func source(_ actor: CloudActor, _ token: String, _ name: String) async throws -> CloudSession {
                let profile: CloudProfile = try await post(CloudProfile.self, api, token, "/v1/profiles", ["provider": provider, "local_id": PublishMessage.hash(Data(UUID().uuidString.utf8)), "label": name])
                let id = UUID().uuidString
                return try await post(CloudSession.self, api, token, base + "/sessions/" + id, ["id": id, "project_id": fixture.projectId, "profile_id": profile.id, "local_session_id": UUID().uuidString, "title": name, "model": "synthetic-model", "state": "idle", "stage": "Ready", "shared": true, "allow_requests": true], method: "PUT")
            }
            let planner = try await source(creator, driver.token, provider + " planner")
            let executor = try await source(login.actor, login.token, provider + " executor")
            let input = CloudPlanningInput(title: "Native API " + provider, projectId: fixture.projectId, brief: "Review the synthetic approval before implementing", shared: true,
                planners: [.init(sessionId: planner.id, model: "synthetic-planner", effort: "high", speed: "standard")], budgetPolicy: "calls", mode: .single)
            FileHandle.standardError.write(Data("Create planning ticket\n".utf8))
            let planBytes = try await api.data("POST", path: base + "/planning", body: CampWire.encoder().encode(input), key: UUID().uuidString, token: driver.token)
            var plan = try CampWire.decoder().decode(CloudPlanningResult.self, from: planBytes)
            let draft: CloudRequest? = try await post(CloudRequest?.self, api, driver.token, base + "/planning-requests/claim", ["session_ids": [planner.id]])
            guard let draft, let lease = draft.leaseToken else { throw CampCloudError.invalidResponse }
            _ = try await post(CloudRequest.self, api, driver.token, base + "/requests/\(draft.id)/actions", ["action": "start", "lease_token": lease])
            _ = try await post(CloudRequest.self, api, driver.token, base + "/requests/\(draft.id)/actions", ["action": "finish", "lease_token": lease, "outcome": "completed", "evidence": "Synthetic integration plan", "output": "Implement a synthetic approval and verify its stream before completion"])
            plan = try await api.get(CloudPlanningResult.self, path: base + "/planning/" + plan.id, token: driver.token)
            check(plan.version.state == "ready", "Planning did not finish")
            let creatorTransport = LostNativeAPIReply(.init(client: api, token: driver.token), losing: ["plan-execution-prepare", "plan-execution-dispatch"])
            let ownerTransport = LostNativeAPIReply(.init(client: api, token: login.token), losing: ["plan-execution-approve", "implementation-stream", "finish"])
            let creatorCache = try CampCache(directory: root.appendingPathComponent(provider + "-creator"))
            let ownerCache = try CampCache(directory: root.appendingPathComponent(provider + "-owner"))
            let creatorPlanning = CampPlanning(cache: creatorCache), ownerPlanning = CampPlanning(cache: ownerCache)
            creatorPlanning.attach(scope: creatorScope, actor: creator, transport: creatorTransport, sessions: [planner, executor])
            ownerPlanning.attach(scope: ownerScope, actor: login.actor, transport: ownerTransport, sessions: [planner, executor])
            try await until("attach") { creatorPlanning.ready && ownerPlanning.ready }
            FileHandle.standardError.write(Data("Prepare executor review\n".utf8))
            let review = SharedExecutionReview(planning: creatorPlanning)
            review.bind(plan); try await until("creator review") { !review.loading }
            await review.loadTarget(executor)
            let permissions = provider == "Codex" ? PermissionSettings(sandbox: "workspace-write", approval: "on-request") : PermissionSettings(claudeMode: "manual")
            try await review.prepare(source: executor, model: "synthetic-executor", tuning: ModelTuning(effort: "high", speed: "standard"), permissions: permissions)
            try await until("durable prepare") { !creatorPlanning.pending(plan.id) }
            review.refresh(); try await until("prepared approval") { !review.loading && review.approval != nil }
            var inspectedByCreator = false
            do { try await review.approve { _ in inspectedByCreator = true; return String(repeating: "a", count: 64) }; preconditionFailure("Creator approved teammate execution") } catch {}
            check(!inspectedByCreator, "Wrong owner inspected provider permissions")
            let ownerReview = SharedExecutionReview(planning: ownerPlanning)
            ownerReview.bind(plan); try await until("owner review") { !ownerReview.loading && ownerReview.approval?.state == "pending" }
            try await ownerReview.approve { value in
                check(value.ownerDeviceId == login.actor.deviceId && value.permissions == permissions.wireValues, "Approval target changed")
                return String(repeating: "a", count: 64)
            }
            try await until("durable approve") { !ownerPlanning.pending(plan.id) }
            review.refresh(); try await until("approved review") { !review.loading && review.approval?.state == "approved" }
            var dispatched: CloudRequest?
            creatorPlanning.implementationDispatched = { dispatched = $0 }
            try await review.dispatch()
            try await until("durable dispatch") { !creatorPlanning.pending(plan.id) && dispatched != nil }
            guard let work = dispatched else { throw CampCloudError.invalidResponse }
            FileHandle.standardError.write(Data("Run isolated executor\n".utf8))
            let local = CampSession(id: "synthetic-" + provider, sessionID: executor.localSessionId, provider: provider, sourceID: "fixture", sourceLabel: "Fixture", root: root.path, title: "Fixture", cwd: root.path, updated: 1, path: "", accountID: "", archived: false)
            let worker = CampLocalWorker(readFences: { [] }, cache: ownerCache, automatic: false, heartbeatInterval: 0)
            worker.nativeSession = { value in value.id == executor.id ? local : nil }; worker.available = { _ in true }
            worker.readHistory = { _ in preconditionFailure("Implementation loaded its account source transcript") }
            worker.inspectExecution = { value, selected in
                check(selected == permissions, "Worker changed approved permissions")
                return ExecutionContext(fingerprint: String(repeating: "a", count: 64), cwd: value.cwd, profile: value.root, provider: provider, permissionSummary: "Synthetic project")
            }
            var starts = 0
            worker.execute = { source, request, prompt, history, contexts, completion in
                check(source.id != local.id && source.sessionID != local.sessionID && history.isEmpty, "Implementation reused its anchor")
                let run = LocalConversation(session: source, history: [], prompt: prompt); run.completed = completion
                do {
                    try run.start(prompt: prompt, model: request.model, tuning: .init(effort: request.effort, speed: request.speed), contexts: contexts, mode: .implementation, executionApproved: true, newSession: (request.providerSessionId ?? "").isEmpty, executionContext: request.executionContext ?? "", permissions: request.permissionSettings, makeProcess: { fixtureProcess("ui-audit") })
                } catch { preconditionFailure(error.localizedDescription) }
                starts += 1; return run
            }
            worker.attach(scope: ownerScope, actor: login.actor, transport: ownerTransport, sessions: [executor])
            await worker.poll()
            let live = CloudLiveConversation()
            let credential = CloudCredential(origin: fixture.url, actor: creator, token: driver.token, expiresAt: .distantFuture)
            live.watchImplementation(work, scope: creatorScope, client: api, credential: credential)
            try await until("provider approval stream") { live.invalidate(executor.id); return !live.approvals.isEmpty }
            check(live.messages.contains { $0.text.contains("Latest synthetic streaming marker") }, "Provider text did not stream through HTTP")
            let running = try await api.get(CloudRequest.self, path: base + "/requests/" + work.id, token: driver.token)
            check(running.state == "started" && running.providerSessionId != executor.localSessionId && running.providerSessionId != nil, "Missing isolated provider identity")
            guard let approval = live.approvals.first else { throw CampCloudError.invalidResponse }
            FileHandle.standardError.write(Data(("Answer provider approval: " + approval.kind + "\n").utf8))
            guard let sequence = live.approvalSequence(approval.id) else { throw CampCloudError.invalidResponse }
            let control = try CampWire.encoder().encode(CloudControlEnvelope(id: UUID().uuidString.lowercased(), approvalSeq: sequence,
                control: .init(type: "approval", id: approval.id, allow: true, decision: "once", answers: [:])))
            do { _ = try await api.data("POST", path: base + "/requests/\(work.id)/controls", body: control, token: driver.token); preconditionFailure("Creator answered teammate provider approval") }
            catch CampCloudError.server(let status, _) { check([403,404].contains(status), "Unexpected approval denial") }
            _ = try await api.data("POST", path: base + "/requests/\(work.id)/controls", body: control, token: login.token)
            try await until("worker completion") {
                await worker.poll()
                let current = try await api.get(CloudRequest.self, path: base + "/requests/" + work.id, token: driver.token)
                let attempts = try await ownerCache.attempts(ownerScope)
                return current.state == "completed" && attempts.isEmpty
            }
            check(starts == 1, "Lost API reply repeated provider work")
            let preparedReplay = await creatorTransport.replayed("plan-execution-prepare"), dispatchedReplay = await creatorTransport.replayed("plan-execution-dispatch")
            let approvedReplay = await ownerTransport.replayed("plan-execution-approve"), streamedReplay = await ownerTransport.replayed("implementation-stream"), finishReconciled = await ownerTransport.reconciledWithoutReplay("finish")
            check(preparedReplay && dispatchedReplay, "Creator commands did not replay the same key")
            check(approvedReplay && streamedReplay, "Owner approval and stream did not replay")
            check(finishReconciled, "Lost finish reply was not reconciled through saved completion")
            let final = try await api.get(CloudPlanningResult.self, path: base + "/planning/" + plan.id, token: driver.token)
            check(final.implementation?.state == "completed" && final.implementation?.id == work.implementationId, "Board lost the implementation identity")
            check(final.implementation?.effort == "high" && final.implementation?.speed == (provider == "Codex" ? "ultrafast" : "fast"), "Board lost reported settings")
            let anchor = try await api.get(CloudSession.self, path: base + "/sessions/" + executor.id, token: driver.token)
            check(anchor.transcriptRevision == executor.transcriptRevision && anchor.revision == executor.revision, "Implementation changed its account source")
            live.stop(); worker.attach(scope: nil, actor: nil, transport: nil, sessions: [])
            creatorPlanning.attach(scope: nil, actor: nil, transport: nil, sessions: []); ownerPlanning.attach(scope: nil, actor: nil, transport: nil, sessions: [])
            print("PASS: " + provider + " native owner approval, isolated worker, actual HTTP stream, provider approval, replay and same-ticket completion")
        }
    }
}
