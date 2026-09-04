package core

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Job is one fully-planned clip: what to fetch, from where to where,
// and what to do with it afterwards.
type Job struct {
	URL  string
	Meta Meta
	Sel  Selection
	Cfg  Config

	HasStart bool
	HasEnd   bool
	StartSec float64
	EndSec   float64

	// TrimLocally means the section could not be seeked server-side,
	// so the whole stream is downloaded and cut during the encode.
	// The cut costs no extra time - the download does.
	TrimLocally bool

	Name    string
	OutDir  string
	TempDir string

	// Encoder is resolved before stage 2 starts.
	Encoder *Encoder
}

// FullVideo reports whether no section was requested at all.
//
// Blank start, blank end, or both - each is meaningful:
//
//	both blank  -> whole video, no --download-sections at all
//	start only  -> start .. end of video
//	end only    -> beginning .. end
//	both        -> the normal case
func (j *Job) FullVideo() bool { return !j.HasStart && !j.HasEnd }

// DownloadStartSec is the start with padding applied, floored at zero.
func (j *Job) DownloadStartSec() float64 {
	return math.Max(0, j.StartSec-j.Cfg.Padding)
}

// DownloadEndSec is the end with padding applied. Only meaningful when
// HasEnd is set.
func (j *Job) DownloadEndSec() float64 { return j.EndSec + j.Cfg.Padding }

// RequestedDuration is the clip length the user asked for, or -1 when
// the end is open. A full-video or open-ended run only learns its real
// length once the file exists, so the encode ETA fills it in then.
func (j *Job) RequestedDuration() float64 {
	if !j.HasEnd {
		return -1
	}
	return j.EndSec - j.StartSec
}

// StartLabel and EndLabel render a blank as intent rather than a gap.
func (j *Job) StartLabel() string {
	if !j.HasStart {
		return "start of video"
	}
	return FormatTimestamp(j.StartSec)
}

func (j *Job) EndLabel() string {
	if !j.HasEnd {
		return "end of video"
	}
	return FormatTimestamp(j.EndSec)
}

// ValidateRange checks the requested window against the video.
//
// Asking for 00:31:00 of a 20 minute video is a typo. yt-dlp will not
// complain - it hands back whatever exists - so catch it here while it
// is still free to fix. Returns a fatal error, and separately a
// non-fatal warning.
func (j *Job) ValidateRange() (warn string, err error) {
	if j.HasEnd && j.EndSec <= j.StartSec {
		return "", fmt.Errorf("end time must be after start time")
	}
	if !j.Meta.HasDuration || j.FullVideo() {
		return "", nil
	}
	if j.HasStart && j.StartSec >= j.Meta.Duration {
		return "", fmt.Errorf(
			"start time (%s) is past the end of the video (%s)",
			j.StartLabel(), FormatTimestamp(j.Meta.Duration))
	}
	if j.HasEnd && j.EndSec > j.Meta.Duration+1 {
		return fmt.Sprintf(
			"end time (%s) is past the end of the video (%s); "+
				"the clip will stop at the end of the video",
			j.EndLabel(), FormatTimestamp(j.Meta.Duration)), nil
	}
	return "", nil
}

// Prepare creates the output directories and clears stale temp files.
func (j *Job) Prepare(baseDir string) error {
	out := j.Cfg.OutputDir
	if out == "" {
		out = filepath.Join(baseDir, "clips")
	}
	j.OutDir = out
	j.TempDir = filepath.Join(out, "_temp")

	if err := os.MkdirAll(j.TempDir, 0o755); err != nil {
		return err
	}

	// FindDownloaded picks the newest file in _temp. A leftover from
	// an aborted run would win that race and get muxed into the wrong
	// clip. Start clean.
	entries, err := os.ReadDir(j.TempDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			os.Remove(filepath.Join(j.TempDir, e.Name()))
		}
	}
	return nil
}

var badNameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// SanitizeName makes a user-typed name safe on every filesystem.
// Windows is the strict one, so its rules apply everywhere - a clip
// named on Linux should still copy onto a Windows machine.
func SanitizeName(s string) string {
	s = badNameChars.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.TrimRight(s, ". ")
	if s == "" {
		s = "clip_" + time.Now().Format("20060102_150405")
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// FinalPath returns a non-colliding output path with the given
// extension, suffixing a timestamp when the plain name is taken.
func (j *Job) FinalPath(ext string) string {
	p := filepath.Join(j.OutDir, j.Name+ext)
	if _, err := os.Stat(p); err != nil {
		return p
	}
	return filepath.Join(j.OutDir,
		j.Name+"_"+time.Now().Format("20060102_150405")+ext)
}

// SourceTemplate is the yt-dlp -o template for the raw download.
func (j *Job) SourceTemplate() string {
	return filepath.Join(j.TempDir, "source.%(ext)s")
}

// YtDlpArgs builds the full stage 1 command line.
func (j *Job) YtDlpArgs() []string {
	// A post-live stream is served off the DVR window and those
	// fragment URLs are short-lived. Hitting them 16 at a time earns
	// an HTTP 401 part way through and kills the whole run. Back off
	// to a concurrency they tolerate.
	fragments := j.Cfg.ConcurrentFragments
	if j.TrimLocally && fragments > 4 {
		fragments = 4
	}

	var args []string

	// Omitted entirely for a full-video download: passing
	// "*00:00:00.000-inf" works but forces the section code path, and
	// with it a needless ffmpeg remux of the whole file.
	if !j.FullVideo() && !j.TrimLocally {
		end := "inf" // yt-dlp's "run to the end of the video"
		if j.HasEnd {
			end = FormatTimestamp(j.DownloadEndSec())
		}
		args = append(args, "--download-sections",
			"*"+FormatTimestamp(j.DownloadStartSec())+"-"+end)
	}

	args = append(args,
		"--no-playlist",
		"--format", j.Sel.Selector(),
		"--output", j.SourceTemplate(),
		"--concurrent-fragments", itoa(fragments),
		// ffmpeg dumps every signed googlevideo segment URL at
		// default verbosity - hundreds of lines that bury the
		// actual progress.
		"--downloader-args", "ffmpeg:-loglevel warning -stats",
		"--retries", "10",
		"--fragment-retries", "10",
		// Exponential backoff. Without this the 10 fragment retries
		// are spent inside a second or two, all against a URL that
		// needs longer than that to come back.
		"--retry-sleep", "fragment:exp=1:20",
		// One progress line per update instead of carriage returns,
		// so the stream is parseable and the log stays readable.
		"--newline",
		"--progress",
		// Machine-readable progress alongside the human lines, so the
		// UI never has to regex yt-dlp's bar - which gets restyled.
		"--progress-template", ProgressTemplate,
		"--no-warnings",
	)

	if j.Sel.Format.ExtractAudio {
		args = append(args, "--extract-audio", "--audio-format", "m4a")
	}

	return append(args, j.URL)
}

// TrimInArgs are the ffmpeg args that must precede -i.
//
// -ss before -i is a fast input seek, and ffmpeg still lands
// frame-accurate when it is re-encoding.
func (j *Job) TrimInArgs() []string {
	if !j.TrimLocally {
		return nil
	}
	return []string{"-ss", FormatTimestamp(j.DownloadStartSec())}
}

// TrimOutArgs follow -i, so the length counts from the seek point
// rather than from the start of the file.
func (j *Job) TrimOutArgs() []string {
	if !j.TrimLocally || !j.HasEnd {
		return nil
	}
	d := j.DownloadEndSec() - j.DownloadStartSec()
	return []string{"-t", FormatTimestamp(d)}
}

// CutArgs builds a stream-copy trim, used when no re-encode is wanted.
//
// The cut lands on the nearest keyframe at or before the start.
// Hitting the exact frame is what ConvertToH264 is for.
func (j *Job) CutArgs(source, dest string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, j.TrimInArgs()...)
	args = append(args, "-i", source)
	args = append(args, j.TrimOutArgs()...)
	return append(args, "-c", "copy", dest)
}

// EncodeArgs builds the stage 2 H.264 command line.
func (j *Job) EncodeArgs(source, dest string) []string {
	e := j.Encoder

	args := []string{
		"-hide_banner",
		// The h264 decoder emits "Late SEI is not implemented" for
		// every affected frame on some YouTube streams. Harmless -
		// the message is skipped, the picture is unaffected - but it
		// buries everything else.
		"-loglevel", "error",
		// Machine-readable progress on stdout instead of the stats
		// line, so we can render our own with an ETA. Real errors
		// still come out on stderr.
		"-progress", "pipe:1",
		"-nostats",
		"-y",
	}

	args = append(args, e.InitArgs()...)
	// Decode on the GPU too, so a VP9/AV1 source never round-trips
	// through the CPU. ffmpeg falls back to software decode on its own
	// when the source codec has no hardware path.
	args = append(args, e.DecodeArgs()...)
	args = append(args, j.TrimInArgs()...)
	args = append(args, "-i", source)
	args = append(args, j.TrimOutArgs()...)
	args = append(args, e.Filters()...)
	args = append(args, e.CodecArgs(j.Cfg.Quality, j.Cfg.Speed)...)
	args = append(args,
		"-c:a", "aac",
		"-b:a", "192k",
		"-ar", "48000",
		"-map", "0:v:0",
		// The "?" makes the audio stream optional, so a video-only
		// download does not fail the mux.
		"-map", "0:a:0?",
		"-movflags", "+faststart",
		dest,
	)
	return args
}

// FindDownloaded returns the newest file in the temp directory.
func (j *Job) FindDownloaded() (string, os.FileInfo, error) {
	entries, err := os.ReadDir(j.TempDir)
	if err != nil {
		return "", nil, err
	}

	var bestPath string
	var best os.FileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == nil || info.ModTime().After(best.ModTime()) {
			best = info
			bestPath = filepath.Join(j.TempDir, e.Name())
		}
	}
	if best == nil {
		return "", nil, fmt.Errorf("no downloaded file was found in %s", j.TempDir)
	}
	return bestPath, best, nil
}
