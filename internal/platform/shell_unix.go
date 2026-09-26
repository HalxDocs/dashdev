//go:build !windows

package platform

// ShellArgv returns the argv that runs command through the platform's shell.
// A service that asks for a shell gets one, and the choice of shell is a
// platform detail rather than something every configuration has to know.
func ShellArgv(command string) []string {
	return []string{"sh", "-c", command}
}
