package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runQuiet runs a command and returns its stderr for the error path.
func runQuiet(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stderr.String()), err
}

// wrapFFmpeg puts ffmpeg's own complaint in front of the exit code,
// which on its own says nothing useful.
func wrapFFmpeg(what, stderr string, err error) error {
	if stderr != "" {
		lines := strings.Split(stderr, "\n")
		return fmt.Errorf("%s: %s", what, strings.TrimSpace(lines[len(lines)-1]))
	}
	return fmt.Errorf("%s: %w", what, err)
}
