package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
	"github.com/shivangrathore/ytclip/internal/tui"
)

// Stamped by build.sh via -ldflags. "dev" when built with a plain
// `go build`, which is the honest answer for an untagged local binary.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	cfg := core.DefaultConfig()

	var (
		showVer   = flag.Bool("version", false, "print the version, then exit")
		doctor    = flag.Bool("doctor", false, "check dependencies and encoders, then exit")
		install   = flag.Bool("install", false, "download any missing dependencies, then exit")
		reinstall = flag.Bool("reinstall", false, "download dependencies even if they are already present")
		uninstall = flag.Bool("uninstall", false, "delete the dependencies ytclip downloaded, then exit")
		noConvert = flag.Bool("no-convert", false, "skip the H.264 re-encode, keep the source codec")
		encoder   = flag.String("encoder", "", "force an ffmpeg encoder (default: auto-detect)")
		quality   = flag.Int("quality", cfg.Quality, "encode quality on the CRF scale, lower is better")
		speed     = flag.String("speed", string(cfg.Speed), "encode speed: fast, balanced, quality")
		outDir    = flag.String("out", "", "output directory (default: ./clips)")
		frags     = flag.Int("fragments", cfg.ConcurrentFragments, "parallel segment downloads")
		noHLS     = flag.Bool("no-hls", false, "do not prefer HLS delivery (much slower)")
		padding   = flag.Float64("padding", cfg.Padding, "extra seconds either side of the section")
	)
	flag.Parse()

	cfg.ConvertToH264 = !*noConvert
	cfg.Encoder = *encoder
	cfg.Quality = *quality
	cfg.Speed = core.Speed(*speed)
	cfg.OutputDir = *outDir
	cfg.ConcurrentFragments = *frags
	cfg.PreferHLS = !*noHLS
	cfg.Padding = *padding

	if *showVer {
		fmt.Printf("ytclip %s (%s) %s/%s %s\n",
			version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}
	if *uninstall {
		os.Exit(runUninstall())
	}
	if *install || *reinstall {
		os.Exit(runInstall(*reinstall))
	}
	if *doctor {
		os.Exit(runDoctor())
	}

	p := tea.NewProgram(tui.New(cfg), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runDoctor prints what is installed and which encoders actually work.
//
// Being listed by `ffmpeg -encoders` proves the build has the code, not
// that this machine has the GPU or the driver - so this reports the
// result of a real one-frame encode for each.
func runDoctor() int {
	ctx := context.Background()
	t := core.FindTools()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tSTATUS\tPATH")

	deps := []core.DepStatus{
		core.CheckDep(ctx, "yt-dlp", t.YtDlp, true, "--version"),
		core.CheckDep(ctx, "ffmpeg", t.FFmpeg, true, "-hide_banner", "-version"),
		core.CheckDep(ctx, "ffprobe", t.FFprobe, false, "-hide_banner", "-version"),
	}
	bad := 0
	var shadowed []string
	for _, d := range deps {
		status := "ok"
		switch {
		case d.Path == "" && d.Required:
			status, bad = "MISSING", bad+1
		case d.Path == "":
			status = "absent (optional)"
		case d.Err != nil:
			status, bad = "BROKEN", bad+1
		}

		where := d.Path
		if core.Managed(d.Path) {
			where += "   (downloaded by ytclip)"
			if other := core.Shadows(d.Name, d.Path); other != "" {
				shadowed = append(shadowed, fmt.Sprintf("  %s is also on PATH at %s", d.Name, other))
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, status, where)
	}
	w.Flush()

	if len(shadowed) > 0 {
		fmt.Println("\nytclip is using its own downloaded copies in preference to:")
		for _, l := range shadowed {
			fmt.Println(l)
		}
		fmt.Println("\nRun  ytclip --uninstall  to delete them and go back to the system copies.")
	}

	if t.FFmpeg == "" {
		fmt.Println("\nffmpeg is required; cannot probe encoders.")
		return 1
	}

	det, err := core.DetectEncoders(ctx, t.FFmpeg, false)
	if err != nil {
		fmt.Println("\nencoder probe failed:", err)
		return 1
	}

	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ENCODER\tWORKS\tDETAIL")
	for _, a := range det.Results {
		works := "no"
		if a.Works {
			works = "YES"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, works, a.Reason)
	}
	w.Flush()

	if best := det.Best(); best != nil {
		fmt.Printf("\nselected: %s (%s)\n", best.Label, best.Name)
	}
	return bad
}

// runInstall fetches the missing dependencies into our own data dir.
//
// Nothing is placed on PATH and nothing needs elevation: LookTool
// checks the data dir before PATH, so an install here is visible to
// this tool and invisible to everything else on the machine.
func runInstall(force bool) int {
	ctx := context.Background()
	t := core.FindTools()

	dir, err := core.InstallDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	type want struct {
		tool    string
		present bool
	}
	wants := []want{
		{"yt-dlp", t.YtDlp != ""},
		// ffprobe rides along with ffmpeg, so one entry covers both.
		{"ffmpeg", t.FFmpeg != "" && t.FFprobe != ""},
	}

	var todo []core.ToolSpec
	for _, w := range wants {
		if w.present && !force {
			fmt.Printf("%-8s already present, skipping\n", w.tool)
			continue
		}
		spec, err := core.PlanInstall(w.tool)
		if err != nil {
			var un *core.UnsupportedError
			if errors.As(err, &un) {
				fmt.Printf("\n%s cannot be installed automatically here:\n  %s\n", un.Tool, un.Why)
				fmt.Println("  install it with one of:")
				for _, h := range un.Hint {
					fmt.Println("    " + h)
				}
				continue
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		todo = append(todo, spec)
	}

	if len(todo) == 0 {
		fmt.Println("\nNothing to install.")
		return 0
	}

	fmt.Printf("\nInstalling into %s\n\n", dir)
	tty := isTTY()
	report := printProgress(tty)

	for _, spec := range todo {
		fmt.Printf("%s\n  from %s\n", spec.Tool, spec.Source)

		files, err := core.Install(ctx, spec, report)
		if tty {
			fmt.Println()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
			return 1
		}
		for _, f := range files {
			fmt.Println("  installed " + f)
		}
		fmt.Println()
	}

	// Downloaded is not the same as working. Prove it the same way the
	// preflight does - by running them.
	fmt.Println("Verifying...")
	t = core.FindTools()
	bad := 0
	for _, d := range []core.DepStatus{
		core.CheckDep(ctx, "yt-dlp", t.YtDlp, true, "--version"),
		core.CheckDep(ctx, "ffmpeg", t.FFmpeg, true, "-hide_banner", "-version"),
		core.CheckDep(ctx, "ffprobe", t.FFprobe, false, "-hide_banner", "-version"),
	} {
		switch {
		case d.OK():
			fmt.Printf("  ok      %-8s %s\n", d.Name, d.Version)
		case d.Path == "" && !d.Required:
			fmt.Printf("  absent  %-8s (optional)\n", d.Name)
		default:
			fmt.Printf("  FAILED  %-8s %s\n", d.Name, d.Path)
			bad++
		}
	}
	if bad == 0 {
		fmt.Println("\nReady. Run ytclip to start.")
	}
	return bad
}

// isTTY reports whether stdout is a terminal. Redrawing one line with
// \r into a pipe or a log produces a single unreadable mega-line, so
// piped output gets discrete lines instead.
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// printProgress renders a redrawn line on a terminal, and a coarse
// stepped log anywhere else.
func printProgress(tty bool) func(core.InstallProgress) {
	lastStep := -1

	return func(p core.InstallProgress) {
		if !tty {
			if p.Phase != "downloading" {
				fmt.Printf("  %s\n", p.Phase)
				lastStep = -1
				return
			}
			step := int(p.Fraction() * 10)
			if p.Fraction() < 0 || step == lastStep {
				return
			}
			lastStep = step
			fmt.Printf("  downloading  %3.0f%%  %s / %s\n",
				p.Fraction()*100, core.FormatBytes(p.Done), core.FormatBytes(p.Total))
			return
		}

		switch {
		case p.Phase == "downloading" && p.Fraction() >= 0:
			fmt.Printf("\r  downloading  %5.1f%%  %s / %s        ",
				p.Fraction()*100, core.FormatBytes(p.Done), core.FormatBytes(p.Total))
		case p.Phase == "downloading":
			fmt.Printf("\r  downloading  %s        ", core.FormatBytes(p.Done))
		default:
			fmt.Printf("\r  %-56s", p.Phase)
		}
	}
}

// runUninstall deletes the copies we downloaded, so the machine goes
// back to whatever it had before.
func runUninstall() int {
	removed, err := core.RemoveManaged()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(removed) == 0 {
		fmt.Println("Nothing to remove - ytclip has not downloaded any dependencies.")
		return 0
	}
	for _, p := range removed {
		fmt.Println("removed " + p)
	}

	t := core.FindTools()
	fmt.Println()
	for _, d := range []struct{ name, path string }{
		{"yt-dlp", t.YtDlp}, {"ffmpeg", t.FFmpeg}, {"ffprobe", t.FFprobe},
	} {
		if d.path == "" {
			fmt.Printf("%-8s no longer found\n", d.name)
		} else {
			fmt.Printf("%-8s now using %s\n", d.name, d.path)
		}
	}
	return 0
}
