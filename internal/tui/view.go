package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/service"
)

// Layout constants. The panes are sized from the terminal on every window size
// message, so nothing here is a magic number that has to match the renderer.
const (
	headerLines  = 1
	footerLines  = 1
	detailLines  = 4
	minLeftPane  = 20
	maxLeftPane  = 34
	minRightPane = 20
)

// layout sizes the panes and the log viewport for the current terminal.
func (m *Model) layout() {
	if !m.ready {
		return
	}
	body := max(m.height-headerLines-footerLines, 3)

	leftWidth := clamp(m.width/3, minLeftPane, maxLeftPane)
	rightWidth := max(m.width-leftWidth-2, minRightPane)

	m.viewport.SetWidth(max(rightWidth-2, 8))
	m.viewport.SetHeight(max(body-detailLines, 1))
	m.viewport.SetContentLines(m.renderLogLines())
}

// render draws the whole dashboard. It is a pure function of the model, which
// is what makes the view testable against a golden file.
func (m *Model) render() string {
	if !m.ready {
		// Nothing is known yet, but the dashboard still owns the screen: the
		// first frame is rendered inside the alternate screen rather than
		// leaving the shell visible behind it.
		return m.styles.Muted.Render("dashdev: reading the service system")
	}

	body := max(m.height-headerLines-footerLines, 3)
	left := m.renderServiceList(body)
	right := m.renderDetail()

	content := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	return strings.Join([]string{m.renderHeader(), content, m.renderFooter()}, "\n")
}

func (m *Model) renderHeader() string {
	title := m.styles.Title.Render("dashdev")
	// The header reports the states a reader actually asks about, in the order
	// they matter, and stays quiet about states nobody is in.
	counts := make([]string, 0, len(m.order))
	for _, state := range []service.State{service.StateRunning, service.StateStarting, service.StateCrashed} {
		count := 0
		for _, status := range m.statuses {
			if status.State == state {
				count++
			}
		}
		if count > 0 {
			counts = append(counts, m.styles.stateStyle(state).Render(strconv.Itoa(count)+" "+state.String()))
		}
	}
	summary := m.styles.Muted.Render(fmt.Sprintf("%d services", len(m.order)))
	if len(counts) > 0 {
		summary += m.styles.Muted.Render(" · ") + strings.Join(counts, m.styles.Muted.Render(" · "))
	}
	if m.dropped > 0 {
		summary += m.styles.Muted.Render(" · ") +
			m.styles.Waiting.Render(strconv.FormatUint(m.dropped, 10)+" events dropped")
	}
	if m.closed {
		summary += m.styles.Muted.Render(" · ") + m.styles.Muted.Render("stream closed")
	}
	return title + "  " + summary
}

