import SwiftUI

struct ProjectAssignmentMenu: View {
    @ObservedObject var store: CampStore
    let session: CampSession
    var body: some View {
        Menu {
            Button("Inbox") { store.assignProject(session,to:nil) }
            let values=session.isCloud ? store.cloud.projects : store.projects.projects
            ForEach(values.filter { !$0.isArchived && !$0.isInbox }) { project in
                Button { store.assignProject(session,to:project.id) } label: {
                    if store.assignedProject(session)?.id==project.id { Label(project.name,systemImage:"checkmark") } else { Text(project.name) }
                }
            }
            Divider()
            Button("Manage projects") { store.projectSheet=true }
        } label: { Label(store.projectLabel(session),systemImage:"folder").lineLimit(1) }
            .disabled(session.isCloud && !store.canManageSharedProjects)
            .accessibilityLabel("Project for "+session.title)
    }
}

struct ProjectSidebar: View {
    @ObservedObject var store: CampStore
    @ObservedObject var local: LocalProjects
    @ObservedObject var cloud: CampConnection
    var body: some View {
        VStack(alignment:.leading,spacing:5) {
            HStack {
                Text("PROJECTS").font(.system(size:10,weight:.semibold)).tracking(1.4).foregroundStyle(campMuted)
                Spacer()
                Button { store.projectSheet=true } label: { Image(systemName:"plus").frame(width:24,height:24) }.buttonStyle(.plain).help("Manage projects").accessibilityLabel("Manage projects")
            }
            entry("All projects",key:"all",icon:"square.stack")
            entry("Inbox",key:"inbox",icon:"tray")
            ForEach(shown(local.projects,prefix:"local:")) { project in entry(project.name+(project.isArchived ? " · Archived":""),key:"local:"+project.id,icon:project.isArchived ? "archivebox":"folder") }
            ForEach(shown(cloud.projects,prefix:"cloud:")) { project in entry(project.name+(project.isArchived ? " · Archived":""),key:"cloud:"+project.id,icon:project.isArchived ? "archivebox":"person.2") }
        }
    }
    private func shown(_ projects:[CampProject],prefix:String)->[CampProject] {
        var result=Array(projects.filter { !$0.isArchived && !$0.isInbox }.prefix(3))
        if let selected=projects.first(where:{prefix+$0.id==store.projectFilter}),!result.contains(where:{$0.id==selected.id}) { result.append(selected) }
        return result
    }
    private func entry(_ title:String,key:String,icon:String) -> some View {
        Button { store.projectFilter=key } label: {
            Label(title,systemImage:icon).font(.system(size:11)).lineLimit(1).frame(maxWidth:.infinity,alignment:.leading).padding(6)
                .background(store.projectFilter==key ? campGreen.opacity(0.1):.clear).clipShape(RoundedRectangle(cornerRadius:6))
        }.buttonStyle(.plain)
    }
}

