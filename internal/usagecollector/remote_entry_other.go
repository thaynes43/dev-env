//go:build !linux

package usagecollector

import (
	"context"
	"io"
	"os"
)

func RunInternal(context.Context, *os.File, io.Writer) error { return ErrCapture }
