// Package buildinfo reports the version and provenance of the running binary.
package buildinfo

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// These are overridden at build time via:
//
//	go build -ldflags "-X github.com/HalxDocs/dashdev/internal/buildinfo.version=1.2.3 ..."
//
// They are the only mutable package state in the project: written once by the
// linker before main runs, read only through Get.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Info describes the provenance of the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Modified  bool   `json:"modified,omitempty"`
}

// Get returns the build information of the running binary, filling any field
// the linker did not set from the embedded module build info.
func Get() Info {
	info := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = setting.Value
			}
		case "vcs.modified":
			info.Modified = setting.Value == "true"
		}
	}
	return info
}

// String renders a single-line human readable summary.
func (i Info) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dashdev %s", i.Version)
	if i.Commit != "" {
		short := i.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		fmt.Fprintf(&b, " (%s)", short)
	}
	fmt.Fprintf(&b, " %s/%s %s", i.OS, i.Arch, i.GoVersion)
	if i.Modified {
		b.WriteString(" modified")
	}
	return b.String()
}

// JSON renders the info as a single JSON object.
func (i Info) JSON() ([]byte, error) {
	return json.MarshalIndent(i, "", "  ")
}
