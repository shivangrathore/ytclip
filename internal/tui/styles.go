package tui

import "github.com/charmbracelet/lipgloss"

// Adaptive colours so the same build is readable on a light or a dark
// terminal. The .ps1 hardcoded Cyan/Yellow/Green, which vanishes on a
// light background.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#0b6bcb", Dark: "#4dabf7"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
	colErr    = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
	colDim    = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"}
	colText   = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6edf3"}
)

var (
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stLabel = lipgloss.NewStyle().Foreground(colDim)
	stValue = lipgloss.NewStyle().Foreground(colText)
	stDim   = lipgloss.NewStyle().Foreground(colDim)
	stOK    = lipgloss.NewStyle().Foreground(colOK)
	stWarn  = lipgloss.NewStyle().Foreground(colWarn)
	stErr   = lipgloss.NewStyle().Foreground(colErr)
	stAcc   = lipgloss.NewStyle().Foreground(colAccent)

	stBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colDim).
		Padding(0, 1)

	// stFocus marks the active form row.
	stFocus = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	stHelp = lipgloss.NewStyle().Foreground(colDim)
)

// hr draws a full-width rule.
func hr(width int) string {
	if width < 1 {
		width = 1
	}
	out := make([]rune, width)
	for i := range out {
		out[i] = '─'
	}
	return stDim.Render(string(out))
}
