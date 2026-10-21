import Foundation
import CryptoKit
import SQLite3

struct CloudScope: Codable, Equatable, Sendable {
    var origin: String; var userId: String; var campId: String
    var key: String { SHA256.hash(data: Data((origin + "\n" + userId + "\n" + campId).utf8)).map { String(format: "%02x", $0) }.joined() }
}
struct CloudOutboxItem: Identifiable, Sendable {
    var id: String; var scope: String; var method: String; var path: String; var body: Data
    var state: String; var error: String
}
struct CloudAttempt: Codable, Identifiable, Sendable {
    var id: String; var scope: String; var request: CloudRequest; var localSessionId: String
    var state: String; var startKey: String; var finishKey: String
    var outcome: String?
    var evidence: String?
}

actor CampCache {
    private var db: OpaquePointer?
    private enum Value { case text(String), integer(Int64), blob(Data) }
    private let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    init(directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let path = directory.appendingPathComponent("camp-cache.sqlite")
        var database: OpaquePointer?
        guard sqlite3_open_v2(path.path, &database, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX, nil) == SQLITE_OK else {
            if let database { sqlite3_close(database) }; throw CampCloudError.cacheUnavailable
        }
        db = database
        sqlite3_busy_timeout(database, 3000)
        var versionStatement: OpaquePointer?
        let versionReady = sqlite3_prepare_v2(database, "PRAGMA user_version", -1, &versionStatement, nil) == SQLITE_OK
        let version = versionReady && sqlite3_step(versionStatement) == SQLITE_ROW ? sqlite3_column_int(versionStatement, 0) : -1
        sqlite3_finalize(versionStatement)
        guard (0...2).contains(version) else { sqlite3_close(database); db = nil; throw CampCloudError.cacheUnavailable }
        let schema = """
        PRAGMA journal_mode=WAL;
        PRAGMA synchronous=FULL;
        CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS sessions(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,transcript_revision INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS tombstones(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS requests(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS messages(scope TEXT NOT NULL,session_id TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,ordinal INTEGER NOT NULL,removed INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,session_id,id));
        CREATE INDEX IF NOT EXISTS transcript_window ON messages(scope,session_id,ordinal DESC);
        CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,scope TEXT NOT NULL,method TEXT NOT NULL,path TEXT NOT NULL,body BLOB NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created REAL NOT NULL);
        CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(scope,state,created);
        CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY,scope TEXT NOT NULL,state TEXT NOT NULL,body BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS share_bindings(scope TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS published_message_hashes(scope TEXT NOT NULL,session_id TEXT NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(scope,session_id,id));
        UPDATE outbox SET state='pending' WHERE state='sending';
        UPDATE attempts SET state='uncertain' WHERE state IN ('start_pending','running');
        PRAGMA user_version=2;
        """
        guard sqlite3_exec(database, schema, nil, nil, nil) == SQLITE_OK else { sqlite3_close(database); db = nil; throw CampCloudError.cacheUnavailable }
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
    }
    deinit { if let db { sqlite3_close(db) } }

    private func statement(_ sql: String, _ values: [Value]) throws -> OpaquePointer {
        var prepared: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &prepared, nil) == SQLITE_OK, let prepared else { throw CampCloudError.cacheUnavailable }
        for (offset, value) in values.enumerated() {
            let index = Int32(offset + 1)
            let result: Int32
            switch value {
            case .text(let string): result = string.withCString { sqlite3_bind_text(prepared, index, $0, Int32(string.utf8.count), transient) }
            case .integer(let number): result = sqlite3_bind_int64(prepared, index, number)
            case .blob(let data): result = data.isEmpty ? sqlite3_bind_zeroblob(prepared,index,0) : data.withUnsafeBytes { sqlite3_bind_blob(prepared, index, $0.baseAddress, Int32(data.count), transient) }
            }
            if result != SQLITE_OK { sqlite3_finalize(prepared); throw CampCloudError.cacheUnavailable }
        }
        return prepared
    }
    private func execute(_ sql: String, _ values: [Value] = []) throws {
        let statement = try statement(sql, values); defer { sqlite3_finalize(statement) }
        guard sqlite3_step(statement) == SQLITE_DONE else { throw CampCloudError.cacheUnavailable }
    }
    private func rows<T>(_ sql: String, _ values: [Value] = [], map: (OpaquePointer) throws -> T) throws -> [T] {
        let statement = try statement(sql, values); defer { sqlite3_finalize(statement) }
        var result: [T] = []
        while true {
            switch sqlite3_step(statement) {
            case SQLITE_ROW: result.append(try map(statement))
            case SQLITE_DONE: return result
            default: throw CampCloudError.cacheUnavailable
            }
        }
    }
    private func blob(_ statement: OpaquePointer, _ index: Int32) -> Data {
        guard let bytes = sqlite3_column_blob(statement, index) else { return Data() }
        return Data(bytes: bytes, count: Int(sqlite3_column_bytes(statement, index)))
    }
    private func text(_ statement: OpaquePointer, _ index: Int32) -> String {
        guard let bytes = sqlite3_column_text(statement, index) else { return "" }
        return String(decoding: UnsafeBufferPointer(start: bytes, count: Int(sqlite3_column_bytes(statement, index))), as: UTF8.self)
    }
    private func transaction<T>(_ body: () throws -> T) throws -> T {
        try execute("BEGIN IMMEDIATE")
        do { let value = try body(); try execute("COMMIT"); return value }
        catch { try? execute("ROLLBACK"); throw error }
    }
    func metadata(_ key: String) throws -> Data? { try rows("SELECT value FROM metadata WHERE key=?", [.text(key)]) { blob($0, 0) }.first }
    func setMetadata(_ key: String, _ data: Data) throws { try execute("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", [.text(key), .blob(data)]) }
    func saveSnapshot(_ snapshot: CloudSnapshot, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try setMetadata("snapshot:" + scope.key, CampWire.encoder().encode(snapshot))
        }
    }
    func cursor(_ scope: CloudScope) throws -> Int64 {
        guard let data = try metadata("cursor:" + scope.key), let string = String(data: data, encoding: .utf8), let value = Int64(string) else { return 0 }
        return value
    }
    func epoch(_ scope: CloudScope) throws -> Int64 {
        guard let data = try metadata("epoch:" + scope.key), let text = String(data: data, encoding: .utf8), let epoch = Int64(text) else { return 0 }
        return epoch
    }
    private func checkEpoch(_ scope: CloudScope, _ expected: Int64?) throws {
        if let expected, try epoch(scope) != expected { throw CampCloudError.server(409, "cache_scope_changed") }
    }
    private func save(_ session: CloudSession, scope: CloudScope) throws {
        try execute("""
        INSERT INTO sessions(scope,id,revision,transcript_revision,body) SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM tombstones WHERE scope=? AND id=? AND revision>=?)
        ON CONFLICT(scope,id) DO UPDATE SET revision=excluded.revision,transcript_revision=excluded.transcript_revision,body=excluded.body WHERE excluded.revision>sessions.revision OR (excluded.revision=sessions.revision AND excluded.transcript_revision>=sessions.transcript_revision)
        """, [.text(scope.key), .text(session.id), .integer(session.revision), .integer(session.transcriptRevision), .blob(try CampWire.encoder().encode(session)), .text(scope.key), .text(session.id), .integer(session.revision)])
    }
    private func save(_ request: CloudRequest, scope: CloudScope) throws {
        var request = request; request.leaseToken = nil
        try execute("INSERT INTO requests(scope,id,revision,body) VALUES(?,?,?,?) ON CONFLICT(scope,id) DO UPDATE SET revision=excluded.revision,body=excluded.body WHERE excluded.revision>=requests.revision",
                    [.text(scope.key), .text(request.id), .integer(request.revision), .blob(try CampWire.encoder().encode(request))])
    }
    func replaceSessions(_ sessions: [CloudSession], scope: CloudScope, cursor: Int64, expectedEpoch: Int64? = nil) throws {
        guard cursor >= (try self.cursor(scope)) else { throw CampCloudError.server(409, "server_history_changed") }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try execute("DELETE FROM sessions WHERE scope=?", [.text(scope.key)])
            for session in sessions { try save(session, scope: scope) }
            try execute("DELETE FROM messages WHERE scope=? AND session_id NOT IN (SELECT id FROM sessions WHERE scope=?)", [.text(scope.key), .text(scope.key)])
            try execute("DELETE FROM requests WHERE scope=? AND json_extract(CAST(body AS TEXT),'$.session_id') NOT IN (SELECT id FROM sessions WHERE scope=?)", [.text(scope.key), .text(scope.key)])
            try setMetadata("cursor:" + scope.key, Data(String(cursor).utf8))
        }
    }
    func ingest(scope: CloudScope, cursor: Int64? = nil, session: CloudSession? = nil, request: CloudRequest? = nil, removed: (String, Int64)? = nil, expectedEpoch: Int64? = nil) throws {
        if let cursor, cursor <= (try self.cursor(scope)) { return }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            if let session { try save(session, scope: scope) }
            if let request { try save(request, scope: scope) }
            if let (id, revision) = removed {
                try execute("INSERT INTO tombstones(scope,id,revision) VALUES(?,?,?) ON CONFLICT(scope,id) DO UPDATE SET revision=MAX(revision,excluded.revision)", [.text(scope.key), .text(id), .integer(revision)])
                try execute("DELETE FROM sessions WHERE scope=? AND id=? AND revision<=?", [.text(scope.key), .text(id), .integer(revision)])
                try execute("DELETE FROM messages WHERE scope=? AND session_id=? AND NOT EXISTS(SELECT 1 FROM sessions WHERE scope=? AND id=? AND revision>?)", [.text(scope.key), .text(id), .text(scope.key), .text(id), .integer(revision)])
                try execute("DELETE FROM requests WHERE scope=? AND json_extract(CAST(body AS TEXT),'$.session_id')=? AND NOT EXISTS(SELECT 1 FROM sessions WHERE scope=? AND id=? AND revision>?)", [.text(scope.key), .text(id), .text(scope.key), .text(id), .integer(revision)])
            }
            if let cursor { try setMetadata("cursor:" + scope.key, Data(String(cursor).utf8)) }
        }
    }
    func sessions(_ scope: CloudScope) throws -> [CloudSession] {
        try rows("SELECT body FROM sessions WHERE scope=? ORDER BY id", [.text(scope.key)]) { try CampWire.decoder().decode(CloudSession.self, from: blob($0, 0)) }.sorted { $0.updatedAt > $1.updatedAt }
    }
    func requests(_ scope: CloudScope) throws -> [CloudRequest] {
        try rows("SELECT body FROM requests WHERE scope=? ORDER BY rowid DESC LIMIT 1000", [.text(scope.key)]) { try CampWire.decoder().decode(CloudRequest.self, from: blob($0, 0)) }.sorted { $0.createdAt > $1.createdAt }
    }
    func saveTranscript(_ transcript: CloudTranscript, scope: CloudScope, sessionId: String, before: Int64 = 0, expectedPermissionRevision: Int64? = nil, expectedEpoch: Int64? = nil) throws {
        guard transcript.revision >= 0, before >= 0, transcript.messages.count <= 200 else { throw CampCloudError.invalidResponse }
        let floor = transcript.hasMore ? transcript.before : 0
        let ceiling = before == 0 ? Int64.max : before
        guard floor >= 0, floor <= ceiling, transcript.messages.allSatisfy({ $0.ordinal >= floor && $0.ordinal < ceiling && $0.revision <= transcript.revision }) else { throw CampCloudError.invalidResponse }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            let permissions = try rows("SELECT json_extract(CAST(body AS TEXT),'$.permission_revision') FROM sessions WHERE scope=? AND id=?", [.text(scope.key), .text(sessionId)]) { sqlite3_column_int64($0, 0) }
            guard let permission = permissions.first, expectedPermissionRevision == nil || permission == expectedPermissionRevision else { throw CampCloudError.server(404, "session_unavailable") }
            // Each page is authoritative for its ordinal window, including messages
            // removed on the server. A pinned older page cannot erase newer revisions.
            let currentIDs = try CampWire.encoder().encode(transcript.messages.map(\.id))
            try execute("DELETE FROM messages WHERE scope=? AND session_id=? AND ordinal>=? AND ordinal<? AND revision<=? AND id NOT IN (SELECT value FROM json_each(?))",
                        [.text(scope.key), .text(sessionId), .integer(floor), .integer(ceiling), .integer(transcript.revision), .blob(currentIDs)])
            for message in transcript.messages {
                try execute("INSERT INTO messages(scope,session_id,id,revision,ordinal,removed,body) VALUES(?,?,?,?,?,?,?) ON CONFLICT(scope,session_id,id) DO UPDATE SET revision=excluded.revision,ordinal=excluded.ordinal,removed=excluded.removed,body=excluded.body WHERE excluded.revision>=messages.revision",
                            [.text(scope.key), .text(sessionId), .text(message.id), .integer(message.revision), .integer(message.ordinal), .integer(message.removed ? 1 : 0), .blob(try CampWire.encoder().encode(message))])
            }
        }
    }
    func transcript(_ scope: CloudScope, sessionId: String, limit: Int = 120) throws -> [CloudMessage] {
        guard (1...200).contains(limit) else { throw CampCloudError.cacheUnavailable }
        return Array(try rows("SELECT body FROM messages WHERE scope=? AND session_id=? AND removed=0 ORDER BY ordinal DESC LIMIT ?", [.text(scope.key), .text(sessionId), .integer(Int64(limit))]) {
            try CampWire.decoder().decode(CloudMessage.self, from: blob($0, 0))
        }.reversed())
    }
    func enqueue(scope: CloudScope, method: String, path: String, body: Data, id: String = UUID().uuidString.lowercased(), expectedEpoch: Int64? = nil) throws -> String {
        try checkEpoch(scope, expectedEpoch)
        let endpoint = try CampEndpoint(scope.origin, allowLoopback: true)
        _ = try endpoint.requestURL(path)
        guard UUID(uuidString: id) != nil, body.count <= 2 << 20, ["POST", "PUT", "PATCH", "DELETE"].contains(method),
              path.hasPrefix("/v1/camps/" + scope.campId + "/") || path == "/v1/profiles" else { throw CampCloudError.invalidResponse }
        try execute("INSERT INTO outbox(id,scope,method,path,body,state,created) VALUES(?,?,?,?,?,'pending',?)", [.text(id), .text(scope.key), .text(method), .text(path), .blob(body), .integer(Int64(Date().timeIntervalSince1970 * 1000))])
        return id
    }
    func outbox(_ scope: CloudScope, pendingOnly: Bool = false) throws -> [CloudOutboxItem] {
        let suffix = pendingOnly ? " AND state='pending'" : ""
        return try rows("SELECT id,scope,method,path,body,state,error FROM outbox WHERE scope=?" + suffix + " ORDER BY created,rowid LIMIT 100", [.text(scope.key)]) {
            CloudOutboxItem(id: text($0, 0), scope: text($0, 1), method: text($0, 2), path: text($0, 3), body: blob($0, 4), state: text($0, 5), error: text($0, 6))
        }
    }
    func markOutbox(_ id: String, state: String, error: String = "") throws {
        guard ["pending", "sending", "conflict", "failed"].contains(state) else { throw CampCloudError.cacheUnavailable }
        try execute("UPDATE outbox SET state=?,error=? WHERE id=?", [.text(state), .text(error), .text(id)])
    }
    func acknowledge(_ id: String) throws { try execute("DELETE FROM outbox WHERE id=?", [.text(id)]) }
    func saveAttempt(_ attempt: CloudAttempt) throws {
        try execute("INSERT INTO attempts(id,scope,state,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,body=excluded.body", [.text(attempt.id), .text(attempt.scope), .text(attempt.state), .blob(try CampWire.encoder().encode(attempt))])
    }
    func saveWorkerAttempt(_ attempt: CloudAttempt, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            guard attempt.scope == scope.key, attempt.request.campId == scope.campId,
                  attempt.request.ownerUserId == scope.userId else { throw CampCloudError.invalidResponse }
            try saveAttempt(attempt)
        }
    }
    func workerClaim(scope: CloudScope, sessions: [String], expectedEpoch: Int64) throws -> CampPublication {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            let key = "worker-claim:" + scope.key
            if let saved = try metadata(key) { return try CampWire.decoder().decode(CampPublication.self, from: saved) }
            let command = CampPublication(kind: "claim", method: "POST", path: "/v1/camps/\(scope.campId)/requests/claim", body: try CampWire.body(["session_ids": sessions]))
            try setMetadata(key, CampWire.encoder().encode(command)); return command
        }
    }
    func finishWorkerClaim(_ attempt: CloudAttempt?, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            if let attempt {
                guard attempt.scope == scope.key, attempt.request.campId == scope.campId,
                      attempt.request.ownerUserId == scope.userId else { throw CampCloudError.invalidResponse }
                try saveAttempt(attempt)
            }
            try execute("DELETE FROM metadata WHERE key=?", [.text("worker-claim:" + scope.key)])
        }
    }
    func removeWorkerAttempt(_ id: String, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try execute("DELETE FROM attempts WHERE id=? AND scope=?", [.text(id), .text(scope.key)])
        }
    }
    func shares(_ scope: CloudScope) throws -> [LocalShare] {
        try rows("SELECT body FROM share_bindings WHERE scope=? ORDER BY id", [.text(scope.key)]) { try JSONDecoder().decode(LocalShare.self, from: blob($0, 0)) }
    }
    func saveShare(_ value: LocalShare, scope: CloudScope, published: [PublishedDigest] = [], expectedEpoch: Int64? = nil) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try execute("INSERT INTO share_bindings(scope,id,body) VALUES(?,?,?) ON CONFLICT(scope,id) DO UPDATE SET body=excluded.body", [.text(scope.key), .text(value.id), .blob(try JSONEncoder().encode(value))])
            for hash in published {
                try execute("INSERT INTO published_message_hashes(scope,session_id,id,digest) VALUES(?,?,?,?) ON CONFLICT(scope,session_id,id) DO UPDATE SET digest=excluded.digest", [.text(scope.key), .text(value.id), .text(hash.id), .text(hash.digest)])
            }
        }
    }
    func unpublished(_ messages: [PublishMessage], scope: CloudScope, sessionId: String) throws -> [PublishMessage] {
        let ids = try CampWire.encoder().encode(messages.map(\.id))
        let pairs = try rows("SELECT id,digest FROM published_message_hashes WHERE scope=? AND session_id=? AND id IN (SELECT value FROM json_each(?))", [.text(scope.key), .text(sessionId), .blob(ids)]) { (text($0, 0), text($0, 1)) }
        let saved = Dictionary(pairs, uniquingKeysWith: { _, new in new })
        return messages.filter { saved[$0.id] != $0.digest }
    }
    func attempts(_ scope: CloudScope) throws -> [CloudAttempt] {
        try rows("SELECT state,body FROM attempts WHERE scope=?", [.text(scope.key)]) {
            var result = try CampWire.decoder().decode(CloudAttempt.self, from: blob($0, 1)); result.state = text($0, 0); return result
        }
    }
    func clear(_ scope: CloudScope) throws {
        try transaction {
            try setMetadata("epoch:" + scope.key, Data(String(try epoch(scope) + 1).utf8))
            for table in ["sessions", "requests", "messages", "tombstones", "outbox", "share_bindings", "published_message_hashes"] { try execute("DELETE FROM \(table) WHERE scope=?", [.text(scope.key)]) }
            try execute("DELETE FROM metadata WHERE key=?", [.text("cursor:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("snapshot:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("worker-claim:" + scope.key)])
            for var attempt in try attempts(scope) {
                if ["completed", "failed", "cancelled"].contains(attempt.state) {
                    try execute("DELETE FROM attempts WHERE id=?", [.text(attempt.id)]); continue
                }
                attempt.request.prompt = ""; attempt.request.sources = []; attempt.state = "uncertain"
                try saveAttempt(attempt)
            }
        }
    }
}
