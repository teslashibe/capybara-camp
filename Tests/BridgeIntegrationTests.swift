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
        do { _ = try Bridge.process("unrecognized-action"); preconditionFailure("The bridge accepted an unsupported command") }
        catch let error as CampError { precondition(error.localizedDescription == "Unsupported local session action") }
        print("PASS: app-bundled Go recovery commands, isolated marker inventory, resolution and action validation")
    }
}
