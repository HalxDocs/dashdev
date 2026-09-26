// Package tui renders the service system and lets a person act on it.
//
// The dashboard is a pure consumer: it receives events, keeps its own copy of
// what it has been told, and renders that. It cannot reach into the manager's
// state, and the only thing it can do is send a named request back. That
// boundary is what makes the manager's lifecycle trustworthy and the dashboard
// easy to test, because a model plus a list of events is the whole input.
package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/service"
)

// noticeDuration is how long an action's result stays on the footer.
const noticeDuration = 4 * time.Second

// Controller is the part of the process manager the dashboard is allowed to
// use. It is deliberately three methods wide: the dashboard can ask for a
// named service to change state, and it can do nothing else.
type Controller interface {
	Restart(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
}

// Options configures the dashboard.
type Options struct {
	// Subscription is the event stream the dashboard reads. It is required.
	Subscription *events.Subscription
	// Store holds the log buffers the dashboard reads selected services from.
	Store *logs.Store
	// Controller receives the actions the user asks for.
	Controller Controller
	// Clock supplies the current time for uptimes and log timestamps.
	Clock platform.Clock
	// LogCapacity is how many lines of one service are held for display.
	LogCapacity int
	// Coloured turns on styling. Tests turn it off so that the golden files are
	// readable and stable.
	Coloured bool
}

// Model is the dashboard's state. It is built only from events, so it can be
// reconstructed from a recorded stream, which is what the view tests do.
type Model struct {
	ctx          context.Context
	subscription *events.Subscription
	store        *logs.Store
	controller   Controller
	clock        platform.Clock
	capacity     int

	order    []string
	statuses map[string]service.Status
	selected int

	logLines []logs.Line
	logSeq   uint64

	dropped uint64
	notice  string
	closed  bool
	err     error

	width, height int
	ready         bool

	styles   Styles
	viewport viewport.Model
}

// New returns a dashboard model ready to be run.
func New(ctx context.Context, options Options) *Model {
	model := &Model{
		ctx:          ctx,
		subscription: options.Subscription,
		store:        options.Store,
		controller:   options.Controller,
		clock:        options.Clock,
		capacity:     options.LogCapacity,
		statuses:     make(map[string]service.Status),
		styles:       NewStyles(options.Coloured),
		viewport:     viewport.New(),
	}
	if model.clock == nil {
		model.clock = platform.System()
	}
	if model.capacity <= 0 {
		model.capacity = 1000
	}
	model.viewport.GotoBottom()
	return model
}

// Run shows the dashboard until the user quits or ctx is done.
func Run(ctx context.Context, options Options) error {
	program := tea.NewProgram(New(ctx, options), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return m.waitForEvents() }

// View implements tea.Model.
func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "dashdev"
	return view
}

// selectedName returns the service the detail pane is showing, or an empty
// string when nothing has been announced yet.
func (m *Model) selectedName() string {
	if m.selected < 0 || m.selected >= len(m.order) {
		return ""
	}
	return m.order[m.selected]
}

// selectedStatus returns the status of the selected service.
func (m *Model) selectedStatus() (service.Status, bool) {
	status, known := m.statuses[m.selectedName()]
	return status, known
}

// waitForEvents reads the next batch of events. It is re-armed after every
// batch, which is what keeps the dashboard live without ever polling.
func (m *Model) waitForEvents() tea.Cmd {
	subscription := m.subscription
	ctx := m.ctx
	return func() tea.Msg {
		batch, err := subscription.Pop(ctx)
		if err != nil {
			return streamClosed{err: err}
		}
		return eventBatch{events: batch}
	}
}

// setNotice reports the outcome of an action on the footer. Errors are shown
// where the user is already looking rather than thrown away or logged to a
// terminal the dashboard is occupying.
func (m *Model) setNotice(text string) {
	m.notice = text
}
