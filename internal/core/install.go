package core

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// Installing binaries we then execute is the same trust model every
// package manager uses, so it is done the same way: fixed upstream
// release URLs, never a mirror or a redirect we chose, and every
// download checked against the SHA-256 the project publishes beside it.
// A mismatch aborts. Nothing is ever placed on PATH - files land in our
// own data dir, which LookTool checks before PATH.

const (
	ytdlpBase  = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	ffmpegBase = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/"
)

// archiveKind is how a downloaded asset is packed.
type archiveKind int

const (
	archiveNone archiveKind = iota
	archiveZip
	archiveTarXZ
)

// ToolSpec is one installable dependency for this exact platform.
type ToolSpec struct {
	Tool string // "yt-dlp" or "ffmpeg"
	// Source is shown to the user before anything is downloaded. Where
	// a binary comes from is not a detail to hide.
	Source  string
	URL     string
	SumsURL string
	// Asset is the filename as it appears in the checksums file.
	Asset   string
	Archive archiveKind
	// Wants are the executables to extract, without any extension.
	Wants []string
}

// InstallProgress reports one step of an install.
type InstallProgress struct {
	Tool  string
	Phase string
	// Done and Total are bytes; Total is -1 when the server does not
	// say how big the file is.
	Done  int64
	Total int64
}

func (p InstallProgress) Fraction() float64 {
	if p.Total <= 0 {
		return -1
	}
	return clamp01(float64(p.Done) / float64(p.Total))
}

// UnsupportedError explains why a tool cannot be auto-installed here,
// and says what to run instead.
type UnsupportedError struct {
	Tool string
	Why  string
	Hint []string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s cannot be installed automatically: %s", e.Tool, e.Why)
}

// PlanInstall returns the spec for one tool on this platform.
func PlanInstall(tool string) (ToolSpec, error) {
	switch tool {
	case "yt-dlp":
		return planYtDlp()
	case "ffmpeg", "ffprobe":
		return planFFmpeg()
	}
	return ToolSpec{}, fmt.Errorf("unknown tool %q", tool)
}

// planYtDlp picks the standalone build for this platform.
//
// These are PyInstaller bundles with their own Python inside, so
// installing one adds no runtime to the machine.
func planYtDlp() (ToolSpec, error) {
	var asset string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		asset = "yt-dlp.exe"
	case "windows/arm64":
		asset = "yt-dlp_arm64.exe"
	case "darwin/amd64", "darwin/arm64":
		asset = "yt-dlp_macos" // universal2
	case "linux/amd64":
		asset = "yt-dlp_linux"
	case "linux/arm64":
		asset = "yt-dlp_linux_aarch64"
	default:
		return ToolSpec{}, &UnsupportedError{
			Tool: "yt-dlp",
			Why:  "no published build for " + runtime.GOOS + "/" + runtime.GOARCH,
			Hint: installHintsFor("yt-dlp"),
		}
	}

	return ToolSpec{
		Tool:    "yt-dlp",
		Source:  "github.com/yt-dlp/yt-dlp (latest release)",
		URL:     ytdlpBase + asset,
		SumsURL: ytdlpBase + "SHA2-256SUMS",
		Asset:   asset,
		Archive: archiveNone,
		Wants:   []string{"yt-dlp"},
	}, nil
}

// planFFmpeg picks a static build for this platform.
//
// The GPL builds are the ones with the hardware encoders compiled in -
// NVENC, AMF, VAAPI - which is the whole point of taking the trouble.
func planFFmpeg() (ToolSpec, error) {
	var asset string
	kind := archiveZip

	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		asset = "ffmpeg-master-latest-win64-gpl.zip"
	case "windows/arm64":
		asset = "ffmpeg-master-latest-winarm64-gpl.zip"
	case "linux/amd64":
		asset, kind = "ffmpeg-master-latest-linux64-gpl.tar.xz", archiveTarXZ
	case "linux/arm64":
		asset, kind = "ffmpeg-master-latest-linuxarm64-gpl.tar.xz", archiveTarXZ
	case "darwin/amd64", "darwin/arm64":
		// There is no static macOS ffmpeg published anywhere stable
		// enough to point a downloader at. Homebrew's is fine and
		// carries VideoToolbox, which is the encoder that matters on
		// a Mac anyway.
		return ToolSpec{}, &UnsupportedError{
			Tool: "ffmpeg",
			Why:  "no upstream static build for macOS is published at a stable URL",
			Hint: installHintsFor("ffmpeg"),
		}
	default:
		return ToolSpec{}, &UnsupportedError{
			Tool: "ffmpeg",
			Why:  "no published build for " + runtime.GOOS + "/" + runtime.GOARCH,
			Hint: installHintsFor("ffmpeg"),
		}
	}

	return ToolSpec{
		Tool:    "ffmpeg",
		Source:  "github.com/BtbN/FFmpeg-Builds (latest, GPL - includes NVENC/AMF/VAAPI)",
		URL:     ffmpegBase + asset,
		SumsURL: ffmpegBase + "checksums.sha256",
		Asset:   asset,
		Archive: kind,
		// ffprobe is what the whole verification stage runs on, so it
		// comes along rather than being a second install.
		Wants: []string{"ffmpeg", "ffprobe"},
	}, nil
}

// InstallDir is where fetched binaries land. LookTool checks it first,
// so nothing has to touch PATH or need elevation.
func InstallDir() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bin"), nil
}

