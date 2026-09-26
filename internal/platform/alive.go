package platform

// Alive reports whether a process identifier still refers to a running process.
//
// It exists so that "dashdev leaves no orphans behind" is something a test can
// assert against the operating system rather than against dashdev's own
// bookkeeping. Process identifiers can be reused, so a false result is a
// reliable statement that the process is gone while a true result is strong
// evidence that it is still there.
func Alive(pid int) bool { return alive(pid) }
