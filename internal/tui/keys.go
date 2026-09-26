package tui

// Key bindings, kept in one place so the footer, the handler and the tests all
// describe the same interface.
const (
	keyQuit       = "q"
	keyQuitCtrlC  = "ctrl+c"
	keyUp         = "up"
	keyDown       = "down"
	keyUpVim      = "k"
	keyDownVim    = "j"
	keyRestart    = "r"
	keyStop       = "s"
	keyStart      = "enter"
	keyPageUp     = "pgup"
	keyPageDown   = "pgdown"
	keyTop        = "g"
	keyBottom     = "G"
	keyScrollUp   = "ctrl+u"
	keyScrollDown = "ctrl+d"
)

// footerHelp is the one-line reminder of the interface. It is spelled out here
// rather than derived from the constants above so that the text a user reads is
// easy to find and edit.
const footerHelp = "↑/↓ select · enter start · r restart · s stop · pgup/pgdn logs · g/G top/bottom · q quit"

// boundKeys lists every key the dashboard reacts to, which is what lets a test
// assert that the footer does not promise something the handler does not do.
var boundKeys = []string{
	keyQuit, keyQuitCtrlC, keyUp, keyDown, keyUpVim, keyDownVim,
	keyRestart, keyStop, keyStart, keyPageUp, keyPageDown,
	keyTop, keyBottom, keyScrollUp, keyScrollDown,
}
