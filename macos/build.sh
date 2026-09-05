#!/bin/bash
set -euo pipefail
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$PROJECT_ROOT/outputs/AI Usage Anxiety.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources" "$PROJECT_ROOT/bin" "$PROJECT_ROOT/work/swift-cache" "$PROJECT_ROOT/work/go-cache"
cd "$PROJECT_ROOT"
CGO_ENABLED=1 GOCACHE="$PROJECT_ROOT/work/go-cache" go build -trimpath -o "$APP/Contents/Helpers/aiusage" ./cmd/aiusage
CGO_ENABLED=1 GOCACHE="$PROJECT_ROOT/work/go-cache" go build -trimpath -o "$PROJECT_ROOT/bin/aiusage" ./cmd/aiusage
xcrun swiftc -swift-version 5 -parse-as-library -O -target arm64-apple-macosx13.0 \
  -module-cache-path "$PROJECT_ROOT/work/swift-cache" \
  macos/AIUsage/Core.swift macos/AIUsage/KeychainService.swift macos/AIUsage/UsageStore.swift macos/AIUsage/AIUsageApp.swift \
  -o "$APP/Contents/MacOS/AIUsage"
cp macos/Info.plist "$APP/Contents/Info.plist"
cp macos/Resources/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
cp docs/assets/app-icon.png "$APP/Contents/Resources/AppIcon.png"
/usr/bin/codesign --force --sign - "$APP/Contents/Helpers/aiusage"
/usr/bin/codesign --force --sign - "$APP"
/usr/bin/codesign --verify --deep --strict "$APP"
printf 'Built: %s\n' "$APP"
