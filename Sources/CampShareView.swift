import SwiftUI

struct CampShareView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var cloud: CampConnection
    @ObservedObject var sharing: CampSharing
    let session: CampSession
    @Environment(\.dismiss) private var dismiss
    @State private var project = ""
    @State private var allowRequests = false
    @State private var working = false
    @State private var error = ""
    var existing: LocalShare? { sharing.binding(for: session) }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack {
                Text("Share with Camp").font(.title2)
                Spacer()
                Button { dismiss() } label: { Image(systemName: "xmark").frame(width: 28, height: 28) }.buttonStyle(.plain).help("Close")
            }
            Text(session.title).font(.system(size: 14, weight: .medium)).lineLimit(3)
            Text("Members of this Camp can read the shared transcript. Your provider login and local folder stay on this Mac.").font(.system(size: 12)).foregroundStyle(campMuted)
            if !cloud.signedIn || cloud.selectedCampId.isEmpty {
                Text("Connect a Camp from the app header to share this session").font(.system(size: 12))
            } else if cloud.projects.isEmpty {
                Text("Add a project in Camp settings first").font(.system(size: 12))
            } else {
                Picker("Project", selection: $project) { ForEach(cloud.projects) { value in Text(value.name).tag(value.id) } }
                Toggle("Allow teammates to request work", isOn: $allowRequests)
                Text("You approve requests from other people before this Mac runs them").font(.system(size: 11)).foregroundStyle(campMuted)
                if let existing { Text(existing.state).font(.system(size: 12)).foregroundStyle(campMuted) }
                if existing?.state == "Reconnect ownership on this Mac" {
                    Button("Use this Mac") { save(shared: existing?.shared ?? true, adopt: true) }.disabled(working)
                    Text("Moves execution here after active or uncertain work has been resolved on the previous device").font(.system(size: 11)).foregroundStyle(campMuted)
                }
            }
            if !error.isEmpty { Text(error).font(.system(size: 12)).foregroundStyle(Color.orange) }
            HStack {
                if existing?.shared == true { Button("Stop sharing", role: .destructive) { save(shared: false) }.disabled(working) }
                Spacer()
                Button(existing?.shared == true ? "Save changes" : "Share session") { save(shared: true) }
                    .buttonStyle(.borderedProminent).tint(campGreen).disabled(working || project.isEmpty || !cloud.signedIn)
            }
        }.padding(26).frame(width: 520).background(campCream)
            .onAppear { project = existing?.projectId ?? cloud.projects.first?.id ?? ""; allowRequests = existing?.allowRequests ?? false }
    }
    private func save(shared: Bool, adopt: Bool = false) {
        working = true
        Task {
            defer { working = false }
            do { try await sharing.configure(local: session, projectId: project, shared: shared, allowRequests: allowRequests, adopt: adopt); dismiss() }
            catch { self.error = error.localizedDescription }
        }
    }
}
