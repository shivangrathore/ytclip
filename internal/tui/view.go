package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/shivangrathore/ytclip/internal/core"
)

// contentWidth is the width every panel is drawn at. Capped so the
// layout does not sprawl across a maximised terminal.
func (m Model) contentWidth() int {
	w := m.width - 4
	return clampInt(w, 40, 72)
}

func (m Model) View() string {
	var body string
	switch m.screen {
	case scrDeps, scrMeta:
		body = m.viewBusy()
	case scrDepsMissing:
		body = m.viewDepsMissing()
	case scrInstall:
		body = m.viewInstall()
	case scrURL:
		body = m.viewURL()
	case scrForm:
		body = m.viewForm()
	case scrConfirm:
		body = m.viewConfirm()
	case scrRun:
		body = m.viewRun()
	case scrDone:
		body = m.viewDone()
	case scrFatal:
		body = m.viewFatal()
	}
	return "\n" + indent(m.header()) + "\n\n" + body + "\n"
}

func (m Model) header() string {
	return stTitle.Render("ytclip") + stDim.Render("  ·  clip a section out of a YouTube video")
}

// indent shifts a block two columns right, matching the panel gutter.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func (m Model) help(keys ...string) string {
	return "\n" + indent(stHelp.Render(strings.Join(keys, stDim.Render("  ·  ")))) + "\n"
}

// -------------------------------------------------------------- busy

func (m Model) viewBusy() string {
	w := m.contentWidth()
	var rows []string
	rows = append(rows, row(m.spin.View()+" "+stValue.Render(m.busyText)))

	if m.screen == scrDeps && len(m.deps) > 0 {
		rows = append(rows, row(""))
		for _, d := range m.deps {
			rows = append(rows, row(m.depLine(d)))
		}
	}
	return indent(panel("Starting up", w, rows...))
}

func (m Model) depLine(d core.DepStatus) string {
	switch {
	case d.OK():
		return stOK.Render("✓ ") + pad(stValue.Render(d.Name), 10) +
			stDim.Render(truncate(d.Version, m.contentWidth()-16))
	case d.Path == "" && !d.Required:
		return stWarn.Render("· ") + pad(stDim.Render(d.Name), 10) +
			stDim.Render("optional — the quality check is skipped")
	case d.Err != nil:
		return stErr.Render("✗ ") + pad(stValue.Render(d.Name), 10) +
			stDim.Render("found, but will not run")
	default:
		return stErr.Render("✗ ") + pad(stValue.Render(d.Name), 10) +
			stDim.Render("not found")
	}
}

// ------------------------------------------------------------ missing

func (m Model) viewDepsMissing() string {
	w := m.contentWidth()
	var out strings.Builder

	rows := make([]string, 0, len(m.deps))
	for _, d := range m.deps {
		rows = append(rows, row(m.depLine(d)))
	}
	out.WriteString(indent(panel(stErr.Render("Missing dependencies"), w, rows...)) + "\n")

	if m.installErr != nil {
		out.WriteString("\n" + indent(wrapText(
			stErr.Render("install failed:")+" "+m.installErr.Error(), w, "")))
	}

	// ---- the automatic route
	if len(m.plan.specs) > 0 {
		rows = rows[:0]
		for _, spec := range m.plan.specs {
			rows = append(rows, row(stValue.Render(spec.Tool)))
			rows = append(rows, row(stDim.Render("  from "+truncate(spec.Source, w-11))))
		}
		dir, _ := core.InstallDir()
		rows = append(rows,
			row(""),
			row(stDim.Render("into "+truncate(dir, w-8))),
			row(stDim.Render("checksum-verified · no admin rights · not added to PATH")),
		)
		out.WriteString("\n" + indent(panel("Press  i  to download these", w, rows...)) + "\n")
	}

	// ---- the manual route, for anything we cannot fetch
	for _, un := range m.plan.blocked {
		rows = []string{
			row(stDim.Render(un.Why)),
			row(""),
		}
		for _, h := range un.Hint {
			rows = append(rows, row(stValue.Render(h)))
		}
		out.WriteString("\n" + indent(panel(
			stWarn.Render(un.Tool+" — install it yourself"), w, rows...)) + "\n")
	}

	if len(m.plan.specs) == 0 && len(m.plan.blocked) == 0 {
		rows = []string{row(stDim.Render("Install them, then open a new terminal so PATH refreshes."))}
		out.WriteString("\n" + indent(panel("What to do", w, rows...)) + "\n")
	}

	out.WriteString("\n" + indent(stDim.Render(
		"Binaries dropped next to ytclip are picked up too.")) + "\n")

	if len(m.plan.specs) > 0 {
		return out.String() + m.help("i  download them", "ctrl+c  quit")
	}
	return out.String() + m.help("ctrl+c  quit")
}

