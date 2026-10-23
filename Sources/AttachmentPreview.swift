import SwiftUI
import Quartz
import UniformTypeIdentifiers

struct AttachmentAccess {
    var id: String
    var read: @MainActor (TranscriptAttachment) async throws -> Data
}

@MainActor final class AttachmentPreviewModel: ObservableObject {
    @Published private(set) var file: URL?
    @Published private(set) var error = ""
    @Published private(set) var loading = false
    private var directory: URL?
    private var generation = UUID()
    private let root: URL
    init(root: URL = FileManager.default.temporaryDirectory) { self.root = root }
    deinit { if let directory { try? FileManager.default.removeItem(at: directory) } }
    static func fileExtension(_ attachment: TranscriptAttachment) -> String? {
        switch (attachment.kind, attachment.mediaType ?? "") {
        case ("image", "image/png"): return "png"
        case ("image", "image/jpeg"): return "jpg"
        case ("image", "image/gif"): return "gif"
        case ("image", "image/webp"): return "webp"
        case ("audio", "audio/wav"), ("audio", "audio/x-wav"): return "wav"
        case ("audio", "audio/mpeg"), ("audio", "audio/mp3"): return "mp3"
        case ("audio", "audio/mp4"): return "m4a"
        case ("audio", "audio/ogg"): return "ogg"
        case ("audio", "audio/webm"): return "webm"
        case ("document", "application/pdf"): return "pdf"
        case ("document", "text/plain"): return "txt"
        default: return nil
        }
    }
    func load(_ attachment: TranscriptAttachment, access: AttachmentAccess) async {
        close()
        let token = generation
        loading = true
        defer { if generation == token { loading = false } }
        do {
            guard attachment.state == "embedded", let ext = Self.fileExtension(attachment) else {
                throw CampError.message("This attachment has no supported preview")
            }
            let data = try await access.read(attachment)
            guard generation == token, !Task.isCancelled else { return }
            guard data.count > 0, data.count <= 16 << 20, data.count == attachment.size,
                  PublishMessage.hash(data) == attachment.sha256 else { throw CampCloudError.invalidResponse }
            let folder = root.appendingPathComponent("CapybaraPreview-" + UUID().uuidString, isDirectory: true)
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
            directory = folder
            let target = folder.appendingPathComponent(attachment.label + "." + ext)
            try data.write(to: target, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: target.path)
            file = target
        } catch {
            guard generation == token, !Task.isCancelled else { return }
            removeFile()
            self.error = error.localizedDescription
        }
    }
    func close() { generation = UUID(); loading = false; error = ""; removeFile() }
    func saveCopy(from source: URL, to destination: URL, attachment: TranscriptAttachment) throws {
        guard source == file else { throw CampError.message("This preview has closed. Open the attachment again to save it") }
        let data = try Data(contentsOf: source)
        guard data.count == attachment.size, data.count <= 16 << 20,
              PublishMessage.hash(data) == attachment.sha256 else { throw CampCloudError.invalidResponse }
        try data.write(to: destination, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: destination.path)
    }
    private func removeFile() {
        file = nil
        if let directory { try? FileManager.default.removeItem(at: directory) }
        directory = nil
    }
}

private struct AttachmentQuickLook: NSViewRepresentable {
    let url: URL
    func makeNSView(context: Context) -> QLPreviewView {
        let view = QLPreviewView(frame: .zero, style: .normal)!
        view.shouldCloseWithWindow = false
        view.autostarts = false
        view.previewItem = url as NSURL
        return view
    }
    func updateNSView(_ view: QLPreviewView, context: Context) {
        if view.previewItem?.previewItemURL != url { view.previewItem = url as NSURL }
    }
    static func dismantleNSView(_ view: QLPreviewView, coordinator: ()) {
        view.previewItem = nil
        view.close()
    }
}

struct AttachmentPreview: View {
    let attachment: TranscriptAttachment
    let access: AttachmentAccess
    @Environment(\.dismiss) private var dismiss
    @StateObject private var preview = AttachmentPreviewModel()
    @State private var saveError = ""
    @State private var savePanel: NSSavePanel?
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Label(attachment.label, systemImage: attachment.symbol).font(.headline)
                Spacer()
                Button("Save a copy") { saveCopy() }.disabled(preview.file == nil || savePanel != nil)
                Button { dismiss() } label: { CampIconLabel(symbol: "xmark") }
                    .buttonStyle(.plain).accessibilityLabel("Close attachment preview").keyboardShortcut(.cancelAction)
            }.padding(16)
            Divider()
            Group {
                if let file = preview.file { AttachmentQuickLook(url: file) }
                else if preview.loading { ProgressView("Loading attachment") }
                else { Text(preview.error.isEmpty ? "Attachment unavailable" : preview.error).foregroundStyle(campMuted).padding(24) }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }.frame(width: 640, height: 480)
            .task(id: access.id + ":" + attachment.sourceDigest) { await preview.load(attachment, access: access) }
            .onDisappear { savePanel?.cancel(nil); savePanel = nil; preview.close() }
            .alert("Could not save attachment", isPresented: Binding(get: { !saveError.isEmpty }, set: { if !$0 { saveError = "" } })) {
                Button("OK") { saveError = "" }
            } message: { Text(saveError) }
    }
    private func saveCopy() {
        guard let source = preview.file, let ext = AttachmentPreviewModel.fileExtension(attachment) else { return }
        let panel = NSSavePanel()
        panel.nameFieldStringValue = attachment.label + "." + ext
        if let type = UTType(filenameExtension: ext) { panel.allowedContentTypes = [type] }
        panel.canCreateDirectories = true
        savePanel = panel
        panel.begin { response in
            savePanel = nil
            guard response == .OK, let destination = panel.url else { return }
            do { try preview.saveCopy(from: source, to: destination, attachment: attachment) }
            catch { saveError = error.localizedDescription }
        }
    }
}
