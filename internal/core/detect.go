package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Availability is the verdict on one candidate encoder.
type Availability struct {
	Encoder *Encoder `json:"-"`
	Name    string   `json:"name"`
	// Listed means ffmpeg was built with it.
	Listed bool `json:"listed"`
	// Works means a real one-frame encode succeeded. This is the only
	// answer that matters. Being listed proves the build has the code,
	// not that this machine has the GPU, the driver, or a display
	// server to talk to - the exact reason the .ps1's `-encoders |
	// match nvenc` check was wrong on any machine without an NVIDIA card.
	Works bool `json:"works"`
	// Reason carries ffmpeg's complaint when Works is false.
	Reason string `json:"reason,omitempty"`
	// Probed is false when the entry was skipped (wrong GOOS, or not
	// listed at all), so the UI can tell "no" from "not asked".
	Probed bool `json:"probed"`
}

// Detection is the full result, cached between runs.
type Detection struct {
	// Key ties the cache to one exact ffmpeg build. A user who
	// upgrades ffmpeg, or installs a second one, gets a fresh probe
	// rather than a stale verdict.
	Key       string          `json:"key"`
	Stamp     time.Time       `json:"stamp"`
	Results   []*Availability `json:"results"`
	Available []*Encoder      `json:"-"`
}

// Best returns the highest-priority working encoder. libx264 is in the
// registry and always works, so this only returns nil if ffmpeg is
// broken outright.
func (d *Detection) Best() *Encoder {
	if len(d.Available) == 0 {
		return nil
	}
	return d.Available[0]
}

// DetectEncoders finds every H.264 encoder that actually works here.
//
// Two stages, because each answers a different question:
//
//	stage 1  ffmpeg -encoders   -> was it compiled in?
//	stage 2  one-frame encode   -> does it run on THIS machine?
//
// Stage 2 is the one that matters and the one the old scripts skipped.
// A headless box, a missing driver, an Optimus laptop on the iGPU, a
// Docker container with no /dev/dri - all list h264_nvenc happily and
// all fail at the first frame, after the download has already happened.
func DetectEncoders(ctx context.Context, ffmpegPath string, useCache bool) (*Detection, error) {
	key, err := ffmpegKey(ffmpegPath)
	if err != nil {
		return nil, err
	}

	if useCache {
		if d, ok := loadCache(key); ok {
			d.rehydrate()
			return d, nil
		}
	}

	listed, err := listEncoders(ctx, ffmpegPath)
	if err != nil {
		return nil, err
	}

	d := &Detection{Key: key, Stamp: time.Now().UTC()}

	for _, e := range registry {
		a := &Availability{Encoder: e, Name: e.Name}

		switch {
		case e.GOOS != "" && e.GOOS != runtime.GOOS:
			a.Reason = "not available on " + runtime.GOOS
		case !listed[e.Name]:
			a.Reason = "not compiled into this ffmpeg build"
		default:
			a.Listed = true
			a.Probed = true
			if err := probeEncoder(ctx, ffmpegPath, e); err != nil {
				a.Reason = firstLine(err.Error())
			} else {
				a.Works = true
			}
		}

		d.Results = append(d.Results, a)
	}

	d.rehydrate()
	saveCache(d)
	return d, nil
}

func (d *Detection) rehydrate() {
	d.Available = nil
	for _, a := range d.Results {
		if a.Encoder == nil {
			a.Encoder = ByName(a.Name)
		}
		if a.Works && a.Encoder != nil {
			d.Available = append(d.Available, a.Encoder)
		}
	}
}

// listEncoders parses `ffmpeg -encoders`.
//
// Lines look like:
//
//	V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
//
// so the encoder name is always the second whitespace-separated field.
// Substring-matching the whole blob (what the .ps1 did) also matches
// the description text of unrelated entries.
func listEncoders(ctx context.Context, ffmpegPath string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}

	found := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "V") {
			continue
		}
		found[fields[1]] = true
	}
	return found, nil
}

// probeEncoder runs a real encode of one synthetic frame.
//
// Cheap - a 320x240 null source, one frame, discarded output - but it
// exercises the whole path: device open, driver handshake, surface
// allocation, session init. Everything that fails on a machine without
// the hardware fails here, in under a second, before any download.
func probeEncoder(ctx context.Context, ffmpegPath string, e *Encoder) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	args = append(args, e.InitArgs()...)
	args = append(args,
		"-f", "lavfi",
		"-i", "nullsrc=s=320x240:r=30:d=0.1",
	)
	args = append(args, e.Filters()...)
	args = append(args, "-frames:v", "1")
	args = append(args, e.CodecArgs(23, SpeedFast)...)
	args = append(args, "-f", "null", "-")

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return &probeError{msg}
		}
		return err
	}
	return nil
}

type probeError struct{ msg string }

func (p *probeError) Error() string { return p.msg }

func firstLine(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// ffmpegKey identifies one ffmpeg build: path, size, mtime, and the
// banner version. Any of those changing invalidates the cache.
func ffmpegKey(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte(st.ModTime().UTC().String()))
	h.Write([]byte(itoa(int(st.Size()))))
	h.Write([]byte(runtime.GOOS + "/" + runtime.GOARCH))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func cachePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "encoders.json"), nil
}

func loadCache(key string) (*Detection, bool) {
	p, err := cachePath()
	if err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var d Detection
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, false
	}
	if d.Key != key {
		return nil, false
	}
	// A driver can be installed after a "no". Re-probe weekly so a
	// cached negative is never permanent.
	if time.Since(d.Stamp) > 7*24*time.Hour {
		return nil, false
	}
	return &d, true
}

func saveCache(d *Detection) {
	p, err := cachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, p)
	}
}
