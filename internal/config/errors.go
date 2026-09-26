package config

import (
	"errors"
	"fmt"
)

// ErrInvalid wraps every problem found while reading a configuration file.
var ErrInvalid = errors.New("invalid configuration")

// ErrNotFound reports that no configuration file could be located.
var ErrNotFound = errors.New("configuration file not found")

// FieldError identifies one problem with one field of the file. Reporting at
// field granularity matters here: a service file is edited by hand, and "some
// service directory is wrong" is not a useful message.
type FieldError struct {
	Path    string
	Problem string
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Problem }

// Is lets errors.Is(err, ErrInvalid) succeed through a joined error tree.
func (e *FieldError) Is(target error) bool { return target == ErrInvalid }

func fieldf(path, format string, args ...any) error {
	return &FieldError{Path: path, Problem: fmt.Sprintf(format, args...)}
}
