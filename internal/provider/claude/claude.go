package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aiusage/internal/model"
)

var ErrNotInstalled = errors.New("Claude data directory not installed")

var dataRoots = []string{
	".claude",
	"Library/Application Support/Claude/local-agent-mode-sessions",
}

type Provider struct {
	Root          fs.FS
	Now           func() time.Time
	oauthToken    string
	usageEndpoint string
	client        *http.Client
}

func New(root fs.FS) *Provider {
	return &Provider{Root: root, Now: time.Now}
}

// NewWithOAuth enables the official Claude Code usage endpoint while keeping
// the existing local transcript fallback available when the endpoint fails.
func NewWithOAuth(root fs.FS, token, endpoint string, client *http.Client) *Provider {
	provider := New(root)
	provider.oauthToken = strings.TrimSpace(token)
	provider.usageEndpoint = strings.TrimSpace(endpoint)
	if provider.usageEndpoint == "" {
		provider.usageEndpoint = DefaultUsageEndpoint
	}
	provider.client = client
	return provider
}

func (p *Provider) Name() string { return "claude" }

func (p *Provider) Available() bool {
	if p == nil {
		return false
	}
	if p.Root != nil {
		for _, root := range dataRoots {
			if _, err := fs.Stat(p.Root, root); err == nil {
				return true
			}
		}
	}
	return strings.TrimSpace(p.oauthToken) != ""
}

type transcriptRecord struct {
	Timestamp          timestampValue   `json:"timestamp"`
	CreatedAt          timestampValue   `json:"created_at"`
	Usage              usageFields      `json:"usage"`
	Message            messageField     `json:"message"`
	RateLimits         rateLimitWindows `json:"rate_limits"`
	RateLimitInfo      rateLimitInfo    `json:"rate_limit_info"`
	RateLimitInfoCamel rateLimitInfo    `json:"rateLimitInfo"`
	QuotaLimits        quotaLimits      `json:"quotaLimits"`
	QuotaLimitsSnake   quotaLimits      `json:"quota_limits"`
}

type timestampValue struct {
	value string
}

func (t *timestampValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		t.value = text
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	t.value = number.String()
	return nil
}

type messageField struct {
	Usage usageFields `json:"usage"`
}

type rateLimitInfo struct {
	UnifiedWindows      rateLimitWindows `json:"unifiedWindows"`
	UnifiedWindowsSnake rateLimitWindows `json:"unified_windows"`
}

type quotaLimits struct {
	FiveHour            *rateLimitWindow `json:"five_hour"`
	FiveHourCamel       *rateLimitWindow `json:"fiveHour"`
	SevenDay            *rateLimitWindow `json:"seven_day"`
	SevenDayCamel       *rateLimitWindow `json:"sevenDay"`
	UnifiedWindows      rateLimitWindows `json:"unifiedWindows"`
	UnifiedWindowsSnake rateLimitWindows `json:"unified_windows"`
}

type rateLimitWindows struct {
	FiveHour *rateLimitWindow `json:"five_hour"`
	SevenDay *rateLimitWindow `json:"seven_day"`
}

type rateLimitWindow struct {
	UsedPercentage *float64       `json:"used_percentage"`
	Utilization    *float64       `json:"utilization"`
	ResetsAt       timestampValue `json:"resets_at"`
	ResetsAtCamel  timestampValue `json:"resetsAt"`
}

type latestRateLimit struct {
	Percent float64
	ResetAt *time.Time
	At      time.Time
}

