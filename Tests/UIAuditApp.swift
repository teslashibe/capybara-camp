import AppKit
import SwiftUI
@testable import CampTestSupport

// Build only through tools/ui-audit.sh with private synthetic profile folders.
// The normal UI and state machine run against the test transport, never a CLI.
@main struct UIAuditApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var store: CampStore

    init() {
        let environment = ProcessInfo.processInfo.environment
        guard environment["CAMP_UI_AUDIT"] == "1",
              let profiles = environment["CAMP_DEV_PROFILE_ROOT"], profiles.hasPrefix("/"),
              let support = environment["CAPY_SUPPORT"], support.hasPrefix("/"),
              profiles != NSHomeDirectory(), support != NSHomeDirectory(),
              FileManager.default.fileExists(atPath: profiles + "/.capybara-ui-fixtures"),
              FileManager.default.fileExists(atPath: support + "/.capybara-ui-fixtures") else {
            fatalError("Launch the UI audit with explicit synthetic profile and support folders")
        }
        // Check the bundled helper before CampStore can load or scan anything.
        // A regular helper must never silently discover the user's profiles.
        let helper = Process(), output = Pipe()
        helper.executableURL = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-local")
        helper.arguments = ["audit-config"]
        helper.standardOutput = output
        do { try helper.run() } catch { fatalError("Could not validate the isolated audit helper") }
        let data = output.fileHandleForReading.readDataToEndOfFile()
        helper.waitUntilExit()
        let configuration = try? JSONDecoder().decode([String: String].self, from: data)
        guard helper.terminationStatus == 0,
              configuration?["fixtureRoot"] == URL(fileURLWithPath: profiles).resolvingSymlinksInPath().path,
              configuration?["support"] == support else {
            fatalError("The bundled audit helper is not using the selected fixtures")
        }
        _store = StateObject(wrappedValue: CampStore(stateDirectory: URL(fileURLWithPath: support), conversationProcess: {
            let process = Process()
            process.executableURL = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/camp-test-provider")
            process.arguments = ["ui-audit"]
            return process
        }))
    }
    var body: some Scene {
        Window("Capybara Camp · Synthetic UI audit", id: "camp") {
            RootView(store: store).preferredColorScheme(.light)
        }.windowStyle(.hiddenTitleBar).defaultSize(width: 1370, height: 860)
    }
}
