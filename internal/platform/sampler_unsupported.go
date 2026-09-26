//go:build !linux && !windows

package platform

import "errors"

// ErrUnsupported reports that a platform feature is not available.
var ErrUnsupported = errors.New("platform: not supported on this operating system")

// NewSampler returns a sampler that reports ErrUnsupported.
//
// Resource monitoring needs kernel interfaces that this platform does not
// expose without a C library, and dashdev does not use cgo. Returning a sampler
// that refuses is more honest than showing a service's usage as zero, and it is
// why the dashboard shows nothing rather than something wrong.
func NewSampler() Sampler { return unsupportedSampler{} }

type unsupportedSampler struct{}

var _ Sampler = unsupportedSampler{}

func (unsupportedSampler) Sample(int) (Stats, error) { return Stats{}, ErrUnsupported }
func (unsupportedSampler) Forget(int)                {}
func (unsupportedSampler) Close() error              { return nil }
