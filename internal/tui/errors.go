package tui

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/shivangrathore/ytclip/internal/core"
)

// confirmPrompt is a stop-and-ask, not a refusal. Every one of these
// describes a file that exists and is probably wrong; whether to spend
// an encode on it is the user's call.
type confirmPrompt struct {
	title string
	body  []string
	ask   string
}

// friendlyError carries a headline plus the detail lines under it.
type friendlyError struct {
	headline string
	detail   []string
}

func (e *friendlyError) Error() string {
	if len(e.detail) == 0 {
		return e.headline
	}
	return e.headline + "\n" + strings.Join(e.detail, "\n")
}

func (e *friendlyError) Headline() string { return e.headline }
func (e *friendlyError) Detail() []string { return e.detail }

// missingDepsError names what is missing and how to install it on the
// platform the user is actually on.
func missingDepsError(missing []core.DepStatus) error {
	e := &friendlyError{headline: "Missing dependencies"}

	for _, d := range missing {
		if d.Err != nil {
			e.detail = append(e.detail,
				fmt.Sprintf("%s was found at %s but will not execute.", d.Name, d.Path),
				"  "+firstLine(d.Err.Error()),
				"")
			continue
		}
		e.detail = append(e.detail, d.Name+":")
		for _, cmd := range installHints(d.Name) {
			e.detail = append(e.detail, "    "+cmd)
		}
		e.detail = append(e.detail, "")
	}

	e.detail = append(e.detail,
		"After installing, open a new terminal so PATH refreshes.",
		"Binaries dropped next to this executable are also picked up.")
	return e
}

// installHints returns the install commands for this OS, most likely
// first. The .ps1 only ever printed winget lines; the .sh only ever
// printed Linux ones. One binary has to know all of them.
func installHints(tool string) []string {
	switch runtime.GOOS {
	case "windows":
		if tool == "yt-dlp" {
			return []string{
				"winget install yt-dlp.yt-dlp",
				"# or:  scoop install yt-dlp",
				"# or:  choco install yt-dlp",
			}
		}
		return []string{
			"winget install Gyan.FFmpeg",
			"# or:  scoop install ffmpeg",
			"# or:  choco install ffmpeg",
		}
	case "darwin":
		return []string{
			"brew install " + tool,
			"# or:  port install " + tool,
		}
	default:
		if tool == "yt-dlp" {
			// A distro yt-dlp goes stale fast, and a stale yt-dlp is
			// the most common cause of extraction breaking outright.
			// pipx keeps it current without root.
			return []string{
				"pipx install yt-dlp    # recommended: self-updates",
				"# or:  sudo apt install yt-dlp",
				"# or:  sudo dnf install yt-dlp",
				"# or:  sudo pacman -S yt-dlp",
			}
		}
		return []string{
			"sudo apt install ffmpeg",
			"# or:  sudo dnf install ffmpeg",
			"# or:  sudo pacman -S ffmpeg",
			"# or:  sudo zypper install ffmpeg",
		}
	}
}

// shortDownloadPrompt explains the silent failure this whole check
// exists to catch.
func shortDownloadPrompt(j *core.Job, v core.Verification) confirmPrompt {
	p := confirmPrompt{
		title: "Short download",
		body: []string{
			fmt.Sprintf("Requested:  %s", core.FormatDurationHMS(v.Expected)),
			fmt.Sprintf("Received:   %s", core.FormatDurationHMS(v.Received)),
			"",
			"The section download did not return the range that was",
			"asked for, and yt-dlp reported success.",
			"",
			"This almost always means the video has no seek index - a",
			"livestream still running, or one YouTube has not finished",
			"processing yet.",
		},
		ask: fmt.Sprintf("Encode this %s clip anyway?",
			core.FormatDurationHMS(v.Received)),
	}
	if j.Meta.LiveStatus != "" {
		p.body = append(p.body, "", "Reported live status: "+j.Meta.LiveStatus)
	}
	p.body = append(p.body, "",
		"Otherwise: re-run and let it download the full stream and cut",
		"locally, or wait for processing to finish.")
	return p
}

// degradedPrompt fires when the format selector fell through.
func degradedPrompt(j *core.Job, v core.Verification) confirmPrompt {
	return confirmPrompt{
		title: "Quality warning",
		body: []string{
			fmt.Sprintf("You asked for %s but got %dp.",
				j.Sel.Quality.Name, v.Info.Height),
			"",
			"The format selector fell through to a fallback. Most often",
			"this means the combined format, which on YouTube only ever",
			"exists as 360p30.",
			"",
			"Re-run with \"Best Video + Audio\".",
		},
		ask: fmt.Sprintf("Encode this %dp clip anyway?", v.Info.Height),
	}
}

// keptSourceError reports a declined confirmation. Nothing is deleted:
// the download already happened and throwing it away would be the
// expensive mistake.
func keptSourceError(title, source string) error {
	return &friendlyError{
		headline: "Stopped: " + title,
		detail: []string{
			"The downloaded source was kept:",
			"  " + source,
		},
	}
}

// stageError explains a tool failure with the last thing it said.
func (m Model) stageError(r core.StageResult) error {
	what := "Download failed"
	if r.Stage == core.StageEncode {
		what = "H.264 conversion failed"
	}

	e := &friendlyError{headline: what}

	// The exit code alone says nothing. The last stderr lines usually
	// say everything.
	tail := m.logs
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	for _, l := range tail {
		e.detail = append(e.detail, "  "+l)
	}
	if len(e.detail) > 0 {
		e.detail = append([]string{"Last output:"}, e.detail...)
		e.detail = append(e.detail, "")
	}
	e.detail = append(e.detail, firstLine(r.Err.Error()))

	if m.source != "" {
		e.detail = append(e.detail, "", "The downloaded source was kept:", "  "+m.source)
	}
	return e
}