// ------------------------------------------------------------ install

func (m Model) viewInstall() string {
	w := m.contentWidth()
	p := m.installProg

	rows := make([]string, 0, 8)
	for _, l := range m.installLog {
		rows = append(rows, row(stDim.Render(truncate(l, w-3))))
	}
	if len(rows) > 0 {
		rows = append(rows, row(""))
	}

	switch {
	case p.Fraction() >= 0:
		rows = append(rows,
			row(m.dlBar.ViewAs(p.Fraction())+"  "+
				stValue.Render(fmt.Sprintf("%.1f%%", p.Fraction()*100))),
			spread(stDim.Render(p.Tool+"  "+p.Phase),
				stDim.Render(core.FormatBytes(p.Done)+" / "+core.FormatBytes(p.Total)), w-3),
		)
	case p.Phase != "":
		rows = append(rows, row(m.spin.View()+" "+stDim.Render(p.Tool+"  "+p.Phase)))
	default:
		rows = append(rows, row(m.spin.View()+" "+stDim.Render("starting")))
	}

	return indent(panel("Installing dependencies", w, rows...)) +
		m.help("ctrl+c  cancel")
}

// --------------------------------------------------------------- url

func (m Model) viewURL() string {
	w := m.contentWidth()

	rows := []string{
		row(stLabel.Render("Paste a YouTube URL")),
		row(""),
		row(m.urlIn.View()),
	}
	out := indent(panel("Source", w, rows...))

	if m.det != nil {
		if e := m.det.Best(); e != nil {
			out += "\n" + indent(stDim.Render("encoder  ")+stAcc.Render(e.Label))
		}
	}
	return out + m.help("enter  continue", "ctrl+c  quit")
}

// -------------------------------------------------------------- form

func (m Model) viewForm() string {
	w := m.contentWidth()
	inner := w - 2
	var out strings.Builder

	// ---- source panel
	if m.meta.Title != "" || m.meta.HasDuration {
		var rows []string
		if m.meta.Title != "" {
			rows = append(rows, row(stValue.Render(truncate(m.meta.Title, inner-2))))
		}
		if m.meta.HasDuration {
			kind := stOK.Render("VOD")
			if !m.meta.Seekable() {
				kind = stWarn.Render(m.meta.LiveStatusText())
			}
			rows = append(rows, row(stDim.Render(core.FormatDurationHMS(m.meta.Duration))+
				stDim.Render("   ·   ")+kind))
		}
		out.WriteString(indent(panel("Source", w, rows...)) + "\n\n")
	}

	// ---- clip panel
	sel := m.selection()
	qName := m.quality().Name
	if sel.Capped() {
		qName += stWarn.Render(fmt.Sprintf("  → %dp", sel.EffectiveHeight))
	}

	rows := []string{
		m.selectRow(fldQuality, "Quality", qName, m.qualityIdx, len(core.Qualities), inner),
		m.selectRow(fldFormat, "Format", m.format().Name, m.formatIdx, len(core.Formats), inner),
		row(strings.Repeat(" ", 10) + stDim.Render(m.format().Blurb)),
	}

	encChoices := m.encoderChoices()
	encName := encChoices[wrap(m.encoderIdx, len(encChoices))]
	if m.encoderIdx == 0 {
		if e := m.chosenEncoder(); e != nil {
			encName = "Auto " + stDim.Render("— "+e.Label)
		}
	}
	rows = append(rows,
		m.selectRow(fldEncoder, "Encoder", encName, m.encoderIdx, len(encChoices), inner),
		row(""),
		m.inputRow(fldStart, "Start", m.startIn.View(), m.previewStart()),
		m.inputRow(fldEnd, "End", m.endIn.View(), m.previewDuration()),
		row(strings.Repeat(" ", 10)+stDim.Render("1:20:00  ·  1h 20m  ·  20 minutes  ·  90")),
		row(""),
		m.inputRow(fldName, "Name", m.nameIn.View(), ""),
	)
	out.WriteString(indent(panel("Clip", w, rows...)) + "\n")

	// ---- output path
	out.WriteString("\n" + indent(stDim.Render("→ ")+
		stAcc.Render(truncate(m.outputPathPreview(), w-2))) + "\n")

	// ---- notices
	for _, n := range m.notices() {
		out.WriteString("\n" + indent(wrapText(n, w, "")))
	}

	// ---- go button
	label := "  Start clip  "
	btn := stDim.Render("[") + stDim.Render(label) + stDim.Render("]")
	if m.focus == fldGo {
		btn = stFocus.Render("▶") + stFocus.Render(label)
	}
	out.WriteString("\n" + indent(btn) + "\n")

	return out.String() + m.help(
		"tab / ↑↓  move", "←→  change", "enter  next", "esc  back")
}

