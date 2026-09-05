package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aiusage/internal/model"
)

const DefaultUsageEndpoint = "https://api.anthropic.com/api/oauth/usage"

var (
	errOAuthUnavailable  = errors.New("Claude OAuth usage unavailable")
	errOAuthUnauthorized = errors.New("Claude OAuth token unauthorized")
	errOAuthRateLimited  = errors.New("Claude OAuth usage rate limited")
)

type oauthUsageResponse struct {
	FiveHour  *oauthUsageWindow `json:"five_hour"`
	SevenDay  *oauthUsageWindow `json:"seven_day"`
	LimitsRaw json.RawMessage   `json:"limits"`
}

type oauthUsageWindow struct {
	Utilization    *float64 `json:"utilization"`
	Percent        *float64 `json:"percent"`
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       string   `json:"resets_at"`
	ResetsAtCamel  string   `json:"resetsAt"`
}

type oauthUsageLimit struct {
	Kind           string   `json:"kind"`
	Group          string   `json:"group"`
	Utilization    *float64 `json:"utilization"`
	Percent        *float64 `json:"percent"`
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       string   `json:"resets_at"`
	ResetsAtCamel  string   `json:"resetsAt"`
}

func (p *Provider) fetchOAuthUsage(ctx context.Context, fetchedAt time.Time) ([]model.Usage, error) {
	token := strings.TrimSpace(p.oauthToken)
	if token == "" {
		return nil, errOAuthUnavailable
	}
	endpoint := strings.TrimSpace(p.usageEndpoint)
	if endpoint == "" {
		endpoint = DefaultUsageEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errOAuthUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	request.Header.Set("User-Agent", "aiusage/0.1")

	client := p.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %v", errOAuthUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errOAuthUnauthorized
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, errOAuthRateLimited
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: status %d", errOAuthUnavailable, response.StatusCode)
	}

	var payload oauthUsageResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: decode response", errOAuthUnavailable)
	}
	usages := mapOAuthUsage(payload, fetchedAt)
	if len(usages) == 0 {
		return nil, errOAuthUnavailable
	}
	return usages, nil
}

func mapOAuthUsage(payload oauthUsageResponse, fetchedAt time.Time) []model.Usage {
	usages := make([]model.Usage, 0, 2)
	seen := make(map[string]bool, 2)
	for _, item := range []struct {
		name string
		data *oauthUsageWindow
	}{
		{name: "5h", data: payload.FiveHour},
		{name: "7d", data: payload.SevenDay},
	} {
		if usage, ok := newOAuthUsage(item.name, item.data, fetchedAt); ok {
			usages = append(usages, usage)
			seen[item.name] = true
		}
	}

	var limits []oauthUsageLimit
	if len(payload.LimitsRaw) > 0 && string(payload.LimitsRaw) != "null" {
		_ = json.Unmarshal(payload.LimitsRaw, &limits)
	}
	for _, limit := range limits {
		name, ok := oauthLimitWindow(limit)
		if !ok || seen[name] {
			continue
		}
		window := &oauthUsageWindow{
			Utilization:    limit.Utilization,
			Percent:        limit.Percent,
			UsedPercentage: limit.UsedPercentage,
			ResetsAt:       limit.ResetsAt,
			ResetsAtCamel:  limit.ResetsAtCamel,
		}
		if usage, ok := newOAuthUsage(name, window, fetchedAt); ok {
			usages = append(usages, usage)
			seen[name] = true
		}
	}
	return usages
}

func oauthLimitWindow(limit oauthUsageLimit) (string, bool) {
	kind := strings.ToLower(strings.TrimSpace(limit.Kind))
	group := strings.ToLower(strings.TrimSpace(limit.Group))
	switch kind {
	case "session":
		return "5h", true
	case "weekly_all":
		return "7d", true
	case "weekly":
		return "7d", group == "weekly"
	default:
		return "", false
	}
}

func newOAuthUsage(window string, data *oauthUsageWindow, fetchedAt time.Time) (model.Usage, bool) {
	if data == nil {
		return model.Usage{}, false
	}
	percent, ok := oauthPercent(data.Utilization, data.Percent, data.UsedPercentage)
	if !ok {
		return model.Usage{}, false
	}
	return model.Usage{
		Provider:  "claude",
		Window:    window,
		Used:      percent,
		Limit:     100,
		Unit:      "percent",
		ResetAt:   oauthReset(data.ResetsAt, data.ResetsAtCamel),
		Source:    model.SourceEndpoint,
		FetchedAt: fetchedAt,
		Note:      "from Anthropic OAuth usage endpoint",
	}, true
}

func oauthPercent(values ...*float64) (float64, bool) {
	for _, value := range values {
		if value == nil {
			continue
		}
		if percent, ok := validPercent(*value); ok {
			return percent, true
		}
	}
	return 0, false
}

func oauthReset(values ...string) *time.Time {
	for _, value := range values {
		if parsed, err := parseTimestamp(value); err == nil {
			return &parsed
		}
	}
	return nil
}