// Install downloads, verifies and unpacks one tool.
func Install(ctx context.Context, spec ToolSpec, report func(InstallProgress)) ([]string, error) {
	if report == nil {
		report = func(InstallProgress) {}
	}
	say := func(phase string) { report(InstallProgress{Tool: spec.Tool, Phase: phase, Total: -1}) }

	dir, err := InstallDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	say("fetching checksums")
	want, err := fetchChecksum(ctx, spec.SumsURL, spec.Asset)
	if err != nil {
		return nil, fmt.Errorf("could not get the published checksum: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	got, err := download(ctx, spec.URL, tmp, func(done, total int64) {
		report(InstallProgress{Tool: spec.Tool, Phase: "downloading", Done: done, Total: total})
	})
	tmp.Close()
	if err != nil {
		return nil, err
	}

	// A mismatch means the bytes are not what the project published.
	// There is no safe way to continue, so do not.
	if !strings.EqualFold(got, want) {
		return nil, fmt.Errorf(
			"checksum mismatch for %s\n  published: %s\n  received:  %s",
			spec.Asset, want, got)
	}
	say("verified")

	say("installing")
	installed, err := place(tmpName, dir, spec)
	if err != nil {
		return nil, err
	}
	return installed, nil
}

// fetchChecksum pulls one "hash  filename" line out of a sums file.
func fetchChecksum(ctx context.Context, url, asset string) (string, error) {
	body, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 4<<20))
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// Some tools prefix the name with "*" for binary mode.
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s is not listed in %s", asset, path.Base(url))
}

// download streams to w, hashing as it goes so the file is never read
// back off disk to be checked.
func download(ctx context.Context, url string, w io.Writer, onProgress func(done, total int64)) (string, error) {
	body, total, err := getWithLength(ctx, url)
	if err != nil {
		return "", err
	}
	defer body.Close()

	h := sha256.New()
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Now()

	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return "", werr
			}
			h.Write(buf[:n])
			done += int64(n)

			// Report at a human rate, not per read.
			if time.Since(last) > 100*time.Millisecond {
				onProgress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	onProgress(done, total)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	body, _, err := getWithLength(ctx, url)
	return body, err
}

func getWithLength(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "ytclip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// place unpacks the download into dir and returns what it installed.
func place(src, dir string, spec ToolSpec) ([]string, error) {
	switch spec.Archive {
	case archiveNone:
		dest := filepath.Join(dir, exeName(spec.Wants[0]))
		if err := moveExecutable(src, dest); err != nil {
			return nil, err
		}
		return []string{dest}, nil

	case archiveZip:
		return extractZip(src, dir, spec.Wants)

	case archiveTarXZ:
		return extractTarXZ(src, dir, spec.Wants)
	}
	return nil, fmt.Errorf("unknown archive kind")
}

// wanted reports whether an archive entry is one of the executables we
// asked for, ignoring the directory prefix the archive wraps it in.
func wanted(name string, wants []string) (string, bool) {
	base := path.Base(filepath.ToSlash(name))
	for _, w := range wants {
		if base == w || base == w+".exe" {
			return w, true
		}
	}
	return "", false
}

func extractZip(src, dir string, wants []string) ([]string, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var out []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name, ok := wanted(f.Name, wants)
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, exeName(name))
		err = writeExecutable(dest, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, dest)
	}
	return checkComplete(out, wants)
}

func extractTarXZ(src, dir string, wants []string) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	xr, err := xz.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(xr)

	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name, ok := wanted(hdr.Name, wants)
		if !ok {
			continue
		}
		dest := filepath.Join(dir, exeName(name))
		if err := writeExecutable(dest, tr); err != nil {
			return nil, err
		}
		out = append(out, dest)
	}
	return checkComplete(out, wants)
}

// checkComplete refuses a partial extraction rather than leaving half
// an install behind for the preflight to trip over later.
func checkComplete(got []string, wants []string) ([]string, error) {
	if len(got) < len(wants) {
		var missing []string
		for _, w := range wants {
			found := false
			for _, g := range got {
				if strings.TrimSuffix(filepath.Base(g), ".exe") == w {
					found = true
				}
			}
			if !found {
				missing = append(missing, w)
			}
		}
		return got, fmt.Errorf("archive did not contain %s", strings.Join(missing, ", "))
	}
	return got, nil
}

// writeExecutable writes to a temp file next to dest, then renames, so
// an interrupted install never leaves a truncated binary in place.
func writeExecutable(dest string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".install-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return finishExecutable(tmpName, dest)
}

func moveExecutable(src, dest string) error {
	return finishExecutable(src, dest)
}

func finishExecutable(tmpName, dest string) error {
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Windows refuses to rename over a running executable; removing
	// first turns that into a clear failure rather than a silent one.
	os.Remove(dest)
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// installHintsFor is the manual fallback, per platform.
func installHintsFor(tool string) []string {
	switch runtime.GOOS {
	case "windows":
		if tool == "yt-dlp" {
			return []string{"winget install yt-dlp.yt-dlp", "scoop install yt-dlp", "choco install yt-dlp"}
		}
		return []string{"winget install Gyan.FFmpeg", "scoop install ffmpeg", "choco install ffmpeg"}
	case "darwin":
		return []string{"brew install " + tool}
	default:
		if tool == "yt-dlp" {
			return []string{"pipx install yt-dlp", "sudo apt install yt-dlp", "sudo dnf install yt-dlp", "sudo pacman -S yt-dlp"}
		}
		return []string{"sudo apt install ffmpeg", "sudo dnf install ffmpeg", "sudo pacman -S ffmpeg"}
	}
}
