package collector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider"
)

const (
	defaultProviderTimeout = 3 * time.Second
	defaultInterval        = 3 * time.Minute
	manualRefreshMinimum   = 2 * time.Second
	initialBackoff         = 3 * time.Second
	maximumBackoff         = 5 * time.Minute
)

type Snapshot struct {
	Statuses  []model.ProviderStatus
	UpdatedAt time.Time
}

type Collector struct {
	providers []provider.Provider
	interval  time.Duration
	timeout   time.Duration
	now       func() time.Time

	mu         sync.RWMutex
	snap       Snapshot
	failures   map[string]failureState
	lastManual time.Time
}

type providerWithCollectionTimeout interface {
	CollectionTimeout() time.Duration
}

type failureState struct {
	delay time.Duration
	next  time.Time
}

func New(providers []provider.Provider, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Collector{
		providers: append([]provider.Provider(nil), providers...),
		interval:  interval,
		timeout:   defaultProviderTimeout,
		now:       time.Now,
		failures:  make(map[string]failureState),
	}
}

func (c *Collector) Interval() time.Duration {
	if c == nil || c.interval <= 0 {
		return defaultInterval
	}
	return c.interval
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneSnapshot(c.snap)
}

func (c *Collector) Collect(ctx context.Context) Snapshot {
	return c.collect(ctx, false)
}

func (c *Collector) Refresh(ctx context.Context) Snapshot {
	if c == nil {
		return Snapshot{}
	}
	now := c.clockNow()
	c.mu.Lock()
	if !c.lastManual.IsZero() && now.Sub(c.lastManual) < manualRefreshMinimum {
		snapshot := cloneSnapshot(c.snap)
		c.mu.Unlock()
		return snapshot
	}
	c.lastManual = now
	c.mu.Unlock()
	return c.collect(ctx, true)
}

func (c *Collector) collect(ctx context.Context, force bool) Snapshot {
	if c == nil {
		return Snapshot{}
	}
	now := c.clockNow()
	previous := c.Snapshot()
	statuses := make([]model.ProviderStatus, len(c.providers))
	var waitGroup sync.WaitGroup

	for index, currentProvider := range c.providers {
		index := index
		currentProvider := currentProvider
		if currentProvider == nil {
			statuses[index] = model.ProviderStatus{Name: "unknown", Available: false}
			continue
		}
		name := currentProvider.Name()
		available := currentProvider.Available()
		if !available {
			statuses[index] = model.ProviderStatus{Name: name, Available: false}
			continue
		}
		if !force {
			if blocked, err := c.backoffStatus(name, now, previousStatus(previous, index)); blocked {
				statuses[index] = err
				continue
			}
		}

		waitGroup.Add(1)
		providerTimeout := c.timeout
		if timedProvider, ok := currentProvider.(providerWithCollectionTimeout); ok {
			if timeout := timedProvider.CollectionTimeout(); timeout > 0 {
				providerTimeout = timeout
			}
		}
		go func() {
			defer waitGroup.Done()
			providerContext, cancel := context.WithTimeout(ctx, providerTimeout)
			defer cancel()

			usages, err := safeFetch(currentProvider, providerContext)
			if err != nil {
				c.recordFailure(name, now)
				status := model.ProviderStatus{Name: name, Available: true, Err: err}
				if old := previousStatus(previous, index); old != nil {
					status.Usages = append([]model.Usage(nil), old.Usages...)
				}
				statuses[index] = status
				return
			}

			c.recordSuccess(name)
			statuses[index] = model.ProviderStatus{
				Name:      name,
				Available: true,
				Usages:    append([]model.Usage(nil), usages...),
			}
		}()
	}

	waitGroup.Wait()
	result := Snapshot{Statuses: statuses, UpdatedAt: now}
	c.mu.Lock()
	c.snap = cloneSnapshot(result)
	c.mu.Unlock()
	return result
}

func (c *Collector) backoffStatus(name string, now time.Time, previous *model.ProviderStatus) (bool, model.ProviderStatus) {
	c.mu.RLock()
	state, found := c.failures[name]
	c.mu.RUnlock()
	if !found || now.Before(state.next) {
		if !found {
			return false, model.ProviderStatus{}
		}
		status := model.ProviderStatus{Name: name, Available: true, Err: fmt.Errorf("provider backoff active")}
		if previous != nil {
			status.Usages = append([]model.Usage(nil), previous.Usages...)
		}
		return true, status
	}
	return false, model.ProviderStatus{}
}

func (c *Collector) recordFailure(name string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.failures[name]
	if state.delay <= 0 {
		state.delay = initialBackoff
	} else {
		state.delay *= 2
		if state.delay > maximumBackoff {
			state.delay = maximumBackoff
		}
	}
	state.next = now.Add(state.delay)
	c.failures[name] = state
}

func (c *Collector) recordSuccess(name string) {
	c.mu.Lock()
	delete(c.failures, name)
	c.mu.Unlock()
}

func (c *Collector) clockNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func safeFetch(currentProvider provider.Provider, ctx context.Context) (usages []model.Usage, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("provider panic: %v", recovered)
			usages = nil
		}
	}()
	return currentProvider.Fetch(ctx)
}

func previousStatus(snapshot Snapshot, index int) *model.ProviderStatus {
	if index < 0 || index >= len(snapshot.Statuses) {
		return nil
	}
	return &snapshot.Statuses[index]
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := Snapshot{UpdatedAt: snapshot.UpdatedAt}
	clone.Statuses = make([]model.ProviderStatus, len(snapshot.Statuses))
	for index, status := range snapshot.Statuses {
		clone.Statuses[index] = model.ProviderStatus{
			Name:      status.Name,
			Available: status.Available,
			Err:       status.Err,
			Usages:    append([]model.Usage(nil), status.Usages...),
		}
	}
	return clone
}
