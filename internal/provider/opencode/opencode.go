package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aiusage/internal/model"
)

const (
	DefaultEndpoint = "https://opencode.ai/zen/go/v1/usage"
	providerName    = "opencode"
	userAgent       = "aiusage/0.1"
	sessionHeader   = "aiusage"
)

var (
	ErrMissingKey   = errors.New("OpenCode Go API key required")
	ErrUnauthorized = errors.New("OpenCode Go API key unauthorized")
	ErrNotEntitled  = errors.New("OpenCode Go subscription required")
)

type Provider struct {
	key      string
	Endpoint string
	Client   *http.Client
	Now      func() time.Time
}

type RateLimitError struct{ RetryAfter time.Time }

func (e *RateLimitError) Error() string { return "OpenCode Go usage returned HTTP 429" }

func New(key string, client *http.Client) *Provider {
	return NewWithEndpoint(key, DefaultEndpoint, client)
}

func NewWithEndpoint(key, endpoint string, client *http.Client) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{
		key:      strings.TrimSpace(key),
		Endpoint: endpoint,
		Client:   client,
		Now:      time.Now,
	}
}

func (p *Provider) Name() string { return providerName }

// OpenCode Go is a configured remote provider. Missing credentials are a
// fetch error so the UI can explain the required first-startup action.
func (p *Provider) Available() bool { return p != nil }

type apiResponse struct {
	Usage *usagePayload `json:"usage"`
}

type usagePayload struct {
	Rolling *windowPayload `json:"rolling"`
	Weekly  *windowPayload `json:"weekly"`
	Monthly *windowPayload `json:"monthly"`
}

type windowPayload struct {
	Status   string   `json:"status"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resetsAt"`
}

func (p *Provider) Fetch(ctx context.Context) ([]model.Usage, error) {
	if p == nil || strings.TrimSpace(p.key) == "" {
		return nil, ErrMissingKey
	}
	if strings.TrimSpace(p.Endpoint) == "" {
		return nil, errors.New("OpenCode Go usage endpoint is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create OpenCode Go usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-opencode-session", sessionHeader)

	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenCode Go usage: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		until := time.Now().Add(5 * time.Minute)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds >= 0 && seconds <= 86400 {
			until = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && date.After(time.Now()) {
			until = date
		}
		return nil, &RateLimitError{RetryAfter: until}
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnauthorized, resp.StatusCode)
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w: HTTP %d", ErrNotEntitled, resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("OpenCode Go usage returned HTTP %d", resp.StatusCode)
	}

	var payload apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode OpenCode Go usage: %w", err)
	}
	if payload.Usage == nil {
		return nil, errors.New("OpenCode Go usage response missing usage")
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	return []model.Usage{
		mapWindow("5h", payload.Usage.Rolling, now),
		mapWindow("7d", payload.Usage.Weekly, now),
		mapWindow("monthly", payload.Usage.Monthly, now),
	}, nil
}

func mapWindow(window string, payload *windowPayload, fetchedAt time.Time) model.Usage {
	usage := model.Usage{
		Provider:  providerName,
		Window:    window,
		Unit:      "percent",
		Source:    model.SourceEndpoint,
		FetchedAt: fetchedAt,
	}
	if payload == nil {
		usage.Note = "usage window missing"
		return usage
	}
	if payload.Percent == nil || math.IsNaN(*payload.Percent) || math.IsInf(*payload.Percent, 0) || *payload.Percent < 0 {
		usage.Note = "usage percent missing or invalid"
		return usage
	}
	usage.Used = *payload.Percent
	usage.Limit = 100
	if payload.Status == "rate-limited" {
		usage.Note = "rate-limited"
	} else if payload.Status != "" && payload.Status != "ok" {
		usage.Note = "status: " + payload.Status
	}
	if resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ResetsAt)); err == nil {
		usage.ResetAt = &resetAt
	} else if strings.TrimSpace(payload.ResetsAt) != "" {
		usage.Note = appendNote(usage.Note, "invalid reset time")
	}
	return usage
}

func appendNote(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}
