import Foundation

private actor LostHistoryRead: CampAccountTransport {
    let api: CampHTTP
    var lost=false
    init(_ api: CampHTTP) { self.api=api }
    func data(_ method: String, path: String, body: Data?, key: String?, token: String?) async throws -> Data {
        let data=try await api.data(method,path:path,body:body,key:key,token:token)
        if path.contains("/transcript?") && !lost { lost=true; throw URLError(.networkConnectionLost) }
        return data
    }
}

@main struct NativeTransferHistoryAPITests {
    struct Fixture: Decodable { var url: String; var campId: String }
    struct Driver: Decodable { var token: String }
    static func rejects(_ label: String, _ action: () async throws -> Void) async {
        do { try await action(); preconditionFailure(label) } catch {}
    }
    static func main() async throws {
        guard let directory=ProcessInfo.processInfo.environment["CAMP_NATIVE_API_FIXTURE"] else {
            print("SKIP: run CAMP_NATIVE_FIXTURE_HISTORY=1 tools/test-native-api.sh NativeTransferHistoryAPITests"); return
        }
        let root=URL(fileURLWithPath:directory)
        precondition(directory.hasPrefix("/") && FileManager.default.fileExists(atPath:root.appendingPathComponent(".capybara-api-fixture").path))
        let fixture=try CampWire.decoder().decode(Fixture.self,from:Data(contentsOf:root.appendingPathComponent("fixture.json")))
        precondition(fixture.url.hasPrefix("http://127.0.0.1:"))
        let driver=try CampWire.decoder().decode(Driver.self,from:Data(contentsOf:root.appendingPathComponent("driver.json")))
        let sessions=try CampWire.decoder().decode([CloudSession].self,from:Data(contentsOf:root.appendingPathComponent("history-sessions.json")))
        precondition(Set(sessions.map(\.provider))==Set(["Codex","Claude"]))
        let api=CampHTTP(endpoint:try CampEndpoint(fixture.url,allowLoopback:true))
        let actor=try await api.get(CloudActor.self,path:"/v1/me",token:driver.token)
        func login(_ email: String, name: String) async throws -> CloudLogin {
            _ = try await api.data("POST",path:"/v1/auth/code",body:CampWire.body(["email":email]))
            let code=try String(contentsOf:root.appendingPathComponent(name+"-code.txt"),encoding:.utf8)
            return try CampWire.decoder().decode(CloudLogin.self,from:await api.data("POST",path:"/v1/auth/verify",body:CampWire.body(["email":email,"code":code,"device_name":"History test Mac"])))
        }
        let second=try await login("owner@example.com",name:"owner")
        let teammate=try await login("k@example.com",name:"k")
        precondition(second.actor.userId==actor.userId && second.actor.deviceId != actor.deviceId && teammate.actor.userId != actor.userId)
        for session in sessions {
            let scope=CloudScope(origin:fixture.url,userId:actor.userId,campId:fixture.campId)
            let cache=try CampCache(directory:root.appendingPathComponent("history-"+session.provider))
            let epoch=try await cache.epoch(scope)
            let auth=CloudCredential(origin:fixture.url,actor:actor,token:driver.token,expiresAt:Date().addingTimeInterval(3600))
            let transport=LostHistoryRead(api)
            let first=try CampTransferHistory(cache:cache,scope:scope,credential:auth,transport:transport,epoch:epoch)
            try await first.begin(session:session)
            await rejects("Expected lost read") { try await first.advance() }
            try await first.advance()
            let renewed=CloudCredential(origin:fixture.url,actor:second.actor,token:second.token,expiresAt:Date().addingTimeInterval(3600))
            let resumed=try CampTransferHistory(cache:cache,scope:scope,credential:renewed,transport:api,epoch:epoch)
            for _ in 0..<10 {
                if try await resumed.current()?.complete == true { break }
                try await resumed.advance()
            }
            let archive=try await resumed.archive()!
            precondition(archive.origin==fixture.url && archive.session.id==session.id && archive.conversations.count==2)
            precondition(archive.conversations[0].messages.count==260 && archive.conversations[1].messages.count==250)
            precondition(archive.conversations[0].messages.first?.id=="parent-0" && archive.conversations[0].messages.last?.id=="parent-259")
            precondition(archive.conversations[1].messages.first?.text=="Original child message 0" && archive.conversations[1].messages.last?.id=="child-249")
            precondition(archive.conversations[1].messages.allSatisfy{$0.model=="recorded-child" && $0.effort=="high" && $0.speed=="fast"})
            let base="/v1/camps/\(fixture.campId)/sessions/\(session.id)"
            await rejects("Teammate read unshared history") { _ = try await api.data("GET",path:base+"/transcript?limit=120",token:teammate.token) }
            // A permission revision changes even when the pinned text is unchanged.
            _ = try await api.data("PUT",path:base,body:CampWire.body(["id":session.id,"project_id":session.projectId,"profile_id":session.profileId,"local_session_id":session.localSessionId,"title":session.title,"model":session.model,"state":session.state,"stage":session.stage,"shared":true,"allow_requests":false,"external_owner":false,"expected_revision":session.revision]),key:UUID().uuidString,token:driver.token)
            await rejects("Cached archive survived changed permissions") { _ = try await resumed.archive() }
            try await cache.clear(scope)
            print("Real API history download passed for \(session.provider): 260 parent and 250 child messages, restart and changed permissions")
        }
    }
}
