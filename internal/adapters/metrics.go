package adapters

import (
	"fmt"
	"io"
	"runtime"
	"time"
)

// Metrics tracks performance data for a command execution.
// When disabled, all methods are no-ops with zero overhead.
type Metrics struct {
	startTime   time.Time
	startMemory uint64
	enabled     bool
}

// NewMetrics creates a metrics tracker. If enabled is false, all methods are
// no-ops so callers do not need conditional logic.
func NewMetrics(enabled bool) *Metrics {
	var startMem uint64
	if enabled {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		startMem = m.Alloc
	}
	return &Metrics{
		startTime:   time.Now(),
		startMemory: startMem,
		enabled:     enabled,
	}
}

// Print outputs the collected metrics to w. Call via defer at the start of
// a command's RunE function so it fires after the command completes.
func (m *Metrics) Print(w io.Writer) {
	if !m.enabled {
		return
	}

	elapsed := time.Since(m.startTime)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	fmt.Fprintf(w, "\n  Metrics:\n")
	fmt.Fprintf(w, "    Wall time:      %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "    Memory current: %.1f MB\n", float64(mem.Alloc)/1024/1024)
	fmt.Fprintf(w, "    Memory peak:    %.1f MB\n", float64(mem.TotalAlloc)/1024/1024)
	fmt.Fprintf(w, "    GC cycles:      %d\n", mem.NumGC)
	fmt.Fprintf(w, "    Goroutines:     %d\n", runtime.NumGoroutine())
	fmt.Fprintln(w)
}
