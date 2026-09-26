//go:build windows

package platform

// ShellArgv returns the argv that runs command through the platform's shell.
func ShellArgv(command string) []string {
	return []string{"cmd", "/c", command}
}
