package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Panels are drawn by hand rather than with lipgloss's border helpers,
// because the title sits inside the top rule and the rows carry ANSI
// that a naive pad would miscount.

const (
	tl, tr, bl, br = "╭", "╮", "╰", "╯"
	hbar, vbar     = "─", "│"
)

// panel renders a titled box of exactly width columns.
func panel(title string, width int, rows ...string) string {
	if width < 20 {
		width = 20
	}
	inner := width - 2

	var b strings.Builder
	b.WriteString(panelTop(title, width) + "\n")
	for _, r := range rows {
		b.WriteString(stDim.Render(vbar) + pad(r, inner) + stDim.Render(vbar) + "\n")
	}
	b.WriteString(stDim.Render(bl + strings.Repeat(hbar, inner) + br))
	return b.String()
}

func panelTop(title string, width int) string {
	inner := width - 2
	if title == "" {
		return stDim.Render(tl + strings.Repeat(hbar, inner) + tr)
	}
	label := " " + title + " "
	fill := inner - 1 - lipgloss.Width(label)
	if fill < 0 {
		fill = 0
	}
	return stDim.Render(tl+hbar) + stLabel.Render(label) +
		stDim.Render(strings.Repeat(hbar, fill)+tr)
}

// pad right-fills to n display columns, ANSI-aware.
func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// row builds a padded content line for a panel.
func row(s string) string { return " " + s }

// kv renders an aligned label/value pair inside a panel.
func kv(label, value string, labelWidth int) string {
	return " " + stLabel.Render(pad(label, labelWidth)) + " " + value
}

// spread puts left flush-left and right flush-right within n columns.
func spread(left, right string, n int) string {
	gap := n - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}
