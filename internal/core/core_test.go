package core

import (
	"errors"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseTimecode(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		bad  bool
	}{
		{"90", 90, false},
		{"1:30", 90, false},
		{"01:02:03", 3723, false},
		{"1:02:03.5", 3723.5, false},
		{" 2:00 ", 120, false},
		// A 30 hour stream is a real thing; hours must not wrap.
		{"30:00:00", 108000, false},
		{"", 0, true},
		{"1:2:3:4", 0, true},
		{"abc", 0, true},
		{"-5", 0, true},
		{"1::3", 0, true},
	}
	for _, c := range cases {
		got, err := ParseTimecode(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseTimecode(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTimecode(%q) errored: %v", c.in, err)
		} else if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ParseTimecode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatTimestampDoesNotWrapAt24Hours(t *testing.T) {
	// The bug this guards: TimeSpan "hh" renders 30 hours as 06.
	if got := FormatTimestamp(108000); got != "30:00:00.000" {
		t.Errorf("FormatTimestamp(30h) = %q, want 30:00:00.000", got)
	}
	if got := FormatTimestamp(3723.5); got != "01:02:03.500" {
		t.Errorf("FormatTimestamp(3723.5) = %q", got)
	}
}

func TestSelectorPrefersHLSFirst(t *testing.T) {
	s := NewSelection(Quality{"1080p", 1080}, Formats[0], true)
	got := s.Selector()
	branches := strings.Split(got, "/")
	if !strings.Contains(branches[0], "protocol^=m3u8") {
		t.Errorf("first branch is not HLS: %q", got)
	}
	if !strings.Contains(got, "[height<=1080]") {
		t.Errorf("height filter missing: %q", got)
	}
}

func TestSelectorDedupesWhenHLSDisabled(t *testing.T) {
	// With HLS off the two leading branches collapse to the same
	// string; emitting it twice is noise in every log.
	s := NewSelection(Quality{"720p", 720}, Formats[0], false)
	branches := strings.Split(s.Selector(), "/")
	seen := map[string]bool{}
	for _, b := range branches {
		if seen[b] {
			t.Fatalf("duplicate branch %q in %q", b, s.Selector())
		}
		seen[b] = true
	}
}

func TestSelectorBestAvailableHasNoHeightFilter(t *testing.T) {
	s := NewSelection(Qualities[0], Formats[0], true)
	if strings.Contains(s.Selector(), "height<=") {
		t.Errorf("best-available should carry no height filter: %q", s.Selector())
	}
}

func TestMP4FormatCapsAbove1080p(t *testing.T) {
	// YouTube publishes no avc1 above 1080p. Uncapped, the avc1 branch
	// matches nothing and yt-dlp silently falls back to a low-res
	// combined format.
	mp4 := Formats[2]
	if mp4.Kind != FormatMP4AV {
		t.Fatalf("Formats[2] is not the MP4 entry")
	}
	s := NewSelection(Quality{"2K / 1440p", 1440}, mp4, true)
	if s.EffectiveHeight != 1080 {
		t.Errorf("EffectiveHeight = %d, want 1080", s.EffectiveHeight)
	}
	if !s.Capped() {
		t.Error("Capped() = false, want true")
	}
	if !strings.Contains(s.Selector(), "[height<=1080]") {
		t.Errorf("selector not capped: %q", s.Selector())
	}
}

func TestMP4FormatCapsBestAvailable(t *testing.T) {
	s := NewSelection(Qualities[0], Formats[2], true)
	if s.EffectiveHeight != 1080 || !s.Capped() {
		t.Errorf("best-available + MP4 should cap to 1080: %+v", s)
	}
}

func newJob(start, end float64, hasStart, hasEnd bool) *Job {
	return &Job{
		URL:      "https://example/watch?v=x",
		Cfg:      DefaultConfig(),
		Sel:      NewSelection(Qualities[0], Formats[0], true),
		HasStart: hasStart, HasEnd: hasEnd,
		StartSec: start, EndSec: end,
		TempDir: "/tmp/x",
	}
}

func TestYtDlpArgsOmitsSectionForFullVideo(t *testing.T) {
	// Passing "*00:00:00.000-inf" works but forces the section code
	// path, and with it a needless remux of the whole file.
	j := newJob(0, 0, false, false)
	if strings.Contains(strings.Join(j.YtDlpArgs(), " "), "--download-sections") {
		t.Error("full-video run should not pass --download-sections")
	}
}

func TestYtDlpArgsSectionOpenEnded(t *testing.T) {
	j := newJob(90, 0, true, false)
	args := strings.Join(j.YtDlpArgs(), " ")
	if !strings.Contains(args, "*00:01:30.000-inf") {
		t.Errorf("open-ended section wrong: %s", args)
	}
}

func TestYtDlpArgsBacksOffFragmentsForLocalTrim(t *testing.T) {
	// Post-live DVR fragment URLs are short-lived; 16 at a time earns
	// an HTTP 401 part way through and kills the run.
	j := newJob(90, 300, true, true)
	j.TrimLocally = true
	args := j.YtDlpArgs()
	got := valueAfter(args, "--concurrent-fragments")
	if got != "4" {
		t.Errorf("concurrent-fragments = %s, want 4 for a local trim", got)
	}
	if strings.Contains(strings.Join(args, " "), "--download-sections") {
		t.Error("a local trim must download the full stream")
	}
}

func TestYtDlpArgsPadding(t *testing.T) {
	j := newJob(100, 200, true, true)
	j.Cfg.Padding = 5
	args := strings.Join(j.YtDlpArgs(), " ")
	if !strings.Contains(args, "*00:01:35.000-00:03:25.000") {
		t.Errorf("padding not applied: %s", args)
	}
	// Padding must never seek before zero.
	j2 := newJob(2, 10, true, true)
	j2.Cfg.Padding = 5
	if j2.DownloadStartSec() != 0 {
		t.Errorf("padded start = %v, want 0", j2.DownloadStartSec())
	}
}

func TestTrimArgsOrder(t *testing.T) {
	// -ss before -i is a fast input seek; -t after -i counts the
	// length from the seek point rather than from the file start.
	j := newJob(60, 120, true, true)
	j.TrimLocally = true
	args := j.CutArgs("in.mp4", "out.mp4")
	joined := strings.Join(args, " ")
	ss := strings.Index(joined, "-ss")
	in := strings.Index(joined, "-i")
	tt := strings.Index(joined, "-t ")
	if !(ss < in && in < tt) {
		t.Errorf("arg order wrong (-ss %d, -i %d, -t %d): %s", ss, in, tt, joined)
	}
}

func TestVerifyDetectsShortDownload(t *testing.T) {
	// The silent failure: yt-dlp exits 0, ffprobe confirms 1440p60,
	// and the file holds 2 seconds instead of 16 minutes.
	j := newJob(600, 1560, true, true) // 16 minutes requested
	j.Meta = Meta{Duration: 7200, HasDuration: true}
	v := j.Verify(MediaInfo{Duration: 2, HasDuration: true, Height: 1440, HasVideo: true})
	if !v.ShortDownload {
		t.Fatal("ShortDownload = false, want true")
	}
	if v.EncodeDuration != 2 {
		t.Errorf("EncodeDuration = %v, want the measured 2s", v.EncodeDuration)
	}
}

func TestVerifyToleratesKeyframeDrift(t *testing.T) {
	j := newJob(0, 100, true, true)
	j.Meta = Meta{Duration: 7200, HasDuration: true}
	v := j.Verify(MediaInfo{Duration: 99, HasDuration: true, Height: 1080, HasVideo: true})
	if v.ShortDownload {
		t.Error("1s of keyframe drift must not be reported as short")
	}
}

func TestVerifyAllowsSectionPastEndOfVideo(t *testing.T) {
	// Asking 0-100 of a 60 second video legitimately returns 60.
	j := newJob(0, 100, true, true)
	j.Meta = Meta{Duration: 60, HasDuration: true}
	v := j.Verify(MediaInfo{Duration: 60, HasDuration: true, Height: 1080, HasVideo: true})
	if v.ShortDownload {
		t.Errorf("clipping at the end of the video is not a short download (%+v)", v)
	}
}

func TestVerifyFullVideoTakesDurationFromFile(t *testing.T) {
	j := newJob(0, 0, false, false)
	v := j.Verify(MediaInfo{Duration: 421, HasDuration: true, Height: 1080, HasVideo: true})
	if v.EncodeDuration != 421 {
		t.Errorf("EncodeDuration = %v, want 421", v.EncodeDuration)
	}
	if v.ShortDownload {
		t.Error("a full-video run has nothing to compare against")
	}
}

func TestVerifyLocalTrimClampsToWhatExists(t *testing.T) {
	// Whole stream on disk, 100..400 requested, but only 350s exist.
	j := newJob(100, 400, true, true)
	j.TrimLocally = true
	v := j.Verify(MediaInfo{Duration: 350, HasDuration: true, Height: 1080, HasVideo: true})
	if v.EncodeDuration != 250 {
		t.Errorf("EncodeDuration = %v, want 250", v.EncodeDuration)
	}
	if v.ShortDownload {
		t.Error("a local trim never reports a short download")
	}
}

func TestVerifyDegradation(t *testing.T) {
	j := newJob(0, 60, true, true)
	j.Sel = NewSelection(Quality{"2K / 1440p", 1440}, Formats[0], true)
	j.Meta = Meta{Duration: 600, HasDuration: true}

	// 360p when 1440p was asked for means a selector fell through.
	v := j.Verify(MediaInfo{Duration: 60, HasDuration: true, Height: 360, HasVideo: true})
	if !v.Degraded {
		t.Error("360p for a 1440p request should be flagged")
	}

	// 1080p when 1440p was asked for is normal and not worth shouting.
	v = j.Verify(MediaInfo{Duration: 60, HasDuration: true, Height: 1080, HasVideo: true})
	if v.Degraded {
		t.Error("1080p for a 1440p request is normal, not a degradation")
	}
}

func TestValidateRange(t *testing.T) {
	j := newJob(100, 50, true, true)
	if _, err := j.ValidateRange(); err == nil {
		t.Error("end before start must be an error")
	}

	j = newJob(2000, 2100, true, true)
	j.Meta = Meta{Duration: 1200, HasDuration: true}
	if _, err := j.ValidateRange(); err == nil {
		t.Error("start past the end of the video must be an error")
	}

	j = newJob(100, 2000, true, true)
	j.Meta = Meta{Duration: 1200, HasDuration: true}
	warn, err := j.ValidateRange()
	if err != nil {
		t.Errorf("end past the video end is a warning, not an error: %v", err)
	}
	if warn == "" {
		t.Error("expected a warning for an end past the video end")
	}
}

func TestSeekable(t *testing.T) {
	for _, s := range []string{"is_live", "post_live", "is_upcoming"} {
		if (Meta{LiveStatus: s}).Seekable() {
			t.Errorf("%s must not be seekable", s)
		}
	}
	for _, s := range []string{"not_live", "was_live", ""} {
		if !(Meta{LiveStatus: s}).Seekable() {
			t.Errorf("%s should be seekable", s)
		}
	}
}

func TestVideoToolboxInvertsQualityScale(t *testing.T) {
	// -q:v runs the other way: higher is better. A CRF number passed
	// straight through would ask for the worst possible picture.
	vt := ByName("h264_videotoolbox")
	if vt == nil {
		t.Fatal("h264_videotoolbox missing from the registry")
	}
	good := valueAfter(vt.CodecArgs(18, SpeedBalanced), "-q:v")
	bad := valueAfter(vt.CodecArgs(35, SpeedBalanced), "-q:v")
	if good <= bad {
		t.Errorf("lower CRF must map to a higher -q:v (got %s vs %s)", good, bad)
	}
}

func TestEncoderQualityFlagsDiffer(t *testing.T) {
	want := map[string]string{
		"h264_nvenc": "-cq",
		"h264_qsv":   "-global_quality",
		"h264_amf":   "-qp_i",
		"h264_vaapi": "-qp",
		"libx264":    "-crf",
	}
	for name, flag := range want {
		e := ByName(name)
		if e == nil {
			t.Fatalf("%s missing from the registry", name)
		}
		if valueAfter(e.CodecArgs(20, SpeedBalanced), flag) != "20" {
			t.Errorf("%s: %s not set to 20: %v", name, flag, e.CodecArgs(20, SpeedBalanced))
		}
	}
}

func TestVAAPINeedsUploadFilter(t *testing.T) {
	// VAAPI encodes from GPU memory only; without the upload the
	// encoder rejects every frame.
	e := ByName("h264_vaapi")
	if got := strings.Join(e.Filters(), " "); !strings.Contains(got, "hwupload") {
		t.Errorf("vaapi filters = %q, want an hwupload", got)
	}
}

func TestParseFFStats(t *testing.T) {
	line := "frame=  874 fps=846 q=-1.0 Lsize=    4416KiB time=00:00:12.00 " +
		"bitrate=3013.5kbits/s speed=11.6x elapsed=0:00:01.03"
	p, ok := parseFFStats(line, 40)
	if !ok {
		t.Fatal("stats line not recognised")
	}
	if math.Abs(p.MediaSeconds-12) > 1e-6 {
		t.Errorf("MediaSeconds = %v, want 12", p.MediaSeconds)
	}
	if math.Abs(p.SpeedX-11.6) > 1e-6 {
		t.Errorf("SpeedX = %v, want 11.6", p.SpeedX)
	}
	if p.Downloaded != 4416*1024 {
		t.Errorf("Downloaded = %d, want %d", p.Downloaded, 4416*1024)
	}
	if math.Abs(p.Fraction()-0.3) > 1e-6 {
		t.Errorf("Fraction = %v, want 0.3", p.Fraction())
	}
}

func TestParseFFStatsIgnoresNA(t *testing.T) {
	// "N/A" reaching the ETA maths as a string is how the old awk
	// pipeline could divide by zero.
	line := "frame=    0 fps=N/A q=-1.0 size=       0KiB time=00:00:00.00 speed=N/A"
	p, ok := parseFFStats(line, 40)
	if !ok {
		t.Fatal("stats line not recognised")
	}
	if p.SpeedX != -1 {
		t.Errorf("SpeedX = %v, want -1 for N/A", p.SpeedX)
	}
	if p.RemainingETA() != -1 {
		t.Errorf("RemainingETA = %v, want -1 when speed is unknown", p.RemainingETA())
	}
}

func TestParseYtProgress(t *testing.T) {
	line := progressPrefix + "1048576|10485760|NA|524288.0|18|30|120"
	p, ok := parseYtProgress(line)
	if !ok {
		t.Fatal("progress line not recognised")
	}
	if p.Downloaded != 1048576 || p.Total != 10485760 {
		t.Errorf("bytes wrong: %+v", p)
	}
	// Fragment counts beat byte estimates: they do not jump around.
	if math.Abs(p.Fraction()-0.25) > 1e-6 {
		t.Errorf("Fraction = %v, want 0.25 from the fragment count", p.Fraction())
	}
}

func TestEncodeProgressETA(t *testing.T) {
	p := EncodeProgress{OutSeconds: 30, Duration: 120, Speed: 3}
	if math.Abs(p.ETA()-30) > 1e-6 {
		t.Errorf("ETA = %v, want 30", p.ETA())
	}
	if (EncodeProgress{Duration: -1}).ETA() != -1 {
		t.Error("unknown duration must give an unknown ETA")
	}
	if (EncodeProgress{Duration: 100, Speed: 0}).ETA() != -1 {
		t.Error("zero speed must not divide")
	}
}

func TestSanitizeName(t *testing.T) {
	if got := SanitizeName(`a/b:c*d?`); strings.ContainsAny(got, `/:*?`) {
		t.Errorf("SanitizeName left illegal characters: %q", got)
	}
	if got := SanitizeName("trailing..."); strings.HasSuffix(got, ".") {
		t.Errorf("trailing dots break on Windows: %q", got)
	}
	if SanitizeName("   ") == "" {
		t.Error("blank name should fall back to a timestamp")
	}
}

// valueAfter returns the argument following flag, or "".
func valueAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestParseTimecodeHumanForms(t *testing.T) {
	// Nobody should have to remember which shape this tool wants.
	// Note the stdlib and every duration library on top of it reject
	// most of these - spaces, spelled-out units, clock form and bare
	// seconds are all outside time.ParseDuration.
	cases := []struct {
		in   string
		want float64
	}{
		{"1h20m", 4800},
		{"1h 20m", 4800},
		{"1h20m30s", 4830},
		{"1 hr 20 minute", 4800},
		{"1 hour and 20 minutes", 4800},
		{"1 hour, 20 minutes, 30 seconds", 4830},
		{"20 minutes", 1200},
		{"45s", 45},
		{"45 sec", 45},
		{"2 hours", 7200},
		{"1.5h", 5400},
		{"90 mins", 5400},
		{"HR 1" /* junk unit first */, -1},

		// A trailing bare number takes the next unit down.
		{"1h 20", 4800},
		{"1h 20m 30", 4830},
		{"5m 30", 330},

		// Clock form and bare seconds still work.
		{"1:20:00", 4800},
		{"1:20", 80},
		{"90", 90},

		// Junk stays junk.
		{"1 fortnight", -1},
		{"h", -1},
		{"1h m", -1},
		{"tomorrow", -1},
		{"30 s 15", -1}, // nothing below seconds to fall to
	}

	for _, c := range cases {
		got, err := ParseTimecode(c.in)
		if c.want < 0 {
			if err == nil {
				t.Errorf("ParseTimecode(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTimecode(%q) errored: %v", c.in, err)
		} else if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ParseTimecode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseTimecodeAcceptsEverythingStdlibDoes guards the one property
// that matters when replacing a library: no regression against it.
func TestParseTimecodeAcceptsEverythingStdlibDoes(t *testing.T) {
	for _, s := range []string{"1h20m", "1h20m30s", "45s", "1.5h", "90m", "3600s"} {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("test case %q is not stdlib-parseable", s)
		}
		got, err := ParseTimecode(s)
		if err != nil {
			t.Errorf("ParseTimecode(%q) rejected what stdlib accepts: %v", s, err)
			continue
		}
		if math.Abs(got-d.Seconds()) > 1e-9 {
			t.Errorf("ParseTimecode(%q) = %v, stdlib says %v", s, got, d.Seconds())
		}
	}
}

func TestManagedOnlyMatchesOurOwnDir(t *testing.T) {
	// The whole point of Managed is telling "we downloaded this" from
	// "it was already on the machine", so a false positive would let
	// --uninstall delete something it did not install.
	dir, err := InstallDir()
	if err != nil {
		t.Skip("no cache dir on this platform")
	}
	if !Managed(filepath.Join(dir, exeName("ffmpeg"))) {
		t.Error("a file in the install dir should be Managed")
	}
	for _, p := range []string{"", "/usr/bin/ffmpeg", filepath.Join(dir, "..", "ffmpeg")} {
		if Managed(p) {
			t.Errorf("Managed(%q) = true, want false", p)
		}
	}
}

func TestPlanInstallCoversThisPlatform(t *testing.T) {
	// yt-dlp publishes a build for every platform we target, so this
	// must never fall through to the manual hint on a supported OS.
	spec, err := PlanInstall("yt-dlp")
	if err != nil {
		t.Fatalf("yt-dlp has no install plan for %s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
	}
	if spec.URL == "" || spec.SumsURL == "" {
		t.Errorf("incomplete spec: %+v", spec)
	}
	if !strings.HasPrefix(spec.URL, "https://github.com/yt-dlp/yt-dlp/") {
		t.Errorf("unexpected download host: %s", spec.URL)
	}

	// ffmpeg has no upstream static macOS build, and that has to
	// surface as a hint rather than a broken download.
	_, err = PlanInstall("ffmpeg")
	if runtime.GOOS == "darwin" {
		var un *UnsupportedError
		if !errors.As(err, &un) {
			t.Errorf("macOS ffmpeg should be reported unsupported, got %v", err)
		} else if len(un.Hint) == 0 {
			t.Error("an unsupported tool must come with a way to install it")
		}
	} else if err != nil {
		t.Errorf("ffmpeg has no install plan for %s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
	}
}

func TestInstallSourcesAreUpstreamHTTPS(t *testing.T) {
	// These URLs are what gets downloaded and then executed, so they
	// are pinned to the projects' own release hosts over TLS.
	for _, tool := range []string{"yt-dlp", "ffmpeg"} {
		spec, err := PlanInstall(tool)
		if err != nil {
			continue // unsupported on this platform, covered above
		}
		for _, u := range []string{spec.URL, spec.SumsURL} {
			if !strings.HasPrefix(u, "https://github.com/") {
				t.Errorf("%s: %q is not an upstream https GitHub release URL", tool, u)
			}
		}
		if spec.Asset == "" {
			t.Errorf("%s: no asset name, so the checksum cannot be looked up", tool)
		}
	}
}
