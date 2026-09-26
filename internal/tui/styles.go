package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/HalxDocs/dashdev/internal/service"
)

// Colours are declared once here so that the meaning of a colour is decided in
// one place rather than at each call site.
var (
	colourAccent  color.Color = lipgloss.Color("#7D56F4")
	colourMuted   color.Color = lipgloss.Color("#6C6C6C")
	colourRunning color.Color = lipgloss.Color("#04B575")
	colourWaiting color.Color = lipgloss.Color("#D7A013")
	colourFailed  color.Color = lipgloss.Color("#E05252")
	colourStderr  color.Color = lipgloss.Color("#E0A0A0")
)

// Styles holds every visual decision the dashboard makes.
//
// It is built once and copied into the model. Nothing in the render path is
// configurable at runtime, which keeps the view a pure function of the model,
// and it makes golden-file tests possible: with Coloured false the same layout
// is produced without escape codes.
type Styles struct {
	Coloured bool

	Title    lipgloss.Style
	Muted    lipgloss.Style
	Selected lipgloss.Style
	Panel    lipgloss.Style
	Footer   lipgloss.Style

	Running lipgloss.Style
	Waiting lipgloss.Style
	Failed  lipgloss.Style
	Stderr  lipgloss.Style
}

// NewStyles returns the dashboard's style set. Passing false for coloured
// yields plain text with the same layout, which is what the tests compare
// against and what a monochrome terminal needs.
func NewStyles(coloured bool) Styles {
	styles := Styles{Coloured: coloured}
	if !coloured {
		return styles
	}
	styles.Title = lipgloss.NewStyle().Bold(true).Foreground(colourAccent)
	styles.Muted = lipgloss.NewStyle().Foreground(colourMuted)
	styles.Selected = lipgloss.NewStyle().Bold(true).Foreground(colourAccent)
	styles.Panel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colourMuted)
	styles.Footer = lipgloss.NewStyle().Foreground(colourMuted)
	styles.Running = lipgloss.NewStyle().Foreground(colourRunning)
	styles.Waiting = lipgloss.NewStyle().Foreground(colourWaiting)
	styles.Failed = lipgloss.NewStyle().Foreground(colourFailed)
	styles.Stderr = lipgloss.NewStyle().Foreground(colourStderr)
	return styles
}

// stateStyle returns the style that expresses a lifecycle state.
func (s Styles) stateStyle(state service.State) lipgloss.Style {
	switch state {
	case service.StateRunning:
		return s.Running
	case service.StateStarting:
		return s.Waiting
	case service.StateExited:
		return s.Muted
	case service.StateStopped:
		return s.Muted
	default:
		return s.Failed
	}
}

// healthStyle returns the style that expresses a health state.
func (s Styles) healthStyle(state service.HealthState) lipgloss.Style {
	switch state {
	case service.HealthHealthy:
		return s.Running
	case service.HealthUnhealthy:
		return s.Failed
	default:
		return s.Muted
	}
}
