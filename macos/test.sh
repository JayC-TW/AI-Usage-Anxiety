#!/bin/bash
set -euo pipefail
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJECT_ROOT"
mkdir -p work/swift-cache
GOCACHE="$PROJECT_ROOT/work/go-cache" go test ./... -race
GOCACHE="$PROJECT_ROOT/work/go-cache" go vet ./...
xcrun swiftc -module-cache-path work/swift-cache macos/Tests/FakeHelper.swift -o work/fake-helper
for mode in hang large invalid; do ln -sf fake-helper "work/helper-$mode"; done
xcrun swiftc -swift-version 5 -parse-as-library -module-cache-path work/swift-cache \
  macos/AIUsage/Core.swift macos/Tests/CoreTests.swift -o work/core-tests
./work/core-tests
xcrun swiftc -swift-version 5 -parse-as-library -module-cache-path work/swift-cache \
  macos/AIUsage/KeychainService.swift macos/Tests/ClaudeOAuthTests.swift -o work/claude-oauth-tests
./work/claude-oauth-tests
xcrun swiftc -swift-version 5 -parse-as-library -module-cache-path work/swift-cache \
  macos/AIUsage/Core.swift macos/AIUsage/KeychainService.swift macos/AIUsage/UsageStore.swift \
  macos/Tests/StoreTests.swift -o work/store-tests
./work/store-tests
xcrun swiftc -swift-version 5 -parse-as-library -module-cache-path work/swift-cache \
  macos/AIUsage/Core.swift macos/AIUsage/KeychainService.swift macos/AIUsage/UsageStore.swift \
  macos/Tests/TimerTests.swift -o work/timer-tests
./work/timer-tests
if [[ "${1:-}" == "--keychain" ]]; then
  xcrun swiftc -swift-version 5 -parse-as-library -module-cache-path work/swift-cache \
    macos/AIUsage/KeychainService.swift macos/Tests/KeychainTests.swift -o work/keychain-tests
  ./work/keychain-tests
fi
