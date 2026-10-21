import Foundation

@main struct BoardPerformanceTests {
    @MainActor static func main() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("camp-board-benchmark-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = CampStore(stateDirectory: directory)
        var fixtures: [CampSession] = []
        fixtures.reserveCapacity(30_000)
        for i in 0..<30_000 {
            let provider = i % 2 == 0 ? "Codex" : "Claude"
            let session = CampSession(id: "fixture-\(i)", sessionID: "session-\(i)", provider: provider, sourceID: "source", sourceLabel: "Benchmark", root: "/fixture", title: "Session \(i)", cwd: "/fixture/project", updated: Double(i), path: "", accountID: "", archived: i % 3 == 0)
            fixtures.append(session)
        }
        store.sessions = fixtures
        let coldStart = Date()
        precondition(store.boardSessions(.ready).count == 20_000)
        precondition(store.boardSessions(.done).count == 10_000)
        precondition(store.sessionCount("all") == 30_000 && store.sessionCount("Codex") == 15_000)
        let coldMS = Date().timeIntervalSince(coldStart) * 1000
        let warmStart = Date()
        var observed = 0
        for _ in 0..<1000 {
            observed += store.boardSessions(.ready).count + store.boardSessions(.done).count
            observed += store.sessionCount("all") + store.visibleSessions.count
        }
        let warmMS = Date().timeIntervalSince(warmStart) * 1000
        precondition(observed == 90_000_000)
        store.search = "Session 29999"
        precondition(store.boardSessions(.ready).count == 1 && store.boardSessions(.done).isEmpty)
        print("30,000-session model benchmark: cold grouping/counts \(String(format: "%.2f", coldMS)) ms; 1,000 cached board/sidebar reads \(String(format: "%.2f", warmMS)) ms")
        print("This measures the native model, not end-to-end UI rendering or the release soak gate")
    }
}
