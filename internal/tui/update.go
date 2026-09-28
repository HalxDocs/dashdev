package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/service"
)

// eventBatch carries a group of events from the stream into Update. Delivering
// them in batches keeps a chatty service from making the dashboard re-render
// once per log line.
type eventBatch struct {
	events []events.Event
}

// streamClosed reports that the event stream ended, which means dashdev is
// shutting down.
type streamClosed struct {
	err error
}

// actionDone carries the outcome of a request the user made.
type actionDone struct {
	text string
}

// noticeExpired clears a footer notice.
type noticeExpired struct{}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case eventBatch:
		m.apply(msg.events)
		// A subscriber that fell behind has lost events, and saying so on the
		// header is better than quietly showing an incomplete system.
		m.dropped = m.subscription.Dropped()
		m.layout()
		return m, m.waitForEvents()

	case streamClosed:
		m.closed = true
		if msg.err != nil && !errors.Is(msg.err, events.ErrClosed) && !errors.Is(msg.err, context.Canceled) {
			m.err = msg.err
		}
		return m, nil

	case actionDone:
		m.setNotice(msg.text)
		return m, clearNoticeAfter(noticeDuration)

	case noticeExpired:
		m.setNotice("")
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey applies a key press. Every branch that changes something the
// manager owns goes through a command, so the dashboard never blocks on a
// process.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit, keyQuitCtrlC:
		return m, tea.Quit

	case keyUp, keyUpVim:
		m.selectOffset(-1)
	case keyDown, keyDownVim:
		m.selectOffset(1)

	case keyPageUp:
		m.viewport.PageUp()
	case keyPageDown:
		m.viewport.PageDown()
	case keyScrollUp:
		m.viewport.ScrollUp(pageStep(m.viewport.Height()))
	case keyScrollDown:
		m.viewport.ScrollDown(pageStep(m.viewport.Height()))
	case keyTop:
		m.viewport.GotoTop()
	case keyBottom:
		m.viewport.GotoBottom()

	case keyRestart:
		return m, m.act("restart", m.controller.Restart)
	case keyStop:
		return m, m.act("stop", m.controller.Stop)
	case keyStart:
		return m, m.act("start", m.controller.Start)
	}
	return m, nil
}

// selectOffset moves the service selection and rebuilds the log pane, which is
// the only way the dashboard ever reads a buffer directly.
func (m *Model) selectOffset(delta int) {
	if len(m.order) == 0 {
		return
	}
	next := m.selected + delta
	if next < 0 || next >= len(m.order) {
		return
	}
	m.selected = next
	m.rebuildLogs()
	m.layout()
}

// act performs a controller request off the update loop.
func (m *Model) act(verb string, call func(context.Context, string) error) tea.Cmd {
	name := m.selectedName()
	if name == "" {
		return nil
	}
	if m.controller == nil {
		return func() tea.Msg { return actionDone{text: "no controller attached"} }
	}
	ctx := m.ctx
	return func() tea.Msg {
		if err := call(ctx, name); err != nil {
			return actionDone{text: fmt.Sprintf("%s %s: %s", verb, name, err)}
		}
		return actionDone{text: fmt.Sprintf("%s %s: requested", verb, name)}
	}
}

// clearNoticeAfter schedules the removal of a footer notice.
func clearNoticeAfter(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return noticeExpired{} })
}

// apply folds a batch of events into the model.
func (m *Model) apply(batch []events.Event) {
	for _, event := range batch {
		switch event := event.(type) {
		case events.ServiceAdded:
			m.addService(event.Status)

		case events.StateChanged:
			// A state change for a service the dashboard never listed still
			// lists it: the event bus drops rather than blocks under load,
			// so a lost ServiceAdded must not hide a service forever.
			m.addService(event.Status)

		case events.StatusUpdated:
			m.addService(event.Status)

		case events.StatsUpdated:
			if status, known := m.statuses[event.Name]; known {
				status.Stats = event.Stats
				m.statuses[event.Name] = status
			}

		case events.LogEmitted:
			m.appendLog(event.Line)
		}
	}
}

func (m *Model) addService(status service.Status) {
	if _, known := m.statuses[status.Name]; !known {
		m.order = append(m.order, status.Name)
	}
	m.statuses[status.Name] = status
}

// appendLog keeps the selected service's visible lines in step with its buffer.
//
// A gap in the sequence numbers means this subscriber missed lines, which can
// happen when a very noisy service outruns the dashboard. Rather than show a
// log with a hole in it the model re-reads the buffer, which is the point of
// keeping one.
func (m *Model) appendLog(line logs.Line) {
	if line.Service != m.selectedName() {
		return
	}
	if line.Seq != m.logSeq+1 {
		m.rebuildLogs()
		return
	}
	m.logLines = append(m.logLines, line)
	if extra := len(m.logLines) - m.capacity; extra > 0 {
		m.logLines = append(m.logLines[:0], m.logLines[extra:]...)
	}
	m.logSeq = line.Seq
}

// rebuildLogs re-reads the selected service's buffer. It is the only direct
// read of shared state in the dashboard, and it is safe because the buffer is
// built to be read while it is written.
func (m *Model) rebuildLogs() {
	name := m.selectedName()
	if name == "" {
		m.logLines, m.logSeq = nil, 0
		return
	}
	buffer := m.store.Buffer(name)
	lines := buffer.Snapshot()
	if len(lines) > m.capacity {
		lines = lines[len(lines)-m.capacity:]
	}
	m.logLines = lines
	m.logSeq = buffer.LastSeq()
	m.viewport.GotoBottom()
}

func pageStep(height int) int {
	if height <= 1 {
		return 1
	}
	return height
}
