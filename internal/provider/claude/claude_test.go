package claude_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider/claude"
)

func TestFetchAggregatesRecentTranscriptTokens(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".claude/projects/session.jsonl": &fstest.MapFile{
			ModTime: now.Add(-time.Hour),
			Data: []byte(`{"timestamp":"2026-09-05T13:00:00Z","message":{"usage":{"input_tokens":100,"output_tokens":20}}}
{"timestamp":"2026-09-01T13:00:00Z","usage":{"input_tokens":30,"output_tokens":10}}
`),
		},
	}
	provider := claude.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 120 || usages[0].Limit != 0 || usages[0].Source != model.SourceDerived {
		t.Errorf("5h usage = %+v", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 160 {
		t.Errorf("7d usage = %+v", usages[1])
	}
}

func TestFetchUsesClaudeRateLimitSnapshots(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".claude/projects/session.jsonl": &fstest.MapFile{
			ModTime: now.Add(-time.Hour),
			Data: []byte(`{"timestamp":"2026-09-05T13:00:00Z","rate_limits":{"five_hour":{"used_percentage":12.5,"resets_at":1788627600},"seven_day":{"used_percentage":34,"resets_at":1788919200}}}
{"timestamp":"2026-09-05T13:30:00Z","type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.15,"resetsAt":1788627600},"seven_day":{"utilization":0.4,"resetsAt":1788919200}}}}
`),
		},
	}
	provider := claude.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 15 || usages[0].Limit != 100 || usages[0].Unit != "percent" || usages[0].Source != model.SourceLocalFile {
		t.Errorf("5h usage = %+v", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 40 || usages[1].Limit != 100 || usages[1].Source != model.SourceLocalFile {
		t.Errorf("7d usage = %+v", usages[1])
	}
}

func TestFetchUsesQuotaLimitsWindows(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".claude/projects/session.jsonl": &fstest.MapFile{
			ModTime: now.Add(-time.Hour),
			Data:    []byte(`{"timestamp":"2026-09-05T13:45:00Z","quotaLimits":{"five_hour":{"utilization":0,"resetsAt":1788627600},"seven_day":{"utilization":0.4,"resetsAt":1788919200}}}`),
		},
	}
	provider := claude.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 0 || usages[0].Source != model.SourceLocalFile {
		t.Errorf("5h usage = %+v, want quotaLimits 0%%", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 40 || usages[1].Source != model.SourceLocalFile {
		t.Errorf("7d usage = %+v, want quotaLimits 40%%", usages[1])
	}
}
