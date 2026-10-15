package main

import "github.com/thaynes43/dev-env/internal/agentd"

// coordArchiveShelfCheck is the shelf startup check for DESIGN-024's archive.
// Tests may replace this private command seam; production always uses the
// archive's existing no-follow mount guard.
var coordArchiveShelfCheck = func(s agentd.Settings) error {
	return newCoordArchive(s).CheckMounted()
}
