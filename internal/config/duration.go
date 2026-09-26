package config

import (
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// duration is a time.Duration that reads the forms a person actually writes in
// a configuration file: "500ms", "30s", "2m". A bare number means seconds,
// because "interval: 30" in a service file reads as thirty seconds to everyone
// except Go's time package.
type duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got %s", nodeKind(node))
	}
	if seconds, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
		*d = duration(time.Duration(seconds) * time.Second)
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: use forms like 250ms, 30s or 2m", node.Value)
	}
	*d = duration(parsed)
	return nil
}

// MarshalYAML implements yaml.Marshaler so that a round trip reads naturally.
func (d duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// value returns the duration as a time.Duration.
func (d duration) value() time.Duration { return time.Duration(d) }

func nodeKind(node *yaml.Node) string {
	switch node.Kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}
