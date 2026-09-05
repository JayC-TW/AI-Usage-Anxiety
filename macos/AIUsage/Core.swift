import Foundation

struct Usage: Decodable {
    let provider: String
    let window: String
    let used: Double
    let limit: Double
    let unit: String
    let resetAt: Date?
    let source: String
    let fetchedAt: Date
    let note: String?

    func hasUsableQuota(now: Date) -> Bool {
        guard used.isFinite, limit.isFinite, used >= 0, limit > 0 else { return false }
        if let resetAt, resetAt <= now { return false }
        return fetchedAt <= now.addingTimeInterval(60)
    }

    func isStale(now: Date) -> Bool {
        hasFiniteQuota && now.timeIntervalSince(fetchedAt) > 540
    }

    func remainingQuotaPercent() -> Double? {
        guard hasFiniteQuota else { return nil }
        let usedPercent = used / limit * 100
        guard usedPercent.isFinite else { return nil }
        return min(100, max(0, 100 - usedPercent))
    }

    func displayableQuotaReason(now: Date) -> String? {
        guard hasFiniteQuota else { return "無法取得額度" }
        if let resetAt, resetAt <= now { return "等待重置後的新資料" }
        if fetchedAt > now.addingTimeInterval(60) { return "來源資料時間異常" }
        return nil
    }

    func unavailableReason(now: Date) -> String? {
        if let reason = displayableQuotaReason(now: now) { return reason }
        if isStale(now: now) { return "來源資料已過期" }
        return nil
    }

    private var hasFiniteQuota: Bool {
        used.isFinite && limit.isFinite && used >= 0 && limit > 0
    }
}

struct ProviderStatus: Decodable {
    let name: String
    let available: Bool
    let usages: [Usage]
    let errorCode: String?
    let error: String?
    let deferred: Bool?
    let retryAfter: Date?
}

struct Snapshot: Decodable {
    let schemaVersion: Int
    let updatedAt: Date
    let statuses: [ProviderStatus]
    let settings: [String: Double]

    static func decode(_ data: Data) throws -> Snapshot {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { input in
            let value = try input.singleValueContainer().decode(String.self)
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = formatter.date(from: value) { return date }
            formatter.formatOptions = [.withInternetDateTime]
            if let date = formatter.date(from: value) { return date }
            throw BridgeFailure.invalidResponse
        }
        let snapshot = try decoder.decode(Snapshot.self, from: data)
        guard snapshot.schemaVersion == 1 else { throw BridgeFailure.invalidResponse }
        return snapshot
    }
}

enum BridgeFailure: Error, LocalizedError {
    case missingHelper, invalidResponse, timeout, cancelled, tooLarge, launch
    var errorDescription: String? {
        switch self {
        case .missingHelper: return "找不到內嵌資料程式，請重新安裝 App"
        case .invalidResponse: return "資料格式無法辨識"
        case .timeout: return "資料收集逾時"
        case .cancelled: return "已取消收集"
        case .tooLarge: return "資料超過大小上限"
        case .launch: return "無法啟動資料收集程式"
        }
    }
}

// Runs entirely off the main thread. Credentials only travel through a private pipe.
final class HelperJob: @unchecked Sendable {
    private let lock = NSLock()
    private var process: Process?
    private var cancelled = false
    private var claudeOAuthToken: String?

    func setClaudeOAuthToken(_ token: String?) {
        lock.lock(); claudeOAuthToken = token; lock.unlock()
    }

    private func claudeOAuthTokenSnapshot() -> String? {
        lock.lock(); defer { lock.unlock() }
        return claudeOAuthToken
    }

    func cancel() {
        lock.lock(); cancelled = true; let active = process; lock.unlock()
        if let active, active.isRunning { active.terminate() }
    }

    func stopForAppExit() {
        lock.lock(); cancelled = true
        if let active = process, active.isRunning { kill(active.processIdentifier, SIGKILL) }
        lock.unlock()
    }

