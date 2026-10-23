import SwiftUI

struct ReturnTransferView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var transfer: CampReturnTransfer
    let session: CloudSession
    let busy: Bool
    @State private var selected: Set<String> = []
    @State private var sourceID = ""
    @State private var implementation = false
    @State private var contextApproved = false
    @State private var executionApproved = false
    @State private var permissions = PermissionSettings()
    @State private var previewPath: String?
    @State private var previewText = ""
    private var sources: [CampSource] { store.sources.filter { $0.provider == session.provider } }
    private var belongs: Bool { transfer.sessionID == nil || transfer.sessionID == session.id }
    private var catalog: PermissionCatalog { .requested(session.provider, inherited:"Choose explicit local permissions") }
    var body: some View {
        VStack(alignment:.leading,spacing:14) {
            if session.ownerUserId != transfer.ownerUserID {
                Text("The session owner chooses where this runs").font(.caption)
            } else if !belongs {
                Text("Finish or cancel the other return to this Mac first").font(.caption)
            } else {
                Text("Keep this ticket, its main chat and its sub-agents").font(.system(size:12))
                content
                if !transfer.error.isEmpty {
                    Text(transfer.error).font(.caption).foregroundStyle(.orange)
                    Button { transfer.retry() } label: { Label("Retry",systemImage:"arrow.clockwise") }.disabled(transfer.working)
                }
                if !["none","complete","cancelled"].contains(transfer.phase) {
                    Button { Task { await transfer.cancel() } } label: { Label("Cancel transfer",systemImage:"xmark") }
                        .disabled(transfer.phase == "cancelling")
                }
            }
        }
        .task { await transfer.restore() }
        .onAppear {
            sourceID = sources.first?.id ?? ""
            permissions = session.provider == "Codex" ? PermissionSettings(sandbox:"workspace-write",approval:"on-request",reviewer:"user") : PermissionSettings(claudeMode:"manual")
        }
        .onChange(of:sources.map(\.id)) { _, ids in if !ids.contains(sourceID) { sourceID = ids.first ?? "" } }
        .onChange(of:transfer.handoff?.id) { _, _ in contextApproved = false; executionApproved = false }
        .popover(isPresented:Binding(get:{previewPath != nil},set:{if !$0 {previewPath = nil;previewText = ""}})) {
            VStack(alignment:.leading,spacing:10) {
                Text(previewPath ?? "Captured file").font(.headline)
                ScrollView { Text(previewText).font(.system(size:11,design:.monospaced)).textSelection(.enabled).frame(maxWidth:.infinity,alignment:.leading) }
            }.padding(18).frame(width:520,height:360)
                .task(id:previewPath) {
                    guard let path = previewPath else { return }
                    do {
                        let value = try await transfer.preview(path)
                        if !Task.isCancelled, previewPath == path { previewText = value }
                    } catch { if !Task.isCancelled, previewPath == path { previewText = error.localizedDescription } }
                }
        }
        .onDisappear { previewPath = nil; previewText = "" }
    }
    @ViewBuilder private var content: some View {
        switch transfer.phase {
        case "none":
            Text("Review the cloud files and history, then choose a profile on this Mac").font(.caption).foregroundStyle(campMuted)
            Button("Review move") { Task { await transfer.inspect(session) } }.buttonStyle(.borderedProminent).disabled(busy || transfer.working)
        case "inspecting": ProgressView("Reading cloud project changes")
        case "select":
            if let changes = transfer.changes {
                Text("Pinned Git revision · \(changes.baseCommit.prefix(12))").font(.system(size:11,design:.monospaced))
                if changes.files.isEmpty { Text("No working-tree changes").font(.caption).foregroundStyle(campMuted) }
                LazyVStack(alignment:.leading,spacing:10) {
                    ForEach(changes.files) { file in
                        Toggle(isOn:Binding(get:{selected.contains(file.path)},set:{if $0 {selected.insert(file.path)} else {selected.remove(file.path)}})) {
                            HStack { Text(file.path).lineLimit(2); Spacer(); Text(file.excluded ?? file.kind).foregroundStyle(campMuted) }
                        }.disabled(file.excluded != nil).font(.caption)
                    }
                }
                Text("Credentials, .env files and provider folders are excluded").font(.caption).foregroundStyle(campMuted)
                Button("Review selected changes") { Task { await transfer.capture(paths:selected.sorted()) } }
                    .buttonStyle(.borderedProminent).disabled(busy || transfer.working || selected.count > 256)
            }
        case "downloading":
            ProgressView("Downloading files and session history")
            Text("\(ByteCountFormatter.string(fromByteCount:Int64(transfer.downloadedBytes),countStyle:.file)) of files · \(transfer.history?.streams.reduce(0,{$0+$1.messageCount}) ?? 0) messages").font(.caption).foregroundStyle(campMuted)
        case "review":
            if let review = transfer.review {
                if let commits = review.commits {
                    TransferCommitSummaryView(commits:commits)
                }
                Text("\(review.files.count) reviewed project changes · \(max(0,(transfer.history?.streams.count ?? 1)-1)) sub-agents").font(.caption)
                ForEach(review.files) { file in
                    HStack {
                        Image(systemName:file.deleted ? "minus.circle" : "doc.text"); Text(file.path).lineLimit(2); Spacer()
                        Text(file.deleted ? "Deleted" : ByteCountFormatter.string(fromByteCount:Int64(file.bytes),countStyle:.file)).foregroundStyle(campMuted)
                        Button { previewText = "Reading captured file"; previewPath = file.path } label: { CampIconLabel(symbol:"doc.text.magnifyingglass") }
                            .buttonStyle(.plain).help("Preview captured file").accessibilityLabel("Preview \(file.path)")
                    }.font(.caption)
                }
                if !review.excluded.isEmpty {
                    DisclosureGroup("Excluded · \(review.excluded.count) files") {
                        ForEach(review.excluded) { Text("\($0.path) · \($0.reason)").font(.caption) }
                    }
                }
                Picker("\(session.provider) profile",selection:$sourceID) {
                    Text("Choose a profile").tag("")
                    ForEach(sources) { Text($0.label).tag($0.id) }
                }
                if sources.isEmpty { Text("Connect a local \(session.provider) profile first · Profiles are found automatically").font(.caption).foregroundStyle(campMuted) }
                Toggle("Allow implementation in this workspace",isOn:$implementation)
                Text("Files will be restored into a separate local checkout").font(.caption).foregroundStyle(campMuted)
                Button("Prepare This Mac") {
                    guard let source = sources.first(where:{$0.id == sourceID}) else { return }
                    Task { await transfer.prepare(source:source,purpose:implementation ? "implementation" : "planning") }
                }.buttonStyle(.borderedProminent).disabled(busy || transfer.working || !sources.contains(where:{$0.id == sourceID}))
            }
        case "checkout": ProgressView("Preparing the local checkout")
        case "installing", "installed": ProgressView("Restoring reviewed files and session history")
        case "uploading", "holding": ProgressView("Verifying the source and destination")
        case "confirm":
            Label("Source verified",systemImage:"checkmark").font(.caption)
            Text("Continues with imported context · The provider’s hidden runtime state is not transferred").font(.caption).foregroundStyle(campMuted)
            Toggle("Continue using the imported conversation history",isOn:$contextApproved)
            if transfer.handoff?.destination.target.purpose == "implementation" {
                PermissionFields(provider:session.provider,catalog:catalog,selection:$permissions)
                Toggle("Allow this local session to use project tools with these permissions",isOn:$executionApproved)
            }
            Button("Move to This Mac") { Task { await transfer.confirm(contextApproved:contextApproved,executionApproved:executionApproved,permissions:permissions) } }
                .buttonStyle(.borderedProminent).disabled(transfer.working || !contextApproved || transfer.handoff?.destination.target.purpose == "implementation" && !executionApproved)
        case "committing": ProgressView("Confirming the move to this Mac")
        case "cancelling": ProgressView("Cancelling the transfer")
        case "cancelled":
            Text(transfer.endMessage).font(.caption)
            Button { Task { await transfer.reset(); selected = []; contextApproved = false; executionApproved = false } } label: { Label("Start over",systemImage:"arrow.counterclockwise") }.disabled(transfer.working)
        case "complete":
            Label("Runs on This Mac",systemImage:"checkmark.circle").foregroundStyle(campGreen)
            Text("Continue from this ticket · The original history stays attached").font(.caption).foregroundStyle(campMuted)
        default: EmptyView()
        }
    }
}
