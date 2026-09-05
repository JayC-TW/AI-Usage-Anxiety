package provider

import (
	"context"
	"io/fs"

	"aiusage/internal/model"
)

type Provider interface {
	Name() string
	Available() bool
	Fetch(ctx context.Context) ([]model.Usage, error)
}

// Factory allows tests to provide an fs.FS rooted at a fixture directory.
type Factory func(root fs.FS) Provider