struct CampProjectsView: View {
    @ObservedObject var store: CampStore
    @ObservedObject var local: LocalProjects
    @ObservedObject var cloud: CampConnection
    @Environment(\.dismiss) private var dismiss
    @State private var shared=false
    @State private var selected:String?
    @State private var name=""
    @State private var repositories=""
    @State private var archived=false
    @State private var showArchived=false
    @State private var error=""
    @State private var working=false
    var values:[CampProject] { shared ? cloud.projects : local.projects }
    var project:CampProject? { values.first { $0.id==selected } }
    var pending:[CloudOutboxItem] { cloud.pending.filter { $0.path.contains("/projects") || $0.path.hasSuffix("/project") } }
    var body:some View {
        VStack(alignment:.leading,spacing:16) {
            HStack { Text("Projects").font(.title2);Spacer();Button { dismiss() } label: { Image(systemName:"xmark").frame(width:28,height:28) }.buttonStyle(.plain).accessibilityLabel("Close projects") }
            Text("Group client work across repositories, plans and sessions").font(.system(size:12)).foregroundStyle(campMuted)
            CampSegmentedControl(options: [(false, "On this Mac"), (true, "Shared Camp")], selection: $shared).frame(width: 260).disabled(cloud.scope==nil)
                .onChange(of:shared) { _,_ in reset() }
            HStack(alignment:.top,spacing:20) {
                VStack(alignment:.leading,spacing:8) {
                    Button("New project",systemImage:"plus") { reset() }.disabled(working)
                    Toggle("Show archived",isOn:$showArchived).toggleStyle(.checkbox).font(.caption)
                    ScrollView {
                        VStack(spacing:3) {
                            ForEach(values.filter { showArchived || !$0.isArchived }) { value in
                                Button { select(value) } label: {
                                    Label(value.name+(value.isArchived ? " · Archived":""),systemImage:value.isInbox ? "tray":"folder")
                                        .frame(maxWidth:.infinity,alignment:.leading).padding(8).background(selected==value.id ? campGreen.opacity(0.12):.clear).clipShape(RoundedRectangle(cornerRadius:6))
                                }.buttonStyle(.plain)
                            }
                        }
                    }.frame(width:200,height:270)
                }
                VStack(alignment:.leading,spacing:10) {
                    CampProjectFields(name:$name,repositories:$repositories)
                    if project != nil { Toggle("Archived",isOn:$archived).toggleStyle(.checkbox) }
                    HStack {
                        if let project { Button("Show tickets") { store.projectFilter=(shared ? "cloud:":"local:")+project.id;dismiss() } }
                        Spacer()
                        Button(selected==nil ? "Create project":"Save project") { save() }.buttonStyle(.borderedProminent).tint(campGreen)
                    }
                }.disabled(working || project?.isInbox==true || (shared && !store.canManageSharedProjects))
            }
            if !error.isEmpty { Text(error).font(.caption).foregroundStyle(.orange) }
            else if !shared && !local.error.isEmpty { Text(local.error).font(.caption).foregroundStyle(.orange) }
            if shared && !pending.isEmpty {
                Divider()
                ForEach(Array(pending.prefix(8))) { item in
                    HStack {
                        Label(["failed","conflict"].contains(item.state) ? "Project change needs review: "+item.error : "Project change waiting to sync",systemImage:item.state=="conflict" ? "exclamationmark.circle":"clock")
                        Spacer()
                        if ["failed","conflict"].contains(item.state) { Button("Discard") { Task { await cloud.discardProjectChange(item) } } }
                    }.font(.caption)
                }
                Text("Conflicting changes stay pending for review. Reopen the latest project or ticket, then choose the change again.").font(.caption).foregroundStyle(campMuted)
            }
        }.padding(24).frame(width:720).background(campCream)
    }
    private func reset() { selected=nil;name="";repositories="";archived=false;error="" }
    private func select(_ value:CampProject) { selected=value.id;name=value.name;repositories=value.repositoryURLs.joined(separator:"\n");archived=value.isArchived;error="" }
    private func save() {
        working=true
        let current=project
        let urls=repositories.split(whereSeparator:\.isNewline).map(String.init)
        Task {
            defer { working=false }
            do {
                if shared { try await cloud.queueProjectSave(current,name:name,repositories:urls,archived:archived);reset() }
                else {
                    let value=CampProject(id:current?.id ?? UUID().uuidString,name:name,revision:current?.version,archived:archived,repositories:urls)
                    try await local.save(value,expectedRevision:current?.version ?? 0)
                    if let saved=local.projects.first(where:{$0.id==value.id}) { select(saved) }
                }
            } catch { self.error=error.localizedDescription }
        }
    }
}

struct CampProjectFields: View {
    @Binding var name: String
    @Binding var repositories: String
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            TextField("Project name", text: $name).textFieldStyle(.roundedBorder).accessibilityLabel("Project name")
            Text("Repository links").font(.system(size: 12, weight: .medium))
            TextEditor(text: $repositories).font(.system(size: 12, design: .monospaced)).frame(height: 115).padding(6).background(.white).clipShape(RoundedRectangle(cornerRadius: 7)).accessibilityLabel("Repository links")
            Text("Optional GitHub URLs, one per line. Linking organizes work; Git access is connected separately.").font(.system(size: 11)).foregroundStyle(campMuted)
        }
    }
}
