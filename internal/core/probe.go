package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Meta is the cheap pre-flight extraction, run before anything else.
//
// Two things depend on it:
//
//  1. Duration catches an end time past the end of the video - a typo
//     far better caught now than after a long download.
//  2. LiveStatus decides whether section downloading works at all.
//     See PlanSections: a stream YouTube has not finished processing
//     cannot be seeked, and the failure is completely silent.
//
// Never fatal. If the probe fails the run continues with the checks it
// would have enabled switched off.
type Meta struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Duration   float64 `json:"duration"`
	LiveStatus string  `json:"live_status"`
	Uploader   string  `json:"uploader"`

	// HasDuration distinguishes "zero length" from "not reported",
	// which is the normal case for a stream still in progress.
	HasDuration bool `json:"-"`
}

// Seekable reports whether --download-sections can be trusted.
//
// A finished VOD has a seek index. A stream still running, or one that
// has just ended and is still being processed ("post_live"), does not:
//
//	Duration: N/A, start: 4948.000000
//
// No duration, and timestamps that do not start at zero. Give ffmpeg
// "-ss 900" against that and the seek lands nowhere - it returns a
// couple of seconds from the tail of the DVR window, reports a negative
// timestamp, and exits 0. yt-dlp succeeds, a file exists, it really is
// 1440p60 - it is just 2 seconds long instead of 16 minutes.
//
// Neither --downloader native nor --live-from-start avoids this;
// --download-sections forces the ffmpeg downloader regardless.
func (m Meta) Seekable() bool {
	switch m.LiveStatus {
	case "is_live", "post_live", "is_upcoming":
		return false
	}
	return true
}

// LiveStatusText explains an unseekable status in plain words.
func (m Meta) LiveStatusText() string {
	switch m.LiveStatus {
	case "is_live":
		return "still live"
	case "post_live":
		return "recently ended - YouTube is still processing it"
	case "is_upcoming":
		return "has not started yet"
	}
	return m.LiveStatus
}

// FetchMeta runs one cheap yt-dlp extraction.
func FetchMeta(ctx context.Context, ytdlp, url string) (Meta, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ytdlp,
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		// Ask yt-dlp for JSON of just these fields rather than
		// delimiter-joining them. Titles are allowed to contain any
		// separator you might pick.
		"--print", "%(.{id,title,duration,live_status,uploader})j",
		url,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return Meta{}, fmt.Errorf("yt-dlp: %s", firstLine(stderr.String()+err.Error()))
	}

	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if line == "" {
		return Meta{}, fmt.Errorf("yt-dlp returned no metadata")
	}

	var m Meta
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return Meta{}, fmt.Errorf("could not parse metadata: %w", err)
	}
	m.HasDuration = m.Duration > 0
	return m, nil
}

// MediaInfo is what ffprobe reports about a file on disk.
type MediaInfo struct {
	Width       int
	Height      int
	FPS         float64
	Duration    float64
	HasVideo    bool
	HasDuration bool
}

// ProbeFile measures a downloaded file.
//
// Format selectors fall through silently: ask for "2K" and a fallback
// branch can hand back 360p without a single error message. The
// download succeeds, ffmpeg succeeds, and you only notice in the edit.
// So probe and say out loud what is really in the file.
func ProbeFile(ctx context.Context, ffprobe, path string) (MediaInfo, error) {
	var info MediaInfo
	if ffprobe == "" {
		return info, fmt.Errorf("ffprobe not available")
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,avg_frame_rate",
		"-of", "csv=p=0",
		path,
	).Output()
	if err == nil {
		line := strings.TrimSpace(string(out))
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 {
			info.Width, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			info.Height, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
			info.FPS = parseRational(parts[2])
			info.HasVideo = info.Height > 0
		}
	}

	// Duration comes from the container, not the stream: a stream
	// entry is missing it often enough to matter.
	durOut, err := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		path,
	).Output()
	if err == nil {
		s := strings.TrimSpace(string(durOut))
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
		if d, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && d > 0 {
			info.Duration = d
			info.HasDuration = true
		}
	}

	return info, nil
}

// parseRational turns ffprobe's "60/1" into 60.
func parseRational(s string) float64 {
	s = strings.TrimSpace(s)
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	n, err1 := strconv.ParseFloat(strings.TrimSpace(num), 64)
	d, err2 := strconv.ParseFloat(strings.TrimSpace(den), 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}
