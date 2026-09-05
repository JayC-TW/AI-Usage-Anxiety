package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider/opencode"
)

type bridgeRequest struct {
	SchemaVersion    int      `json:"schemaVersion"`
	OpenCodeKey      string   `json:"opencodeKey"`
	ClaudeOAuthToken string   `json:"claudeOAuthToken"`
	SkipProviders    []string `json:"skipProviders"`
}
type bridgeStatus struct {
	Name       string        `json:"name"`
	Available  bool          `json:"available"`
	Usages     []model.Usage `json:"usages"`
	ErrorCode  string        `json:"errorCode,omitempty"`
	Error      string        `json:"error,omitempty"`
	Deferred   bool          `json:"deferred,omitempty"`
	RetryAfter *time.Time    `json:"retryAfter,omitempty"`
}
type bridgeResponse struct {
	SchemaVersion int                `json:"schemaVersion"`
	UpdatedAt     time.Time          `json:"updatedAt"`
	Statuses      []bridgeStatus     `json:"statuses"`
	Settings      map[string]float64 `json:"settings"`
}

func (a *application) runBridge(ctx context.Context, in io.Reader, out io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(in, 16385))
	var request bridgeRequest
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &request) != nil || request.SchemaVersion != 1 || strings.ContainsAny(request.OpenCodeKey, "\r\n") || strings.ContainsAny(request.ClaudeOAuthToken, "\r\n") || len(request.ClaudeOAuthToken) > 8192 {
		return 2
	}
	skipped := false
	for _, name := range request.SkipProviders {
		if name != "opencode" {
			return 2
		}
		skipped = true
	}
	a.key = strings.TrimSpace(request.OpenCodeKey)
	a.claudeOAuthToken = strings.TrimSpace(request.ClaudeOAuthToken)
	// This path never resolves credentials or prompts. Clone provider settings.
	enabled := make(map[string]bool)
	for name, value := range a.config.Providers {
		enabled[name] = value
	}
	if skipped {
		enabled["opencode"] = false
	}
	a.config.Providers = enabled
	a.buildCollector()
	snapshot := a.collector.Collect(ctx)
	response := bridgeResponse{SchemaVersion: 1, UpdatedAt: snapshot.UpdatedAt, Statuses: []bridgeStatus{}, Settings: map[string]float64{"warn": a.config.Warn, "danger": a.config.Danger}}
	code := 0
	for _, status := range snapshot.Statuses {
		item := bridgeStatus{Name: status.Name, Available: status.Available, Usages: status.Usages}
		if item.Usages == nil {
			item.Usages = []model.Usage{}
		}
		if status.Err != nil {
			code = 1
			item.ErrorCode = bridgeErrorCode(status.Err)
			// Never echo raw errors that may contain user paths or credential material.
			item.Error = item.ErrorCode
			var limited *opencode.RateLimitError
			if errors.As(status.Err, &limited) {
				item.RetryAfter = &limited.RetryAfter
			}
		}
		response.Statuses = append(response.Statuses, item)
	}
	if skipped {
		response.Statuses = append(response.Statuses, bridgeStatus{Name: "opencode", Available: true, Usages: []model.Usage{}, Deferred: true})
	}
	if json.NewEncoder(out).Encode(response) != nil {
		return 2
	}
	return code
}

func bridgeErrorCode(err error) string {
	switch {
	case errors.Is(err, opencode.ErrMissingKey):
		return "missing_key"
	case errors.Is(err, opencode.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, opencode.ErrNotEntitled):
		return "not_entitled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, fs.ErrPermission):
		return "read_denied"
	}
	var limited *opencode.RateLimitError
	if errors.As(err, &limited) {
		return "rate_limited"
	}
	if strings.Contains(strings.ToLower(err.Error()), "parse") || strings.Contains(strings.ToLower(err.Error()), "decode") {
		return "parse_error"
	}
	return "fetch_failed"
}