    func run(url: URL, key: String?, skip: Bool, timeout: TimeInterval = 15) async throws -> Snapshot {
        try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                do { continuation.resume(returning: try self.execute(url: url, key: key, skip: skip, timeout: timeout)) }
                catch { continuation.resume(throwing: error) }
            }
        }
    }

    private func execute(url: URL, key: String?, skip: Bool, timeout: TimeInterval) throws -> Snapshot {
        guard FileManager.default.isExecutableFile(atPath: url.path) else { throw BridgeFailure.missingHelper }
        let task = Process(), input = Pipe(), output = Pipe(), diagnostic = Pipe()
        task.executableURL = url
        task.arguments = ["--json", "--non-interactive"]
        task.standardInput = input; task.standardOutput = output; task.standardError = diagnostic
        task.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        // Do not inherit provider credentials from the launching shell.
        task.environment = ["HOME": FileManager.default.homeDirectoryForCurrentUser.path, "PATH": "/usr/bin:/bin", "LANG": "en_US.UTF-8"]
        let claudeToken = claudeOAuthTokenSnapshot()
        let body: [String: Any] = [
            "schemaVersion": 1,
            "opencodeKey": key as Any? ?? NSNull(),
            "claudeOAuthToken": claudeToken as Any? ?? NSNull(),
            "skipProviders": skip ? ["opencode"] : []
        ]
        let request = try JSONSerialization.data(withJSONObject: body)
        guard request.count <= 16384 else { throw BridgeFailure.tooLarge }
        lock.lock()
        if cancelled { lock.unlock(); throw BridgeFailure.cancelled }
        process = task
        do { try task.run() } catch { lock.unlock(); throw BridgeFailure.launch }
        lock.unlock()
        defer { lock.lock(); process = nil; lock.unlock() }
        let group = DispatchGroup()
        let stdout = BoundedBytes(), stderr = BoundedBytes()
        for (pipe, buffer) in [(output, stdout), (diagnostic, stderr)] {
            group.enter()
            DispatchQueue.global(qos: .utility).async {
                defer { group.leave() }
                while let chunk = try? pipe.fileHandleForReading.read(upToCount: 4096), !chunk.isEmpty {
                    if !buffer.append(chunk) { self.cancel(); break }
                }
            }
        }
        do { try input.fileHandleForWriting.write(contentsOf: request) }
        catch { cancel() }
        try? input.fileHandleForWriting.close()
        let deadline = Date().addingTimeInterval(timeout)
        var timedOut = false
        while task.isRunning {
            lock.lock(); let stopped = cancelled; lock.unlock()
            if stopped || Date() >= deadline {
                timedOut = !stopped
                task.terminate()
                let grace = Date().addingTimeInterval(2)
                while task.isRunning && Date() < grace { Thread.sleep(forTimeInterval: 0.02) }
                if task.isRunning { kill(task.processIdentifier, SIGKILL) }
                break
            }
            Thread.sleep(forTimeInterval: 0.02)
        }
        task.waitUntilExit()
        guard group.wait(timeout: .now() + 2) == .success else { throw BridgeFailure.timeout }
        if stdout.exceeded || stderr.exceeded { throw BridgeFailure.tooLarge }
        if timedOut { throw BridgeFailure.timeout }
        lock.lock(); let stopped = cancelled; lock.unlock()
        if stopped { throw BridgeFailure.cancelled }
        guard task.terminationReason == .exit, [0, 1].contains(task.terminationStatus) else { throw BridgeFailure.invalidResponse }
        do { return try Snapshot.decode(stdout.data) }
        catch { throw BridgeFailure.invalidResponse }
    }
}

private final class BoundedBytes: @unchecked Sendable {
    private let lock = NSLock()
    private var bytes = Data()
    private var overflow = false
    var data: Data { lock.lock(); defer { lock.unlock() }; return bytes }
    var exceeded: Bool { lock.lock(); defer { lock.unlock() }; return overflow }
    func append(_ next: Data) -> Bool {
        lock.lock(); defer { lock.unlock() }
        if bytes.count + next.count > 1048576 { overflow = true; return false }
        bytes.append(next); return true
    }
}

func statusReason(_ code: String?) -> String? {
    guard let code else { return nil }
    switch code {
    case "missing_key": return "未設定 API key"
    case "unauthorized": return "API key 無效，請至設定修改"
    case "not_entitled": return "缺少 OpenCode Go 訂閱權限"
    case "rate_limited": return "請求受限，稍後自動重試"
    case "timeout": return "資料收集逾時"
    case "read_denied": return "沒有讀取來源的權限"
    case "parse_error": return "無法解析來源資料"
    case "disabled": return "此來源已停用"
    default: return "無法取得用量"
    }
}
