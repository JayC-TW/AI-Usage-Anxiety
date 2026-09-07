import Foundation

final class TestKeys: KeychainAccess, @unchecked Sendable {
    private let lock = NSLock()
    private var key: String?
    private var failure = false
    init(_ key: String?) { self.key = key }
    func fail(_ value: Bool) { lock.lock(); defer { lock.unlock() }; failure = value }
    func load(interactive: Bool) throws -> String? { lock.lock(); defer { lock.unlock() }; return key }
    func save(_ key: String) throws {
        lock.lock(); defer { lock.unlock() }
        if failure { throw BridgeFailure.launch }; self.key = key
    }
    func delete() throws {
        lock.lock(); defer { lock.unlock() }
        if failure { throw BridgeFailure.launch }; key = nil
    }
}

@MainActor final class Fetches {
    var requests: [(String?, Bool)] = []
    var waiting: [CheckedContinuation<Snapshot, Error>] = []
    func fetch(_ job: HelperJob, _ key: String?, _ skip: Bool) async throws -> Snapshot {
        requests.append((key, skip))
        return try await withCheckedThrowingContinuation { waiting.append($0) }
    }
    func finish(_ code: String? = nil) {
        let result = Snapshot(schemaVersion: 1, updatedAt: Date(), statuses: [ProviderStatus(name: "opencode", available: true, usages: [], errorCode: code, error: nil, deferred: nil, retryAfter: code == "rate_limited" ? Date().addingTimeInterval(300) : nil)], settings: [:])
        waiting.removeFirst().resume(returning: result)
    }
}

@main struct StoreTests {
    @MainActor static func settle(_ condition: () -> Bool) async throws {
        for _ in 0..<1000 {
            if condition() { return }
            try await Task.sleep(nanoseconds: 1_000_000)
        }
        fatalError("async condition failed")
    }
    @MainActor static func main() async throws {
        let suite = "aiusage.tests." + UUID().uuidString
        let prefs = UserDefaults(suiteName: suite)!
        defer { prefs.removePersistentDomain(forName: suite) }

        let firstLaunchSuite = "aiusage.first-launch.tests." + UUID().uuidString
        let firstLaunchPrefs = UserDefaults(suiteName: firstLaunchSuite)!
        let firstLaunchFetch = Fetches()
        let firstLaunch = UsageStore(keychain: TestKeys(nil), preferences: firstLaunchPrefs,
                                     automatic: false, fetch: firstLaunchFetch.fetch)
        await firstLaunch.start(scheduleTimer: false)
        try await settle { firstLaunchFetch.requests.count == 1 }
        precondition(!firstLaunch.settingsOpen && !firstLaunch.keyConfigured,
                     "first launch must show the dashboard before settings")
        firstLaunchFetch.finish("missing_key")
        try await settle { !firstLaunch.loading }
        firstLaunch.shutdown()
        firstLaunchPrefs.removePersistentDomain(forName: firstLaunchSuite)

        let keys = TestKeys("old"), fetch = Fetches()
        var now = Date()
        let store = UsageStore(keychain: keys, preferences: prefs, clock: { now }, automatic: false, fetch: fetch.fetch)
        await store.start(scheduleTimer: false)
        try await settle { fetch.requests.count == 1 }
        precondition(store.keyConfigured && !store.settingsOpen)
        store.refresh(); store.refresh()
        precondition(fetch.requests.count == 1, "must not overlap")
        fetch.finish(); try await settle { fetch.requests.count == 2 }
        fetch.finish(); try await settle { !store.loading }
        // 157s is the due threshold: the 180s period minus the timer's 18s tolerance and
        // a 5s jitter margin, so a late tick still counts as due instead of skipping a cycle.
        now = now.addingTimeInterval(156); store.refreshIfDue()
        precondition(fetch.requests.count == 2)
        now = now.addingTimeInterval(1); store.refreshIfDue()
        try await settle { fetch.requests.count == 3 }
        // Delete while a response holding the old credential is still pending.
        let deleted = await store.deleteKey()
        precondition(deleted && !store.keyConfigured && store.reason(for: "opencode") == "未設定 API key")
        fetch.finish("unauthorized")
        try await settle { fetch.requests.count == 4 }
        precondition(store.statuses["opencode"] == nil, "old response resurrected deleted state")
        precondition(fetch.requests[3].0 == nil, "deleted credential reused")
        fetch.finish("missing_key"); try await settle { !store.loading }
        precondition(prefs.bool(forKey: "keyDeleted"))
        let saved = await store.saveKey("new")
        precondition(saved && store.keyConfigured)
        try await settle { fetch.requests.count == 5 }
        precondition(fetch.requests[4].0 == "new" && !fetch.requests[4].1)
        fetch.finish("unauthorized"); try await settle { !store.loading }
        store.refresh(); try await settle { fetch.requests.count == 6 }
        precondition(fetch.requests[5].1, "401 must suspend queries")
        fetch.finish(); try await settle { !store.loading }
        keys.fail(true)
        let failedDelete = await store.deleteKey()
        precondition(!failedDelete && store.keyConfigured, "failed deletion must preserve state")
        let emptySave = await store.saveKey("  ")
        precondition(!emptySave)
        store.shutdown()
        keys.fail(false)
        try keys.delete(); prefs.set(true, forKey: "keyDeleted")
        let restartedFetch = Fetches()
        let restarted = UsageStore(keychain: keys, preferences: prefs, automatic: false, fetch: restartedFetch.fetch)
        await restarted.start(scheduleTimer: false)
        precondition(!restarted.settingsOpen && !restarted.keyConfigured, "deleted key prompted after restart")
        try await settle { restartedFetch.requests.count == 1 }
        restartedFetch.finish("missing_key"); try await settle { !restarted.loading }
        restarted.shutdown()
        print("PASS: schedule/single-flight, modify/delete, in-flight invalidation, 401, delete failure, restart")
    }
}
