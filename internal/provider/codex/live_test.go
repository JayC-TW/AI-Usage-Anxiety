package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"aiusage/internal/model"
)

func TestParseLiveRateLimitResponse(t *testing.T) {
	fetchedAt := time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC)
	data := []byte(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1788605766},"secondary":{"usedPercent":16,"windowDurationMins":10080,"resetsAt":1789192566}},"rateLimitsByLimitId":{"base_model_inference":{"limitId":"base_model_inference","limitName":"gpt-reserve","primary":{"usedPercent":28,"windowDurationMins":10080,"resetsAt":1789194323},"secondary":null}}}`)

	usages, err := parseLiveRateLimitResponse(data, fetchedAt)
	if err != nil {
		t.Fatalf("parseLiveRateLimitResponse() error = %v", err)
	}
	if len(usages) != 3 {
		t.Fatalf("len(usages) = %d, want 3: %+v", len(usages), usages)
	}
	want := map[string]float64{"5h": 100, "7d": 16, "reserve": 28}
	for _, usage := range usages {
		if usage.Provider != "codex" || usage.Limit != 100 || usage.Unit != "percent" || usage.Source != model.SourceEndpoint {
			t.Errorf("usage metadata = %+v", usage)
		}
		if usage.Used != want[usage.Window] {
			t.Errorf("%s used = %v, want %v", usage.Window, usage.Used, want[usage.Window])
		}
		if !usage.FetchedAt.Equal(fetchedAt) {
			t.Errorf("%s fetchedAt = %v, want %v", usage.Window, usage.FetchedAt, fetchedAt)
		}
	}
}

func TestParseLiveRateLimitResponseKeepsReserveOutOfSevenDay(t *testing.T) {
	data := []byte(`{"rateLimits":{"limitId":"base_model_inference","limitName":"gpt-reserve","primary":{"usedPercent":28,"windowDurationMins":10080,"resetsAt":1789194323},"secondary":null},"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1788605766},"secondary":{"usedPercent":16,"windowDurationMins":10080,"resetsAt":1789192566}}}}`)

	usages, err := parseLiveRateLimitResponse(data, time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parseLiveRateLimitResponse() error = %v", err)
	}
	if len(usages) != 3 {
		t.Fatalf("len(usages) = %d, usages = %+v; want 5h, 7d, reserve", len(usages), usages)
	}
	want := map[string]float64{"5h": 100, "7d": 16, "reserve": 28}
	seen := make(map[string]bool, len(usages))
	for _, usage := range usages {
		wantUsed, ok := want[usage.Window]
		if !ok {
			t.Errorf("unexpected window %q; usages = %+v", usage.Window, usages)
			continue
		}
		seen[usage.Window] = true
		if usage.Used != wantUsed {
			t.Errorf("%s used = %v, want %v; usages = %+v", usage.Window, usage.Used, wantUsed, usages)
		}
	}
	for window := range want {
		if !seen[window] {
			t.Errorf("missing %s usage; usages = %+v", window, usages)
		}
	}
}

func TestParseLiveRateLimitResponseRecognizesCompositeReserveIdentity(t *testing.T) {
	data := []byte(`{"rateLimits":{"limitId":"openai-codex:base-model-inference:primary","primary":{"usedPercent":28,"windowDurationMins":10080,"resetsAt":1789194323},"secondary":null},"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1788605766},"secondary":{"usedPercent":16,"windowDurationMins":10080,"resetsAt":1789192566}}}}`)

	usages, err := parseLiveRateLimitResponse(data, time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parseLiveRateLimitResponse() error = %v", err)
	}
	want := map[string]float64{"5h": 100, "7d": 16, "reserve": 28}
	if len(usages) != len(want) {
		t.Fatalf("len(usages) = %d, usages = %+v; want %d windows", len(usages), usages, len(want))
	}
	for _, usage := range usages {
		if want[usage.Window] != usage.Used {
			t.Errorf("%s used = %v, want %v; usages = %+v", usage.Window, usage.Used, want[usage.Window], usages)
		}
		delete(want, usage.Window)
	}
	if len(want) != 0 {
		t.Errorf("missing windows: %v; usages = %+v", want, usages)
	}
}

