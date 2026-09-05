package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiusage/internal/model"
)

var (
	ErrNotInstalled  = errors.New("Codex data directory not installed")
	ErrSchemaUnknown = errors.New("Codex usage fields not found")
)

type Provider struct {
	Root fs.FS
	Now  func() time.Time
	live func(context.Context, time.Time) ([]model.Usage, error)
}

func New(root fs.FS) *Provider {
	return &Provider{Root: root, Now: time.Now}
}

func NewWithLive(root fs.FS) *Provider {
	provider := New(root)
	provider.live = fetchLiveRateLimits
	return provider
}

func (p *Provider) Name() string { return "codex" }

// CollectionTimeout gives Codex app-server enough time for a cold start. The
// collector keeps the shorter default for providers that do not need a CLI.
func (p *Provider) CollectionTimeout() time.Duration { return codexCollectionTimeout }

func (p *Provider) Available() bool {
	if p == nil || p.Root == nil {
		return false
	}
	_, err := fs.Stat(p.Root, ".codex")
	return err == nil
}

func (p *Provider) Fetch(ctx context.Context) ([]model.Usage, error) {
	if !p.Available() {
		return nil, ErrNotInstalled
	}
	if p.live != nil {
		fetchedAt := time.Now()
		if p.Now != nil {
			fetchedAt = p.Now()
		}
		if usages, err := p.live(ctx, fetchedAt); err == nil && len(usages) > 0 {
			return usages, nil
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return p.fetchLocal(ctx)
}

func (p *Provider) fetchLocal(ctx context.Context) ([]model.Usage, error) {

	var samples []windowSample
	err := fs.WalkDir(p.Root, ".codex", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (filepath.Ext(path) != ".json" && filepath.Ext(path) != ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 64<<20 {
			return nil
		}
		file, err := p.Root.Open(path)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(file)
		decoder.UseNumber()
		fileSamples, decodeErr := scanJSON(decoder)
		closeErr := file.Close()
		if decodeErr != nil {
			return fmt.Errorf("parse Codex JSON %s: %w", path, decodeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		for i := range fileSamples {
			if fileSamples[i].At.IsZero() {
				fileSamples[i].At = info.ModTime()
			}
		}
		samples = append(samples, fileSamples...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, ErrSchemaUnknown
	}

	byWindow := make(map[string]windowSample)
	for _, sample := range samples {
		if previous, ok := byWindow[sample.Window]; !ok || !sample.At.Before(previous.At) {
			byWindow[sample.Window] = sample
		}
	}
	windows := make([]string, 0, len(byWindow))
	for window := range byWindow {
		windows = append(windows, window)
	}
	sort.Strings(windows)
	result := make([]model.Usage, 0, len(windows))
	for _, window := range windows {
		sample := byWindow[window]
		usage := model.Usage{
			Provider:  "codex",
			Window:    window,
			Used:      sample.Used,
			Limit:     sample.Limit,
			Unit:      sample.Unit,
			Source:    model.SourceLocalFile,
			FetchedAt: sample.At,
			Note:      "parsed from local Codex data",
		}
		if sample.ResetAt != nil {
			resetAt := *sample.ResetAt
			usage.ResetAt = &resetAt
		}
		result = append(result, usage)
	}
	return result, nil
}

type windowSample struct {
	At      time.Time
	Window  string
	Used    float64
	Limit   float64
	Unit    string
	ResetAt *time.Time
}

func scanJSON(decoder *json.Decoder) ([]windowSample, error) {
	var samples []windowSample
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return samples, nil
			}
			return nil, err
		}
		var meta struct {
			Timestamp string `json:"timestamp"`
		}
		_ = json.Unmarshal(raw, &meta)
		at, _ := time.Parse(time.RFC3339Nano, meta.Timestamp)
		part := json.NewDecoder(bytes.NewReader(raw))
		part.UseNumber()
		var current []windowSample
		if err := scanValue(part, &current); err != nil {
			return nil, err
		}
		for i := range current {
			current[i].At = at
		}
		samples = append(samples, current...)
	}
}

func scanValue(decoder *json.Decoder, samples *[]windowSample) error {
	return scanValueWithWindowHint(decoder, samples, "")
}

func scanValueWithWindowHint(decoder *json.Decoder, samples *[]windowSample, windowHint string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("Codex JSON object key is not a string")
				}
				if strings.EqualFold(key, "rate_limits") {
					if err := scanRateLimits(decoder, samples); err != nil {
						return err
					}
					continue
				}
				if strings.EqualFold(key, "additional_rate_limits") {
					if err := scanAdditionalRateLimits(decoder, samples); err != nil {
						return err
					}
					continue
				}
				if window := windowForKey(key); window != "" {
					if windowHint != "" {
						window = windowHint
					}
					sample, err := scanWindow(decoder, window)
					if err != nil {
						return err
					}
					if sample != nil {
						*samples = append(*samples, *sample)
					}
					continue
				}
				if err := scanValueWithWindowHint(decoder, samples, windowHint); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := scanValueWithWindowHint(decoder, samples, windowHint); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
	}
	return nil
}

func scanAdditionalRateLimits(decoder *json.Decoder, samples *[]windowSample) error {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	var entries []struct {
		LimitName string          `json:"limit_name"`
		RateLimit json.RawMessage `json:"rate_limit"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	for _, entry := range entries {
		if !isReserveLimitName(entry.LimitName) || len(entry.RateLimit) == 0 {
			continue
		}
		part := json.NewDecoder(bytes.NewReader(entry.RateLimit))
		part.UseNumber()
		if err := scanValueWithWindowHint(part, samples, "reserve"); err != nil {
			return err
		}
	}
	return nil
}

func scanRateLimits(decoder *json.Decoder, samples *[]windowSample) error {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	var meta struct {
		LimitID   string `json:"limit_id"`
		LimitName string `json:"limit_name"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	windowHint := ""
	if isReserveLimitName(meta.LimitName) || isReserveLimitName(meta.LimitID) {
		windowHint = "reserve"
	}
	part := json.NewDecoder(bytes.NewReader(raw))
	part.UseNumber()
	return scanValueWithWindowHint(part, samples, windowHint)
}

func isReserveLimitName(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"), " ", "_"))
	if normalized == "gpt_reserve" || normalized == "luna_reserve" || normalized == "reserve" || normalized == "base_model_inference" {
		return true
	}
	// App-server keys are opaque and may include a provider namespace and
	// window suffix, for example openai-codex:base-model-inference:primary.
	return strings.Contains(normalized, "gpt_reserve") ||
		strings.Contains(normalized, "luna_reserve") ||
		strings.Contains(normalized, "base_model_inference")
}

func scanWindow(decoder *json.Decoder, window string) (*windowSample, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, nil
	}
	sample := &windowSample{Window: window, Unit: "percent"}
	hasUsed := false
	var windowMinutes float64
	hasWindowMinutes := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(keyToken.(string))
		switch key {
		case "used_percent", "usedpercentage", "percent":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			sample.Used = value
			sample.Limit = 100
			hasUsed = true
		case "remaining_percent", "remainingpercentage":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			if value >= 0 && value <= 100 {
				sample.Used = 100 - value
				sample.Limit = 100
				hasUsed = true
			}
		case "used":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			sample.Used = value
			hasUsed = true
		case "limit", "total":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			sample.Limit = value
		case "window_minutes", "windowdurationmins":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			windowMinutes = value
			hasWindowMinutes = true
		case "window_duration_seconds", "windowdurationseconds", "limit_window_seconds":
			value, err := numberToken(decoder)
			if err != nil {
				return nil, err
			}
			windowMinutes = value / 60
			hasWindowMinutes = true
		case "reset_at", "resetat", "resets_at", "resetsat":
			resetAt, err := timeToken(decoder)
			if err != nil {
				return nil, err
			}
			sample.ResetAt = resetAt
		default:
			if err := skipValue(decoder); err != nil {
				return nil, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if !hasUsed {
		return nil, nil
	}
	// Codex sometimes reports a single `primary` window whose duration is 7d.
	// The duration is authoritative when present; keep the field-name aliases
	// as the fallback for older snapshots without a duration.
	if hasWindowMinutes && (window == "5h" || window == "7d") {
		switch windowMinutes {
		case 300:
			sample.Window = "5h"
		case 10080:
			sample.Window = "7d"
		}
	}
	return sample, nil
}

func numberToken(decoder *json.Decoder) (float64, error) {
	token, err := decoder.Token()
	if err != nil {
		return 0, err
	}
	if number, ok := token.(json.Number); ok {
		value, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid usage number: %w", err)
		}
		return value, nil
	}
	if text, ok := token.(string); ok {
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid usage number: %w", err)
		}
		return value, nil
	}
	return 0, errors.New("usage field is not numeric")
}

func timeToken(decoder *json.Decoder) (*time.Time, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, nil
		}
		return &parsed, nil
	case json.Number:
		seconds, err := value.Int64()
		if err != nil {
			return nil, nil
		}
		parsed := time.Unix(seconds, 0)
		return &parsed, nil
	default:
		return nil, nil
	}
}

func skipValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{', '[':
			for decoder.More() {
				if delimiter == '{' {
					if _, err := decoder.Token(); err != nil {
						return err
					}
				}
				if err := skipValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		}
	}
	return err
}

func windowForKey(key string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	switch normalized {
	case "primary", "primary_window", "rolling", "5h", "five_hour", "five_hours":
		return "5h"
	case "secondary", "secondary_window", "weekly", "7d", "seven_day", "seven_days":
		return "7d"
	case "reserve", "reserved", "reserve_window", "gpt_reserve", "luna", "luna_reserve", "luna_reserve_window":
		return "reserve"
	default:
		return ""
	}
}