type usageFields struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (p *Provider) Fetch(ctx context.Context) ([]model.Usage, error) {
	if !p.Available() {
		return nil, ErrNotInstalled
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if strings.TrimSpace(p.oauthToken) != "" {
		usages, err := p.fetchOAuthUsage(ctx, now)
		if err == nil && len(usages) > 0 {
			return usages, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	if p.Root == nil {
		return nil, ErrNotInstalled
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	var fiveHourTokens, sevenDayTokens int64
	latestLimits := make(map[string]latestRateLimit)

	for _, dataRoot := range dataRoots {
		if _, err := fs.Stat(p.Root, dataRoot); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := fs.WalkDir(p.Root, dataRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.ModTime().Before(cutoff) || info.Size() > 32<<20 {
				return nil
			}
			file, err := p.Root.Open(path)
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 32<<10), 4<<20)
			lineNumber := 0
			for scanner.Scan() {
				lineNumber++
				if err := ctx.Err(); err != nil {
					_ = file.Close()
					return err
				}
				var record transcriptRecord
				if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
					_ = file.Close()
					return fmt.Errorf("parse Claude JSONL %s line %d: %w", path, lineNumber, err)
				}
				timestamp := record.Timestamp.value
				if strings.TrimSpace(timestamp) == "" {
					timestamp = record.CreatedAt.value
				}
				when, err := parseTimestamp(timestamp)
				if err != nil || when.Before(cutoff) || when.After(now) {
					continue
				}
				recordRateLimits(latestLimits, record.RateLimits, when)
				recordRateLimits(latestLimits, record.RateLimitInfo.UnifiedWindows, when)
				recordRateLimits(latestLimits, record.RateLimitInfo.UnifiedWindowsSnake, when)
				recordRateLimits(latestLimits, record.RateLimitInfoCamel.UnifiedWindows, when)
				recordRateLimits(latestLimits, record.RateLimitInfoCamel.UnifiedWindowsSnake, when)
				recordQuotaLimits(latestLimits, record.QuotaLimits, when)
				recordQuotaLimits(latestLimits, record.QuotaLimitsSnake, when)
				tokens := totalTokens(record.Usage) + totalTokens(record.Message.Usage)
				if tokens <= 0 {
					continue
				}
				sevenDayTokens += tokens
				if !when.Before(now.Add(-5 * time.Hour)) {
					fiveHourTokens += tokens
				}
			}
			scanErr := scanner.Err()
			closeErr := file.Close()
			if scanErr != nil {
				return scanErr
			}
			return closeErr
		})
		if err != nil {
			return nil, err
		}
	}

	result := make([]model.Usage, 0, 2)
	if limit, ok := latestLimits["5h"]; ok {
		result = append(result, snapshotUsage("5h", limit, now))
	} else if fiveHourTokens > 0 {
		result = append(result, derivedUsage("5h", fiveHourTokens, now))
	}
	if limit, ok := latestLimits["7d"]; ok {
		result = append(result, snapshotUsage("7d", limit, now))
	} else if sevenDayTokens > 0 {
		result = append(result, derivedUsage("7d", sevenDayTokens, now))
	}
	return result, nil
}

func recordRateLimits(latest map[string]latestRateLimit, windows rateLimitWindows, at time.Time) {
	for name, window := range map[string]*rateLimitWindow{
		"5h": windows.FiveHour,
		"7d": windows.SevenDay,
	} {
		if window == nil {
			continue
		}
		percent, ok := rateLimitPercent(*window)
		if !ok {
			continue
		}
		previous, found := latest[name]
		if found && at.Before(previous.At) {
			continue
		}
		latest[name] = latestRateLimit{Percent: percent, ResetAt: rateLimitReset(*window), At: at}
	}
}

func recordQuotaLimits(latest map[string]latestRateLimit, limits quotaLimits, at time.Time) {
	recordRateLimits(latest, limits.UnifiedWindows, at)
	recordRateLimits(latest, limits.UnifiedWindowsSnake, at)
	recordRateLimits(latest, rateLimitWindows{
		FiveHour: limits.FiveHour,
		SevenDay: limits.SevenDay,
	}, at)
	recordRateLimits(latest, rateLimitWindows{
		FiveHour: limits.FiveHourCamel,
		SevenDay: limits.SevenDayCamel,
	}, at)
}

func rateLimitPercent(window rateLimitWindow) (float64, bool) {
	if window.UsedPercentage != nil {
		return validPercent(*window.UsedPercentage)
	}
	if window.Utilization != nil {
		percent := *window.Utilization
		if percent >= 0 && percent <= 1 {
			percent *= 100
		}
		return validPercent(percent)
	}
	return 0, false
}

func validPercent(percent float64) (float64, bool) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return 0, false
	}
	return percent, true
}

func rateLimitReset(window rateLimitWindow) *time.Time {
	for _, value := range []string{window.ResetsAt.value, window.ResetsAtCamel.value} {
		if parsed, err := parseTimestamp(value); err == nil {
			return &parsed
		}
	}
	return nil
}

func snapshotUsage(window string, limit latestRateLimit, fetchedAt time.Time) model.Usage {
	sourceAt := limit.At
	if sourceAt.IsZero() {
		sourceAt = fetchedAt
	}
	return model.Usage{
		Provider:  "claude",
		Window:    window,
		Used:      limit.Percent,
		Limit:     100,
		Unit:      "percent",
		ResetAt:   limit.ResetAt,
		Source:    model.SourceLocalFile,
		FetchedAt: sourceAt,
		Note:      "from Claude Code local rate-limit snapshot",
	}
}

func totalTokens(usage usageFields) int64 {
	return usage.InputTokens + usage.OutputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
}

func derivedUsage(window string, tokens int64, fetchedAt time.Time) model.Usage {
	return model.Usage{
		Provider:  "claude",
		Window:    window,
		Used:      float64(tokens),
		Limit:     0,
		Unit:      "token",
		Source:    model.SourceDerived,
		FetchedAt: fetchedAt,
		Note:      "derived from local transcript",
	}
}

func parseTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("timestamp is empty")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds >= 100_000_000_000 {
			return time.UnixMilli(seconds), nil
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, errors.New("timestamp is not RFC3339")
}
