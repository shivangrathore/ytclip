package core

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Stage identifies which half of the run an event came from.
type Stage int

const (
	StageDownload Stage = iota
	StageEncode
)

// Event is anything a running stage reports.
type Event interface{ isEvent() }

// StageResult is the outcome of a stage. Pushed onto the event channel
// as the final item so it arrives in order behind the last progress
// update, rather than racing it through a second channel.
type StageResult struct {
	Stage Stage
	Err   error
}

func (StageResult) isEvent() {}

// LogLine is raw tool output, kept for the log pane and the run log.
type LogLine struct {
	Stage  Stage
	Text   string
	Stderr bool
}

func (LogLine) isEvent() {}

// DownloadProgress is one yt-dlp progress update.
type DownloadProgress struct {
	Downloaded int64
	// Total is -1 when yt-dlp does not know it, which is normal for a
	// fragmented stream until the last fragment arrives.
	Total     int64
	Speed     float64 // bytes/sec, -1 unknown
	ETA       float64 // seconds, -1 unknown
	FragIndex int
	FragCount int

	// MediaSeconds and MediaDuration come from the ffmpeg downloader,
	// which is what --download-sections forces. That path reports
	// elapsed media time, never bytes or fragments, so it is the only
	// progress signal a section download has.
	MediaSeconds  float64
	MediaDuration float64
	// SpeedX is ffmpeg's realtime multiplier on that same path.
	SpeedX float64
}

func (DownloadProgress) isEvent() {}

// Fraction returns 0..1, or -1 when nothing usable is known.
//
// Fragment counts are the reliable signal on HLS: byte totals are
// estimates that jump around, but "fragment 300 of 1200" does not.
func (p DownloadProgress) Fraction() float64 {
	if p.FragCount > 0 && p.FragIndex >= 0 {
		return clamp01(float64(p.FragIndex) / float64(p.FragCount))
	}
	if p.MediaDuration > 0 && p.MediaSeconds >= 0 {
		return clamp01(p.MediaSeconds / p.MediaDuration)
	}
	if p.Total > 0 && p.Downloaded >= 0 {
		return clamp01(float64(p.Downloaded) / float64(p.Total))
	}
	return -1
}

// RemainingETA is yt-dlp's own ETA where it has one, otherwise the
// media-time estimate from the ffmpeg downloader.
func (p DownloadProgress) RemainingETA() float64 {
	if p.ETA >= 0 {
		return p.ETA
	}
	if p.MediaDuration > 0 && p.SpeedX > 0 {
		r := (p.MediaDuration - p.MediaSeconds) / p.SpeedX
		if r < 0 {
			return 0
		}
		return r
	}
	return -1
}

// EncodeProgress is one ffmpeg -progress block.
type EncodeProgress struct {
	// OutSeconds is media time encoded so far.
	OutSeconds float64
	FPS        float64
	// Speed is ffmpeg's realtime multiplier, e.g. 4.2 for 4.2x.
	Speed float64
	Bytes int64
	// Duration is the total length being encoded, needed for the ETA.
	// -1 when unknown.
	Duration float64
	Done     bool
}

func (EncodeProgress) isEvent() {}

// Fraction returns 0..1, or -1 when the total length is unknown.
func (p EncodeProgress) Fraction() float64 {
	if p.Duration <= 0 {
		return -1
	}
	return clamp01(p.OutSeconds / p.Duration)
}

// ETA is remaining wall seconds, or -1.
//
// ffmpeg's own stats line has no ETA. It does report elapsed media
// time and a speed multiplier, and we know the clip length, so:
//
//	remaining wall seconds = (duration - encoded) / speed
func (p EncodeProgress) ETA() float64 {
	if p.Duration <= 0 || p.Speed <= 0 {
		return -1
	}
	remaining := (p.Duration - p.OutSeconds) / p.Speed
	if remaining < 0 {
		return 0
	}
	return remaining
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// progressPrefix marks the machine-readable yt-dlp lines. Parsing
// yt-dlp's human progress bar with a regex breaks every time it is
// restyled; --progress-template is a contract.
const progressPrefix = "YTCLIP|"

// ProgressTemplate is passed to yt-dlp --progress-template.
const ProgressTemplate = "download:" + progressPrefix +
	"%(progress.downloaded_bytes)s|" +
	"%(progress.total_bytes)s|" +
	"%(progress.total_bytes_estimate)s|" +
	"%(progress.speed)s|" +
	"%(progress.eta)s|" +
	"%(progress.fragment_index)s|" +
	"%(progress.fragment_count)s"

// RunDownload runs yt-dlp and streams its progress.
//
// clipDuration is the requested section length, or -1. It is only used
// to turn the ffmpeg downloader's elapsed media time into a percentage
// - that downloader reports no byte or fragment totals at all.
func RunDownload(ctx context.Context, ytdlp string, args []string, clipDuration float64, out chan<- Event) error {
	cmd := exec.CommandContext(ctx, ytdlp, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scan(stdout, func(line string) {
			if p, ok := parseYtProgress(line); ok {
				out <- p
				return
			}
			if p, ok := parseFFStats(line, clipDuration); ok {
				out <- p
				return
			}
			out <- LogLine{Stage: StageDownload, Text: line}
		})
	}()

	go func() {
		defer wg.Done()
		scan(stderr, func(line string) {
			// The ffmpeg downloader writes its stats to stderr, and
			// that is where a section download's only progress lives.
			if p, ok := parseFFStats(line, clipDuration); ok {
				out <- p
				return
			}
			out <- LogLine{Stage: StageDownload, Text: line, Stderr: true}
		})
	}()

	wg.Wait()
	return cmd.Wait()
}

// parseYtProgress reads one templated progress line.
func parseYtProgress(line string) (DownloadProgress, bool) {
	if !strings.HasPrefix(line, progressPrefix) {
		return DownloadProgress{}, false
	}
	f := strings.Split(strings.TrimPrefix(line, progressPrefix), "|")
	if len(f) < 7 {
		return DownloadProgress{}, false
	}

	p := DownloadProgress{
		Downloaded: parseInt(f[0], -1),
		Total:      parseInt(f[1], -1),
		Speed:      parseFloat(f[3], -1),
		ETA:        parseFloat(f[4], -1),
		FragIndex:  int(parseInt(f[5], -1)),
		FragCount:  int(parseInt(f[6], -1)),
	}
	// total_bytes is unknown for fragmented streams; the estimate is
	// the only number available, and a rough bar beats none.
	if p.Total <= 0 {
		p.Total = parseInt(f[2], -1)
	}
	return p, true
}

// RunEncode runs ffmpeg with -progress on stdout and streams updates.
//
// duration is what stage 2 will actually process - the whole file, or
// just the slice cut out of it - and is what the ETA divides by.
func RunEncode(ctx context.Context, ffmpeg string, args []string, duration float64, out chan<- Event) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		parseFFProgress(stdout, duration, out)
	}()

	go func() {
		defer wg.Done()
		scan(stderr, func(line string) {
			out <- LogLine{Stage: StageEncode, Text: line, Stderr: true}
		})
	}()

	wg.Wait()
	return cmd.Wait()
}

