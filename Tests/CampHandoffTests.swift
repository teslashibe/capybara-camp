import Foundation

@MainActor final class SourceGuardFixture: LocalSourceGuard {
    var active = true
    var verifies = 0
    var failure = false
    var fenced = false
    var duringVerify: (() async throws -> Void)?
    let receipt: SourceVerification
    init(_ receipt: SourceVerification) { self.receipt = receipt }
    func verify() async throws -> SourceVerification {
        verifies += 1
        if let duringVerify { try await duringVerify() }
        if failure { active = false; throw CampError.message("Changed source") }
        return receipt
    }
    func stop() { active = false }
    func prepareCommit() async throws -> SourceVerification {
        let value = try await verify(); fenced = true; return value
    }
}

actor HandoffFixture: CampPublishingTransport {
    var handoff: CloudHandoff
    var source: CloudTransferSource
    var payload = Data("{}".utf8)
    var loseClaim = false
    var loseAck = false
    var loseFinish = false
    var loseSession = false
    var corrupt = false
    var claims: [String] = []
    var acknowledgements = 0
    var completions = 0
    var skipFinish = false
    init(handoff: CloudHandoff, source: CloudTransferSource) { self.handoff = handoff; self.source = source }
    func configure(claim: Bool = false, ack: Bool = false, corrupt: Bool = false) { loseClaim = claim; loseAck = ack; self.corrupt = corrupt }
    func finish() { handoff.state = "released" }
    func requestCommit(loseFinish: Bool = false, loseSession: Bool = false, skipFinish: Bool = false) {
        precondition(handoff.state == "acknowledged")
        handoff.commitNonce = UUID().uuidString
        self.loseFinish = loseFinish; self.loseSession = loseSession
        self.skipFinish = skipFinish
    }
    func completionCount() -> Int { completions }
    func stats() -> ([String],Int,String) { (claims,acknowledgements,handoff.state) }
    func perform(_ operation: CampPublication) async throws -> Data {
        if operation.path.hasSuffix("/next") { return ["pending","claimed"].contains(handoff.state) ? try CampWire.body(["id":handoff.id]) : Data() }
        if operation.method == "GET" && operation.path.contains("/handoffs/") { return try CampWire.encoder().encode(handoff) }
        if operation.method == "GET" && operation.path.hasSuffix("/sessions/"+source.session.id) {
            if loseSession { loseSession = false; throw URLError(.networkConnectionLost) }
            return try CampWire.encoder().encode(source.session)
        }
        guard ["pending","claimed","acknowledged","committed"].contains(handoff.state) else { throw CampCloudError.server(409,"invalid_transition") }
        let body = try JSONSerialization.jsonObject(with:operation.body) as! [String:Any]
        let claim = body["claim_id"] as! String
        if operation.path.hasSuffix("/claim") {
            claims.append(claim)
            if let old = handoff.claimId { precondition(old == claim,"Claim changed during retry") }
            handoff.claimId = claim
            if handoff.state == "pending" { handoff.state = "claimed" }
            if loseClaim { loseClaim = false; throw URLError(.networkConnectionLost) }
        } else {
            precondition(claim == handoff.claimId)
            if operation.path.hasSuffix("/source") { source.handoff = handoff; return try CampWire.encoder().encode(source) }
            if operation.path.contains("/chunks/") {
                return try CampWire.body(["data":payload.base64EncodedString(),"sha256":corrupt ? String(repeating:"0",count:64) : PublishMessage.hash(payload)])
            }
            if operation.path.hasSuffix("/ack") {
                precondition(body["stopped"] as? Bool == true && body["payload_sha256"] as? String == source.transfer.payloadSha256 && body["review_sha256"] as? String == source.transfer.reviewSha256)
                acknowledgements += 1; handoff.state = "acknowledged"
                if loseAck { loseAck = false; throw URLError(.networkConnectionLost) }
            } else if operation.path.hasSuffix("/finish") {
                if skipFinish { throw URLError(.networkConnectionLost) }
                precondition(body["commit_nonce"] as? String == handoff.commitNonce && handoff.state == "acknowledged")
                completions += 1; handoff.state = "committed"; handoff.executionEpoch = handoff.sourceEpoch+1
                source.session.executionEpoch = handoff.executionEpoch!
                source.session.executionLocation = "cloud"; source.session.executionWorkspaceId = handoff.workspaceId
                if loseFinish { loseFinish = false; throw URLError(.networkConnectionLost) }
            } else if operation.path.hasSuffix("/reject") { handoff.state = "invalidated" }
            else { preconditionFailure("Unexpected handoff call") }
        }
        return try CampWire.encoder().encode(handoff)
    }
}

