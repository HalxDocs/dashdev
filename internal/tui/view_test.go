package tui

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/platform/platformtest"
	"github.com/HalxDocs/dashdev/internal/service"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// testStart is the instant every fixture is built around, so that a rendered
// uptime is a fixed string rather than whatever the test machine's clock says.
var testStart = time.Date(2026, 9, 26, 10, 30, 0, 0, time.UTC)

// recordingController remembers what the dashboard asked for and can be told to
// fail, so that the footer's error path is exercised too.
type recordingController struct {
	calls []string
	err   error
}

func (c *recordingController) Restart(context.Context, string) error { return c.record("restart") }
func (c *recordingController) Stop(context.Context, string) error    { return c.record("stop") }
func (c *recordingController) Start(context.Context, string) error   { return c.record("start") }

func (c *recordingController) record(verb string) error {
	c.calls = append(c.calls, verb)
	return c.err
}

// newTestModel builds a dashboard with a fixed clock and a real event stream,
// so that tests exercise the same path the program does.
func newTestModel(t *testing.T) (*Model, *platformtest.Clock, *recordingController, *logs.Store) {
	t.Helper()
	clock := platformtest.NewClock()
	store := logs.NewStore(64)
	controller := &recordingController{}
	bus := events.NewBus()

	model := New(context.Background(), Options{
		Subscription: bus.Subscribe(32),
		Store:        store,
		Controller:   controller,
		Clock:        clock,
		LogCapacity:  64,
		Coloured:     false,
	})
	model.width, model.height = 110, 24
	model.ready = true
	return model, clock, controller, store
}

// fixtureEvents describes a small system in the order the manager would publish
// it: every service is announced, then lifecycle changes, then output.
func fixtureEvents(start time.Time) []events.Event {
	running := service.Status{
		Name:    "api",
		State:   service.StateRunning,
		PID:     4321,
		Started: start.Add(-90 * time.Second),
		Health:  service.HealthStatus{State: service.HealthHealthy, Since: start.Add(-70 * time.Second)},
		Stats:   platform.Stats{CPUPercent: 12.5, MemoryBytes: 48 * 1024 * 1024, SampledAt: start},
	}
	crashed := service.Status{
		Name:          "worker",
		State:         service.StateCrashed,
		ExitCode:      7,
		Started:       start.Add(-4 * time.Second),
		Stopped:       start.Add(-2 * time.Second),
		Restarts:      2,
		Reason:        "exit code 7",
		Health:        service.HealthStatus{State: service.HealthUnknown},
		NextRestartAt: start.Add(6 * time.Second),
	}
	stopped := service.Status{
		Name:    "db",
		State:   service.StateStopped,
		Started: start.Add(-10 * time.Minute),
		Stopped: start.Add(-3 * time.Second),
		Reason:  "stopped on request",
		Health:  service.HealthStatus{State: service.HealthUnknown},
	}

	return []events.Event{
		events.ServiceAdded{Status: service.Status{Name: "api", State: service.StateStarting}, Time: start},
		events.ServiceAdded{Status: service.Status{Name: "worker", State: service.StateStarting}, Time: start},
		events.ServiceAdded{Status: service.Status{Name: "db", State: service.StateStarting}, Time: start},
		events.StateChanged{
			Status: running, From: service.StateStarting, To: service.StateRunning,
			Reason: "started", Time: start,
		},
		events.StateChanged{
			Status: crashed, From: service.StateRunning, To: service.StateCrashed,
			Reason: "exit code 7", Time: start,
		},
		events.StateChanged{
			Status: stopped, From: service.StateRunning, To: service.StateStopped,
			Reason: "stopped on request", Time: start,
		},
		events.LogEmitted{Line: logs.Line{
			Service: "api", Stream: logs.StreamStdout, Message: "listening on :8080",
			Time: start, Seq: 1,
		}},
		events.LogEmitted{Line: logs.Line{
			Service: "api", Stream: logs.StreamStderr, Message: "warning: DEBUG is set",
			Time: start, Seq: 2,
		}},
		events.LogEmitted{Line: logs.Line{
			Service: "api", Stream: logs.StreamStdout, Message: strings.Repeat("very long line ", 20),
			Time: start, Seq: 3, Truncated: true,
		}},
	}
}

