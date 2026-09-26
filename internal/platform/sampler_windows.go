//go:build windows

package platform

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS from psapi.h. The
// fields up to WorkingSetSize are the ones used here; the rest are declared so
// that the size passed to the API is the size the API expects.
type processMemoryCounters struct {
	Size                    uint32
	PageFaultCount          uint32
	PeakWorkingSetSize      uintptr
	WorkingSetSize          uintptr
	QuotaPeakPagedPoolUsage uintptr
	QuotaPagedPoolUsage     uintptr
	QuotaPeakNonPagedPool   uintptr
	QuotaNonPagedPoolUsage  uintptr
	PagefileUsage           uintptr
	PeakPagefileUsage       uintptr
}

// NewSampler returns a sampler backed by the Windows process APIs.
//
// The psapi entry point is created per sampler rather than kept in a package
// variable, so nothing in this project is global mutable state.
func NewSampler() Sampler {
	dll := windows.NewLazySystemDLL("psapi.dll")
	return &sampler{
		previous:         make(map[int]cpuSample),
		getProcessMemory: dll.NewProc("GetProcessMemoryInfo"),
	}
}

type cpuSample struct {
	at    time.Time
	total time.Duration
}

type sampler struct {
	mu               sync.Mutex
	previous         map[int]cpuSample
	getProcessMemory *windows.LazyProc
}

var _ Sampler = (*sampler)(nil)

// Sample implements Sampler. A CPU percentage needs two measurements, so the
// first call after a stop reports memory only.
func (s *sampler) Sample(pid int) (Stats, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Stats{}, fmt.Errorf("platform: open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return Stats{}, fmt.Errorf("platform: read process %d times: %w", pid, err)
	}
	total := filetimeDuration(kernel) + filetimeDuration(user)

	memory, err := s.memory(handle)
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
			stats.CPUPercent = max(float64(total-previous.total)/float64(elapsed)*100, 0)
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

func (s *sampler) memory(handle windows.Handle) (uint64, error) {
	var counters processMemoryCounters
	counters.Size = uint32(unsafe.Sizeof(counters))
	result, _, err := s.getProcessMemory.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Size),
	)
	if result == 0 {
		return 0, fmt.Errorf("platform: read process memory: %w", err)
	}
	return uint64(counters.WorkingSetSize), nil
}

// filetimeDuration converts a Windows FILETIME, which counts 100 nanosecond
// ticks, into a duration.
func filetimeDuration(value windows.Filetime) time.Duration {
	ticks := int64(value.HighDateTime)<<32 | int64(value.LowDateTime)
	return time.Duration(ticks * 100)
}
