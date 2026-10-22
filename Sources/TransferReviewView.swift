import SwiftUI

struct TransferReviewView: View {
    @ObservedObject var transfer: CampTransfer
    let local: CampSession
    let session: CloudSession
    let input: CloudWorkspaceInput?
    let busy: Bool
    let permissions: Binding<PermissionSettings>
    let catalog: PermissionCatalog
    var accountLabel: String = ""
    var back: ()->Void
    @State private var selected: Set<String> = []
    @State private var approved = false
    @State private var previewPath: String?
    @State private var previewText = ""
    private var job: TransferReviewJob? { transfer.job?.session.id==session.id ? transfer.job : nil }
    var body: some View {
        VStack(alignment:.leading,spacing:14) {
            Text("Keep this ticket and its main conversation history").font(.system(size:12))
            if let pending=transfer.job,pending.session.id != session.id,!["complete","cancelled"].contains(pending.phase) {
                Text("Finish the move for \(pending.session.title) before starting another").font(.system(size:12))
                Button(action:back){Label("Back",systemImage:"chevron.left")}
            } else if let job {
                Text("\(local.provider) · \(accountLabel.isEmpty ? "Selected cloud account" : accountLabel)").font(.caption).foregroundStyle(campMuted)
                HStack { Label("Selected session history",systemImage:"checkmark");Spacer();Text("Revision \(job.session.transcriptRevision)") }.font(.caption)
                if let workspace=job.workspace {
                    Text("\(workspace.repositoryName) · @\(workspace.githubLogin)").font(.caption)
                }
                Text("Pinned Git revision · \(job.capture.baseCommit.prefix(12))").font(.system(size:11,design:.monospaced))
                Text("\(job.capture.files.count) reviewed project changes").font(.system(size:12,weight:.medium))
                ForEach(job.capture.files) { file in
                    HStack { Image(systemName:file.deleted ? "minus.circle" : "doc.text");Text(file.path).lineLimit(2);Spacer();Text(file.deleted ? "Deleted" : ByteCountFormatter.string(fromByteCount:Int64(file.bytes),countStyle:.file)).foregroundStyle(campMuted)
                        Button {previewText="Reading captured file";previewPath=file.path} label:{Image(systemName:"doc.text.magnifyingglass")}
                            .buttonStyle(.plain).help("Preview captured file").accessibilityLabel("Preview \(file.path)")
                    }.font(.caption)
                }
                if !job.capture.excluded.isEmpty {
                    DisclosureGroup("Excluded · \(job.capture.excluded.count) files") {
                        ForEach(job.capture.excluded) { file in Text("\(file.path) · \(file.reason)").font(.caption).foregroundStyle(campMuted) }
                    }
                }
                Picker("Continue with",selection:.constant("Imported context")) {
                    Text("Imported context").tag("Imported context")
                    Text("Native resume · Requires compatibility check").tag("Native resume").disabled(true)
                }.font(.caption)
                Text("The provider’s hidden runtime state is not transferred").font(.caption).foregroundStyle(campMuted)
                phase(job)
            } else if let changes=transfer.changes,transfer.inspectionID==local.id {
                Text("Select the project changes to include").font(.system(size:12,weight:.medium))
                Text("Pinned Git revision · \(changes.baseCommit.prefix(12))").font(.system(size:11,design:.monospaced))
                if changes.files.isEmpty { Text("No working-tree changes").font(.caption).foregroundStyle(campMuted) }
                ScrollView {
                    LazyVStack(alignment:.leading,spacing:8) {
                        ForEach(changes.files) { file in
                            HStack {
                                Toggle(isOn:Binding(get:{selected.contains(file.path)},set:{enabled in if enabled {selected.insert(file.path)} else {selected.remove(file.path)}})) {Text(file.path).lineLimit(2)}
                                    .disabled(file.excluded != nil)
                                Spacer();Text(file.excluded ?? file.kind).foregroundStyle(campMuted)
                            }.font(.caption)
                        }
                    }
                }.frame(height:min(CGFloat(changes.files.count)*38,220))
                Text("Credentials, .env files and provider folders are excluded").font(.caption).foregroundStyle(campMuted)
                HStack {
                    Button(action:back){Label("Back",systemImage:"chevron.left")}
                    Spacer()
                    Button("Review selected changes") {
                        guard let input else{return}
                        Task {await transfer.capture(local,session:session,input:input,paths:selected.sorted())}
                    }.buttonStyle(.borderedProminent).disabled(input==nil || transfer.working || busy || selected.count>256)
                }
                if selected.count>256 {Text("Select no more than 256 files").font(.caption).foregroundStyle(.orange)}
            } else if transfer.working { ProgressView("Reading project changes") }
            if !transfer.error.isEmpty {
                Text(transfer.error).font(.caption).foregroundStyle(.orange)
                Button("Retry") {
                    if job==nil {Task{await transfer.inspect(local)}} else {transfer.retry()}
                }.disabled(transfer.working)
            }
        }
        .task {
            await transfer.restore()
            if let job,["complete","cancelled"].contains(job.phase) {await transfer.resetReview()}
            if job==nil {await transfer.inspect(local)}
        }
        .popover(isPresented:Binding(get:{previewPath != nil},set:{if !$0 {previewPath=nil;previewText=""}})) {
            VStack(alignment:.leading,spacing:10) {
                Text(previewPath ?? "Captured file").font(.headline)
                ScrollView {Text(previewText).font(.system(size:11,design:.monospaced)).textSelection(.enabled).frame(maxWidth:.infinity,alignment:.leading)}
            }.padding(18).frame(width:520,height:360)
                .task(id:previewPath) {
                    guard let path=previewPath else{return}
                    do {
                        let text=try await transfer.preview(path)
                        guard !Task.isCancelled,previewPath==path else{return}
                        previewText=text
                    } catch {
                        guard !Task.isCancelled,previewPath==path else{return}
                        previewText=error.localizedDescription
                    }
                }
        }
        .onDisappear{previewPath=nil;previewText=""}
    }
    @ViewBuilder private func phase(_ job:TransferReviewJob)->some View {
        switch job.phase {
        case "review":
            HStack {Button{Task{await transfer.resetReview();back()}}label:{Label("Back",systemImage:"chevron.left")};Spacer();Button("Prepare Cloud"){Task{await transfer.prepare()}}.buttonStyle(.borderedProminent).disabled(busy || transfer.working)}
        case "uploading":
            ProgressView("Uploading reviewed changes",value:Double(transfer.uploadedBytes),total:Double(job.payloadBytes))
            cancelButton
        case "preparing":
            ProgressView("Preparing the cloud workspace");cancelButton
        case "ready":
            Text("The destination is ready. Verify that the source has stopped before moving.").font(.caption)
            HStack {cancelButton;Spacer();Button("Verify source"){Task{await transfer.hold()}}.buttonStyle(.borderedProminent).disabled(busy || transfer.working)}
        case "holding":
            ProgressView("Waiting for the source Mac to verify its files");cancelButton
        case "confirm":
            Label("Source verified",systemImage:"checkmark").font(.caption)
            if job.input.purpose=="implementation" {
                PermissionFields(provider:local.provider,catalog:catalog,selection:permissions)
                Toggle("Allow this cloud session to use project tools with these permissions",isOn:$approved)
            }
            HStack {cancelButton;Spacer();Button("Move to cloud"){Task{await transfer.confirm(permissions:permissions.wrappedValue,approved:job.input.purpose=="implementation" && approved)}}.buttonStyle(.borderedProminent).disabled(transfer.working || job.input.purpose=="implementation" && !approved)}
        case "committing":
            ProgressView("Confirming the move to cloud");cancelButton
        case "cancelling":ProgressView("Cancelling the transfer")
        case "blocked":cancelButton
        case "complete":Label("Runs in Cloud",systemImage:"checkmark.circle").foregroundStyle(campGreen)
        case "cancelled":Label("Transfer cancelled · The source was kept",systemImage:"checkmark")
        default:EmptyView()
        }
    }
    private var cancelButton:some View {Button(transfer.cancelling ? "Cancelling…" : "Cancel transfer"){Task{await transfer.cancel()}}.disabled(transfer.cancelling)}
}