func TestViewMatchesTheGoldenFile(t *testing.T) {
	model, clock, _, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))
	model.dropped = 7
	model.layout()

	checkGolden(t, "view_full.txt", model.render())
}

func TestViewShowsASelectionOfAnotherService(t *testing.T) {
	model, clock, _, store := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))

	// Output that arrived for a service that was not selected is still in its
	// buffer, which is what makes switching services instant and complete.
	store.Buffer("worker").Append(logs.Line{
		Service: "worker", Stream: logs.StreamStdout, Message: "working on batch 12", Time: clock.Now(),
	})
	store.Buffer("worker").Append(logs.Line{
		Service: "worker", Stream: logs.StreamStderr, Message: "batch 12 failed", Time: clock.Now(),
	})

	model.selectOffset(1)
	model.layout()
	if name := model.selectedName(); name != "worker" {
		t.Fatalf("selected %q, want worker", name)
	}
	rendered := model.render()
	for _, want := range []string{"working on batch 12", "batch 12 failed"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the view does not show output from the newly selected service: %q missing\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "listening on :8080") {
		t.Error("the view still shows the previous service's output")
	}

	checkGolden(t, "view_selected.txt", rendered)
}

func TestViewDoesNotLieAboutTheTerminalSize(t *testing.T) {
	model, _, _, _ := newTestModel(t)
	model.ready = false

	view := model.View()
	if !strings.Contains(view.Content, "reading the service system") {
		t.Errorf("the first frame is %q, want a message from before the window size is known", view.Content)
	}
	if !view.AltScreen {
		t.Error("the dashboard does not ask for the alternate screen, so it would scribble over the shell")
	}
}

func TestHeaderReportsWhatWasDropped(t *testing.T) {
	model, clock, _, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))
	model.layout()

	if rendered := model.render(); strings.Contains(rendered, "dropped") {
		t.Errorf("the header reports drops when nothing was dropped:\n%s", rendered)
	}

	model.dropped = 412
	renderWithDrops := model.render()
	if !strings.Contains(renderWithDrops, "412 events dropped") {
		t.Errorf("the header does not report dropped events:\n%s", renderWithDrops)
	}
	checkGolden(t, "view_dropped.txt", renderWithDrops)
}

// TestTruncatedLinesAreMarkedAndBounded guards the two things a log pane has
// to get right with a line that never ends: it must not exceed the pane, and it
// must say that something was cut rather than quietly presenting a prefix as
// the whole line.
func TestTruncatedLinesAreMarkedAndBounded(t *testing.T) {
	model, clock, _, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))
	model.layout()

	rendered := model.renderLogLines()
	if len(rendered) != 3 {
		t.Fatalf("rendered %d lines, want 3", len(rendered))
	}

	last := strings.TrimRight(rendered[2], " ")
	if !strings.HasSuffix(last, "…") {
		t.Errorf("a truncated line is not marked: %q", last)
	}
	if strings.Contains(last, "……") {
		t.Errorf("a truncated line is marked twice: %q", last)
	}
	if width := lipgloss.Width(last); width > model.viewport.Width() {
		t.Errorf("the line is %d columns wide, want at most the pane's %d", width, model.viewport.Width())
	}

	// A line that fits is left exactly as it was.
	if first := rendered[0]; !strings.Contains(first, "listening on :8080") {
		t.Errorf("a short line was altered: %q", first)
	}
}

func TestTruncateKeepsWithinItsBudget(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{name: "fits", text: "abc", width: 5, want: "abc"},
		{name: "exactly fits", text: "abcde", width: 5, want: "abcde"},
		{name: "cut", text: "abcdef", width: 4, want: "abc…"},
		{name: "very narrow", text: "abcdef", width: 1, want: "…"},
		{name: "no room", text: "abcdef", width: 0, want: ""},
		{name: "empty", text: "", width: 10, want: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := truncate(test.text, test.width)
			if got != test.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", test.text, test.width, got, test.want)
			}
			if width := lipgloss.Width(got); test.width > 0 && width > test.width {
				t.Errorf("truncate(%q, %d) = %q, which is %d columns wide", test.text, test.width, got, width)
			}
		})
	}
}

