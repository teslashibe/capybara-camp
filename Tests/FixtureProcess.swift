import Foundation

// Test-only Go executable; build.sh never includes it in an application bundle
func fixtureProcess(_ scenario: String, _ arguments: [String] = []) -> Process {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
        .appendingPathComponent("build/camp-test-provider")
    process.arguments = [scenario] + arguments
    TestWatchdog.shared.track(process)
    return process
}

// Names the case that stalls instead of leaving a silent CI timeout. It runs on
// its own thread, so a blocked concurrency pool or main actor cannot stop it, and
// it stops the suite's fixture processes so none outlive the failure.
func testCase(_ name: String, seconds: TimeInterval = 30) { TestWatchdog.shared.begin(name, seconds: seconds) }

final class TestWatchdog: @unchecked Sendable {
    static let shared = TestWatchdog()
    private let lock = NSLock()
    private var name = "", seconds: TimeInterval = 0, deadline = Date.distantFuture, started = false
    private var processes: [Process] = []

    func track(_ process: Process) { lock.lock(); processes.append(process); lock.unlock() }
    func begin(_ name: String, seconds: TimeInterval) {
        lock.lock(); defer { lock.unlock() }
        self.name = name; self.seconds = seconds; deadline = Date().addingTimeInterval(seconds)
        guard !started else { return }; started = true
        Thread.detachNewThread { [self] in
            while true {
                Thread.sleep(forTimeInterval: 0.2)
                lock.lock()
                let expired = Date() > deadline, name = self.name, seconds = self.seconds, running = processes.filter(\.isRunning)
                lock.unlock()
                guard expired else { continue }
                let suite = URL(fileURLWithPath: CommandLine.arguments[0]).lastPathComponent
                FileHandle.standardError.write(Data("FAIL: \(suite) case \"\(name)\" did not finish within \(Int(seconds))s\n".utf8))
                running.forEach { $0.terminate() }
                exit(1)
            }
        }
    }
}
