import Foundation

@main struct CampProjectsTests {
    @MainActor static func until(_ condition: () -> Bool) async throws {
        for _ in 0..<300 { if condition() { return };try await Task.sleep(for:.milliseconds(10)) }
        preconditionFailure("Project store did not become ready")
    }
    @MainActor static func main() async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("camp-projects-"+UUID().uuidString)
        defer { try? FileManager.default.removeItem(at:root) }
        let store=CampStore(stateDirectory:root)
        try await until { store.projects.ready }
        func session(_ id:String,_ cwd:String)->CampSession { CampSession(id:id,sessionID:id,provider:"Codex",sourceID:"profile",sourceLabel:"Fixture",root:root.path,title:id,cwd:cwd,updated:1,path:"",accountID:"",archived:false) }
        let a=session("repo-one","/tmp/repository-one"),b=session("repo-two","/tmp/repository-two"),noRepo=session("no-repo","")
        store.sessions=[a,b]
        let planID=store.planning.create(planners:[LocalPlanner(session:noRepo,model:"fixture",tuning:ModelTuning())],mode:.single)!
        let plan=store.planning.ticket(planID)!.session
        precondition(plan.cwd.isEmpty)
        precondition(store.projectLabel(a)=="Inbox")
        let project=CampProject(id:UUID().uuidString,name:"Client A",repositories:["https://github.com/Owner/One.git","https://github.com/owner/two"])
        try await store.projects.save(project,expectedRevision:0)
        for s in [a,b,plan] { try await store.projects.assign(s.id,to:project.id) }
        store.projectFilter="local:"+project.id
        precondition(Set(store.filtered.map(\.id))==Set([a.id,b.id,planID]))
        store.conversations[a.id]=LocalConversation(session:a,history:[],prompt:"fixture")
        try await store.projects.assign(a.id,to:nil)
        precondition(store.conversations[a.id]?.session.cwd==a.cwd && store.stage(a) == .progress)
        precondition(store.filtered.count==2 && store.sessions.first(where:{$0.id==b.id})==b)
        var renamed=store.projects.byID[project.id]!
        renamed.name="Renamed client"
        try await store.projects.save(renamed,expectedRevision:renamed.version)
        precondition(store.projectLabel(b)=="Renamed client")
        do { try await store.projects.save(renamed,expectedRevision:1);preconditionFailure("Stale project overwrote a rename") } catch {}
        var archived=store.projects.byID[project.id]!
        archived.archived=true
        try await store.projects.save(archived,expectedRevision:archived.version)
        precondition(store.filtered.count==2,"Archiving removed history")
        do { try await store.projects.assign(a.id,to:project.id);preconditionFailure("Assigned an archived project") } catch {}
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        try await store.cloud.cache!.clear(scope)
        let restored=CampStore(stateDirectory:root)
        try await until { restored.projects.ready }
        precondition(restored.projects.assignments[b.id]==project.id && restored.projects.byID[project.id]?.isArchived==true,"Sign-out erased private projects")
        precondition(restored.projects.byID[project.id]?.repositoryURLs==["https://github.com/owner/one","https://github.com/owner/two"])
        for invalid in ["https://secret@github.com/a/b","http://github.com/a/b","https://github.com/a/b/tree/main","https://github.com/a/%62","https://github.com/a/b?token=secret"] {
            do { _=try ProjectRepositories.normalize([invalid]);preconditionFailure("Unsafe repository URL accepted") } catch {}
        }
        let old=try CampWire.decoder().decode(CampProject.self,from:Data("{\"id\":\"legacy\",\"name\":\"Existing\"}".utf8))
        precondition(old.version==1 && !old.isArchived && !old.isInbox)
        store.conversations.removeAll()
        print("PASS: client projects span repositories, preserve running sessions, survive restart/sign-out, and reject stale or invalid edits")
    }
}
