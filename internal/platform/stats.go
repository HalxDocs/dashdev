package platform

import "time"

// Stats is a point-in-time resource sample for one process.
type Stats struct {
	CPUPercent  float64
	MemoryBytes uint64
	SampledAt   time.Time
}

// Sampler reads resource usage for running processes. Implementations are
// expected to be safe for concurrent use.
type Sampler interface {
	// Sample reports usage for pid. Consecutive samples of the same pid are
	// required for a meaningful CPU percentage; an implementation that cannot
	// provide one reports zero.
	Sample(pid int) (Stats, error)
	// Forget drops any measurement history kept for pid.
	Forget(pid int)
	// Close releases any resources held by the sampler.
	Close() error
}