// notices collects every warning the form has to show, in the order
// they matter.
func (m Model) notices() []string {
	var out []string

	if w := m.format().Warn; w != "" {
		out = append(out, stWarn.Render("note:")+" "+stDim.Render(w))
	}
	if !m.meta.Seekable() && (m.startIn.Value() != "" || m.endIn.Value() != "") {
		out = append(out, stWarn.Render("note:")+" "+stDim.Render(
			"this stream is "+m.meta.LiveStatusText()+", so it has no seek index. "+
				"The FULL stream will be downloaded and cut locally — correct, "+
				"but it downloads everything."))
	}
	if m.formWarn != "" {
		out = append(out, stWarn.Render("warn:")+" "+stDim.Render(m.formWarn))
	}
	if m.formErr != "" {
		out = append(out, stErr.Render("error:")+" "+stErr.Render(m.formErr))
	}
	return out
}

// selectRow renders a ‹ value › cycler with its position on the right.
func (m Model) selectRow(field int, label, value string, idx, n, inner int) string {
	pos := stDim.Render(fmt.Sprintf("%d/%d", idx+1, n))

	left := stLabel.Render(pad(label, 8))
	if m.focus == field {
		left = stFocus.Render(pad(label, 8))
		value = stAcc.Render("‹ ") + stValue.Render(value) + stAcc.Render(" ›")
	} else {
		value = "  " + stValue.Render(value) + "  "
	}
	return spread(left+" "+value, pos, inner-1)
}

// inputRow renders a text field, with an optional right-hand hint.
func (m Model) inputRow(field int, label, view, hint string) string {
	left := stLabel.Render(pad(label, 8))
	if m.focus == field {
		left = stFocus.Render(pad(label, 8))
	}
	body := left + " " + view
	if hint == "" {
		return row(body)
	}
	return spread(body, stDim.Render(hint), m.contentWidth()-3)
}

// previewStart echoes back what a typed start time was understood to
// mean, so "1 hr 20 minute" visibly becomes 1:20:00 before anything runs.
func (m Model) previewStart() string {
	s := strings.TrimSpace(m.startIn.Value())
	if s == "" {
		return ""
	}
	v, err := core.ParseTimecode(s)
	if err != nil {
		return stErr.Render("?")
	}
	// No point echoing a value back in the shape it was already typed.
	if core.FormatShortClock(v) == s {
		return ""
	}
	return "= " + core.FormatShortClock(v)
}

// previewDuration shows the resulting clip length as the range is typed.
func (m Model) previewDuration() string {
	s := strings.TrimSpace(m.startIn.Value())
	e := strings.TrimSpace(m.endIn.Value())

	if s == "" && e == "" {
		return "whole video"
	}

	startSec := 0.0
	if s != "" {
		v, err := core.ParseTimecode(s)
		if err != nil {
			return stErr.Render("bad start")
		}
		startSec = v
	}
	if e == "" {
		return "→ end of video"
	}
	endSec, err := core.ParseTimecode(e)
	if err != nil {
		return stErr.Render("bad end")
	}
	if endSec <= startSec {
		return stErr.Render("end before start")
	}

	out := ""
	if core.FormatShortClock(endSec) != e {
		out = core.FormatShortClock(endSec) + "   "
	}
	return out + "= " + core.FormatDurationHMS(endSec-startSec)
}

// ----------------------------------------------------------- confirm

func (m Model) viewConfirm() string {
	w := m.contentWidth()
	rows := []string{}
	for _, l := range m.confirm.body {
		rows = append(rows, row(stDim.Render(truncate(l, w-3))))
	}
	rows = append(rows, row(""), row(stValue.Render(m.confirm.ask)+" "+stDim.Render("(y/N)")))

	return indent(panel(stWarn.Render(m.confirm.title), w, rows...)) +
		m.help("y  encode anyway", "n  stop, keep the source")
}