func TestQuittingKeysAskTheProgramToQuit(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'q', Text: "q"},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		model, _, _, _ := newTestModel(t)
		_, command := model.Update(key)
		if command == nil {
			t.Errorf("%q did not produce a quit command", key.String())
			continue
		}
		if msg := command(); msg == nil {
			t.Errorf("%q produced a command that returned nothing", key.String())
		}
	}
}

func TestNavigationMovesTheSelectionWithoutTouchingTheManager(t *testing.T) {
	model, clock, controller, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))

	model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := model.selectedName(); got != "worker" {
		t.Errorf("after moving down the selection is %q, want worker", got)
	}
	model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if got := model.selectedName(); got != "db" {
		t.Errorf("after moving down again the selection is %q, want db", got)
	}
	// The selection stops at the end rather than wrapping, so a key held down
	// cannot turn into a jump to an unrelated service.
	model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if got := model.selectedName(); got != "db" {
		t.Errorf("the selection moved past the last service: %q", got)
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.selectedName(); got != "worker" {
		t.Errorf("after moving up the selection is %q, want worker", got)
	}
	if len(controller.calls) != 0 {
		t.Errorf("navigating called the manager: %v", controller.calls)
	}
}

func TestServiceActionsGoThroughTheController(t *testing.T) {
	model, clock, controller, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))

	for key, want := range map[string]string{
		keyRestart: "restart",
		keyStop:    "stop",
		keyStart:   "start",
	} {
		controller.calls = nil
		command := press(model, key)
		if command == nil {
			t.Fatalf("%q produced no command", key)
		}
		msg := command()
		if len(controller.calls) != 1 || controller.calls[0] != want {
			t.Fatalf("%q called %v, want %s", key, controller.calls, want)
		}
		done, ok := msg.(actionDone)
		if !ok {
			t.Fatalf("%q produced %T, want an actionDone message", key, msg)
		}
		if !strings.Contains(done.text, want) || !strings.Contains(done.text, "api") {
			t.Errorf("%q reported %q, want it to name the action and the service", key, done.text)
		}
	}
}

func TestAFailingActionIsReportedToTheUser(t *testing.T) {
	model, clock, controller, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))
	controller.err = errors.New("service is not running")

	command := press(model, keyRestart)
	model.Update(command())

	if !strings.Contains(model.notice, "service is not running") {
		t.Errorf("the footer says %q, want the failure", model.notice)
	}
	if !strings.Contains(model.render(), "service is not running") {
		t.Error("the failure is not visible in the view")
	}
}

func TestAServiceThatIsBeingWaitedOnCannotBeActedOn(t *testing.T) {
	model, _, controller, _ := newTestModel(t)
	// Nothing has been announced yet, so there is no service to act on.
	if command := press(model, keyRestart); command != nil {
		t.Error("an action was offered before any service was known")
	}
	if len(controller.calls) != 0 {
		t.Errorf("the manager was asked to act on nothing: %v", controller.calls)
	}
}

