package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
)

// depsMsg carries the dependency preflight result.
type depsMsg struct {
	tools   core.Tools
	results []core.DepStatus
	err     error
}

// detectMsg carries the encoder probe result.
type detectMsg struct {
	det *core.Detection
	err error
}

// metaMsg carries the yt-dlp pre-flight extraction.
type metaMsg struct {
	meta core.Meta
	err  error
}

// eventMsg wraps one streamed event from a running stage.
type eventMsg struct{ ev core.Event }

// streamClosedMsg fires when a stage's event channel drains.
type streamClosedMsg struct{ stage core.Stage }

// probedMsg carries the post-download verification.
type probedMsg struct {
	source string
	size   int64
	info   core.MediaInfo
	verify core.Verification
	err    error
}

// finishedMsg is the end of a successful run.
type finishedMsg struct {
	path string
	size int64
	info core.MediaInfo
}

// waitForEvent pulls one event, re-armed by Update after each receive.
func waitForEvent(ch chan core.Event, stage core.Stage) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{stage: stage}
		}
		return eventMsg{ev: ev}
	}
}

// statSize returns a file size, or 0.
func statSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}
