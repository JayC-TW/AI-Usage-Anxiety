import SwiftUI
import AppKit

private let usageIcon: NSImage = {
    let image = Bundle.main.url(forResource: "AppIcon", withExtension: "png")
        .flatMap { NSImage(contentsOf: $0) }
        ?? NSImage(systemSymbolName: "chart.bar.xaxis", accessibilityDescription: nil)!
    image.size = NSSize(width: 20, height: 20)
    image.isTemplate = true
    return image
}()

@main
struct AIUsageApp: App {
    @StateObject private var store = UsageStore()
    var body: some Scene {
        MenuBarExtra {
            UsagePopover(store: store)
                .onAppear { store.refreshIfDue() }
                .onReceive(NotificationCenter.default.publisher(for: NSApplication.willTerminateNotification)) { _ in store.shutdown() }
        } label: {
            Image(nsImage: usageIcon)
                .accessibilityLabel("AI Usage Anxiety 用量")
        }
        .menuBarExtraStyle(.window)
    }
}

struct UsagePopover: View {
    @ObservedObject var store: UsageStore
    @State private var draft = ""
    @State private var confirmingDelete = false

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(nsImage: usageIcon)
                Text(store.settingsOpen ? "設定" : "AI Usage Anxiety").font(.headline)
                Spacer()
                if store.loading { ProgressView().controlSize(.small) }
                if store.settingsOpen {
                    Button("返回") { draft = ""; store.settingsOpen = false }
                        .disabled(store.keyBusy)
                }
            }.padding(18)
            Divider()
            if store.settingsOpen { settings.padding(18) }
            else {
                TimelineView(.periodic(from: .now, by: 30)) { context in
                    VStack(spacing: 8) {
                        provider("codex", title: "Codex", windows: ["5h", "7d", "reserve"], now: context.date)
                        provider("claude", title: "Claude", windows: ["5h", "7d"], now: context.date)
                        provider("opencode", title: "OpenCode Go", windows: ["5h", "7d", "monthly"], now: context.date)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                }
            }
            Divider()
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text("每 3 分鐘自動更新")
                    if let date = store.lastCollection { Text("收集於 \(date.formatted(date: .omitted, time: .standard))") }
                }.font(.caption2).foregroundStyle(.secondary)
                Spacer()
                Button { draft = ""; store.settingsMessage = nil; store.settingsOpen = true } label: {
                    Image(systemName: "gearshape")
                }.help("設定").accessibilityLabel("設定").disabled(store.keyBusy)
                Button("結束") { store.shutdown(); NSApplication.shared.terminate(nil) }
            }.padding(14)
        }
        // The compact cards fit all provider windows in one menu-bar popover.
        .frame(width: 380, height: 560, alignment: .top)
        .background(.regularMaterial)
        .confirmationDialog("刪除 OpenCode Go API key？", isPresented: $confirmingDelete, titleVisibility: .visible) {
            Button("刪除 API key", role: .destructive) {
                Task { if await store.deleteKey() { draft = "" } }
            }
            Button("取消", role: .cancel) { }
        } message: { Text("刪除本機金鑰後將停止查詢 OpenCode Go，額度顯示 N/A。") }
        .onDisappear { draft = "" }
    }

    private var settings: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("OpenCode Go API key").font(.headline)
            Label(store.keyConfigured ? "已設定" : "未設定", systemImage: store.keyConfigured ? "checkmark.shield" : "key")
                .foregroundStyle(.secondary)
            SecureField(store.keyConfigured ? "輸入新的 API key" : "輸入 API key", text: $draft)
                .textFieldStyle(.roundedBorder).disabled(store.keyBusy)
            Text("金鑰儲存於 macOS Keychain。修改後會立即重新取得用量。")
                .font(.caption).foregroundStyle(.secondary)
            HStack {
                Button(store.keyConfigured ? "儲存修改" : "儲存") {
                    let value = draft
                    Task { if await store.saveKey(value) { draft = "" } }
                }.buttonStyle(.borderedProminent)
                    .disabled(store.keyBusy || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                Spacer()
                Button("刪除 API key", role: .destructive) { confirmingDelete = true }
                    .disabled(store.keyBusy)
            }
            if store.keyBusy { ProgressView().controlSize(.small) }
            if let message = store.settingsMessage { Text(message).font(.caption).fixedSize(horizontal: false, vertical: true) }
        }
    }

    private func provider(_ name: String, title: String, windows: [String], now: Date) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.system(.subheadline, design: .rounded).bold())
            ForEach(windows, id: \.self) { window in
                let usage = store.statuses[name]?.usages.first { $0.window == window }
                let reason = store.reason(for: name) ?? usage?.displayableQuotaReason(now: now) ?? (usage == nil ? "尚無可用資料" : nil)
                let stale = usage?.isStale(now: now) == true
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 8) {
                        Text(window == "monthly" ? "每月" : window == "reserve" ? "Reserve" : window == "5h" ? "5 小時" : "7 天")
                            .foregroundStyle(.secondary)
                            .frame(width: 44, alignment: .leading)
                        if let usage, let remaining = usage.remainingQuotaPercent(), reason == nil {
                            let usedPercent = 100 - remaining
                            ProgressView(value: remaining, total: 100)
                                .tint(usedPercent >= store.danger ? .red : usedPercent >= store.warn ? .orange : .green)
                                .accessibilityLabel("\(title) \(window) 剩餘 \(Int(remaining)) 百分比")
                                .frame(maxWidth: .infinity)
                            Text(String(format: "%.1f%%", remaining))
                                .font(.caption2.monospacedDigit())
                                .frame(width: 48, alignment: .trailing)
                        } else {
                            Spacer(minLength: 0)
                            Text(reason == "載入中" ? "載入中" : "N/A")
                                .foregroundStyle(.secondary)
                                .font(.caption2.monospacedDigit())
                        }
                    }
                    if let usage, reason == nil {
                        HStack(spacing: 4) {
                            Text("重置 \(usage.resetAt.map { $0.formatted(date: .abbreviated, time: .shortened) } ?? "N/A")")
                            Spacer(minLength: 4)
                            Text(stale ? "舊資料 \(usage.fetchedAt.formatted(date: .omitted, time: .shortened))" : "更新 \(usage.fetchedAt.formatted(date: .omitted, time: .shortened))")
                        }.font(.system(size: 9)).foregroundStyle(.secondary)
                    } else if let reason {
                        Text(reason).font(.system(size: 9)).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
        }.padding(8)
            .background(Color.primary.opacity(0.035), in: RoundedRectangle(cornerRadius: 10))
    }
}
