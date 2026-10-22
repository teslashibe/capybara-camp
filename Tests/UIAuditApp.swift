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
