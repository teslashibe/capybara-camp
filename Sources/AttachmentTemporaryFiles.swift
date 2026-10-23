import Foundation
import Darwin

// Process ownership distinguishes abandoned bytes from another running Camp
// instance. A reused PID is retained until that process exits.
enum AttachmentTemporaryFiles {
    static func create(kind: String, root: URL = FileManager.default.temporaryDirectory) throws -> URL {
        guard ["Preview", "Transfer"].contains(kind) else { throw CampCloudError.invalidResponse }
        let folder = root.appendingPathComponent("Capybara" + kind + "-v2-" + String(getpid()) + "-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        return folder
    }
    static func removeAbandoned(root: URL = FileManager.default.temporaryDirectory) {
        let fm = FileManager.default
        guard let entries = try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) else { return }
        for entry in entries {
            let name = entry.lastPathComponent
            guard let prefix = ["CapybaraPreview-v2-", "CapybaraTransfer-v2-"].first(where: { name.hasPrefix($0) }) else { continue }
            let fields = name.dropFirst(prefix.count).split(separator: "-", maxSplits: 1)
            guard fields.count == 2, let pid = pid_t(fields[0]), pid > 1, pid != getpid(), UUID(uuidString: String(fields[1])) != nil else { continue }
            var metadata = stat()
            guard lstat(entry.path, &metadata) == 0, metadata.st_uid == geteuid(),
                  metadata.st_mode & S_IFMT == S_IFDIR, metadata.st_mode & 0o777 == 0o700 else { continue }
            // EPERM and all ambiguous results retain the folder. Never signal a
            // process or infer abandonment from an age threshold.
            guard kill(pid, 0) == -1, errno == ESRCH else { continue }
            try? fm.removeItem(at: entry)
        }
    }
}
