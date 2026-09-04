package tui

import (
	"context"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
)

// checkDeps resolves and actually executes each binary.
//
// Presence on disk is not the same as working: a truncated download, a
// file blocked by the internet zone, or a half-finished package upgrade
// all leave something that exists and refuses to run.
func checkDeps() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		t := core.FindTools()
		return depsMsg{
			tools: t,
			results: []core.DepStatus{
				core.CheckDep(ctx, "yt-dlp", t.YtDlp, true, "--version"),
				core.CheckDep(ctx, "ffmpeg", t.FFmpeg, true, "-hide_banner", "-version"),
				core.CheckDep(ctx, "ffprobe", t.FFprobe, false, "-hide_banner", "-version"),
			},
		}
	}
}

// detectEncoders probes every H.264 path for real.
func detectEncoders(ffmpeg string) tea.Cmd {
	return func() tea.Msg {
		det, err := core.DetectEncoders(context.Background(), ffmpeg, true)
		return detectMsg{det: det, err: err}
	}
}

// fetchMeta runs the cheap pre-flight extraction.
func fetchMeta(ytdlp, url string) tea.Cmd {
	return func() tea.Msg {
		meta, err := core.FetchMeta(context.Background(), ytdlp, url)
		return metaMsg{meta: meta, err: err}
	}
}

// startDownload launches stage 1 and returns the event channel.
func startDownload(ctx context.Context, ytdlp string, j *core.Job, ch chan core.Event) tea.Cmd {
	return func() tea.Msg {
		go func() {
			err := core.RunDownload(ctx, ytdlp, j.YtDlpArgs(), j.RequestedDuration(), ch)
			ch <- core.StageResult{Stage: core.StageDownload, Err: err}
			close(ch)
		}()
		return nil
	}
}

// verifyDownload finds the file, measures it, and checks it against
// what was actually asked for.
func verifyDownload(ffprobe string, j *core.Job) tea.Cmd {
	return func() tea.Msg {
		src, fi, err := j.FindDownloaded()
		if err != nil {
			return probedMsg{err: err}
		}
		info, _ := core.ProbeFile(context.Background(), ffprobe, src)
		return probedMsg{
			source: src,
			size:   fi.Size(),
			info:   info,
			verify: j.Verify(info),
		}
	}
}

// startEncode launches stage 2.
func startEncode(ctx context.Context, ffmpeg string, j *core.Job, src, dest string, dur float64, ch chan core.Event) tea.Cmd {
	return func() tea.Msg {
		go func() {
			err := core.RunEncode(ctx, ffmpeg, j.EncodeArgs(src, dest), dur, ch)
			ch <- core.StageResult{Stage: core.StageEncode, Err: err}
			close(ch)
		}()
		return nil
	}
}

// finishCopy handles the two no-encode routes: audio-only, and
// ConvertToH264 turned off.
//
// With a local trim still pending the cut is a stream copy, which lands
// on the nearest keyframe at or before the start. Hitting the exact
// frame is what the H.264 re-encode is for.
func finishCopy(ctx context.Context, ffmpeg, ffprobe string, j *core.Job, src, dest string) tea.Cmd {
	return func() tea.Msg {
		if j.TrimLocally {
			out, err := runQuiet(ctx, ffmpeg, j.CutArgs(src, dest))
			if err != nil {
				return probedMsg{err: wrapFFmpeg("trim failed", out, err)}
			}
			os.Remove(src)
		} else if err := moveFile(src, dest); err != nil {
			return probedMsg{err: err}
		}

		info, _ := core.ProbeFile(ctx, ffprobe, dest)
		return finishedMsg{path: dest, size: statSize(dest), info: info}
	}
}

// probeFinal measures the finished file.
//
// Reporting the requested length back is how a 2 second clip got to
// announce itself as 960 seconds. Measure the artifact, never the ask.
func probeFinal(ffprobe, path string) tea.Cmd {
	return func() tea.Msg {
		info, _ := core.ProbeFile(context.Background(), ffprobe, path)
		return finishedMsg{path: path, size: statSize(path), info: info}
	}
}

// moveFile renames, falling back to copy when the temp dir and the
// output dir are on different filesystems.
func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Close()
	os.Remove(src)
	return nil
}
