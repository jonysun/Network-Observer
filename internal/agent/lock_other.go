//go:build !linux && !windows

package agent

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("agent WAL locking supports Linux and Windows test hosts only")
}
