//go:build !linux && !darwin && !windows

package subagents

import (
	"context"
	"errors"
	"io"

	"github.com/OrdalieTech/orb/sandbox"
)

func runExternalCommand(context.Context, string, string, map[string]string, sandbox.Mode, io.Reader, io.Writer, io.Writer) (externalRun, error) {
	return externalRun{}, unavailableError{errors.New("descendant isolation is supported only on linux, darwin and windows")}
}
