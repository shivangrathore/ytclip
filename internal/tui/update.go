package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shivangrathore/ytclip/internal/core"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// The bar lives inside a panel and shares its row with the
		// percentage, so it is sized off the panel, not the terminal.
		barWidth := clampInt(m.contentWidth()-12, 10, 58)
		m.dlBar.Width = barWidth
		m.encBar.Width = barWidth
		// The input has to fit inside the panel it is drawn in, or it
		// scrolls past the right border.
		m.urlIn.Width = clampInt(m.contentWidth()-4, 20, 68)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case depsMsg:
		return m.handleDeps(msg)

	case detectMsg:
		if msg.err != nil {
			m.fatal = fmt.Errorf("encoder detection failed: %w", msg.err)
			m.screen = scrFatal
			return m, nil
		}
		m.det = msg.det
		m.screen = scrURL
		m.urlIn.Focus()
		return m, textinput.Blink

	case metaMsg:
		return m.handleMeta(msg)

	case installEvent:
		return m.handleInstallEvent(msg)

	case installClosedMsg:
		return m, nil

	case eventMsg:
		return m.handleEvent(msg)

	case streamClosedMsg:
		// The StageResult sentinel always arrives before the close, so
		// by here the outcome has been handled already.
		return m, nil

	case probedMsg:
		return m.handleProbed(msg)

	case finishedMsg:
		m.finalPath, m.finalSize, m.finalInfo = msg.path, msg.size, msg.info
		// The source only goes once the real output exists.
		if m.source != "" {
			os.Remove(m.source)
		}
		m.screen = scrDone
		return m, nil
	}

	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	return m, cmd
}

// ---------------------------------------------------------------- keys

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.screen == scrRun && m.cancel != nil {
			// Cancel the running stage rather than killing the UI, so
			// the partial file and its location still get reported.
			m.cancel()
			return m, nil
		}
		return m, tea.Quit
	case "q":
		if m.screen == scrDone || m.screen == scrFatal {
			return m, tea.Quit
		}
	case "esc":
		if m.screen == scrForm {
			m.screen = scrURL
			m.urlIn.Focus()
			return m, textinput.Blink
		}
	}

	switch m.screen {
	case scrDepsMissing:
		if msg.String() == "i" && len(m.plan.specs) > 0 {
			return m.beginInstall()
		}

	case scrDeps, scrFatal:
		if msg.String() == "enter" && m.screen == scrFatal {
			return m, tea.Quit
		}
	case scrURL:
		return m.keyURL(msg)
	case scrForm:
		return m.keyForm(msg)
	case scrConfirm:
		switch msg.String() {
		case "y", "Y":
			return m.proceed()
		case "n", "N", "enter":
			m.fatal = keptSourceError(m.confirm.title, m.source)
			m.screen = scrFatal
			return m, nil
		}

	case scrRun:
		if msg.String() == "l" {
			m.showLog = !m.showLog
		}
	case scrDone:
		if msg.String() == "enter" {
			return m.reset(), textinput.Blink
		}
	}
	return m, nil
}

func (m Model) keyURL(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "enter" {
		url := strings.TrimSpace(m.urlIn.Value())
		if url == "" {
			return m, nil
		}
		m.screen = scrMeta
		m.busyText = "Reading video info..."
		m.urlIn.Blur()
		return m, tea.Batch(m.spin.Tick, fetchMeta(m.tools.YtDlp, url))
	}
	var cmd tea.Cmd
	m.urlIn, cmd = m.urlIn.Update(msg)
	return m, cmd
}

