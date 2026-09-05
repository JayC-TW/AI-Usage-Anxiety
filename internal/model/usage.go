package model

import "time"

type Source string

const (
	SourceLocalFile Source = "local-file"
	SourceEndpoint  Source = "official-endpoint"
	SourceDerived   Source = "derived"
)

type Usage struct {
	Provider  string     `json:"provider"`
	Window    string     `json:"window"`
	Used      float64    `json:"used"`
	Limit     float64    `json:"limit"`
	Unit      string     `json:"unit"`
	ResetAt   *time.Time `json:"resetAt,omitempty"`
	Source    Source     `json:"source"`
	FetchedAt time.Time  `json:"fetchedAt"`
	Note      string     `json:"note,omitempty"`
}

// Known reports whether the usage has a trusted limit that can be rendered as a percentage.
func (u Usage) Known() bool { return u.Limit > 0 }

type ProviderStatus struct {
	Name      string
	Available bool
	Err       error `json:"-"`
	Usages    []Usage
}