// --------------------------------------------------------------- run

func (m Model) viewRun() string {
	w := m.contentWidth()
	var out strings.Builder

	encoding := m.cfg.ConvertToH264 && !m.format().ExtractAudio
	total := 1
	if encoding {
		total = 2
	}

	// ---- stage 1
	dlDone := m.stage != core.StageDownload
	frac := m.dlProg.Fraction()
	if dlDone && frac < 0 {
		// A section download reports no total, so it can finish having
		// never produced a fraction. It is done; show it done.
		frac = 1
	}
	out.WriteString(indent(panel(
		fmt.Sprintf("Stage 1 of %d  ·  Download", total), w,
		m.barRows(m.dlBar, frac, m.dlDetail(), !dlDone, true, m.dlStart, m.dlEnd, w)...,
	)) + "\n")

	// ---- stage 2
	if encoding {
		active := m.stage == core.StageEncode
		title := fmt.Sprintf("Stage 2 of %d  ·  Encode", total)
		if m.job != nil && m.job.Encoder != nil {
			title += "  ·  " + m.job.Encoder.Label
		}
		out.WriteString("\n" + indent(panel(title, w,
			m.barRows(m.encBar, m.encProg.Fraction(), m.encDetail(),
				active, active, m.encStart, m.encEnd, w)...,
		)) + "\n")
	}

	if m.stageMsg != "" {
		out.WriteString("\n" + indent(stDim.Render(truncate(m.stageMsg, w))) + "\n")
	}

	// ---- log
	if m.showLog {
		n := clampInt(m.height-22, 3, 14)
		tail := m.logs
		if len(tail) > n {
			tail = tail[len(tail)-n:]
		}
		rows := make([]string, 0, len(tail))
		for _, l := range tail {
			rows = append(rows, row(stDim.Render(truncate(l, w-3))))
		}
		if len(rows) == 0 {
			rows = append(rows, row(stDim.Render("(nothing yet)")))
		}
		out.WriteString("\n" + indent(panel("Log", w, rows...)) + "\n")
	}

	logKey := "l  show log"
	if m.showLog {
		logKey = "l  hide log"
	}
	return out.String() + m.help(logKey, "ctrl+c  cancel")
}

// barRows renders the progress bar and its detail line. An
// indeterminate stage gets a spinner, never a bar that cannot move.
func (m Model) barRows(bar interface{ ViewAs(float64) string },
	frac float64, detail string, active, started bool,
	start, end time.Time, w int) []string {

	if !started {
		return []string{row(stDim.Render("waiting"))}
	}

	var head string
	if frac < 0 {
		head = m.spin.View() + " " + stDim.Render("working")
	} else {
		head = bar.ViewAs(frac) + "  " + stValue.Render(fmt.Sprintf("%.1f%%", frac*100))
	}

	rows := []string{row(head)}

	elapsed := ""
	if !start.IsZero() {
		until := time.Now()
		if !end.IsZero() {
			until = end
		}
		elapsed = "elapsed " + core.FormatClock(until.Sub(start).Seconds())
	}

	if detail != "" || elapsed != "" {
		rows = append(rows, spread(stDim.Render(detail), stDim.Render(elapsed), w-3))
	}
	return rows
}

func (m Model) dlDetail() string {
	p := m.dlProg
	var parts []string

	if p.Speed > 0 {
		parts = append(parts, core.FormatRate(p.Speed))
	} else if p.SpeedX > 0 {
		parts = append(parts, fmt.Sprintf("%.1fx", p.SpeedX))
	}
	if p.FragCount > 0 {
		parts = append(parts, fmt.Sprintf("frag %d/%d", p.FragIndex, p.FragCount))
	}
	if p.Downloaded > 0 {
		parts = append(parts, core.FormatBytes(p.Downloaded))
	}
	if eta := p.RemainingETA(); eta >= 0 {
		parts = append(parts, "ETA "+core.FormatClock(eta))
	}
	return strings.Join(parts, "   ")
}

func (m Model) encDetail() string {
	p := m.encProg
	if p.OutSeconds == 0 && p.Bytes == 0 {
		return ""
	}
	var parts []string
	if p.Duration > 0 {
		parts = append(parts, core.FormatClock(p.OutSeconds)+" / "+core.FormatClock(p.Duration))
	}
	if p.FPS > 0 {
		parts = append(parts, fmt.Sprintf("%.0f fps", p.FPS))
	}
	if p.Speed > 0 {
		parts = append(parts, fmt.Sprintf("%.1fx", p.Speed))
	}
	if eta := p.ETA(); eta >= 0 {
		parts = append(parts, "ETA "+core.FormatClock(eta))
	}
	if p.Bytes > 0 {
		parts = append(parts, core.FormatBytes(p.Bytes))
	}
	return strings.Join(parts, "   ")
}

