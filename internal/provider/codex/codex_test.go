package codex_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider/codex"
)

func TestFetchMapsKnownRateLimitWindows(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/rate_limits.json": &fstest.MapFile{Data: []byte(`{
            "rate_limits": {
                "primary_window": {"used_percent": 58, "reset_at": "2026-09-05T16:10:00Z"},
                "secondary_window": {"used_percent": 31, "reset_at": "2026-09-07T00:00:00Z"}
            },
            "token": "must-not-be-retained"
        }`), ModTime: now},
	}
	provider := codex.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 58 || usages[0].Limit != 100 || usages[0].Source != model.SourceLocalFile {
		t.Errorf("primary usage = %+v", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 31 || usages[1].Limit != 100 {
		t.Errorf("secondary usage = %+v", usages[1])
	}
}

func TestFetchReadsLatestRateLimitsFromSessionJSONL(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/sessions/2026/09/05/rollout.jsonl": &fstest.MapFile{
			ModTime: now,
			Data: []byte(`{"type":"event_msg","payload":{"rate_limits":{"primary":{"used_percent":2,"window_minutes":300,"resets_at":1788553975},"secondary":{"used_percent":81,"window_minutes":10080,"resets_at":1788778747}}}}
{"type":"event_msg","payload":{"rate_limits":{"primary":{"used_percent":3,"window_minutes":300,"resets_at":1788553975},"secondary":{"used_percent":81,"window_minutes":10080,"resets_at":1788778747}}}}`),
		},
	}
	provider := codex.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 3 || usages[0].Limit != 100 {
		t.Errorf("primary usage = %+v, want latest 5h usage 3%%", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 81 || usages[1].Limit != 100 {
		t.Errorf("secondary usage = %+v, want 7d usage 81%%", usages[1])
	}
}

func TestFetchMapsRateLimitWindowsByDuration(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/sessions/rollout.jsonl": &fstest.MapFile{
			ModTime: now,
			Data: []byte(`{"timestamp":"2026-09-05T13:00:00Z","payload":{"rate_limits":{"primary":{"used_percent":100,"window_minutes":300,"resets_at":1788627600},"secondary":{"used_percent":16,"window_minutes":10080,"resets_at":1789192800}}}}
{"timestamp":"2026-09-05T13:30:00Z","payload":{"rate_limits":{"primary":{"used_percent":19,"window_minutes":10080,"resets_at":1789194600},"secondary":null}}}`),
		},
	}
	provider := codex.New(root)
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2", len(usages))
	}
	if usages[0].Window != "5h" || usages[0].Used != 100 {
		t.Errorf("5h usage = %+v, want the 5h event", usages[0])
	}
	if usages[1].Window != "7d" || usages[1].Used != 19 {
		t.Errorf("7d usage = %+v, want the newer 7d-only event", usages[1])
	}
}

func TestFetchMapsReserveRemainingPercent(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/rate_limits.json": &fstest.MapFile{Data: []byte(`{
            "rate_limits": {
                "reserve": {"remaining_percent": 65, "resets_at": "2026-09-06T00:00:00Z"}
            }
        }`), ModTime: now},
	}

	usages, err := codex.New(root).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("len(usages) = %d, want 1", len(usages))
	}
	usage := usages[0]
	if usage.Window != "reserve" || usage.Used != 35 || usage.Limit != 100 || usage.Unit != "percent" {
		t.Fatalf("reserve usage = %+v, want 35%% used", usage)
	}
	wantReset := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if usage.ResetAt == nil || !usage.ResetAt.Equal(wantReset) {
		t.Fatalf("reserve reset = %v, want %v", usage.ResetAt, wantReset)
	}
}

func TestFetchMapsGPTReserveAdditionalRateLimit(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/usage.json": &fstest.MapFile{Data: []byte(`{
            "additional_rate_limits": [{
                "limit_name": "gpt-reserve",
                "rate_limit": {
                    "primary_window": {"used_percent": 12, "reset_at": "2026-09-06T00:00:00Z"}
                }
            }]
        }`), ModTime: now},
	}

	usages, err := codex.New(root).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 1 || usages[0].Window != "reserve" || usages[0].Used != 12 || usages[0].Limit != 100 {
		t.Fatalf("reserve usage = %+v, want 12%% used", usages)
	}
}

func TestFetchMapsGPTReserveSessionRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/sessions/reserve.jsonl": &fstest.MapFile{
			ModTime: now,
			Data:    []byte(`{"timestamp":"2026-09-05T13:00:00Z","payload":{"rate_limits":{"limit_id":"base_model_inference","limit_name":"gpt-reserve","primary":{"used_percent":0,"window_minutes":10080,"resets_at":1789192382},"secondary":null}}}`),
		},
	}

	usages, err := codex.New(root).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 1 || usages[0].Window != "reserve" || usages[0].Used != 0 || usages[0].Limit != 100 {
		t.Fatalf("reserve usage = %+v, want 0%% used", usages)
	}
}

func TestFetchFailsClosedForMalformedJSON(t *testing.T) {
	root := fstest.MapFS{
		".codex/rate_limits.json": &fstest.MapFile{Data: []byte(`{"rate_limits":`)},
	}
	provider := codex.New(root)

	usages, err := provider.Fetch(context.Background())
	if err == nil || len(usages) != 0 {
		t.Fatalf("Fetch() = usages %v, err %v; want no partial result and error", usages, err)
	}
}

func TestFetchReportsUnknownSchema(t *testing.T) {
	root := fstest.MapFS{
		".codex/state.json": &fstest.MapFile{Data: []byte(`{"version":1}`)},
	}
	provider := codex.New(root)

	_, err := provider.Fetch(context.Background())
	if !errors.Is(err, codex.ErrSchemaUnknown) {
		t.Fatalf("Fetch() error = %v, want ErrSchemaUnknown", err)
	}
}

func TestFetchSelectsSourceTimestampAcrossFiles(t *testing.T) {
	newer := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/sessions/a.jsonl": &fstest.MapFile{ModTime: newer, Data: []byte(`{"timestamp":"2026-09-05T13:00:00Z","payload":{"rate_limits":{"primary":{"used_percent":42}}}}`)},
		".codex/sessions/z.jsonl": &fstest.MapFile{ModTime: newer.Add(time.Hour), Data: []byte(`{"timestamp":"2026-09-05T12:00:00Z","payload":{"rate_limits":{"primary":{"used_percent":10}}}}`)},
	}
	result, err := codex.New(root).Fetch(context.Background())
	if err != nil || len(result) != 1 {
		t.Fatalf("%v %v", result, err)
	}
	if result[0].Used != 42 || !result[0].FetchedAt.Equal(newer) {
		t.Fatalf("did not use source timestamp: %+v", result[0])
	}
}
