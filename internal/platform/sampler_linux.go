//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// userHZ is the unit of the CPU times in /proc/<pid>/stat.
//
// Reading the real value needs sysconf(_SC_CLK_TCK), which is a C call, and
// dashdev does not use cgo. Every Linux architecture that Go supports reports
// 100 here, so the assumption is stated rather than hidden: if it is ever
// wrong, CPU percentages are scaled rather than the feature failing.
const userHZ = 100

// NewSampler returns a sampler backed by /proc.
func NewSampler() Sampler {
	return &sampler{previous: make(map[int]cpuSample)}
}

type cpuSample struct {
	at    time.Time
	total time.Duration
}

type sampler struct {
	mu       sync.Mutex
	previous map[int]cpuSample
}

var _ Sampler = (*sampler)(nil)

// Sample implements Sampler. A CPU percentage needs two measurements, so the
// first call after a stop reports memory only.
func (s *sampler) Sample(pid int) (Stats, error) {
	total, err := cpuTime(pid)
	if err != nil {
		return Stats{}, err
	}
	memory, err := residentBytes(pid)
	if err != nil {
		return Stats{}, err
	}

	now := time.Now()
	stats := Stats{MemoryBytes: memory, SampledAt: now}

	s.mu.Lock()
	previous, seen := s.previous[pid]
	s.previous[pid] = cpuSample{at: now, total: total}
	s.mu.Unlock()

	if seen {
		if elapsed := now.Sub(previous.at); elapsed > 0 {
			percent := float64(total-previous.total) / float64(elapsed) * 100
			stats.CPUPercent = max(percent, 0)
		}
	}
	return stats, nil
}

// Forget implements Sampler.
func (s *sampler) Forget(pid int) {
	s.mu.Lock()
	delete(s.previous, pid)
	s.mu.Unlock()
}

// Close implements Sampler.
func (s *sampler) Close() error { return nil }

// cpuTime reads the user and system time a process has consumed.
func cpuTime(pid int) (time.Duration, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("platform: read process %d: %w", pid, err)
	}
	// The command name is in parentheses and may contain spaces, so the fields
	// after the last ')' are the only ones that can be split positionally.
	text := string(raw)
	closing := strings.LastIndexByte(text, ')')
	if closing < 0 {
		return 0, errors.New("platform: malformed /proc stat entry")
	}
	fields := strings.Fields(text[closing+1:])
	// State is field 3, utime is field 14 and stime is field 15, so within this
	// slice they are at 11 and 12.
	if len(fields) < 13 {
		return 0, errors.New("platform: short /proc stat entry")
	}
	user, err := strconv.ParseInt(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("platform: parse user time: %w", err)
	}
	kernel, err := strconv.ParseInt(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("platform: parse system time: %w", err)
	}
	ticks := user + kernel
	return time.Duration(ticks) * time.Second / userHZ, nil
}

// residentBytes reads the resident set size of a process.
func residentBytes(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0, fmt.Errorf("platform: read process %d memory: %w", pid, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, errors.New("platform: malformed /proc statm entry")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("platform: parse resident pages: %w", err)
	}
	return pages * uint64(os.Getpagesize()), nil
}
