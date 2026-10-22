import Foundation

actor ChildReadGate {
    var waiter:CheckedContinuation<CloudChildHistoryList,Never>?
    func read()async->CloudChildHistoryList {await withCheckedContinuation{waiter=$0}}
    func waiting()->Bool {waiter != nil}
    func finish(_ value:CloudChildHistoryList){waiter?.resume(returning:value);waiter=nil}
}

@main struct CampChildHistoryTests {
    @MainActor static func until(_ body:()async->Bool)async {
        for _ in 0..<300 {if await body(){return};try? await Task.sleep(for:.milliseconds(5))}
        preconditionFailure("Child reader timed out")
    }
    static func session(_ scope:CloudScope)->CloudSession {
        CloudSession(id:UUID().uuidString,campId:scope.campId,projectId:UUID().uuidString,profileId:UUID().uuidString,provider:"Claude",profileLabel:"Fixture",ownerUserId:scope.userId,ownerDeviceId:UUID().uuidString,localSessionId:UUID().uuidString,title:"Parent",model:"fixture",state:"idle",stage:"Ready",shared:true,allowRequests:false,externalOwner:false,revision:1,transcriptRevision:2,permissionRevision:1,updatedAt:Date())
    }
    static func list(_ parent:CloudSession)->CloudChildHistoryList {
        CloudChildHistoryList(parentRevision:2,permissionRevision:1,children:[CloudChildHistory(id:UUID().uuidString,parentId:parent.id,sourceKey:String(repeating:"a",count:64),sourceRevision:String(repeating:"b",count:64),name:"Review helper",phase:"review",workflow:"Feature",state:"done",model:"recorded-model",complete:true,removed:false,revision:2,transcriptRevision:1)])
    }
    @MainActor static func main()async throws {
        let root=FileManager.default.temporaryDirectory.appendingPathComponent("camp-child-read-"+UUID().uuidString)
        defer{try? FileManager.default.removeItem(at:root)}
        let store=CampStore(stateDirectory:root)
        let scope=CloudScope(origin:"https://fixture.example",userId:UUID().uuidString,campId:UUID().uuidString)
        let parent=session(scope),other=session(scope),first=list(parent),second=list(other)
        precondition(first.valid(parent:parent.id) && !first.valid(parent:other.id))
        precondition(first.children[0].childSession.model=="recorded-model")
        store.applyCloud([parent,other],projects:[])
        let local=store.sessions.first{$0.id=="camp:"+parent.id}!,otherLocal=store.sessions.first{$0.id=="camp:"+other.id}!
        store.selectedID=local.id;store.cloudChildReader={_ in first}
        store.loadChildren(local);await until{!store.childrenLoading}
        precondition(store.children.count==1 && store.children[0].id==first.children[0].id)
        store.selectedChildID=first.children[0].id
        let gate=ChildReadGate();store.cloudChildReader={_ in await gate.read()}
        store.loadChildren(local);await until{await gate.waiting()}
        precondition(store.children.count==1 && store.selectedChildID==first.children[0].id,"Refresh dropped the selected child")
        await gate.finish(first);await until{!store.childrenLoading}
        let race=ChildReadGate()
        store.cloudChildReader={value in if value.id==parent.id{return await race.read()};return second}
        store.loadChildren(local);await until{await race.waiting()}
        store.selectedID=otherLocal.id;store.loadChildren(otherLocal);await until{!store.childrenLoading}
        await race.finish(first);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.first?.id==second.children.first?.id,"Late response replaced another parent's children")
        let navigation=ChildReadGate();store.cloudChildReader={_ in await navigation.read()}
        store.loadChildren(otherLocal);await until{await navigation.waiting()}
        let plan=CampSession(id:"plan:"+UUID().uuidString,sessionID:UUID().uuidString,provider:"Planning",sourceID:"fixture",sourceLabel:"Fixture",root:"",title:"Plan",cwd:"",updated:1,path:"",accountID:"",archived:false)
        store.open(plan)
        await navigation.finish(second);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.isEmpty && store.subagentsParent==nil,"Late child response returned after opening a plan")
        let revoked=ChildReadGate();store.cloudChildReader={_ in await revoked.read()}
        store.selectedID=otherLocal.id
        store.loadChildren(otherLocal);await until{await revoked.waiting()}
        store.applyCloud([],projects:[])
        await revoked.finish(second);try await Task.sleep(for:.milliseconds(20))
        precondition(store.children.isEmpty && store.selectedChildID==nil && store.selectedID==nil,"Revoked children returned after a delayed response")
        print("PASS: cloud child projection, preserved selection, parent-switch fencing and revoked-response rejection")
    }
}