// TestEveryBoundKeyDoesSomething asserts that the interface the footer
// advertises is the interface the handler implements.
//
// Each key is pressed against a model prepared so that the key *can* have an
// effect: a selection key needs something to move to, and a scroll key needs a
// log pane that is not already at the end it would move towards. Without that
// setup the test would fail on correct behaviour, which is how it failed the
// first time it ran.
func TestEveryBoundKeyDoesSomething(t *testing.T) {
	type keyCase struct {
		key     string
		prepare func(m *Model, store *logs.Store)
	}

	middle := func(m *Model, _ *logs.Store) { m.selectOffset(1) }
	scrollable := func(m *Model, store *logs.Store) {
		// Enough output that the pane has somewhere to go in both directions.
		for i := range 200 {
			store.Buffer("api").Append(logs.Line{
				Service: "api", Stream: logs.StreamStdout,
				Message: "line " + strconv.Itoa(i), Time: testStart,
			})
		}
		m.rebuildLogs()
		m.layout()
	}

	cases := []keyCase{
		{key: keyUp, prepare: middle},
		{key: keyUpVim, prepare: middle},
		{key: keyDown, prepare: middle},
		{key: keyDownVim, prepare: middle},
		{key: keyPageUp, prepare: func(m *Model, s *logs.Store) {
			scrollable(m, s)
			m.viewport.GotoBottom()
		}},
		{key: keyScrollUp, prepare: func(m *Model, s *logs.Store) {
			scrollable(m, s)
			m.viewport.GotoBottom()
		}},
		{key: keyTop, prepare: func(m *Model, s *logs.Store) {
			scrollable(m, s)
			m.viewport.GotoBottom()
		}},
		{key: keyPageDown, prepare: scrollable},
		{key: keyScrollDown, prepare: scrollable},
		{key: keyBottom, prepare: scrollable},
		{key: keyRestart, prepare: middle},
		{key: keyStop, prepare: middle},
		{key: keyStart, prepare: middle},
		{key: keyQuit, prepare: middle},
		{key: keyQuitCtrlC, prepare: middle},
	}

	// Every binding the handler knows about is covered above.
	covered := make(map[string]bool, len(cases))
	for _, test := range cases {
		covered[test.key] = true
	}
	for _, key := range boundKeys {
		if !covered[key] {
			t.Errorf("key %q is bound but no case tests it", key)
		}
	}

	for _, test := range cases {
		t.Run(test.key, func(t *testing.T) {
			model, clock, controller, store := newTestModel(t)
			clock.Advance(testStart.Sub(clock.Now()))
			model.apply(fixtureEvents(testStart))
			model.layout()
			if test.prepare != nil {
				test.prepare(model, store)
			}

			before := model.selectedName()
			offset := model.viewport.YOffset()
			controller.calls = nil

			_, command := model.Update(keyMessage(test.key))

			moved := model.selectedName() != before
			scrolled := model.viewport.YOffset() != offset
			acted := len(controller.calls) > 0
			quit := command != nil
			if !moved && !scrolled && !acted && !quit {
				t.Errorf("key %q is advertised but does nothing", test.key)
			}
		})
	}
}

func TestStreamClosureIsVisible(t *testing.T) {
	model, clock, _, _ := newTestModel(t)
	clock.Advance(testStart.Sub(clock.Now()))
	model.apply(fixtureEvents(testStart))

	model.Update(streamClosed{})
	if !model.closed {
		t.Fatal("the model did not notice that the stream ended")
	}
	if rendered := model.render(); !strings.Contains(rendered, "event stream ended") {
		t.Errorf("the stream ending is not visible:\n%s", rendered)
	}

	// A stream that ended for a reason is reported as that reason.
	model.Update(streamClosed{err: errors.New("broken pipe")})
	if !strings.Contains(model.render(), "broken pipe") {
		t.Errorf("the stream failure is not visible:\n%s", model.render())
	}
}

func TestNoticeExpires(t *testing.T) {
	model, _, _, _ := newTestModel(t)
	model.Update(actionDone{text: "restart api: requested"})
	if model.notice == "" {
		t.Fatal("the action's outcome was not shown")
	}
	model.Update(noticeExpired{})
	if model.notice != "" {
		t.Errorf("the notice %q outlived its welcome", model.notice)
	}
}

// press sends a key and returns the command it produced.
func press(model *Model, key string) tea.Cmd {
	_, command := model.Update(keyMessage(key))
	return command
}

// keyMessage builds the message a terminal would deliver for a key name.
func keyMessage(key string) tea.KeyPressMsg {
	switch key {
	case keyUp:
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case keyDown:
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case keyPageUp:
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case keyPageDown:
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case keyStart:
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case keyQuitCtrlC, keyScrollUp, keyScrollDown:
		letter := strings.TrimPrefix(key, "ctrl+")
		return tea.KeyPressMsg{Code: rune(letter[0]), Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key, ShiftedCode: rune(key[0])}
	}
}

// checkGolden compares a rendered view against a file, and rewrites the file
// when the test is run with -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating the golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run this test with -update to create it)", path, err)
	}
	if got == string(want) {
		return
	}
	t.Errorf("the rendered view no longer matches %s\n--- rendered ---\n%s\n--- golden ---\n%s", path, got, want)
}
