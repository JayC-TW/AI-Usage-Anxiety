import Foundation

@main struct CoreTests {
    static func main() async throws {
        let json = #"{"schemaVersion":1,"updatedAt":"2026-09-05T00:00:00Z","settings":{"warn":75},"statuses":[{"name":"codex","available":true,"usages":[{"provider":"codex","window":"5h","used":0,"limit":100,"unit":"percent","source":"local-file","fetchedAt":"2026-09-05T00:00:00.123Z","resetAt":"2026-09-05T05:00:00Z"}]}]}"#
        let snapshot = try Snapshot.decode(Data(json.utf8))
        let usage = snapshot.statuses[0].usages[0]
        precondition(usage.unavailableReason(now: snapshot.updatedAt) == nil, "real zero must remain valid")
        precondition(usage.remainingQuotaPercent() == 100, "used 0 should display remaining 100")
        let partial = Usage(
            provider: "codex", window: "reserve", used: 17, limit: 100, unit: "percent",
            resetAt: snapshot.updatedAt.addingTimeInterval(86400), source: "local-file",
            fetchedAt: snapshot.updatedAt, note: nil
        )
        precondition(partial.remainingQuotaPercent() == 83, "used 17 should display remaining 83")
        precondition(usage.unavailableReason(now: snapshot.updatedAt.addingTimeInterval(600)) != nil, "stale must be N/A")
        precondition(usage.unavailableReason(now: snapshot.updatedAt.addingTimeInterval(20000)) != nil, "expired must be N/A")
        let staleClaude = Usage(
            provider: "claude", window: "7d", used: 23, limit: 100, unit: "percent",
            resetAt: snapshot.updatedAt.addingTimeInterval(86400), source: "local-file",
            fetchedAt: snapshot.updatedAt.addingTimeInterval(-600), note: "rate-limit snapshot"
        )
        precondition(staleClaude.hasUsableQuota(now: snapshot.updatedAt), "future-reset Claude quota should remain displayable")
        precondition(staleClaude.isStale(now: snapshot.updatedAt), "old Claude snapshot should be marked stale")
        precondition(staleClaude.displayableQuotaReason(now: snapshot.updatedAt) == nil, "stale future-reset quota should not become N/A")
        let unknown = json.replacingOccurrences(of: "\"limit\":100", with: "\"limit\":0")
        let unknownUsage = try Snapshot.decode(Data(unknown.utf8)).statuses[0].usages[0]
        precondition(unknownUsage.unavailableReason(now: snapshot.updatedAt) != nil)
        do { _ = try Snapshot.decode(Data(json.replacingOccurrences(of: "\"schemaVersion\":1", with: "\"schemaVersion\":2").utf8)); fatalError("version accepted") } catch {}
        precondition(statusReason("missing_key") == "未設定 API key")
        let root = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
        let result = try await HelperJob().run(url: root.appendingPathComponent("outputs/AI Usage Anxiety.app/Contents/Helpers/aiusage"), key: nil, skip: false)
        precondition(result.statuses.first { $0.name == "opencode" }?.errorCode == "missing_key", "partial failure JSON must decode")
        let deferred = try await HelperJob().run(url: root.appendingPathComponent("outputs/AI Usage Anxiety.app/Contents/Helpers/aiusage"), key: nil, skip: true)
        precondition(deferred.statuses.first { $0.name == "opencode" }?.deferred == true)
        for (mode, expected) in [("helper-hang", "timeout"), ("helper-large", "tooLarge"), ("helper-invalid", "invalidResponse")] {
            do {
                _ = try await HelperJob().run(url: root.appendingPathComponent("work/" + mode), key: nil, skip: false, timeout: mode == "helper-hang" ? 0.3 : 5)
                fatalError("bad helper accepted")
            } catch let error as BridgeFailure { precondition(String(describing: error) == expected, "\(mode): \(error)") }
        }
        let cancelled = HelperJob()
        let work = Task { try await cancelled.run(url: root.appendingPathComponent("work/helper-hang"), key: nil, skip: false) }
        try await Task.sleep(nanoseconds: 100_000_000)
        cancelled.cancel()
        do { _ = try await work.value; fatalError("cancel failed") }
        catch let error as BridgeFailure { precondition(String(describing: error) == "cancelled") }
        print("PASS: JSON dates/schema, zero/N/A/stale, real helper partial/deferred responses")
        print("PASS: helper timeout, force termination, bounded output, malformed JSON, cancellation")
    }
}
