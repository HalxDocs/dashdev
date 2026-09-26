package config

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/HalxDocs/dashdev/internal/service"
)

// The types in this file are the on-disk schema and nothing else. They are
// unexported on purpose: the file format is a detail of this package, and the
// rest of dashdev only ever sees service.Spec values.

type fileSchema struct {
	Version  int             `yaml:"version"`
	Defaults defaultsSchema  `yaml:"defaults"`
	Services []serviceSchema `yaml:"services"`
}

type defaultsSchema struct {
	Directory string         `yaml:"directory"`
	Env       []envSchema    `yaml:"env"`
	Restart   *restartSchema `yaml:"restart"`
	Monitor   *bool          `yaml:"monitor"`
}

type serviceSchema struct {
	Name      string         `yaml:"name"`
	Command   commandSchema  `yaml:"command"`
	Directory string         `yaml:"directory"`
	Env       []envSchema    `yaml:"env"`
	Shell     *bool          `yaml:"shell"`
	DependsOn dependsSchema  `yaml:"depends_on"`
	Health    *healthSchema  `yaml:"health"`
	Restart   *restartSchema `yaml:"restart"`
	Monitor   *bool          `yaml:"monitor"`
}

type envSchema struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type restartSchema struct {
	Policy      string         `yaml:"policy"`
	MaxAttempts *int           `yaml:"max_attempts"`
	Backoff     *backoffSchema `yaml:"backoff"`
}

type backoffSchema struct {
	Base   duration `yaml:"base"`
	Max    duration `yaml:"max"`
	Factor *float64 `yaml:"factor"`
}

type healthSchema struct {
	Type        string   `yaml:"type"`
	Target      string   `yaml:"target"`
	Interval    duration `yaml:"interval"`
	Timeout     duration `yaml:"timeout"`
	Retries     *int     `yaml:"retries"`
	StartPeriod duration `yaml:"start_period"`
}

// commandSchema accepts either a verbatim argument list or a single string that
// is split with shell-like quoting rules.
type commandSchema struct {
	argv []string
	raw  string
	list bool
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *commandSchema) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var argv []string
		if err := node.Decode(&argv); err != nil {
			return fmt.Errorf("command list must contain only strings: %w", err)
		}
		c.argv, c.list = argv, true
		return nil
	case yaml.ScalarNode:
		argv, err := service.SplitCommand(node.Value)
		if err != nil {
			return err
		}
		c.argv, c.raw = argv, node.Value
		return nil
	default:
		return fmt.Errorf("command must be a string or a list of arguments, got %s", nodeKind(node))
	}
}

// dependsSchema accepts either a list of service names or a mapping from a
// service name to the condition it must satisfy.
type dependsSchema []dependsEntry

type dependsEntry struct {
	Name      string
	Condition string
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *dependsSchema) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		entries := make(dependsSchema, 0, len(node.Content))
		for i, item := range node.Content {
			var name string
			if err := item.Decode(&name); err != nil {
				return fmt.Errorf("item %d must be a service name: %w", i, err)
			}
			entries = append(entries, dependsEntry{Name: name, Condition: ""})
		}
		*d = entries
		return nil
	case yaml.MappingNode:
		entries := make(dependsSchema, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			name := node.Content[i].Value
			var condition string
			if err := node.Content[i+1].Decode(&condition); err != nil {
				return fmt.Errorf("%s: expected a condition such as started or healthy: %w", name, err)
			}
			entries = append(entries, dependsEntry{Name: name, Condition: condition})
		}
		*d = entries
		return nil
	default:
		return fmt.Errorf(
			"depends_on must be a list of service names or a mapping of name to condition, got %s",
			nodeKind(node),
		)
	}
}
