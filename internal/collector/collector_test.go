package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"aiusage/internal/collector"
	"aiusage/internal/model"
	"aiusage/internal/provider"
)

type fakeProvider struct {
	name      string
	available bool
	usages    []model.Usage
	err       error
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) Available() bool { return p.available }

func (p fakeProvider) Fetch(context.Context) ([]model.Usage, error) {
	return p.usages, p.err
}

type timeoutProvider struct {
	fakeProvider
	timeout time.Duration
}

func (p timeoutProvider) CollectionTimeout() time.Duration { return p.timeout }

func (p timeoutProvider) Fetch(ctx context.Context) ([]model.Usage, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < p.timeout-time.Second {
		return nil, errors.New("provider-specific timeout was not applied")
	}
	return p.usages, p.err
}

func TestCollectIsolatesProviderFailure(t *testing.T) {
	goodUsage := model.Usage{
		Provider:  "good",
		Window:    "5h",
		Used:      10,
		Limit:     100,
		Unit:      "percent",
		Source:    model.SourceLocalFile,
		FetchedAt: time.Now(),
	}
	providers := []provider.Provider{
		fakeProvider{name: "good", available: true, usages: []model.Usage{goodUsage}},
		fakeProvider{name: "bad", available: true, err: errors.New("fixture failure")},
		fakeProvider{name: "missing", available: false},
	}

	snapshot := collector.New(providers, time.Minute).Collect(context.Background())
	if len(snapshot.Statuses) != 3 {
		t.Fatalf("len(Statuses) = %d, want 3", len(snapshot.Statuses))
	}
	if len(snapshot.Statuses[0].Usages) != 1 || snapshot.Statuses[0].Err != nil {
		t.Fatalf("good status = %+v, want successful usage", snapshot.Statuses[0])
	}
	if snapshot.Statuses[1].Err == nil {
		t.Fatalf("bad status = %+v, want error", snapshot.Statuses[1])
	}
	if snapshot.Statuses[2].Available {
		t.Fatalf("missing status = %+v, want unavailable", snapshot.Statuses[2])
	}
}

func TestCollectUsesProviderSpecificTimeout(t *testing.T) {
	usage := model.Usage{Provider: "codex", Window: "5h", Used: 10, Limit: 100, Unit: "percent"}
	providers := []provider.Provider{
		timeoutProvider{
			fakeProvider: fakeProvider{name: "codex", available: true, usages: []model.Usage{usage}},
			timeout:      10 * time.Second,
		},
	}

	snapshot := collector.New(providers, time.Minute).Collect(context.Background())
	if len(snapshot.Statuses) != 1 || snapshot.Statuses[0].Err != nil || len(snapshot.Statuses[0].Usages) != 1 {
		t.Fatalf("snapshot = %+v, want provider-specific timeout", snapshot)
	}
}
