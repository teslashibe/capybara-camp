import Foundation

@main struct BridgeIntegrationTests {
    @MainActor static func main() async throws {
        guard let path = ProcessInfo.processInfo.environment["CAPY_SUPPORT"], path.contains("/.camp-tests."), path.hasSuffix("/bridge-support") else {
            preconditionFailure("Bridge tests require an isolated support directory")
        }
        let root = URL(fileURLWithPath:path), id = UUID().uuidString.lowercased()
        let key = String(repeating:"a",count:64)
        let file = root.appendingPathComponent("run-"+key+".lock")
        try Data(("capybara-transfer-v1:"+id+"\n").utf8).write(to:file)
        try FileManager.default.setAttributes([.posixPermissions:0o600],ofItemAtPath:file.path)

        let helper = try Bridge.process("transfer-fences")
        precondition(helper.executableURL == Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local"))
        let coordinator = CampLocalHandoff(cache:nil)
        let values = try await coordinator.readFences()
        precondition(values == [LocalTransferFence(key:key,handoffID:id)],"The app did not reach its bundled recovery helper")
        try await coordinator.resolveRecoveredFence(values[0])
        let remaining = try Data(contentsOf:file)
        precondition(remaining.isEmpty,"The bundled helper did not resolve its marker")
        let empty = try JSONDecoder().decode([LocalTransferFence].self,from:await Bridge.call("transfer-fences"))
        precondition(empty.isEmpty)
        let repository=root.appendingPathComponent("repository")
        try FileManager.default.createDirectory(at:repository,withIntermediateDirectories:true)
        func git(_ arguments:[String]) throws {
            let process=Process();process.executableURL=URL(fileURLWithPath:"/usr/bin/git")
            process.arguments=["-C",repository.path]+arguments;process.standardOutput=FileHandle.nullDevice;process.standardError=FileHandle.nullDevice
            try process.run();process.waitUntilExit();precondition(process.terminationStatus==0)
        }
        try git(["init","-q"])
        try Data("before\n".utf8).write(to:repository.appendingPathComponent("file.txt"))
        try git(["add","file.txt"]);try git(["-c","user.name=Fixture","-c","user.email=fixture@example.com","commit","-qm","Fixture"])
        try Data("after\n".utf8).write(to:repository.appendingPathComponent("file.txt"))
        let session=CampSession(id:"fixture",sessionID:UUID().uuidString,provider:"Codex",sourceID:"fixture",sourceLabel:"Fixture",root:root.path,title:"Fixture",cwd:repository.path,updated:1,path:"",accountID:"",archived:false)
        let request=try CampWire.body(["session":JSONSerialization.jsonObject(with:JSONEncoder().encode(session)),"paths":["file.txt"]])
        let changes=try JSONDecoder().decode(TransferChanges.self,from:await Bridge.call("transfer-changes",input:request))
        precondition(changes.files.count==1 && changes.files[0].path=="file.txt")
        let capture=try JSONDecoder().decode(TransferCapture.self,from:await Bridge.call("transfer-capture",input:request))
        precondition(capture.valid && capture.files.count==1 && capture.payload.map(PublishMessage.hash)==capture.payloadSha256,"The bundled capture helper was not used")
        let profile=root.appendingPathComponent("claude"), parentPath=profile.appendingPathComponent("projects/fixture/parent.jsonl")
        let childPath=profile.appendingPathComponent("projects/fixture/parent/subagents/agent-one.jsonl")
        try FileManager.default.createDirectory(at:childPath.deletingLastPathComponent(),withIntermediateDirectories:true)
        try Data().write(to:parentPath)
        try Data("{\"type\":\"assistant\",\"uuid\":\"child-message\",\"isSidechain\":true,\"message\":{\"model\":\"fixture-model\",\"content\":\"Child history\"}}\n".utf8).write(to:childPath)
        let parent=CampSession(id:"parent",sessionID:UUID().uuidString,provider:"Claude",sourceID:"fixture",sourceLabel:"Fixture",root:profile.path,title:"Parent",cwd:repository.path,updated:1,path:parentPath.path,accountID:"",archived:false)
        let encodedParent=try JSONSerialization.jsonObject(with:JSONEncoder().encode(parent))
        struct Catalog:Decodable {struct Source:Decodable {var id:String;var revision:String};var sources:[Source]}
        let catalog=try JSONDecoder().decode(Catalog.self,from:await Bridge.call("child-history-catalog",input:CampWire.body(["session":encodedParent])))
        precondition(catalog.sources.count==1)
        struct ChildPage:Decodable {var page:TranscriptExport}
        let childPage=try JSONDecoder().decode(ChildPage.self,from:await Bridge.call("child-export-page",input:CampWire.body(["session":encodedParent,"sourceID":catalog.sources[0].id,"revision":catalog.sources[0].revision,"after":-1,"limit":200])))
        precondition(childPage.page.messages.count==1 && childPage.page.messages[0].text=="Child history")
        do { _ = try Bridge.process("unrecognized-action"); preconditionFailure("The bridge accepted an unsupported command") }
        catch let error as CampError { precondition(error.localizedDescription == "Unsupported local session action") }
        print("PASS: app-bundled Go recovery, change review, exact capture, pinned child export and action validation")
    }
}
