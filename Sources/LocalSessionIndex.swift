import Foundation

actor LocalSessionIndex {
    static let shared = LocalSessionIndex()
    static func read(directory: URL) -> ImportResult? {
        let path = directory.appendingPathComponent("session-index.json")
        guard let attributes = try? FileManager.default.attributesOfItem(atPath: path.path),
              let size = attributes[.size] as? NSNumber, size.intValue <= 64 << 20,
              let data = try? Data(contentsOf: path), let value = try? JSONDecoder().decode(ImportResult.self, from: data),
              value.sessions.count <= 200_000, !value.sessions.contains(where: \.isCloud) else { return nil }
        return value
    }
    func save(_ value: ImportResult, directory: URL) {
        guard FileManager.default.fileExists(atPath: directory.path) else { return }
        do {
            let data = try JSONEncoder().encode(value)
            let path = directory.appendingPathComponent("session-index.json")
            try data.write(to: path, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
        } catch { /* Discovery remains authoritative; a failed cache write does not lose sessions. */ }
    }
}
