import Foundation

// Test-only Go executable; build.sh never includes it in an application bundle
func fixtureProcess(_ scenario: String, _ arguments: [String] = []) -> Process {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
        .appendingPathComponent("build/camp-test-provider")
    process.arguments = [scenario] + arguments
    return process
}