// parseFFProgress reads ffmpeg's flat key=value stream.
//
// One block per update, each terminated by progress=continue (or
// progress=end). Values accumulate until the terminator, then the
// whole block is emitted as one event.
func parseFFProgress(r io.Reader, duration float64, out chan<- Event) {
	cur := EncodeProgress{Duration: duration, FPS: -1, Speed: -1}

	scan(r, func(line string) {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "out_time_us", "out_time_ms":
			// out_time_ms is misnamed and also microseconds. Prefer
			// out_time_us where present; take either as micros.
			if v := parseInt(value, -1); v >= 0 {
				cur.OutSeconds = float64(v) / 1e6
			}
		case "total_size":
			cur.Bytes = parseInt(value, 0)
		case "fps":
			// ffmpeg reports "N/A" until the first frames land.
			// Letting that through as a string is how the old awk
			// pipeline could divide by zero in the ETA.
			cur.FPS = parseFloat(value, -1)
		case "speed":
			cur.Speed = parseFloat(strings.TrimSuffix(value, "x"), -1)
		case "progress":
			cur.Done = value == "end"
			out <- cur
		}
	})
}

// scan reads lines, treating a bare carriage return as a terminator.
//
// ffmpeg's -stats output redraws one line with \r and never emits \n
// until the process ends. A plain line scanner therefore glues every
// update in the run into a single unusable string.
func scan(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fn(line)
	}
}

// scanLinesCR splits on \n, \r, or \r\n.
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		// Consume \r\n as one terminator, never as an empty line.
		width := 1
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			width = 2
		} else if data[i] == '\r' && i+1 == len(data) && !atEOF {
			// Might be the first half of \r\n; wait for more.
			return 0, nil, nil
		}
		return i + width, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ffStatsRe pulls the fields we need out of an ffmpeg -stats line:
//
//	frame=  874 fps=846 q=-1.0 Lsize=4416KiB time=00:00:12.00 \
//	  bitrate=3013.5kbits/s speed=11.6x
//
// This is the only progress a --download-sections run produces, because
// that flag forces yt-dlp to hand the fetch to ffmpeg.
var ffStatsRe = regexp.MustCompile(
	`(?:^|\s)(frame|fps|L?size|time|speed)=\s*([0-9:.eE+-]+|N/A)`)

// parseFFStats reads one ffmpeg -stats line.
func parseFFStats(line string, mediaDuration float64) (DownloadProgress, bool) {
	if !strings.Contains(line, "time=") || !strings.Contains(line, "frame=") {
		return DownloadProgress{}, false
	}

	p := DownloadProgress{
		Downloaded:    -1,
		Total:         -1,
		Speed:         -1,
		ETA:           -1,
		FragIndex:     -1,
		FragCount:     -1,
		MediaSeconds:  -1,
		MediaDuration: mediaDuration,
		SpeedX:        -1,
	}

	found := false
	for _, m := range ffStatsRe.FindAllStringSubmatch(line, -1) {
		key, value := m[1], m[2]
		if value == "N/A" {
			continue
		}
		switch key {
		case "time":
			if sec, err := ParseTimecode(value); err == nil {
				p.MediaSeconds = sec
				found = true
			}
		case "speed":
			p.SpeedX = parseFloat(strings.TrimSuffix(value, "x"), -1)
		case "size", "Lsize":
			// Reported in KiB by ffmpeg's stats line.
			if v := parseFloat(value, -1); v >= 0 {
				p.Downloaded = int64(v * 1024)
			}
		}
	}
	return p, found
}

func parseInt(s string, def int64) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "NA" || s == "N/A" || s == "none" {
		return def
	}
	// yt-dlp renders floats for byte counts often enough to matter.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return def
}

func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "NA" || s == "N/A" || s == "none" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

// FormatRate renders bytes/sec for the UI.
func FormatRate(bps float64) string {
	if bps <= 0 {
		return "--"
	}
	return fmt.Sprintf("%s/s", FormatBytes(int64(bps)))
}
