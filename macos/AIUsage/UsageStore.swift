import AppKit
import SwiftUI

@MainActor
final class UsageStore: ObservableObject {
    @Published var statuses: [String: ProviderStatus] = [:]
    @Published var lastCollection: Date?
    @Published var loading = false
    @Published var settingsOpen = false
    @Published var keyConfigured = false
    @Published var keyBusy = false
    @Published var settingsMessage: String?
    @Published var globalError: String?
    @Published var warn = 75.0
    @Published var danger = 90.0
    private var key: String?
    private var claudeOAuthToken: String?
    private var blocked = false
    private var retryAfter: Date?
    private var generation = 0
    private var job: HelperJob?
    private var pending = false
    private var stopped = false
    private var timer: Timer?
    private var lastStarted: Date?
    private var wakeObserver: NSObjectProtocol?
    private var initialized = false
    private let keychain: any KeychainAccess
    private let preferences: UserDefaults
    private let clock: () -> Date
    private let fetch: (HelperJob, String?, Bool) async throws -> Snapshot
    private var history: [String: [Usage]] = [:]

    init(keychain: any KeychainAccess = KeychainService(), preferences: UserDefaults = .standard,
         clock: @escaping () -> Date = Date.init, automatic: Bool = true,
         fetch: @escaping (HelperJob, String?, Bool) async throws -> Snapshot = { job, key, skip in
             try await job.run(url: Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/aiusage"), key: key, skip: skip)
         }) {
        self.keychain = keychain; self.preferences = preferences; self.clock = clock; self.fetch = fetch
        if !automatic { return }
        wakeObserver = NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.refreshIfDue() }
        }
        Task { await start() }
    }

    func start(scheduleTimer: Bool = true, timerInterval: TimeInterval = 180) async {
            guard !initialized, !keyBusy else { return }
            keyBusy = true
            let storage = keychain
            do {
                let stored = try await Task.detached { try storage.load(interactive: false) }.value
                key = stored; keyConfigured = stored != nil
                // Keep the dashboard visible on first launch. OpenCode Go reports N/A
                // until a key is configured, while Codex and Claude can still load.
                settingsOpen = false
            } catch { settingsMessage = error.localizedDescription }
            initialized = true; keyBusy = false
            guard !stopped else { return }
            refresh()
            if !scheduleTimer { return }
            let refreshTimer = Timer(timeInterval: timerInterval, repeats: true) { [weak self] _ in
                Task { @MainActor in self?.refreshIfDue() }
            }
            RunLoop.main.add(refreshTimer, forMode: .common)
            timer = refreshTimer
    }

    func refreshIfDue() {
        if lastStarted == nil || clock().timeIntervalSince(lastStarted!) >= 180 { refresh() }
    }

    func refresh() {
        guard initialized, !stopped else { return }
        if job != nil { pending = true; return }
        let current = HelperJob(), revision = generation
        job = current; loading = true; lastStarted = clock()
        let currentKey = key
        let skip = blocked || (retryAfter.map { $0 > clock() } ?? false)
        Task {
            if let storage = keychain as? any ClaudeOAuthRefreshAccess {
                claudeOAuthToken = await loadClaudeOAuthTokenWithRefresh(storage)
            } else if keychain is any ClaudeOAuthTokenAccess {
                claudeOAuthToken = await loadClaudeOAuthToken()
            }
            current.setClaudeOAuthToken(claudeOAuthToken)
            do {
                let result = try await fetch(current, currentKey, skip)
                if revision == generation && !stopped {
                    globalError = nil; lastCollection = result.updatedAt
                    warn = result.settings["warn"] ?? 75; danger = result.settings["danger"] ?? 90
                    var next: [String: ProviderStatus] = [:]
                    for status in result.statuses {
                        if status.deferred == true {
                            next[status.name] = statuses[status.name]
                        } else {
                            next[status.name] = status
                            if status.errorCode == nil { history[status.name] = status.usages }
                            if status.name == "opencode" {
                                blocked = status.errorCode == "unauthorized"
                                retryAfter = status.retryAfter
                            }
                        }
                    }
                    statuses = next
                }
            } catch {
                if revision == generation && !stopped { globalError = error.localizedDescription }
            }
            job = nil; loading = false
            if pending && !stopped { pending = false; refresh() }
        }
    }

    private func loadClaudeOAuthToken() async -> String? {
        guard let storage = keychain as? any ClaudeOAuthTokenAccess else { return nil }
        return try? await Task.detached {
            try storage.loadClaudeOAuthToken(interactive: false)
        }.value
    }

    private func loadClaudeOAuthTokenWithRefresh(_ storage: any ClaudeOAuthRefreshAccess) async -> String? {
        try? await Task.detached {
            try await storage.loadClaudeOAuthTokenWithRefresh(interactive: false)
        }.value
    }

    func saveKey(_ input: String) async -> Bool {
        guard !keyBusy, initialized else { return false }
        let replacement = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !replacement.isEmpty else { settingsMessage = "請輸入 API key"; return false }
        keyBusy = true; defer { keyBusy = false }
        let storage = keychain
        do {
            try await Task.detached { try storage.save(replacement) }.value
            invalidateKeyRequests()
            key = replacement; keyConfigured = true
            preferences.set(false, forKey: "keyDeleted")
            settingsMessage = "已儲存，正在驗證用量"
            refresh()
            return true
        } catch { settingsMessage = error.localizedDescription; return false }
    }

    func deleteKey() async -> Bool {
        guard !keyBusy, initialized else { return false }
        keyBusy = true; defer { keyBusy = false }
        let storage = keychain
        do {
            try await Task.detached { try storage.delete() }.value
            invalidateKeyRequests()
            key = nil; keyConfigured = false
            preferences.set(true, forKey: "keyDeleted")
            settingsMessage = "已刪除 API key"
            refresh()
            return true
        } catch { settingsMessage = error.localizedDescription; return false }
    }

    private func invalidateKeyRequests() {
        generation += 1; job?.cancel()
        statuses.removeValue(forKey: "opencode"); history.removeValue(forKey: "opencode")
        blocked = false; retryAfter = nil
    }

    func reason(for name: String) -> String? {
        if name == "opencode" && !keyConfigured { return "未設定 API key" }
        if let globalError { return globalError }
        guard let status = statuses[name] else { return loading ? "載入中" : "尚無可用資料" }
        if !status.available { return "未偵測到來源" }
        return statusReason(status.errorCode)
    }

    func shutdown() {
        stopped = true; timer?.invalidate(); job?.stopForAppExit()
        if let wakeObserver { NSWorkspace.shared.notificationCenter.removeObserver(wakeObserver) }
    }
}