func (m Model) keyForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {

	case "tab", "down":
		m.focus = (m.focus + 1) % fldCount
		return m.syncFocus(), textinput.Blink

	case "shift+tab", "up":
		m.focus = (m.focus - 1 + fldCount) % fldCount
		return m.syncFocus(), textinput.Blink

	case "left":
		switch m.focus {
		case fldQuality:
			m.qualityIdx = wrap(m.qualityIdx-1, len(core.Qualities))
		case fldFormat:
			m.formatIdx = wrap(m.formatIdx-1, len(core.Formats))
		case fldEncoder:
			m.encoderIdx = wrap(m.encoderIdx-1, len(m.encoderChoices()))
		}
		return m, nil

	case "right":
		switch m.focus {
		case fldQuality:
			m.qualityIdx = wrap(m.qualityIdx+1, len(core.Qualities))
		case fldFormat:
			m.formatIdx = wrap(m.formatIdx+1, len(core.Formats))
		case fldEncoder:
			m.encoderIdx = wrap(m.encoderIdx+1, len(m.encoderChoices()))
		}
		return m, nil

	case "enter":
		if m.focus != fldGo {
			m.focus = (m.focus + 1) % fldCount
			return m.syncFocus(), textinput.Blink
		}
		return m.start()
	}

	var cmd tea.Cmd
	switch m.focus {
	case fldStart:
		m.startIn, cmd = m.startIn.Update(msg)
	case fldEnd:
		m.endIn, cmd = m.endIn.Update(msg)
	case fldName:
		m.nameIn, cmd = m.nameIn.Update(msg)
	}
	// Clear a stale error as soon as the user edits anything.
	m.formErr = ""
	return m, cmd
}

func (m Model) syncFocus() Model {
	m.startIn.Blur()
	m.endIn.Blur()
	m.nameIn.Blur()
	switch m.focus {
	case fldStart:
		m.startIn.Focus()
	case fldEnd:
		m.endIn.Focus()
	case fldName:
		m.nameIn.Focus()
	}
	return m
}

// ------------------------------------------------------------ handlers

func (m Model) handleDeps(msg depsMsg) (tea.Model, tea.Cmd) {
	m.tools = msg.tools
	m.deps = msg.results

	var missing []core.DepStatus
	for _, d := range msg.results {
		if d.Required && !d.OK() {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		// A missing dependency is not a dead end - most of them we can
		// just fetch. Offer that before falling back to instructions.
		m.plan = planInstall(msg.results)
		m.screen = scrDepsMissing
		return m, nil
	}

	m.busyText = "Probing H.264 encoders..."
	return m, tea.Batch(m.spin.Tick, detectEncoders(m.tools.FFmpeg))
}

func (m Model) handleMeta(msg metaMsg) (tea.Model, tea.Cmd) {
	// Never fatal. Without metadata the range and seekability checks
	// switch off, and everything else still works.
	m.meta = msg.meta
	if msg.err != nil {
		m.formWarn = "could not read video info (" +
			firstLine(msg.err.Error()) + "); range checks are off"
	}
	m.screen = scrForm
	m.focus = fldQuality
	return m.syncFocus(), textinput.Blink
}

func (m Model) start() (tea.Model, tea.Cmd) {
	job, err := m.buildJob()
	if err != nil {
		m.formErr = err.Error()
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.job = job
	m.cancel = cancel
	m.events = make(chan core.Event, 512)
	m.stage = core.StageDownload
	m.screen = scrRun
	m.dlStart = time.Now()
	m.logs = nil
	m.dlProg = core.DownloadProgress{Total: -1, Speed: -1, ETA: -1, FragIndex: -1, FragCount: -1, MediaSeconds: -1}
	m.encProg = core.EncodeProgress{Duration: -1, FPS: -1, Speed: -1}

	switch {
	case job.FullVideo():
		m.stageMsg = "Downloading the entire video."
	case job.TrimLocally:
		m.stageMsg = "Downloading the FULL stream - the section is cut in stage 2."
	default:
		m.stageMsg = "Downloading ONLY the requested section."
	}

	return m, tea.Batch(
		startDownload(ctx, m.tools.YtDlp, job, m.events),
		waitForEvent(m.events, core.StageDownload),
	)
}

func (m Model) handleEvent(msg eventMsg) (tea.Model, tea.Cmd) {
	stage := m.stage

	switch e := msg.ev.(type) {
	case core.DownloadProgress:
		m.dlProg = e
	case core.EncodeProgress:
		m.encProg = e
	case core.LogLine:
		m.appendLog(e.Text)
	case core.StageResult:
		return m.handleStageResult(e)
	}

	return m, waitForEvent(m.events, stage)
}

func (m Model) handleStageResult(r core.StageResult) (tea.Model, tea.Cmd) {
	if r.Err != nil {
		m.fatal = m.stageError(r)
		m.screen = scrFatal
		return m, nil
	}

	if r.Stage == core.StageDownload {
		m.dlEnd = time.Now()
		m.stageMsg = "Checking what actually arrived..."
		return m, verifyDownload(m.tools.FFprobe, m.job)
	}

	// Encode finished.
	m.encEnd = time.Now()
	return m, probeFinal(m.tools.FFprobe, m.finalPath)
}

func (m Model) handleProbed(msg probedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.fatal = msg.err
		m.screen = scrFatal
		return m, nil
	}

	m.source = msg.source
	m.verify = msg.verify

	// A short download or a fallen-through selector means the file on
	// disk is probably not the clip that was asked for. Stop and ask
	// rather than silently spending an encode on it - but it IS the
	// user's call, and the source is kept either way.
	if msg.verify.ShortDownload {
		m.confirm = shortDownloadPrompt(m.job, msg.verify)
		m.screen = scrConfirm
		return m, nil
	}
	if msg.verify.Degraded {
		m.confirm = degradedPrompt(m.job, msg.verify)
		m.screen = scrConfirm
		return m, nil
	}

	return m.proceed()
}

// proceed runs whatever comes after a verified download: stage 2, or
// one of the two routes that skip it.
func (m Model) proceed() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	// Audio-only and "no H.264 conversion" both skip stage 2 entirely.
	m.screen = scrRun

	if m.job.Sel.Format.ExtractAudio {
		m.finalPath = m.job.FinalPath(".m4a")
		m.stageMsg = "Finishing audio file..."
		return m, finishCopy(ctx, m.tools.FFmpeg, m.tools.FFprobe, m.job, m.source, m.finalPath)
	}
	if !m.cfg.ConvertToH264 {
		m.finalPath = m.job.FinalPath(ext(m.source))
		m.stageMsg = "Finishing file..."
		return m, finishCopy(ctx, m.tools.FFmpeg, m.tools.FFprobe, m.job, m.source, m.finalPath)
	}

	m.finalPath = m.job.FinalPath(".mp4")
	m.stage = core.StageEncode
	m.encStart = time.Now()
	m.stageMsg = "Converting to H.264 with " + m.job.Encoder.Label + "."
	m.events = make(chan core.Event, 512)
	m.encProg = core.EncodeProgress{Duration: m.verify.EncodeDuration, FPS: -1, Speed: -1}

	return m, tea.Batch(
		startEncode(ctx, m.tools.FFmpeg, m.job, m.source, m.finalPath,
			m.verify.EncodeDuration, m.events),
		waitForEvent(m.events, core.StageEncode),
	)
}

