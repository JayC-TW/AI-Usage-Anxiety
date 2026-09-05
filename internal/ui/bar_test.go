package ui_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aiusage/internal/collector"
	"aiusage/internal/model"
	"aiusage/internal/ui"
)

func TestRenderBarUnknownLimit(t *testing.T) {
	if got := ui.RenderBar(10, 0, 10); got != "n/a" {
		t.Fatalf("RenderBar() = %q, want n/a", got)
	}
}

func TestRenderBarClampsPercentage(t *testing.T) {
	if got := ui.RenderBar(150, 100, 10); got != "██████████" {
		t.Fatalf("RenderBar(over limit) = %q, want full bar", got)
	}
	if got := ui.RenderBar(-1, 100, 10); got != "░░░░░░░░░░" {
		t.Fatalf("RenderBar(negative) = %q, want empty bar", got)
	}
}

func TestRenderBarUsesExpectedWidth(t *testing.T) {
	if got := ui.RenderBar(50, 100, 10); got != "█████░░░░░" {
		t.Fatalf("RenderBar(50%%) = %q, want half-width bar", got)
	}
}

func TestRenderKeepsPreviousValueWhenProviderFails(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	output := ui.Render(collector.Snapshot{
		Statuses: []model.ProviderStatus{{
			Name:      "opencode",
			Available: true,
			Err:       errors.New("timeout"),
			Usages: []model.Usage{{
				Provider:  "opencode",
				Window:    "5h",
				Used:      25,
				Limit:     100,
				FetchedAt: now,
			}},
		}},
		UpdatedAt: now,
	}, now, 80, time.Minute)
	if !strings.Contains(output, "stale: timeout") || !strings.Contains(output, "25.0%~") {
		t.Fatalf("Render() = %q, want stale error and previous value", output)
	}
}

func TestRenderOmitsInteractiveFooter(t *testing.T) {
	output := ui.Render(collector.Snapshot{
		Statuses: []model.ProviderStatus{{
			Name:      "codex",
			Available: true,
			Usages:    []model.Usage{{Provider: "codex", Window: "5h", Used: 25, Limit: 100, Unit: "percent"}},
		}},
	}, time.Now(), 80, 3*time.Minute)

	for _, control := range []string{"[r] refresh", "[tab] detail", "[q] quit"} {
		if strings.Contains(output, control) {
			t.Fatalf("Render() contains removed control %q: %q", control, output)
		}
	}
}

func TestRenderShowsDerivedTokenCountWithoutKnownLimit(t *testing.T) {
	output := ui.Render(collector.Snapshot{
		Statuses: []model.ProviderStatus{{
			Name:      "claude",
			Available: true,
			Usages:    []model.Usage{{Provider: "claude", Window: "7d", Used: 1234567, Unit: "token", Note: "derived from local transcript"}},
		}},
	}, time.Now(), 80, 3*time.Minute)

	if !strings.Contains(output, "1.2M tokens") || !strings.Contains(output, "quota unknown") {
		t.Fatalf("Render() = %q, want derived token count and unknown quota note", output)
	}
}

func TestRenderTruncatesLongLinesToTerminalWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	output := ui.Render(collector.Snapshot{
		Statuses: []model.ProviderStatus{{
			Name:      "opencode",
			Available: true,
			Err:       errors.New(strings.Repeat("provider failure ", 20)),
		}},
	}, time.Now(), 40, 3*time.Minute)

	for _, line := range strings.Split(output, "\n") {
		if width := utf8.RuneCountInString(line); width > 40 {
			t.Fatalf("rendered line width = %d, want <= 40: %q", width, line)
		}
	}
}
