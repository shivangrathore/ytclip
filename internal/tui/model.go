package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
)

type screen int

const (
	scrDeps screen = iota
	scrDepsMissing
	scrInstall
	scrURL
	scrMeta
	scrForm
	scrConfirm
	scrRun
	scrDone
	scrFatal
)

// field indexes on the form screen.
const (
	fldQuality = iota
	fldFormat
	fldEncoder
	fldStart
	fldEnd
	fldName
	fldGo
	fldCount
)

const maxLogLines = 500

// Model is the whole application state.
type Model struct {
	cfg     core.Config
	baseDir string

	width  int
	height int

	screen screen
	fatal  error

	// preflight
	tools       core.Tools
	deps        []core.DepStatus
	plan        installPlan
	installCh   chan installEvent
	installProg core.InstallProgress
	installLog  []string
	installErr  error
	det         *core.Detection
	spin        spinner.Model
	busyText    string

	// inputs
	urlIn   textinput.Model
	startIn textinput.Model
	endIn   textinput.Model
	nameIn  textinput.Model

	meta core.Meta

	// form state
	focus      int
	qualityIdx int
	formatIdx  int
	encoderIdx int // 0 = auto, then det.Available in order
	formErr    string
	formWarn   string

	// run state
	job      *core.Job
	cancel   context.CancelFunc
	events   chan core.Event
	dlBar    progress.Model
	encBar   progress.Model
	dlProg   core.DownloadProgress
	encProg  core.EncodeProgress
	stage    core.Stage
	stageMsg string
	logs     []string
	showLog  bool

	source  string
	verify  core.Verification
	confirm confirmPrompt

	// Stage timings, reported on the done screen. Measured, like
	// everything else here.
	dlStart  time.Time
	dlEnd    time.Time
	encStart time.Time
	encEnd   time.Time

	// done state
	finalPath string
	finalSize int64
	finalInfo core.MediaInfo
}

// New builds the initial model.
func New(cfg core.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	url := textinput.New()
	url.Placeholder = "https://www.youtube.com/watch?v=..."
	url.CharLimit = 500
	url.Width = 60

	// Our own focus marker is the "> " in the left gutter; the widget's
	// default prompt would double it up.
	url.Prompt = ""

	start := textinput.New()
	start.Placeholder = "from the beginning"
	start.CharLimit = 20
	start.Width = 28
	start.Prompt = ""

	end := textinput.New()
	end.Placeholder = "to the end"
	end.CharLimit = 20
	end.Width = 28
	end.Prompt = ""

	name := textinput.New()
	name.Placeholder = "auto"
	name.CharLimit = 120
	name.Width = 40
	name.Prompt = ""

	base, _ := os.Getwd()

	return Model{
		cfg:      cfg,
		baseDir:  base,
		screen:   scrDeps,
		spin:     sp,
		busyText: "Checking dependencies...",
		urlIn:    url,
		startIn:  start,
		endIn:    end,
		nameIn:   name,
		dlBar:    newBar(),
		encBar:   newBar(),
	}
}

func newBar() progress.Model {
	p := progress.New(progress.WithDefaultGradient())
	p.Width = 40
	p.ShowPercentage = false
	return p
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, checkDeps())
}

// quality/format/encoder accessors -------------------------------------

func (m Model) quality() core.Quality { return core.Qualities[m.qualityIdx] }
func (m Model) format() core.FormatChoice {
	return core.Formats[m.formatIdx]
}

// encoderChoices is "Auto" followed by every encoder that passed the
// probe. Forcing one is the escape hatch for a GPU that detects but
// misbehaves on real content.
func (m Model) encoderChoices() []string {
	out := []string{"Auto"}
	if m.det != nil {
		for _, e := range m.det.Available {
			out = append(out, e.Label)
		}
	}
	return out
}

func (m Model) chosenEncoder() *core.Encoder {
	if m.det == nil || len(m.det.Available) == 0 {
		return nil
	}
	if m.encoderIdx <= 0 || m.encoderIdx > len(m.det.Available) {
		return m.det.Best()
	}
	return m.det.Available[m.encoderIdx-1]
}

func (m Model) selection() core.Selection {
	return core.NewSelection(m.quality(), m.format(), m.cfg.PreferHLS)
}

// appendLog keeps a bounded tail of tool output.
func (m *Model) appendLog(line string) {
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
}

// buildJob turns the form into a validated Job.
func (m *Model) buildJob() (*core.Job, error) {
	j := &core.Job{
		URL:  strings.TrimSpace(m.urlIn.Value()),
		Meta: m.meta,
		Sel:  m.selection(),
		Cfg:  m.cfg,
		Name: core.SanitizeName(m.nameIn.Value()),
	}

	if s := strings.TrimSpace(m.startIn.Value()); s != "" {
		v, err := core.ParseTimecode(s)
		if err != nil {
			return nil, fmt.Errorf("start time: %w", err)
		}
		j.HasStart, j.StartSec = true, v
	}
	if s := strings.TrimSpace(m.endIn.Value()); s != "" {
		v, err := core.ParseTimecode(s)
		if err != nil {
			return nil, fmt.Errorf("end time: %w", err)
		}
		j.HasEnd, j.EndSec = true, v
	}

	warn, err := j.ValidateRange()
	if err != nil {
		return nil, err
	}
	m.formWarn = warn

	// A stream YouTube has not finished processing has no seek index,
	// so a section download silently returns the wrong two seconds.
	// The only correct route is fetching the whole thing and cutting
	// locally - which costs the full download, so it is never a silent
	// fallback.
	if !j.FullVideo() && !j.Meta.Seekable() {
		j.TrimLocally = true
	}

	j.Encoder = m.chosenEncoder()
	if m.cfg.ConvertToH264 && j.Encoder == nil {
		return nil, fmt.Errorf("no working H.264 encoder was found")
	}

	if err := j.Prepare(m.baseDir); err != nil {
		return nil, fmt.Errorf("could not prepare output directory: %w", err)
	}
	return j, nil
}

// outputPathPreview shows where the file will land, before it exists.
func (m Model) outputPathPreview() string {
	name := core.SanitizeName(m.nameIn.Value())
	ext := ".mp4"
	if m.format().ExtractAudio {
		ext = ".m4a"
	} else if !m.cfg.ConvertToH264 {
		ext = ".<source>"
	}
	out := m.cfg.OutputDir
	if out == "" {
		out = filepath.Join(m.baseDir, "clips")
	}
	return filepath.Join(out, name+ext)
}