// beginInstall downloads whatever is missing into our own data dir.
func (m Model) beginInstall() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.installCh = make(chan installEvent, 256)
	m.installLog = nil
	m.installErr = nil
	m.installProg = core.InstallProgress{Total: -1}
	m.screen = scrInstall

	return m, tea.Batch(
		startInstall(ctx, m.plan.specs, m.installCh),
		waitForInstall(m.installCh),
		m.spin.Tick,
	)
}

func (m Model) handleInstallEvent(ev installEvent) (tea.Model, tea.Cmd) {
	switch {
	case ev.err != nil:
		m.installErr = ev.err
		m.screen = scrDepsMissing
		return m, nil

	case ev.done:
		// Downloaded is not the same as working. Re-run the same
		// preflight that sent us here, which executes each binary.
		m.busyText = "Verifying..."
		m.screen = scrDeps
		return m, tea.Batch(m.spin.Tick, checkDeps())

	case ev.line != "":
		m.installLog = append(m.installLog, ev.line)

	default:
		m.installProg = ev.prog
	}

	return m, waitForInstall(m.installCh)
}

// reset returns to the URL screen keeping the preflight results.
func (m Model) reset() Model {
	n := New(m.cfg)
	n.width, n.height = m.width, m.height
	n.tools, n.deps, n.det = m.tools, m.deps, m.det
	n.screen = scrURL
	n.urlIn.SetValue(m.urlIn.Value())
	n.urlIn.Focus()
	n.dlBar.Width, n.encBar.Width = m.dlBar.Width, m.encBar.Width
	return n
}

// ------------------------------------------------------------- helpers

func wrap(i, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i % n) + n) % n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ext(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i:]
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