@main struct CampHandoffTests {
    static func check(_ value: Bool, _ message: String = "Handoff check failed") { precondition(value,message) }
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("camp-handoff-"+UUID().uuidString)
        try FileManager.default.createDirectory(at:root,withIntermediateDirectories:true)
        defer { try? FileManager.default.removeItem(at:root) }
        let scope = CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let actor = CloudActor(userId:scope.userId,deviceId:UUID().uuidString,email:"fixture@example.com",name:"Fixture")
        let local = CampSession(id:"fixture",sessionID:UUID().uuidString,provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:root.path,title:"Fixture",cwd:root.path,updated:1,path:"",accountID:"",archived:false)
        let session = CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:local.provider,profileLabel:"Fixture",ownerUserId:actor.userId,ownerDeviceId:actor.deviceId,localSessionId:local.sessionID,title:"Fixture",model:"fixture",state:"idle",stage:"Ready",shared:false,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:1,permissionRevision:1,updatedAt:Date(),executionLocation:"local",executionEpoch:1)
        let h = CloudHandoff(id:UUID().uuidString,transferId:UUID().uuidString,campId:scope.campId,sessionId:session.id,ownerUserId:actor.userId,sourceDeviceId:actor.deviceId,sourceLocation:"local",sourceEpoch:1,workspaceId:UUID().uuidString,workspaceRevision:1,state:"pending",revision:1,expiresAt:Date().addingTimeInterval(60))
        let payloadHash = PublishMessage.hash(Data("{}".utf8)), reviewHash = String(repeating:"a",count:64)
        let source = CloudTransferSource(handoff:h,transfer:.init(id:h.transferId,campId:scope.campId,sessionId:session.id,ownerUserId:actor.userId,sourceLocation:"local",sourceEpoch:1,sourceRevision:1,sourceTranscriptRevision:1,sourcePermissionRevision:1,payloadSha256:payloadHash,reviewSha256:reviewHash,payloadBytes:2,uploadedBytes:2,chunkBytes:256 << 10,state:"ready"),session:session)
        let receipt = SourceVerification(payloadSHA256:payloadHash,reviewSHA256:reviewHash)
        let cache = try CampCache(directory:root.appendingPathComponent("cache"))
        let api = HandoffFixture(handoff:h,source:source)
        await api.configure(claim:true,ack:true)
        var guards: [SourceGuardFixture] = []
        func setup() -> CampLocalHandoff {
            let worker = CampLocalHandoff(cache:cache)
            worker.nativeSession = { _ in local }; worker.available = { _ in true }
            worker.makeGuard = { _,_,_,_ in let g = SourceGuardFixture(receipt); guards.append(g); return g }
            worker.attach(scope:scope,actor:actor,transport:api)
            return worker
        }
        let worker = setup()
        do { _ = try await worker.poll(); preconditionFailure("Lost claim should retain journal") } catch {}
        precondition(guards.isEmpty)
        do { _ = try await worker.poll(); preconditionFailure("Lost ack should retain source guard") } catch {}
        precondition(guards.count == 1 && guards[0].active)
        check(try await worker.poll())
        let firstStats = await api.stats()
        precondition(Set(firstStats.0).count == 1 && firstStats.1 == 1 && firstStats.2 == "acknowledged")
        precondition(guards[0].verifies == 2,"Source was not rechecked after lost acknowledgment")
        worker.attach(scope:nil,actor:nil,transport:nil)
        precondition(!guards[0].active,"Disconnect retained source lock")
        let restarted = setup()
        check(try await restarted.poll())
        precondition(guards.count == 2 && guards[1].verifies == 1,"Restart did not verify source again")
        await api.finish()
        do { _ = try await restarted.poll(); preconditionFailure("Released source was accepted") } catch {}
        precondition(!guards[1].active)
        check(try await restarted.poll() == false)
        restarted.attach(scope:nil,actor:nil,transport:nil)

        let badAPI = HandoffFixture(handoff:h,source:source)
        await badAPI.configure(corrupt:true)
        let bad = CampLocalHandoff(cache:try CampCache(directory:root.appendingPathComponent("bad")))
        bad.nativeSession = { _ in local }; bad.available = { _ in true }
        bad.makeGuard = { _,_,_,_ in preconditionFailure("Corrupt payload reached local files") }
        bad.attach(scope:scope,actor:actor,transport:badAPI)
        do { _ = try await bad.poll(); preconditionFailure("Corrupt payload accepted") } catch {}
        check(await badAPI.stats().1 == 0)
        bad.attach(scope:nil,actor:nil,transport:nil)

        let changedAPI = HandoffFixture(handoff:h,source:source)
        let changed = CampLocalHandoff(cache:try CampCache(directory:root.appendingPathComponent("changed")))
        changed.nativeSession = { _ in local }; changed.available = { _ in true }
        changed.makeGuard = { _,_,_,_ in let g = SourceGuardFixture(receipt); g.failure = true; return g }
        changed.attach(scope:scope,actor:actor,transport:changedAPI)
        do { _ = try await changed.poll(); preconditionFailure("Changed source acknowledged") } catch {}
        let changedStats = await changedAPI.stats()
        precondition(changedStats.1 == 0 && changedStats.2 == "invalidated")
        changed.attach(scope:nil,actor:nil,transport:nil)
        let revokedCache = try CampCache(directory:root.appendingPathComponent("revoked"))
        let revokedAPI = HandoffFixture(handoff:h,source:source)
        let revoked = CampLocalHandoff(cache:revokedCache), revokedGuard = SourceGuardFixture(receipt)
        revoked.nativeSession = { _ in local }; revoked.available = { _ in true }
        revoked.makeGuard = { _,_,_,_ in revokedGuard }
        revoked.attach(scope:scope,actor:actor,transport:revokedAPI)
        check(try await revoked.poll())
        try await revokedCache.clear(scope)
        do { _ = try await revoked.poll(); preconditionFailure("Cleared Camp retained source authority") } catch is CancellationError {}
        check(!revokedGuard.active,"Cleared Camp retained source lock")
        check(await revokedAPI.stats().1 == 1,"Cleared Camp sent another acknowledgment")
        revoked.attach(scope:nil,actor:nil,transport:nil)
        let interruptedCache = try CampCache(directory:root.appendingPathComponent("interrupted"))
        let interruptedAPI = HandoffFixture(handoff:h,source:source)
        let interrupted = CampLocalHandoff(cache:interruptedCache), interruptedGuard = SourceGuardFixture(receipt)
        interruptedGuard.duringVerify = { try await interruptedCache.clear(scope) }
        interrupted.nativeSession = { _ in local }; interrupted.available = { _ in true }
        interrupted.makeGuard = { _,_,_,_ in interruptedGuard }
        interrupted.attach(scope:scope,actor:actor,transport:interruptedAPI)
        do { _ = try await interrupted.poll(); preconditionFailure("Revoked source sent a verification") } catch is CancellationError {}
        let interruptedStats = await interruptedAPI.stats()
        check(!interruptedGuard.active && interruptedStats.1 == 0,"Revoked source sent an acknowledgement")
        interrupted.attach(scope:nil,actor:nil,transport:nil)
        let commitCache = try CampCache(directory:root.appendingPathComponent("commit"))
        let commitAPI = HandoffFixture(handoff:h,source:source), commit = CampLocalHandoff(cache:commitCache)
        let commitGuard = SourceGuardFixture(receipt)
        commit.resolveFence = { _,_ in }; commit.nativeSession = { _ in local }; commit.available = { _ in true }; commit.makeGuard = { _,_,_,_ in commitGuard }
        var publications = 0, publicationFails = true
        commit.acceptSession = { value, actualScope, epoch in
            precondition(commitGuard.active,"Source unlocked before routing changed")
            precondition(value.executionLocation == "cloud" && value.executionWorkspaceId == h.workspaceId && value.executionEpoch == 2 && actualScope == scope)
            if publicationFails { publicationFails = false; throw CampCloudError.cacheUnavailable }
            try await commitCache.ingest(scope:actualScope,session:value,expectedEpoch:epoch)
            publications += 1
        }
        commit.attach(scope:scope,actor:actor,transport:commitAPI)
        check(try await commit.poll())
        await commitAPI.requestCommit(loseFinish:true,loseSession:true)
        do { _ = try await commit.poll(); preconditionFailure("Lost completion response should retain guard") } catch {}
        check(commitGuard.active && commitGuard.verifies == 2 && commitGuard.fenced,"Commit did not fence and recheck source")
        do { _ = try await commit.poll(); preconditionFailure("Lost session read should retain guard") } catch {}
        check(commitGuard.active)
        do { _ = try await commit.poll(); preconditionFailure("Failed routing update should retain guard") } catch {}
        check(commitGuard.active)
        check(try await commit.poll())
        check(!commitGuard.active && publications == 1,"Successful routing did not release source")
        check(await commitAPI.completionCount() == 1,"Lost receipt repeated commit")
        check(try await commit.poll() == false)
        commit.attach(scope:nil,actor:nil,transport:nil)

        let staleAPI = HandoffFixture(handoff:h,source:source), stale = CampLocalHandoff(cache:try CampCache(directory:root.appendingPathComponent("stale")))
        let staleGuard = SourceGuardFixture(receipt)
        stale.resolveFence = { _,_ in }; stale.nativeSession = { _ in local }; stale.available = { _ in true }; stale.makeGuard = { _,_,_,_ in staleGuard }
        stale.attach(scope:scope,actor:actor,transport:staleAPI)
        check(try await stale.poll())
        await staleAPI.requestCommit(); staleGuard.failure = true
        do { _ = try await stale.poll(); preconditionFailure("Changed source committed") } catch {}
        check(await staleAPI.completionCount() == 0 && !staleGuard.active)
        stale.attach(scope:nil,actor:nil,transport:nil)

        let cancelledCache = try CampCache(directory:root.appendingPathComponent("cancelled-commit"))
        let cancelledAPI = HandoffFixture(handoff:h,source:source), cancelled = CampLocalHandoff(cache:cancelledCache)
        let cancelledGuard = SourceGuardFixture(receipt)
        cancelled.nativeSession = { _ in local }; cancelled.available = { _ in true }
        cancelled.makeGuard = { _,_,_,_ in cancelledGuard }
        cancelled.attach(scope:scope,actor:actor,transport:cancelledAPI)
        check(try await cancelled.poll())
        await cancelledAPI.requestCommit(skipFinish:true)
        do { _ = try await cancelled.poll(); preconditionFailure("Unconfirmed commit accepted") } catch {}
        check(cancelledGuard.fenced,"Commit sent before persisting fence")
        cancelled.attach(scope:nil,actor:nil,transport:nil)
        await cancelledAPI.finish()
        let cancelledRestart = CampLocalHandoff(cache:cancelledCache)
        var resolutions = 0
        cancelledRestart.resolveFence = { resolved, id in
            check(resolved.id == local.id && id == h.id)
            resolutions += 1
        }
        cancelledRestart.attach(scope:scope,actor:actor,transport:cancelledAPI)
        check(try await cancelledRestart.poll())
        check(resolutions == 1,"Cancelled fence was not reconciled after restart")
        check(try await cancelledRestart.poll() == false)
        cancelledRestart.attach(scope:nil,actor:nil,transport:nil)
        try await cache.clear(scope)
        check(try await cache.metadata("source-handoff:"+scope.key) == nil,"Sign out retained source claim")
        try await realSourceGuard(root:root)
        print("PASS: native source handoff recovery, retained locks, hash verification, rejection and scope erasure")
    }

    @MainActor static func realSourceGuard(root: URL) async throws {
        let repository = root.appendingPathComponent("repository"), profile = root.appendingPathComponent("profile")
        try FileManager.default.createDirectory(at:repository,withIntermediateDirectories:true)
        try FileManager.default.createDirectory(at:profile,withIntermediateDirectories:true)
        func git(_ arguments: [String]) throws -> String {
            let p = Process(), out = Pipe()
            p.executableURL = URL(fileURLWithPath:"/usr/bin/git"); p.arguments = ["-c","commit.gpgSign=false","-c","core.hooksPath=/dev/null"]+arguments
            p.currentDirectoryURL = repository; p.standardOutput = out; p.standardError = FileHandle.nullDevice
            try p.run(); let data = out.fileHandleForReading.readDataToEndOfFile(); p.waitUntilExit()
            precondition(p.terminationStatus == 0,"Git fixture failed")
            return String(decoding:data,as:UTF8.self).trimmingCharacters(in:.whitespacesAndNewlines)
        }
        _ = try git(["init","-q"]); _ = try git(["config","user.name","Fixture"]); _ = try git(["config","user.email","fixture@example.com"])
        let before = Data("before\n".utf8), after = Data("after\n".utf8), file = repository.appendingPathComponent("file.txt")
        try before.write(to:file); _ = try git(["add","file.txt"]); _ = try git(["commit","-qm","Fixture"])
        let head = try git(["rev-parse","HEAD"]), branch = try git(["rev-parse","--abbrev-ref","HEAD"])
        try after.write(to:file)
        let payload = try CampWire.body(["version":1,"base_commit":head,"branch":branch,"files":[["path":"file.txt","before_sha256":PublishMessage.hash(before),"sha256":PublishMessage.hash(after),"data":after.base64EncodedString()]],"excluded":[]])
        let local = CampSession(id:"real-guard",sessionID:"native-fixture",provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:profile.path,title:"Fixture",cwd:repository.path,updated:1,path:"",accountID:"",archived:false)
        let handoffID = UUID().uuidString.lowercased()
        func guardProcess() -> LocalTransferHold {
            LocalTransferHold(session:local,payload:payload,deadline:Date().addingTimeInterval(15),handoffID:handoffID) {
                let p = Process(); p.executableURL = URL(fileURLWithPath:FileManager.default.currentDirectoryPath).appendingPathComponent("build/camp-local")
                p.arguments = ["transfer-source"]
                p.environment = ProcessInfo.processInfo.environment.merging(["CAPY_SUPPORT":root.appendingPathComponent("support").path]) { _,new in new }
                return p
            }
        }
        let beforeProcesses = Bridge.activeProcesses.count
        let first = guardProcess(), receipt = try await first.verify()
        check(receipt.payloadSHA256 == PublishMessage.hash(payload) && first.active)
        check(try await first.verify() == receipt)
        check(try await first.prepareCommit() == receipt,"Real helper did not persist the commit fence")
        let concurrent = guardProcess()
        do { _ = try await concurrent.verify(); preconditionFailure("Concurrent source guard acquired lock") } catch {}
        concurrent.stop()
        try Data("changed after review\n".utf8).write(to:file)
        do { _ = try await first.verify(); preconditionFailure("Actual changed source was acknowledged") } catch {}
        first.stop()
        for _ in 0..<200 {
            if Bridge.activeProcesses.count == beforeProcesses { break }
            try await Task.sleep(for:.milliseconds(10))
        }
        check(Bridge.activeProcesses.count == beforeProcesses,"Source helper process leaked")
    }
}