// -------------------------------------------------------------- done

func (m Model) viewDone() string {
	w := m.contentWidth()
	var rows []string

	if m.job != nil {
		if m.job.FullVideo() {
			rows = append(rows, kv("Clip", stValue.Render("full video"), 9))
		} else {
			rows = append(rows, kv("Clip", stValue.Render(
				shortClock(m.job.StartSec, m.job.HasStart, "start")+
					stDim.Render("  →  ")+
					shortClock(m.job.EndSec, m.job.HasEnd, "end")), 9))
		}
	}
	// Measured off the finished file, never the request. Reporting the
	// ask back is how a 2 second clip announced itself as 960 seconds.
	if m.finalInfo.HasDuration {
		rows = append(rows, kv("Duration",
			stValue.Render(core.FormatDurationHMS(m.finalInfo.Duration)), 9))
	}
	if m.finalInfo.HasVideo {
		rows = append(rows, kv("Video", stValue.Render(fmt.Sprintf("%d×%d @ %g fps",
			m.finalInfo.Width, m.finalInfo.Height, roundFPS(m.finalInfo.FPS))), 9))
	}
	if m.job != nil && m.job.Encoder != nil && m.cfg.ConvertToH264 && !m.format().ExtractAudio {
		rows = append(rows, kv("Encoder", stValue.Render(m.job.Encoder.Label), 9))
	}
	rows = append(rows, kv("Size", stValue.Render(core.FormatBytes(m.finalSize)), 9))

	if t := m.timings(); t != "" {
		rows = append(rows, kv("Time", stDim.Render(t), 9))
	}

	out := indent(panel(stOK.Render("Done"), w, rows...))
	out += "\n\n" + indent(stDim.Render("→ ")+stAcc.Render(truncate(m.finalPath, w-2))) + "\n"
	return out + m.help("enter  another clip", "q  quit")
}

func (m Model) timings() string {
	var parts []string
	if !m.dlStart.IsZero() && !m.dlEnd.IsZero() {
		parts = append(parts, "download "+core.FormatClock(m.dlEnd.Sub(m.dlStart).Seconds()))
	}
	if !m.encStart.IsZero() && !m.encEnd.IsZero() {
		parts = append(parts, "encode "+core.FormatClock(m.encEnd.Sub(m.encStart).Seconds()))
	}
	return strings.Join(parts, "  ·  ")
}

// ------------------------------------------------------------- fatal

func (m Model) viewFatal() string {
	w := m.contentWidth()

	headline := "Error"
	var detail []string
	if fe, ok := m.fatal.(*friendlyError); ok {
		headline, detail = fe.Headline(), fe.Detail()
	} else if m.fatal != nil {
		detail = []string{m.fatal.Error()}
	}

	rows := make([]string, 0, len(detail))
	for _, l := range detail {
		if l == "" {
			rows = append(rows, row(""))
			continue
		}
		rows = append(rows, row(stDim.Render(truncate(l, w-3))))
	}
	if len(rows) == 0 {
		rows = append(rows, row(stDim.Render("no further detail")))
	}

	return indent(panel(stErr.Render(headline), w, rows...)) + m.help("q  quit")
}

// ------------------------------------------------------------ helpers

// shortClock renders a boundary the way the user thinks of it.
func shortClock(sec float64, has bool, blank string) string {
	if !has {
		return blank
	}
	return core.FormatDurationHMS(sec)
}

func roundFPS(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func truncate(s string, n int) string {
	if n < 4 {
		n = 4
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// wrapText hard-wraps at word boundaries with a fixed indent.
func wrapText(s string, width int, indent string) string {
	if width < 20 {
		width = 20
	}
	words := strings.Fields(s)
	var lines []string
	cur := indent
	for _, w := range words {
		if lipgloss.Width(cur)+lipgloss.Width(w)+1 > width && strings.TrimSpace(cur) != "" {
			lines = append(lines, cur)
			cur = indent + "      "
		}
		if strings.TrimSpace(cur) == "" {
			cur += w
		} else {
			cur += " " + w
		}
	}
	if strings.TrimSpace(cur) != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n") + "\n"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
