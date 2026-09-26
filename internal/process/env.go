package process

import (
	"runtime"
	"strings"

	"github.com/HalxDocs/dashdev/internal/service"
)

// mergeEnv overlays overrides on base and returns the result in a stable order:
// inherited entries keep their positions and new entries are appended in the
// order they were declared. Variable names are compared case-insensitively on
// Windows, where the environment is not case-sensitive.
func mergeEnv(base []string, overrides []service.EnvVar) []string {
	if len(overrides) == 0 {
		return base
	}
	merged := make([]string, 0, len(base)+len(overrides))
	index := make(map[string]int, len(base)+len(overrides))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		key := envKey(name)
		if _, exists := index[key]; exists {
			continue
		}
		index[key] = len(merged)
		merged = append(merged, entry)
	}
	for _, override := range overrides {
		entry := override.Name + "=" + override.Value
		key := envKey(override.Name)
		if i, exists := index[key]; exists {
			merged[i] = entry
			continue
		}
		index[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