// renderServiceList draws the service pane, padded to exactly the body height
// so that the panes line up.
func (m *Model) renderServiceList(height int) string {
	width := clamp(m.width/3, minLeftPane, maxLeftPane)
	lines := make([]string, 0, height)

	if len(m.order) == 0 {
		lines = append(lines, m.styles.Muted.Render("waiting for services"))
	}
	for i, name := range m.order {
		if len(lines) >= height {
			lines[len(lines)-1] = m.styles.Muted.Render("…")
			break
		}
		status := m.statuses[name]
		cursor := "  "
		nameStyle := m.styles.stateStyle(status.State)
		if i == m.selected {
			cursor = m.styles.Selected.Render("> ")
			nameStyle = m.styles.Selected
		}
		label := nameStyle.Render(padRight(name, width-12))
		state := m.styles.stateStyle(status.State).Render(status.State.String())
		lines = append(lines, cursor+label+" "+state)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// renderDetail draws the selected service's summary and its output.
func (m *Model) renderDetail() string {
	status, known := m.selectedStatus()
	if !known {
		return m.styles.Muted.Render("select a service")
	}

	now := m.clock.Now()
	headline := m.styles.Selected.Render(status.Name) + "  " +
		m.styles.stateStyle(status.State).Render(status.State.String())

	// The summary already carries the uptime of a running service, so it is not
	// repeated here.
	facts := []string{status.Summary(now)}
	if status.Restarts > 0 {
		facts = append(facts, fmt.Sprintf("restarts %d", status.Restarts))
	}
	if status.Health.State != service.HealthUnknown {
		facts = append(facts, m.styles.healthStyle(status.Health.State).Render("health "+status.Health.State.String()))
	}
	if !status.NextRestartAt.IsZero() {
		facts = append(facts, m.styles.Waiting.Render("next attempt in "+
			status.NextRestartAt.Sub(now).Round(time.Second).String()))
	}
	second := m.styles.Muted.Render(strings.Join(facts, " · "))

	third := ""
	if status.Reason != "" {
		third = m.styles.Muted.Render("reason: " + status.Reason)
	}

	fourth := m.styles.Muted.Render(m.renderStats(status))
	factLines := []string{headline, second, third, fourth}
	if m.viewport.Height() <= 0 {
		return strings.Join(factLines, "\n")
	}
	return strings.Join(append(factLines, m.viewport.View()), "\n")
}

func (m *Model) renderStats(status service.Status) string {
	parts := make([]string, 0, 3)
	if status.PID > 0 {
		parts = append(parts, "pid "+strconv.Itoa(status.PID))
	}
	if status.Stats.SampledAt.IsZero() {
		return strings.Join(parts, " · ")
	}
	if status.Stats.CPUPercent > 0 {
		parts = append(parts, fmt.Sprintf("cpu %.1f%%", status.Stats.CPUPercent))
	}
	parts = append(parts, "memory "+humanBytes(status.Stats.MemoryBytes))
	return strings.Join(parts, " · ")
}

// renderLogLines formats the selected service's lines, marking the stream each
// one came from and any line that was cut short.
func (m *Model) renderLogLines() []string {
	if len(m.logLines) == 0 {
		if name := m.selectedName(); name != "" {
			return []string{m.styles.Muted.Render("no output yet")}
		}
		return nil
	}
	rendered := make([]string, 0, len(m.logLines))
	width := max(m.viewport.Width(), 20)
	for _, line := range m.logLines {
		tag := "out"
		style := m.styles.Muted
		if line.Stream == logs.StreamStderr {
			tag = "err"
			style = m.styles.Stderr
		}

		// The budget is whatever is left after the prefix, measured rather than
		// assumed: a hard-coded allowance is how a line ends up one column too
		// wide and wraps the pane.
		timestamp := line.Time.Format("15:04:05")
		prefix := timestamp + " " + tag + " "
		available := max(width-lipgloss.Width(prefix), 1)

		message := truncate(line.Message, available)
		if line.Truncated && !strings.HasSuffix(message, "…") {
			// A line the pipeline cut short is marked. The marker comes out of
			// the same budget rather than being an extra character, and it is not
			// added twice when truncate has already had to cut the line itself.
			message = truncate(line.Message, max(available-1, 1)) + "…"
		}

		rendered = append(rendered, m.styles.Muted.Render(timestamp)+" "+style.Render(tag)+" "+style.Render(message))
	}
	return rendered
}

func (m *Model) renderFooter() string {
	if m.notice != "" {
		return m.styles.Waiting.Render(m.notice)
	}
	if m.err != nil {
		return m.styles.Failed.Render("event stream ended: " + m.err.Error())
	}
	if m.closed {
		return m.styles.Muted.Render("event stream ended · q quit")
	}
	return m.styles.Footer.Render(footerHelp)
}

// padRight pads a string to width using the style-free length, so that colour
// codes do not disturb the alignment of the panes.
func padRight(text string, width int) string {
	if width <= 0 {
		return text
	}
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(" ", padding)
}

// truncate shortens text so that it occupies at most width columns, ending it
// with an ellipsis when anything was removed. One very long log line would
// otherwise wrap the pane into a mess.
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:max(width-1, 0)]) + "…"
}

func humanBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatUint(bytes, 10) + " B"
	}
	value := float64(bytes)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

// clamp bounds value to the range low..high.
func clamp(value, low, high int) int { return min(max(value, low), high) }
