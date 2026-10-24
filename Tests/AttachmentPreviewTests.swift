import Foundation

@MainActor final class PreviewReadGate {
    var started = false
    var continuation: CheckedContinuation<Data, Never>?
    func read() async -> Data { started = true; return await withCheckedContinuation { continuation = $0 } }
    func release(_ data: Data) { continuation?.resume(returning: data); continuation = nil }
}

@main struct AttachmentPreviewTests {
    @MainActor static func main() async throws {
        try cleanupAbandoned()
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("CapybaraPreviewTests-" + UUID().uuidString)
        try fm.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? fm.removeItem(at: root) }
        let bytes = Data("Synthetic attachment preview\n".utf8)
        let reference = TranscriptAttachment(id: "attachment-0", kind: "document", source: "inline", sourceDigest: String(repeating: "a", count: 64), state: "embedded", mediaType: "text/plain", sha256: PublishMessage.hash(bytes), size: bytes.count)
        let access = AttachmentAccess(id: "fixture") { _ in bytes }
        let model = AttachmentPreviewModel(root: root)
        func contents() -> [URL] { try! fm.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) }
        await model.load(reference, access: access)
        let first = model.file!
        let savedBytes = try Data(contentsOf: first); precondition(savedBytes == bytes)
        let savedCopy = fm.temporaryDirectory.appendingPathComponent("CapybaraSavedCopy-" + UUID().uuidString + ".txt")
        defer { try? fm.removeItem(at: savedCopy) }
        try model.saveCopy(from: first, to: savedCopy, attachment: reference)
        let exportedBytes = try Data(contentsOf: savedCopy); precondition(exportedBytes == bytes)
        precondition((try! fm.attributesOfItem(atPath: savedCopy.path)[.posixPermissions] as! NSNumber).intValue == 0o600)
        precondition((try! fm.attributesOfItem(atPath: first.path)[.posixPermissions] as! NSNumber).intValue == 0o600)
        precondition((try! fm.attributesOfItem(atPath: first.deletingLastPathComponent().path)[.posixPermissions] as! NSNumber).intValue == 0o700)
        precondition(!model.loading && model.error.isEmpty)
        await model.load(reference, access: access)
        precondition(model.file != first && !fm.fileExists(atPath: first.path))
        do { try model.saveCopy(from: first, to: savedCopy, attachment: reference); preconditionFailure("Saved a replaced preview") }
        catch {}
        precondition(contents().count == 1)
        model.close()
        precondition(model.file == nil && contents().isEmpty)
        precondition(fm.fileExists(atPath: savedCopy.path), "Closing a preview deleted the user's saved copy")
        await model.load(reference, access: AttachmentAccess(id: "corrupt") { _ in Data(repeating: 1, count: bytes.count) })
        precondition(model.file == nil && !model.error.isEmpty && contents().isEmpty)
        var unsupported = reference; unsupported.mediaType = "application/octet-stream"
        await model.load(unsupported, access: AttachmentAccess(id: "must-not-read") { _ in preconditionFailure("Read unsupported preview") })
        precondition(model.file == nil && !model.error.isEmpty)
        var unavailable = reference; unavailable.state = "reference"
        await model.load(unavailable, access: AttachmentAccess(id: "must-not-read") { _ in preconditionFailure("Read external reference") })
        precondition(model.file == nil)
        for operation in ["close", "cancel", "replace"] {
            let gate = PreviewReadGate()
            let pending = Task { await model.load(reference, access: AttachmentAccess(id: "held") { _ in await gate.read() }) }
            while !gate.started { await Task.yield() }
            if operation == "cancel" { pending.cancel() }
            else if operation == "replace" { await model.load(reference, access: access) }
            else { model.close() }
            let current = model.file
            gate.release(bytes)
            await pending.value
            precondition(model.file == current && !model.loading)
            precondition(contents().count == (operation == "replace" ? 1 : 0))
            model.close()
        }
        var released: AttachmentPreviewModel? = AttachmentPreviewModel(root: root)
        await released!.load(reference, access: access)
        weak var weakModel = released
        released = nil
        precondition(weakModel == nil && contents().isEmpty)
        print("Attachment preview tests passed: private files, verified bytes, explicit export, replacement, cancellation, close and deallocation")
    }
}


extension AttachmentPreviewTests {
    static func cleanupAbandoned() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("attachment-cleanup-test-" + UUID().uuidString)
        try fm.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? fm.removeItem(at: root) }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/sleep"); process.arguments = ["60"]
        try process.run()
        defer { if process.isRunning { process.terminate(); process.waitUntilExit() } }
        let pid = process.processIdentifier
        func folder(_ kind: String, permissions: Int = 0o700) throws -> URL {
            let value = root.appendingPathComponent("Capybara" + kind + "-v2-" + String(pid) + "-" + UUID().uuidString)
            try fm.createDirectory(at: value, withIntermediateDirectories: false, attributes: [.posixPermissions: permissions])
            try Data("private bytes".utf8).write(to: value.appendingPathComponent("contents"))
            return value
        }
        let preview = try folder("Preview"), transfer = try folder("Transfer")
        let foreign = try folder("Preview", permissions: 0o755)
        let current = try AttachmentTemporaryFiles.create(kind: "Preview", root: root)
        let outside = root.appendingPathComponent("keep-user-files")
        try fm.createDirectory(at: outside, withIntermediateDirectories: false)
        let link = root.appendingPathComponent("CapybaraPreview-v2-" + String(pid) + "-" + UUID().uuidString)
        try fm.createSymbolicLink(at: link, withDestinationURL: outside)
        AttachmentTemporaryFiles.removeAbandoned(root: root)
        precondition(fm.fileExists(atPath: preview.path) && fm.fileExists(atPath: transfer.path), "Removed another live process's bytes")
        process.terminate(); process.waitUntilExit()
        AttachmentTemporaryFiles.removeAbandoned(root: root)
        precondition(!fm.fileExists(atPath: preview.path) && !fm.fileExists(atPath: transfer.path))
        precondition(fm.fileExists(atPath: current.path) && fm.fileExists(atPath: foreign.path) && fm.fileExists(atPath: outside.path))
        precondition((try? fm.destinationOfSymbolicLink(atPath: link.path)) != nil)
    }
}
