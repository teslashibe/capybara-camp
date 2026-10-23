import Foundation

@main struct CampReturnTransferTests {
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("return-screen-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at:root) }
        for provider in ["Codex","Claude"] {
            for scenario in ["complete","cancel","install-cancel","signout","reopen"] {
                let fixture = try await ReturnScreenFixture(directory:root.appendingPathComponent(provider+scenario),provider:provider,unpublishedCommits:scenario == "complete")
                var screen = fixture.screen()
                var delivered = 0
                if scenario == "complete" {
                    screen.adopted = { _,_,_ in
                        delivered += 1
                        if delivered == 1 { throw URLError(.networkConnectionLost) }
                    }
                }
                await screen.advance()
                precondition(screen.phase == "review",screen.error)
                let preview = try await screen.preview("notes.txt")
                precondition(preview == "Reviewed notes from the cloud workspace\n")
                await screen.prepare(source:fixture.source,purpose:"implementation")
                precondition(screen.phase == "checkout",screen.error)
                await screen.advance()
                precondition(screen.phase == "installing",screen.error)
                if scenario == "install-cancel" || scenario == "signout" {
                    await fixture.bridge.delay()
                    let running = Task { await screen.advance() }
                    for _ in 0..<100 { if await fixture.bridge.started() { break };try await Task.sleep(for:.milliseconds(5)) }
                    let started = await fixture.bridge.started();precondition(started)
                    if scenario == "install-cancel" { await screen.cancel() }
                    else { screen.attach(scope:nil,credential:nil,transport:nil) }
                    await running.value
                    if scenario == "signout" {
                        precondition(screen.phase == "none" && screen.error.isEmpty && !screen.working,"Late result reappeared after signout")
                        screen = fixture.screen()
                        await screen.restore()
                        precondition(screen.phase == "installed",screen.error)
                    } else {
                        await screen.advance()
                        precondition(screen.phase == "cancelled",screen.error)
                        await screen.reset()
                        precondition(screen.phase == "none",screen.error)
                        continue
                    }
                } else { await screen.advance() }
                if scenario == "reopen" {
                    screen.attach(scope:nil,credential:nil,transport:nil)
                    screen = fixture.screen()
                }
                for _ in 0..<15 {
                    await screen.advance()
                    if screen.phase == "confirm" { break }
                }
                precondition(screen.phase == "confirm",screen.error)
                let before = await fixture.transport.counts()
                precondition(before.2 == 0,"Reopening implicitly approved execution")
                if scenario == "cancel" {
                    await screen.confirm(contextApproved:false,executionApproved:false,permissions:PermissionSettings())
                    precondition(!screen.error.isEmpty)
                    await screen.cancel();await screen.advance()
                    precondition(screen.phase == "cancelled",screen.error)
                    await screen.reset()
                    precondition(screen.phase == "none",screen.error)
                    let counts = await fixture.transport.counts();precondition(counts.2 == 0)
                } else {
                    let permissions = provider == "Codex" ? PermissionSettings(sandbox:"workspace-write",approval:"on-request",reviewer:"user") : PermissionSettings(claudeMode:"manual")
                    await screen.confirm(contextApproved:false,executionApproved:true,permissions:permissions)
                    precondition(!screen.error.isEmpty && screen.phase == "confirm")
                    await screen.confirm(contextApproved:true,executionApproved:false,permissions:permissions)
                    precondition(!screen.error.isEmpty && screen.phase == "confirm")
                    await screen.confirm(contextApproved:true,executionApproved:true,permissions:permissions)
                    for _ in 0..<10 { await screen.advance();if screen.phase == "complete" { break } }
                    precondition(screen.phase == "complete" && screen.completed?.session.id == fixture.session.id,screen.error)
                    if scenario == "complete" {
                        precondition(!screen.error.isEmpty && delivered == 1)
                        await screen.advance()
                        precondition(screen.error.isEmpty && delivered == 2,"Failed adoption delivery could not be retried")
                        let counts = await fixture.transport.counts();precondition(counts.2 == 1,"Delivery retry repeated the handoff")
                    }
                    await screen.reset()
                    precondition(screen.phase == "none",screen.error)
                    let saved = try await fixture.cache.localContinuation(sessionID:fixture.session.id,executionEpoch:3,scope:fixture.scope,expectedEpoch:fixture.epoch)
                    precondition(saved != nil,"Reset discarded the adopted history binding")
                }
                screen.attach(scope:nil,credential:nil,transport:nil)
            }
        }
        print("PASS: return screen review, explicit approvals, reopen, signout, install cancellation, adoption and reset for both providers")
    }
}
