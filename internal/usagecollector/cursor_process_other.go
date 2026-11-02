//go:build !linux

package usagecollector

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type CursorOptions struct {
	Executable          string
	Workspace           string
	AllowWorkspaceTrust bool
}

// CollectCursor is unqualified outside the Linux session-pod terminal boundary.
func CollectCursor(context.Context, CursorOptions, func() time.Time) (providerallowance.CursorCLIUsageCapture, error) {
	return providerallowance.CursorCLIUsageCapture{}, ErrCapture
}