func TestParseLiveRateLimitResponseRecognizesCompositeReserveMapKey(t *testing.T) {
	data := []byte(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300},"secondary":{"usedPercent":16,"windowDurationMins":10080}},"rateLimitsByLimitId":{"openai-codex:base-model-inference:primary":{"primary":{"usedPercent":28,"windowDurationMins":10080},"secondary":null}}}`)

	usages, err := parseLiveRateLimitResponse(data, time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parseLiveRateLimitResponse() error = %v", err)
	}
	want := map[string]float64{"5h": 100, "7d": 16, "reserve": 28}
	if len(usages) != len(want) {
		t.Fatalf("len(usages) = %d, usages = %+v; want %d windows", len(usages), usages, len(want))
	}
	for _, usage := range usages {
		if want[usage.Window] != usage.Used {
			t.Errorf("%s used = %v, want %v; usages = %+v", usage.Window, usage.Used, want[usage.Window], usages)
		}
		delete(want, usage.Window)
	}
	if len(want) != 0 {
		t.Errorf("missing windows: %v; usages = %+v", want, usages)
	}
}

func TestParseLiveRateLimitResponseDoesNotOverwriteReservePrimary(t *testing.T) {
	data := []byte(`{"rateLimits":{"limitId":"base-model-inference","limitName":"gpt-reserve","primary":{"usedPercent":28,"windowDurationMins":10080,"resetsAt":1789194323},"secondary":{"usedPercent":72,"windowDurationMins":300,"resetsAt":1788605766}}}`)

	usages, err := parseLiveRateLimitResponse(data, time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parseLiveRateLimitResponse() error = %v", err)
	}
	if len(usages) != 1 || usages[0].Window != "reserve" || usages[0].Used != 28 {
		t.Fatalf("usages = %+v, want reserve primary at 28%%", usages)
	}
}

func TestFetchLiveRateLimitsAllowsAppServerStartup(t *testing.T) {
	commandPath := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
IFS= read -r _ || exit 0
IFS= read -r _ || exit 0
IFS= read -r _ || exit 0
/bin/sleep 2
printf '%s\n' '{"id":2,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300},"secondary":{"usedPercent":16,"windowDurationMins":10080}},"rateLimitsByLimitId":{"base_model_inference":{"limitId":"base_model_inference","limitName":"gpt-reserve","primary":{"usedPercent":86,"windowDurationMins":10080},"secondary":null}}}}'
`
	if err := os.WriteFile(commandPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Codex command: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(commandPath)+string(os.PathListSeparator)+os.Getenv("PATH"))

	usages, err := fetchLiveRateLimits(context.Background(), time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fetchLiveRateLimits() error = %v", err)
	}
	want := map[string]float64{"5h": 100, "7d": 16, "reserve": 86}
	if len(usages) != len(want) {
		t.Fatalf("len(usages) = %d, usages = %+v; want %+v", len(usages), usages, want)
	}
	for _, usage := range usages {
		if usage.Used != want[usage.Window] {
			t.Errorf("%s used = %v, want %v; usages = %+v", usage.Window, usage.Used, want[usage.Window], usages)
		}
	}
}

func TestFetchPrefersLiveRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/local.json": &fstest.MapFile{
			ModTime: now,
			Data:    []byte(`{"primary":{"used_percent":1}}`),
		},
	}
	provider := New(root)
	provider.Now = func() time.Time { return now }
	provider.live = func(context.Context, time.Time) ([]model.Usage, error) {
		return []model.Usage{{Provider: "codex", Window: "7d", Used: 16, Limit: 100, Unit: "percent", Source: model.SourceEndpoint, FetchedAt: now}}, nil
	}

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 1 || usages[0].Source != model.SourceEndpoint || usages[0].Used != 16 {
		t.Fatalf("Fetch() = %+v, want live usage", usages)
	}
}

func TestFetchFallsBackToLocalRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 5, 15, 30, 0, 0, time.UTC)
	root := fstest.MapFS{
		".codex/local.json": &fstest.MapFile{
			ModTime: now,
			Data:    []byte(`{"primary":{"used_percent":42}}`),
		},
	}
	provider := New(root)
	provider.live = func(context.Context, time.Time) ([]model.Usage, error) {
		return nil, errors.New("live source unavailable")
	}

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 1 || usages[0].Source != model.SourceLocalFile || usages[0].Used != 42 {
		t.Fatalf("Fetch() = %+v, want local fallback", usages)
	}
}
