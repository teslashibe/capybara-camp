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
    var sessionID: String = ""
}
struct CloudAttempt: Codable, Identifiable, Sendable {
    var id: String; var scope: String; var request: CloudRequest; var localSessionId: String
    var state: String; var startKey: String; var finishKey: String
    var outcome: String?
    var evidence: String?
    var output: String?
    var usage: ConversationUsage?
    var localProviderSessionId: String? = nil
    var pendingStream: Data? = nil
    var deliveredControls: [String]? = nil
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
        guard (0...5).contains(version) else { sqlite3_close(database); db = nil; throw CampCloudError.cacheUnavailable }
        let schema = """
        PRAGMA journal_mode=WAL;
        PRAGMA synchronous=FULL;
        BEGIN IMMEDIATE;
        CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS sessions(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,transcript_revision INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS tombstones(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS requests(scope TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS messages(scope TEXT NOT NULL,session_id TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,ordinal INTEGER NOT NULL,removed INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,session_id,id));
        CREATE INDEX IF NOT EXISTS transcript_window ON messages(scope,session_id,ordinal DESC);
        CREATE TABLE IF NOT EXISTS child_cache(scope TEXT NOT NULL,parent_id TEXT NOT NULL,cache_key TEXT NOT NULL,permission_revision INTEGER NOT NULL,parent_revision INTEGER NOT NULL,body BLOB NOT NULL,saved_at INTEGER NOT NULL,PRIMARY KEY(scope,parent_id,cache_key));
        CREATE INDEX IF NOT EXISTS child_cache_age ON child_cache(saved_at);
        CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,scope TEXT NOT NULL,method TEXT NOT NULL,path TEXT NOT NULL,body BLOB NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created REAL NOT NULL);
        CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(scope,state,created);
        CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY,scope TEXT NOT NULL,state TEXT NOT NULL,body BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS share_bindings(scope TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(scope,id));
        CREATE TABLE IF NOT EXISTS published_message_hashes(scope TEXT NOT NULL,session_id TEXT NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(scope,session_id,id));
        CREATE TABLE IF NOT EXISTS local_projects(id TEXT PRIMARY KEY,name TEXT NOT NULL,revision INTEGER NOT NULL,archived INTEGER NOT NULL DEFAULT 0);
        CREATE TABLE IF NOT EXISTS local_project_repositories(project_id TEXT NOT NULL,url TEXT NOT NULL,PRIMARY KEY(project_id,url));
        CREATE TABLE IF NOT EXISTS local_project_assignments(ticket_id TEXT PRIMARY KEY,project_id TEXT NOT NULL);
        \(version < 4 ? "ALTER TABLE outbox ADD COLUMN session_id TEXT NOT NULL DEFAULT '';" : "")
        UPDATE outbox SET state='pending' WHERE state='sending';
        UPDATE attempts SET state='uncertain' WHERE state IN ('start_pending','running');
        PRAGMA user_version=5;
        COMMIT;
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
    func saveExecutionJournal(_ data: Data, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try setMetadata("execution-setup:" + scope.key, data)
        }
    }
    func savePlanImport(_ value: PlanImportJournal?, scope: CloudScope, expectedEpoch: Int64) throws {
        let data = try value.map { try CampWire.encoder().encode($0) }
        guard value?.valid ?? true, (data?.count ?? 0) <= 24 << 20 else { throw CampCloudError.invalidResponse }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            if let data { try setMetadata("planning-import:" + scope.key, data) }
            else { try execute("DELETE FROM metadata WHERE key=?", [.text("planning-import:" + scope.key)]) }
        }
    }
    func savePlanPromotion(_ data: Data, scope: CloudScope, expectedEpoch: Int64) throws {
        guard data.count <= 24 << 20 else { throw CampCloudError.responseTooLarge }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try setMetadata("plan-promotion:" + scope.key, data)
        }
    }
    func saveHandoffJournal(_ data: Data, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try setMetadata("source-handoff:" + scope.key, data)
        }
    }
    func saveTransferJournal(_ data: Data, payload: Data? = nil, id: String, scope: CloudScope, expectedEpoch: Int64, discardPayload: Bool = false) throws {
        guard UUID(uuidString:id) != nil, data.count <= 512 << 10, (payload?.count ?? 0) <= 46 << 20 else { throw CampCloudError.invalidResponse }
        try transaction {
            try checkEpoch(scope,expectedEpoch)
            if let payload {
                try setMetadata("transfer-payload:"+scope.key+":"+id,payload)
                try execute("DELETE FROM metadata WHERE key=?",[.text("transfer-cancel:"+scope.key)])
            }
            if discardPayload { try execute("DELETE FROM metadata WHERE key=?",[.text("transfer-payload:"+scope.key+":"+id)]) }
            if discardPayload { try execute("DELETE FROM metadata WHERE key=?",[.text("transfer-cancel:"+scope.key)]) }
            try setMetadata("transfer-review:"+scope.key,data)
        }
    }
    func requestTransferCancellation(_ id:String,scope:CloudScope,expectedEpoch:Int64) throws {
        guard UUID(uuidString:id) != nil else {throw CampCloudError.invalidResponse}
        try transaction {try checkEpoch(scope,expectedEpoch);try setMetadata("transfer-cancel:"+scope.key,Data(id.utf8))}
    }
    func localProjectState() throws -> LocalProjectState {
        var projects = try rows("SELECT id,name,revision,archived FROM local_projects ORDER BY archived,name,id") { CampProject(id: text($0,0), name: text($0,1), revision: sqlite3_column_int64($0,2), archived: sqlite3_column_int($0,3) != 0, repositories: []) }
        let links = try rows("SELECT project_id,url FROM local_project_repositories ORDER BY url") { (text($0,0),text($0,1)) }
        let urls = Dictionary(grouping: links, by: { $0.0 }).mapValues { $0.map { $0.1 } }
        for index in projects.indices { projects[index].repositories = urls[projects[index].id] ?? [] }
        let assignments = try rows("SELECT ticket_id,project_id FROM local_project_assignments") { (text($0,0),text($0,1)) }
        let revision = try metadata("local-project-revision").flatMap { Int64(String(decoding:$0,as:UTF8.self)) } ?? 0
        return LocalProjectState(revision: revision, projects: projects, assignments: Dictionary(uniqueKeysWithValues: assignments))
    }
    func saveLocalProject(_ value: CampProject, expectedRevision: Int64) throws -> LocalProjectState {
        guard UUID(uuidString:value.id) != nil, !value.name.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty, value.name.utf8.count <= 120, !value.isInbox else { throw CampCloudError.invalidResponse }
        let urls = try ProjectRepositories.normalize(value.repositoryURLs)
        return try transaction {
            let current = try rows("SELECT revision FROM local_projects WHERE id=?", [.text(value.id)]) { sqlite3_column_int64($0,0) }.first ?? 0
            guard current == expectedRevision else { throw CampError.message("This project changed. Reopen it before saving") }
            try execute("INSERT INTO local_projects(id,name,revision,archived) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,revision=excluded.revision,archived=excluded.archived", [.text(value.id),.text(value.name.trimmingCharacters(in:.whitespacesAndNewlines)),.integer(current+1),.integer(value.isArchived ? 1:0)])
            try execute("DELETE FROM local_project_repositories WHERE project_id=?",[.text(value.id)])
            for url in urls { try execute("INSERT INTO local_project_repositories(project_id,url) VALUES(?,?)",[.text(value.id),.text(url)]) }
            try bumpProjectRevision(); return try localProjectState()
        }
    }
    func assignLocalProject(ticketID: String, projectID: String?) throws -> LocalProjectState {
        guard !ticketID.isEmpty, ticketID.utf8.count <= 8192 else { throw CampCloudError.invalidResponse }
        return try transaction {
            if let projectID {
                guard try rows("SELECT id FROM local_projects WHERE id=? AND archived=0",[.text(projectID)],map:{text($0,0)}).first != nil else { throw CampError.message("Choose an active local project") }
                try execute("INSERT INTO local_project_assignments(ticket_id,project_id) VALUES(?,?) ON CONFLICT(ticket_id) DO UPDATE SET project_id=excluded.project_id",[.text(ticketID),.text(projectID)])
            } else { try execute("DELETE FROM local_project_assignments WHERE ticket_id=?",[.text(ticketID)]) }
            try bumpProjectRevision(); return try localProjectState()
        }
    }
    private func bumpProjectRevision() throws {
        let revision = try metadata("local-project-revision").flatMap { Int64(String(decoding:$0,as:UTF8.self)) } ?? 0
        try setMetadata("local-project-revision",Data(String(revision+1).utf8))
    }
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
        ON CONFLICT(scope,id) DO UPDATE SET revision=excluded.revision,transcript_revision=excluded.transcript_revision,body=excluded.body WHERE excluded.revision>sessions.revision OR (excluded.revision=sessions.revision AND excluded.transcript_revision>=sessions.transcript_revision AND COALESCE(json_extract(CAST(excluded.body AS TEXT),'$.activity_revision'),0)>=COALESCE(json_extract(CAST(sessions.body AS TEXT),'$.activity_revision'),0))
        """, [.text(scope.key), .text(session.id), .integer(session.revision), .integer(session.transcriptRevision), .blob(try CampWire.encoder().encode(session)), .text(scope.key), .text(session.id), .integer(session.revision)])
    }
    private func save(_ request: CloudRequest, scope: CloudScope) throws {
        var request = request
        let previous = try rows("SELECT body FROM requests WHERE scope=? AND id=?", [.text(scope.key), .text(request.id)]) { try CampWire.decoder().decode(CloudRequest.self, from: blob($0, 0)) }.first
        request = request.merging(previous)
        request.leaseToken = nil
        try execute("INSERT INTO requests(scope,id,revision,body) VALUES(?,?,?,?) ON CONFLICT(scope,id) DO UPDATE SET revision=excluded.revision,body=excluded.body WHERE excluded.revision>=requests.revision",
                    [.text(scope.key), .text(request.id), .integer(request.revision), .blob(try CampWire.encoder().encode(request))])
    }
    func replaceSessions(_ sessions: [CloudSession], scope: CloudScope, cursor: Int64, expectedEpoch: Int64? = nil) throws {
        guard cursor >= (try self.cursor(scope)) else { throw CampCloudError.server(409, "server_history_changed") }
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try execute("DELETE FROM sessions WHERE scope=?", [.text(scope.key)])
            for session in sessions { try save(session, scope: scope) }
            try purgeUnavailableChildren(scope)
            try execute("DELETE FROM messages WHERE scope=? AND session_id NOT IN (SELECT id FROM sessions WHERE scope=?)", [.text(scope.key), .text(scope.key)])
            try execute("DELETE FROM requests WHERE scope=? AND json_extract(CAST(body AS TEXT),'$.session_id') NOT IN (SELECT id FROM sessions WHERE scope=?)", [.text(scope.key), .text(scope.key)])
            try execute("DELETE FROM outbox WHERE scope=? AND session_id<>'' AND session_id NOT IN (SELECT id FROM sessions WHERE scope=?)", [.text(scope.key),.text(scope.key)])
            try setMetadata("cursor:" + scope.key, Data(String(cursor).utf8))
        }
    }
    func ingest(scope: CloudScope, cursor: Int64? = nil, session: CloudSession? = nil, request: CloudRequest? = nil, removed: (String, Int64)? = nil, expectedEpoch: Int64? = nil, childrenChanged: String? = nil) throws {
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
                try execute("DELETE FROM outbox WHERE scope=? AND session_id=? AND NOT EXISTS(SELECT 1 FROM sessions WHERE scope=? AND id=? AND revision>?)", [.text(scope.key),.text(id),.text(scope.key),.text(id),.integer(revision)])
            }
            if session != nil || removed != nil {try purgeUnavailableChildren(scope)}
            if let childrenChanged {try invalidateChildHistory(scope:scope,parent:childrenChanged)}
            if let cursor { try setMetadata("cursor:" + scope.key, Data(String(cursor).utf8)) }
        }
    }
    func sessions(_ scope: CloudScope) throws -> [CloudSession] {
        try rows("SELECT body FROM sessions WHERE scope=? ORDER BY id", [.text(scope.key)]) { try CampWire.decoder().decode(CloudSession.self, from: blob($0, 0)) }.sorted { $0.updatedAt > $1.updatedAt }
    }
    func requests(_ scope: CloudScope) throws -> [CloudRequest] {
        try rows("SELECT body FROM requests WHERE scope=? ORDER BY rowid DESC LIMIT 1000", [.text(scope.key)]) { try CampWire.decoder().decode(CloudRequest.self, from: blob($0, 0)) }.sorted { $0.createdAt > $1.createdAt }
    }
    func notifications(_ scope: CloudScope) throws -> [CloudNotification] {
        guard let data = try metadata("notifications:" + scope.key) else { return [] }
        return try CampWire.decoder().decode([CloudNotification].self, from: data)
    }
    func saveNotifications(_ values: [CloudNotification], scope: CloudScope, expectedEpoch: Int64) throws {
        try checkEpoch(scope, expectedEpoch)
        guard values.count <= 1000, values.allSatisfy(\.valid), Set(values.map(\.id)).count == values.count else { throw CampCloudError.invalidResponse }
        try setMetadata("notifications:" + scope.key, CampWire.encoder().encode(values))
    }
    func planning(_ scope: CloudScope) throws -> CampPlanningState {
        guard let data = try metadata("planning:" + scope.key) else { return CampPlanningState() }
        return try CampWire.decoder().decode(CampPlanningState.self, from: data)
    }
    func savePlanning(_ state: CampPlanningState, scope: CloudScope, expectedEpoch: Int64) throws {
        try checkEpoch(scope, expectedEpoch)
        guard state.drafts.count <= 50, state.tickets.count <= 100, state.details.count <= 100, state.operations.count <= 100,
              state.tickets.allSatisfy(\.valid), state.details.values.allSatisfy({ $0.valid && $0.campId == scope.campId }) else { throw CampCloudError.invalidResponse }
        let old = try planning(scope)
        guard state.revision >= old.revision else { return }
        let data = try CampWire.encoder().encode(state)
        guard data.count <= 8 << 20 else { throw CampCloudError.responseTooLarge }
        try setMetadata("planning:" + scope.key, data)
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
    func enqueue(scope: CloudScope, method: String, path: String, body: Data, id: String = UUID().uuidString.lowercased(), sessionID: String = "", expectedEpoch: Int64? = nil) throws -> String {
        try checkEpoch(scope, expectedEpoch)
        let endpoint = try CampEndpoint(scope.origin, allowLoopback: true)
        _ = try endpoint.requestURL(path)
        guard UUID(uuidString: id) != nil, sessionID.isEmpty || UUID(uuidString: sessionID) != nil, body.count <= 2 << 20, ["POST", "PUT", "PATCH", "DELETE"].contains(method),
              path.hasPrefix("/v1/camps/" + scope.campId + "/") || path == "/v1/profiles" else { throw CampCloudError.invalidResponse }
        try execute("INSERT INTO outbox(id,scope,method,path,body,state,created,session_id) VALUES(?,?,?,?,?,'pending',?,?)", [.text(id), .text(scope.key), .text(method), .text(path), .blob(body), .integer(Int64(Date().timeIntervalSince1970 * 1000)), .text(sessionID)])
        return id
    }
    func outbox(_ scope: CloudScope, pendingOnly: Bool = false, limit: Int = 100) throws -> [CloudOutboxItem] {
        guard (1...1000).contains(limit) else { throw CampCloudError.invalidResponse }
        let suffix = pendingOnly ? " AND state='pending'" : ""
        return try rows("SELECT id,scope,method,path,body,state,error,session_id FROM outbox WHERE scope=?" + suffix + " ORDER BY created,rowid LIMIT ?", [.text(scope.key), .integer(Int64(limit))]) {
            CloudOutboxItem(id: text($0, 0), scope: text($0, 1), method: text($0, 2), path: text($0, 3), body: blob($0, 4), state: text($0, 5), error: text($0, 6), sessionID: text($0,7))
        }
    }
    func markOutbox(_ id: String, state: String, error: String = "") throws {
        guard ["pending", "sending", "waiting", "conflict", "failed"].contains(state) else { throw CampCloudError.cacheUnavailable }
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
            var value = attempt
            if let old = try workerAttempt(value.id, scope: scope), old.request.isImplementation {
                value.pendingStream = old.pendingStream
                value.request.streamSeq = old.request.streamSeq
                value.deliveredControls = old.deliveredControls
                if let id = old.request.providerSessionId, !id.isEmpty { value.request.providerSessionId = id }
            }
            try saveAttempt(value)
        }
    }
    private func workerAttempt(_ id: String, scope: CloudScope) throws -> CloudAttempt? {
        try rows("SELECT state,body FROM attempts WHERE id=? AND scope=?", [.text(id), .text(scope.key)]) {
            var result = try CampWire.decoder().decode(CloudAttempt.self, from: blob($0, 1)); result.state = text($0, 0); return result
        }.first
    }
    func workerClaim(scope: CloudScope, sessions: [String], expectedEpoch: Int64) throws -> CampPublication {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            let key = "worker-claim:" + scope.key
            if let saved = try metadata(key) { return try CampWire.decoder().decode(CampPublication.self, from: saved) }
            let command = CampPublication(kind: "claim", method: "POST", path: "/v1/camps/\(scope.campId)/requests/claim", body: try CampWire.body(["session_ids": sessions, "include_planning": true, "include_implementation": true]))
            try setMetadata(key, CampWire.encoder().encode(command)); return command
        }
    }
    func stageWorkerEvent(_ id: String, event: Data, scope: CloudScope, expectedEpoch: Int64) throws -> Data {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            guard var attempt = try workerAttempt(id, scope: scope), attempt.request.isImplementation,
                  attempt.state == "running", event.count <= 128 << 10 else { throw CampCloudError.invalidResponse }
            if let pending = attempt.pendingStream {
                guard let value = try JSONSerialization.jsonObject(with: pending) as? [String: Any],
                      let previous = value["event"], try JSONSerialization.data(withJSONObject: previous, options: .sortedKeys) == event else { throw CampCloudError.invalidResponse }
                return pending
            }
            let seq = (attempt.request.streamSeq ?? 0) + 1
            guard seq <= 32768 else { throw CampCloudError.responseTooLarge }
            let body = try CampWire.body(["seq": seq, "event": JSONSerialization.jsonObject(with: event), "lease_token": attempt.request.leaseToken ?? ""])
            attempt.pendingStream = body; try saveAttempt(attempt); return body
        }
    }
    func acknowledgeWorkerEvent(_ id: String, body: Data, seq: Int64, scope: CloudScope, expectedEpoch: Int64) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            guard var attempt = try workerAttempt(id, scope: scope), attempt.pendingStream == body,
                  let value = try JSONSerialization.jsonObject(with: body) as? [String: Any],
                  (value["seq"] as? NSNumber)?.int64Value == seq else { throw CampCloudError.invalidResponse }
            if let event = value["event"] as? [String: Any], event["type"] as? String == "session", let providerID = event["sessionID"] as? String {
                attempt.request.providerSessionId = providerID
            }
            attempt.request.streamSeq = seq; attempt.pendingStream = nil; try saveAttempt(attempt)
        }
    }
    func markWorkerControl(_ id: String, control: String, scope: CloudScope, expectedEpoch: Int64) throws -> Bool {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            guard var attempt = try workerAttempt(id, scope: scope), attempt.state == "running", UUID(uuidString: control) != nil else { throw CampCloudError.invalidResponse }
            var delivered = attempt.deliveredControls ?? []
            if delivered.contains(control) { return false }
            guard delivered.count < 64 else { throw CampCloudError.responseTooLarge }
            delivered.append(control); attempt.deliveredControls = delivered; try saveAttempt(attempt); return true
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
    func saveShare(_ value: LocalShare, scope: CloudScope, published: [PublishedDigest] = [], publishedSessionID: String? = nil, expectedEpoch: Int64? = nil) throws {
        try transaction {
            try checkEpoch(scope, expectedEpoch)
            try execute("INSERT INTO share_bindings(scope,id,body) VALUES(?,?,?) ON CONFLICT(scope,id) DO UPDATE SET body=excluded.body", [.text(scope.key), .text(value.id), .blob(try JSONEncoder().encode(value))])
            for hash in published {
                try execute("INSERT INTO published_message_hashes(scope,session_id,id,digest) VALUES(?,?,?,?) ON CONFLICT(scope,session_id,id) DO UPDATE SET digest=excluded.digest", [.text(scope.key), .text(publishedSessionID ?? value.id), .text(hash.id), .text(hash.digest)])
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
    private func purgeUnavailableChildren(_ scope:CloudScope)throws {
        try execute("DELETE FROM child_cache WHERE scope=? AND NOT EXISTS(SELECT 1 FROM sessions s WHERE s.scope=child_cache.scope AND s.id=child_cache.parent_id AND json_extract(CAST(s.body AS TEXT),'$.permission_revision')=child_cache.permission_revision)",[.text(scope.key)])
    }
    func childEpoch(_ scope:CloudScope,parent:String)throws->Int64 {
        guard let data=try metadata("child-epoch:"+scope.key+":"+parent) else{return 0}
        guard let value=Int64(String(decoding:data,as:UTF8.self)),value>=0 else{throw CampCloudError.cacheUnavailable}
        return value
    }
    private func checkChildEpoch(_ scope:CloudScope,parent:String,expected:Int64)throws {
        guard try childEpoch(scope,parent:parent)==expected else{throw CancellationError()}
    }
    private func invalidateChildHistory(scope:CloudScope,parent:String)throws {
        let epoch=try childEpoch(scope,parent:parent)
        guard epoch<Int64.max else{throw CampCloudError.cacheUnavailable}
        try setMetadata("child-epoch:"+scope.key+":"+parent,Data(String(epoch+1).utf8))
        try execute("DELETE FROM child_cache WHERE scope=? AND parent_id=?",[.text(scope.key),.text(parent)])
    }
    func invalidateChildHistory(scope:CloudScope,parent:String,expectedEpoch:Int64,expectedChildEpoch:Int64)throws {
        try transaction {
            try checkEpoch(scope,expectedEpoch);try checkChildEpoch(scope,parent:parent,expected:expectedChildEpoch)
            try invalidateChildHistory(scope:scope,parent:parent)
        }
    }
    private func requireChildParent(_ scope:CloudScope,_ parent:String,_ permission:Int64)throws {
        let values=try rows("SELECT json_extract(CAST(body AS TEXT),'$.permission_revision') FROM sessions WHERE scope=? AND id=?",[.text(scope.key),.text(parent)]){sqlite3_column_int64($0,0)}
        guard permission>0,values.first==permission else{throw CampCloudError.server(404,"session_unavailable")}
    }
    private func childCacheWrite(_ data:Data,scope:CloudScope,parent:String,key:String,permission:Int64,revision:Int64)throws {
        guard data.count<=4<<20 else{throw CampCloudError.responseTooLarge}
        try execute("INSERT INTO child_cache(scope,parent_id,cache_key,permission_revision,parent_revision,body,saved_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(scope,parent_id,cache_key) DO UPDATE SET permission_revision=excluded.permission_revision,parent_revision=excluded.parent_revision,body=excluded.body,saved_at=excluded.saved_at",[.text(scope.key),.text(parent),.text(key),.integer(permission),.integer(revision),.blob(data),.integer(Int64(Date().timeIntervalSince1970*1000))])
        // One shared ceiling across Camps bounds retained payload and row count.
        try execute("DELETE FROM child_cache WHERE rowid IN (SELECT rowid FROM (SELECT rowid,SUM(length(body)) OVER(ORDER BY (cache_key='catalog') DESC,saved_at DESC,rowid DESC) AS bytes,ROW_NUMBER() OVER(ORDER BY (cache_key='catalog') DESC,saved_at DESC,rowid DESC) AS entries FROM child_cache) WHERE bytes>33554432 OR entries>128)")
        try execute("DELETE FROM child_cache WHERE cache_key<>'catalog' AND NOT EXISTS(SELECT 1 FROM child_cache c WHERE c.scope=child_cache.scope AND c.parent_id=child_cache.parent_id AND c.cache_key='catalog')")
    }
    func saveChildCatalog(_ value:CloudChildHistoryList,scope:CloudScope,parent:String,expectedEpoch:Int64,expectedChildEpoch:Int64)throws {
        guard value.valid(parent:parent) else{throw CampCloudError.invalidResponse}
        var value=value;value.cachedAt=nil
        let data=try CampWire.encoder().encode(value)
        try transaction {
            try checkEpoch(scope,expectedEpoch);try requireChildParent(scope,parent,value.permissionRevision)
            try checkChildEpoch(scope,parent:parent,expected:expectedChildEpoch)
            if let old=try childCatalog(scope,parent:parent,permission:value.permissionRevision) {
                guard old.parentRevision<=value.parentRevision else{return}
                if old.parentRevision != value.parentRevision || old.children != value.children {
                    try execute("DELETE FROM child_cache WHERE scope=? AND parent_id=?",[.text(scope.key),.text(parent)])
                }
            }
            try childCacheWrite(data,scope:scope,parent:parent,key:"catalog",permission:value.permissionRevision,revision:value.parentRevision)
        }
    }
    func childCatalog(_ scope:CloudScope,parent:String,permission:Int64)throws->CloudChildHistoryList? {
        try requireChildParent(scope,parent,permission)
        return try rows("SELECT body,saved_at FROM child_cache WHERE scope=? AND parent_id=? AND cache_key='catalog' AND permission_revision=?",[.text(scope.key),.text(parent),.integer(permission)]) {
            var value=try CampWire.decoder().decode(CloudChildHistoryList.self,from:blob($0,0))
            guard value.valid(parent:parent),value.permissionRevision==permission else{throw CampCloudError.invalidResponse}
            value.cachedAt=Date(timeIntervalSince1970:Double(sqlite3_column_int64($0,1))/1000);return value
        }.first
    }
    func saveChildPage(_ value:CloudChildTranscript,scope:CloudScope,parent:String,before:Int64,expectedEpoch:Int64,expectedChildEpoch:Int64)throws {
        guard value.valid(parent:parent,before:before) else{throw CampCloudError.invalidResponse}
        var value=value;value.cachedAt=nil
        let data=try CampWire.encoder().encode(value)
        try transaction {
            try checkEpoch(scope,expectedEpoch);try requireChildParent(scope,parent,value.permissionRevision)
            try checkChildEpoch(scope,parent:parent,expected:expectedChildEpoch)
            guard let catalog=try childCatalog(scope,parent:parent,permission:value.permissionRevision),catalog.parentRevision==value.parentRevision,catalog.children.contains(value.child) else{return}
            try childCacheWrite(data,scope:scope,parent:parent,key:"page:\(value.child.id):\(before)",permission:value.permissionRevision,revision:value.parentRevision)
        }
    }
    func childPage(_ scope:CloudScope,parent:String,child:String,permission:Int64,before:Int64,revision:Int64)throws->CloudChildTranscript? {
        guard before>=0,revision>=0,let catalog=try childCatalog(scope,parent:parent,permission:permission),revision==0 || revision==catalog.parentRevision else{return nil}
        guard let metadata=catalog.children.first(where:{$0.id==child && $0.complete && !$0.removed}) else{return nil}
        return try rows("SELECT body,saved_at FROM child_cache WHERE scope=? AND parent_id=? AND cache_key=? AND permission_revision=? AND parent_revision=?",[.text(scope.key),.text(parent),.text("page:\(child):\(before)"),.integer(permission),.integer(catalog.parentRevision)]) {
            var value=try CampWire.decoder().decode(CloudChildTranscript.self,from:blob($0,0))
            guard value.valid(parent:parent,before:before),value.child==metadata else{throw CampCloudError.invalidResponse}
            value.cachedAt=Date(timeIntervalSince1970:Double(sqlite3_column_int64($0,1))/1000);return value
        }.first
    }
    func clear(_ scope: CloudScope) throws {
        try transaction {
            try setMetadata("epoch:" + scope.key, Data(String(try epoch(scope) + 1).utf8))
            for table in ["sessions", "requests", "messages", "child_cache", "tombstones", "outbox", "share_bindings", "published_message_hashes"] { try execute("DELETE FROM \(table) WHERE scope=?", [.text(scope.key)]) }
            try execute("DELETE FROM metadata WHERE key=?", [.text("cursor:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("snapshot:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("notifications:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("planning:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("planning-import:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("plan-promotion:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("worker-claim:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("execution-setup:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("source-handoff:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("transfer-review:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key=?", [.text("transfer-cancel:" + scope.key)])
            try execute("DELETE FROM metadata WHERE key LIKE ?", [.text("transfer-payload:" + scope.key + ":%")])
            try execute("DELETE FROM metadata WHERE key LIKE ?", [.text("child-epoch:" + scope.key + ":%")])
            for var attempt in try attempts(scope) {
                if ["completed", "failed", "cancelled"].contains(attempt.state) {
                    try execute("DELETE FROM attempts WHERE id=?", [.text(attempt.id)]); continue
                }
                attempt.request.prompt = ""; attempt.request.sources = []; attempt.request.quotedSources = nil; attempt.request.quotedContextDigest = nil; attempt.output = nil; attempt.usage = nil; attempt.state = "uncertain"
                try saveAttempt(attempt)
            }
        }
    }
}
