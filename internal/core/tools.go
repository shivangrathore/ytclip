package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Tools holds resolved paths to the external binaries the tool drives.
//
// ffprobe is optional: without it the post-download verification is
// skipped, which is a downgrade but not a failure.
type Tools struct {
	YtDlp   string
	FFmpeg  string
	FFprobe string
}

// FindTools resolves every binary we need.
func FindTools() Tools {
	return Tools{
		YtDlp:   LookTool("yt-dlp"),
		FFmpeg:  LookTool("ffmpeg"),
		FFprobe: LookTool("ffprobe"),
	}
}

// LookTool resolves a binary by three routes, in order:
//
//  1. our own data dir, where --bootstrap drops fetched binaries
//  2. the directory holding this executable, so a user can just
//     drop yt-dlp next to it (the .ps1/.sh behaviour)
//  3. PATH
//
// exec.LookPath already honours PATHEXT on Windows, so only the
// directory probes need the .exe suffix spelled out.
func LookTool(name string) string {
	var dirs []string

	if d, err := DataDir(); err == nil {
		dirs = append(dirs, filepath.Join(d, "bin"))
	}
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		dirs = append(dirs, filepath.Dir(self))
	}

	for _, dir := range dirs {
		p := filepath.Join(dir, exeName(name))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}

	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// DataDir is where we keep fetched binaries and caches.
func DataDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "youtube-clip"), nil
}

// DepStatus is the result of checking one dependency.
type DepStatus struct {
	Name     string
	Path     string
	Version  string
	Required bool
	// Err is set when the binary exists but refuses to execute.
	// A truncated download, a blocked file from the internet zone,
	// or a half-finished package upgrade all leave a file that
	// exists and does not run, so presence is never enough.
	Err error
}

func (d DepStatus) OK() bool { return d.Path != "" && d.Err == nil }

// CheckDep runs the binary to prove it actually works.
func CheckDep(ctx context.Context, name, path string, required bool, versionArgs ...string) DepStatus {
	st := DepStatus{Name: name, Path: path, Required: required}
	if path == "" {
		return st
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, versionArgs...).Output()
	if err != nil && len(out) == 0 {
		st.Err = err
		return st
	}

	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	if len(line) > 70 {
		line = line[:70]
	}
	st.Version = line
	return st
}

// Managed reports whether a resolved path is a copy this tool
// downloaded, rather than something already on the machine.
func Managed(path string) bool {
	if path == "" {
		return false
	}
	dir, err := InstallDir()
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// Shadows returns the PATH copy that a managed binary is hiding, or "".
//
// Our data dir is searched before PATH, which is what makes a bootstrap
// take effect without touching the system. The cost is that a managed
// copy silently wins over a perfectly good system one, so anywhere the
// resolved path is reported, say when that is happening.
func Shadows(name, resolved string) string {
	if !Managed(resolved) {
		return ""
	}
	p, err := exec.LookPath(name)
	if err != nil || p == resolved {
		return ""
	}
	return p
}

// RemoveManaged deletes every binary this tool downloaded and reports
// what it removed.
func RemoveManaged() ([]string, error) {
	dir, err := InstallDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := os.Remove(p); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	os.Remove(dir)
	return removed, nil
}
