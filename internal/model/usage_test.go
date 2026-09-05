package model_test

import (
	"testing"

	"aiusage/internal/model"
)

func TestUsageKnownRequiresPositiveLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit float64
		want  bool
	}{
		{name: "unknown zero", limit: 0, want: false},
		{name: "unknown negative", limit: -1, want: false},
		{name: "known", limit: 100, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := model.Usage{Limit: tt.limit, Used: 40}
			if got := usage.Known(); got != tt.want {
				t.Fatalf("Known() = %v, want %v", got, tt.want)
			}
		})
	}
}
