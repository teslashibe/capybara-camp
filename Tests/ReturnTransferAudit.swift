import SwiftUI
import AppKit

@MainActor final class ReturnTransferAuditState: ObservableObject {
    @Published var fixture: ReturnScreenFixture?
    @Published var transfer: CampReturnTransfer?
    @Published var error = ""
    let store: CampStore
    let root: URL
    init() {
        let env = ProcessInfo.processInfo.environment
        guard let path = env["CAMP_RETURN_AUDIT"], path.contains("/.return-ui."),
              env["CAPY_SUPPORT"] == path + "/state",
              FileManager.default.fileExists(atPath:path + "/.capybara-ui-fixtures") else { fatalError("Missing isolated audit directory") }
        root = URL(fileURLWithPath:path)
        store = CampStore(stateDirectory:root.appendingPathComponent("state"))
    }
    func load() async {
        do {
            let value = try await ReturnScreenFixture(directory:root.appendingPathComponent(UUID().uuidString),provider:"Claude")
            store.sources = [value.source,CampSource(id:"fixture-team",provider:"Claude",root:"/fixture/team",label:"Team test account",count:0)]
            fixture = value; transfer = value.screen(automatic:true)
        } catch { self.error = error.localizedDescription }
    }
}

@main struct ReturnTransferAudit: App {
    @StateObject private var state = ReturnTransferAuditState()
    @State private var location = "Local"
    var body: some Scene {
        Window("Synthetic return to Mac · No provider calls",id:"return") {
            VStack(alignment:.leading,spacing:16) {
                HStack {Text("Run on").font(.title2);Spacer();Button("Done"){NSApplication.shared.terminate(nil)}}
                if let fixture = state.fixture, let transfer = state.transfer {
                    Text(fixture.session.title).font(.system(size:14,weight:.medium)).lineLimit(2)
                    Text("Currently Cloud").font(.system(size:11)).foregroundStyle(campMuted)
                    ExecutionLocationPicker(selection:$location)
                    ScrollView {
                        if location == "Local" {
                            ReturnTransferView(store:state.store,transfer:transfer,session:fixture.session,busy:false)
                                .frame(maxWidth:.infinity,alignment:.leading)
                        } else { Text("This fixture exercises the return to this Mac").font(.caption) }
                    }.frame(height:360)
                } else if !state.error.isEmpty { Text(state.error) }
                else { ProgressView("Preparing synthetic review") }
            }.padding(24).frame(width:540).background(campCream).foregroundStyle(campInk).tint(campGreen).preferredColorScheme(.light)
                .task { await state.load() }
        }.windowResizability(.contentSize)
    }
}
