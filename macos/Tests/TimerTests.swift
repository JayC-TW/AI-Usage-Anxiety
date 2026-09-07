import AppKit

private struct TimerKeys: KeychainAccess {
    func load(interactive: Bool) throws -> String? { nil }
    func save(_ key: String) throws {}
    func delete() throws {}
}

@main struct TimerTests {
    @MainActor static func main() {
        // Menu tracking runs outside the default mode. No manual refresh calls.
        CFRunLoopAddCommonMode(CFRunLoopGetMain(), CFRunLoopMode(rawValue: RunLoop.Mode.eventTracking.rawValue as CFString))
        var now = Date()
        var count = 0
        var ready = false
        let store = UsageStore(keychain: TimerKeys(), clock: { now }, automatic: false) { _, _, _ in
            count += 1
            return Snapshot(schemaVersion: 1, updatedAt: now, statuses: [], settings: [:])
        }
        Task { @MainActor in
            await store.start(timerInterval: 0.05)
            ready = true
        }
        let setupDeadline = Date().addingTimeInterval(2)
        while (!ready || store.loading || count == 0) && Date() < setupDeadline {
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.01))
        }
        guard ready && count == 1 else { fatalError("initial collection did not complete") }
        for mode in [RunLoop.Mode.eventTracking, .default] {
            let previousCount = count
            let previousCollection = store.lastCollection
            now = now.addingTimeInterval(180)
            let deadline = Date().addingTimeInterval(1)
            while (count == previousCount || store.loading) && Date() < deadline {
                RunLoop.main.run(mode: mode, before: Date().addingTimeInterval(0.01))
            }
            guard count == previousCount + 1, store.lastCollection != previousCollection else {
                fputs("FAIL: elapsed 180s but automatic collection did not update in \(mode.rawValue)\n", stderr)
                exit(1)
            }
        }
        // Regression: the tick period and the due threshold were identical, so a few
        // milliseconds of dispatch jitter left a tick just short of due and skipped it.
        var liveCount = 0
        var liveReady = false
        let interval = 0.2
        let live = UsageStore(keychain: TimerKeys(), automatic: false) { _, _, _ in
            liveCount += 1
            return Snapshot(schemaVersion: 1, updatedAt: Date(), statuses: [], settings: [:])
        }
        Task { @MainActor in
            await live.start(timerInterval: interval)
            liveReady = true
        }
        let liveStart = Date()
        let liveDeadline = liveStart.addingTimeInterval(interval * 12)
        while Date() < liveDeadline {
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.01))
        }
        live.shutdown()
        // One tick of headroom: the timer's tolerance may defer the last one past the deadline.
        let expected = Int(Date().timeIntervalSince(liveStart) / interval) - 1
        guard liveReady, liveCount >= expected else {
            fputs("FAIL: \(liveCount) collections on a real clock, expected at least \(expected)\n", stderr)
            exit(1)
        }

        store.shutdown()
        let finalCount = count
        now = now.addingTimeInterval(180)
        let shutdownDeadline = Date().addingTimeInterval(0.15)
        while Date() < shutdownDeadline {
            RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.01))
        }
        precondition(count == finalCount, "shutdown must stop automatic collection")
        print("PASS: real timer updates state during menu tracking and default mode; shutdown stops it")
    }
}
