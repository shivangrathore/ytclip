package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
)

// installEvent is one step of the bootstrap, streamed to the UI.
type installEvent struct {
	prog core.InstallProgress
	line string
	done bool
	err  error
}

// installPlan is what can and cannot be fetched on this platform.
type installPlan struct {
	specs []core.ToolSpec
	// blocked carries the tools that have no automatic route here,
	// each with the command to run instead.
	blocked []*core.UnsupportedError
}

// planInstall works out what is missing and how to get it.
func planInstall(deps []core.DepStatus) installPlan {
	var plan installPlan

	need := map[string]bool{}
	for _, d := range deps {
		if !d.Required || d.OK() {
			continue
		}
		// ffprobe ships inside the ffmpeg archive, so one spec covers
		// both and asking for it twice would download 122 MB twice.
		if d.Name == "ffprobe" {
			need["ffmpeg"] = true
			continue
		}
		need[d.Name] = true
	}
	// ffprobe is optional, but the verification stage is worth having;
	// pull it in whenever ffmpeg is being fetched anyway.
	for _, d := range deps {
		if d.Name == "ffprobe" && !d.OK() && need["ffmpeg"] {
			break
		}
	}

	for _, tool := range []string{"yt-dlp", "ffmpeg"} {
		if !need[tool] {
			continue
		}
		spec, err := core.PlanInstall(tool)
		if err != nil {
			var un *core.UnsupportedError
			if errors.As(err, &un) {
				plan.blocked = append(plan.blocked, un)
			}
			continue
		}
		plan.specs = append(plan.specs, spec)
	}
	return plan
}

// startInstall runs the whole plan on a goroutine, streaming progress.
func startInstall(ctx context.Context, specs []core.ToolSpec, ch chan installEvent) tea.Cmd {
	return func() tea.Msg {
		go func() {
			defer close(ch)

			for _, spec := range specs {
				ch <- installEvent{line: spec.Tool + "  " + spec.Source}

				files, err := core.Install(ctx, spec, func(p core.InstallProgress) {
					// Never block the download on a slow UI; progress
					// is the one thing safe to drop.
					select {
					case ch <- installEvent{prog: p}:
					default:
					}
				})
				if err != nil {
					ch <- installEvent{done: true, err: fmt.Errorf("%s: %w", spec.Tool, err)}
					return
				}
				for _, f := range files {
					ch <- installEvent{line: "installed " + f}
				}
			}
			ch <- installEvent{done: true}
		}()
		return nil
	}
}

func waitForInstall(ch chan installEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return installClosedMsg{}
		}
		return ev
	}
}

type installClosedMsg struct{}
