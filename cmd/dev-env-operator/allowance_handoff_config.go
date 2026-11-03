package main

import (
	"path/filepath"

	"github.com/thaynes43/dev-env/internal/allowancecollector"
)

// allowanceHandoffSelection is one decoded, human-approved handoff request.
// Decoding it grants nothing: the runnable still fences every attempt by the
// approved prepared hash, the running collector configuration and the live slot.
type allowanceHandoffSelection struct {
	approved string
	snapshot allowancecollector.HandoffSnapshot
}

// parseAllowanceHandoff is off for an empty path. A path requires the enabled
// collector (checked before any file IO by parseFlags). A bad file is returned
// to the caller, which must not stop the core API for an optional maintenance.
func parseAllowanceHandoff(path string, collector *allowancecollector.Config, sel *aggregateAllowanceSelection) (*allowanceHandoffSelection, error) {
	if path == "" {
		return nil, nil
	}
	if collector == nil || sel == nil || !filepath.IsAbs(path) {
		return nil, allowancecollector.ErrCollector
	}
	raw, err := readOperatorPrivateFile(path, 512<<10)
	if err != nil {
		return nil, err
	}
	req, snap, err := allowancecollector.DecodeHandoffRequest(raw)
	if err != nil {
		return nil, err
	}
	return &allowanceHandoffSelection{approved: req.ApprovedHash, snapshot: snap}, nil
}
