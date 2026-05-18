package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/internal/domain"
)

// MetricsRenderer outputs quality signals and metrics to the terminal.
type MetricsRenderer struct {
	out   io.Writer
	color bool
}

// NewMetricsRenderer creates a new MetricsRenderer that writes to stdout.
func NewMetricsRenderer() *MetricsRenderer {
	return &MetricsRenderer{
		out:   os.Stdout,
		color: isTTY(),
	}
}

// NewMetricsRendererTo creates a MetricsRenderer that writes to the given writer.
func NewMetricsRendererTo(w io.Writer, color bool) *MetricsRenderer {
	return &MetricsRenderer{out: w, color: color}
}

// RenderQualitySignals outputs the quality signals dashboard.
func (r *MetricsRenderer) RenderQualitySignals(signals *domain.QualitySignals) {
	w := r.out

	fmt.Fprintln(w)
	r.writeBold(w, "  Quality Signals")
	r.writeLine(w, "  "+strings.Repeat("\u2550", 55))
	fmt.Fprintln(w)

	// Slowest tests.
	if len(signals.SlowestTests) > 0 {
		r.writeBold(w, "  Slowest Tests (top 10):")
		for i, tm := range signals.SlowestTests {
			durStr := formatDurationHuman(tm.Duration)
			fmt.Fprintf(w, "    %2d. %7s  %-30s  %s\n", i+1, durStr, truncate(tm.Name, 30), tm.File)
		}
		fmt.Fprintln(w)
	}

	// Flaky tests.
	if len(signals.FlakyTests) > 0 {
		r.writeBold(w, "  Flaky Tests (retries > 0):")
		for _, tm := range signals.FlakyTests {
			icon := r.warningIcon()
			retryInfo := ""
			if tm.Retries > 0 {
				retryInfo = fmt.Sprintf(" (%d retries)", tm.Retries)
			}
			fmt.Fprintf(w, "    %s  %s:%s%s\n", icon, tm.File, tm.Name, retryInfo)
		}
		fmt.Fprintln(w)
	}

	// Memory hogs (Go only).
	if len(signals.MemoryHogs) > 0 {
		r.writeBold(w, "  Memory Intensive (Go only, top 10):")
		for i, tm := range signals.MemoryHogs {
			memStr := formatBytes(tm.BytesPerOp)
			allocStr := fmt.Sprintf("(%s allocs/op)", formatInt(tm.AllocsPerOp))
			fmt.Fprintf(w, "    %2d. %10s  %-30s  %s\n", i+1, memStr, truncate(tm.Name, 30), allocStr)
		}
		fmt.Fprintln(w)
	}

	// Race conditions.
	if len(signals.RaceConditions) > 0 {
		r.writeBold(w, "  Race Conditions:")
		for _, tm := range signals.RaceConditions {
			icon := r.failIcon()
			fmt.Fprintf(w, "    %s  %-30s  (detected in last run)\n", icon, tm.Name)
		}
		fmt.Fprintln(w)
	}

	// Failing trends.
	if len(signals.FailingTrends) > 0 {
		r.writeBold(w, "  Health Trends (declining):")
		for _, fid := range signals.FailingTrends {
			icon := r.trendDownIcon()
			fmt.Fprintf(w, "    %s  %s\n", icon, fid)
		}
		fmt.Fprintln(w)
	}

	// If everything is clean.
	if len(signals.SlowestTests) == 0 && len(signals.FlakyTests) == 0 &&
		len(signals.MemoryHogs) == 0 && len(signals.RaceConditions) == 0 &&
		len(signals.FailingTrends) == 0 {
		r.writeGreen(w, "  No quality signals detected. All clear.")
		fmt.Fprintln(w)
	}
}

// RenderFeatureHealth outputs the health trend for a specific feature.
func (r *MetricsRenderer) RenderFeatureHealth(trend *domain.FeatureHealthTrend) {
	w := r.out

	fmt.Fprintln(w)
	r.writeBold(w, fmt.Sprintf("  Health Trend: %s", trend.FeatureID))
	r.writeLine(w, "  "+strings.Repeat("\u2550", 55))
	fmt.Fprintln(w)

	if len(trend.DataPoints) == 0 {
		r.writeDim(w, "  No data points available for this feature.")
		fmt.Fprintln(w)
		return
	}

	fmt.Fprintf(w, "    %-22s  %-8s  %-10s  %s\n", "Timestamp", "Health", "Pass Rate", "Avg Duration")
	fmt.Fprintf(w, "    %s\n", strings.Repeat("-", 60))

	for _, dp := range trend.DataPoints {
		ts := dp.Timestamp.Format("2006-01-02 15:04")
		health := fmt.Sprintf("%.0f%%", dp.HealthScore*100)
		pass := fmt.Sprintf("%.0f%%", dp.PassRate*100)
		dur := formatDurationHuman(dp.AvgDuration)
		fmt.Fprintf(w, "    %-22s  %-8s  %-10s  %s\n", ts, health, pass, dur)
	}

	fmt.Fprintln(w)
}

// RenderSlowestTests renders only slow tests above a threshold duration.
func (r *MetricsRenderer) RenderSlowestTests(tests []domain.TestMetric, threshold time.Duration) {
	w := r.out

	fmt.Fprintln(w)
	r.writeBold(w, fmt.Sprintf("  Slow Tests (> %s):", formatDurationHuman(threshold)))
	r.writeLine(w, "  "+strings.Repeat("\u2550", 55))
	fmt.Fprintln(w)

	var filtered []domain.TestMetric
	for _, tm := range tests {
		if tm.Duration >= threshold {
			filtered = append(filtered, tm)
		}
	}

	if len(filtered) == 0 {
		r.writeGreen(w, "  No tests exceed the threshold.")
		fmt.Fprintln(w)
		return
	}

	for i, tm := range filtered {
		durStr := formatDurationHuman(tm.Duration)
		fmt.Fprintf(w, "    %2d. %7s  %-30s  %s\n", i+1, durStr, truncate(tm.Name, 30), tm.File)
	}
	fmt.Fprintln(w)
}

// RenderJSON writes the quality signals as JSON to the writer.
func (r *MetricsRenderer) RenderJSON(signals *domain.QualitySignals) error {
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(signals)
}

// --- helper methods ---

func (r *MetricsRenderer) writeBold(w io.Writer, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", colorBold, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

func (r *MetricsRenderer) writeLine(w io.Writer, s string) {
	fmt.Fprintln(w, s)
}

func (r *MetricsRenderer) writeDim(w io.Writer, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", colorDim, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

func (r *MetricsRenderer) writeGreen(w io.Writer, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", colorGreen, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

func (r *MetricsRenderer) warningIcon() string {
	if r.color {
		return colorYellow + "\u26a0" + colorReset
	}
	return "[!]"
}

func (r *MetricsRenderer) failIcon() string {
	if r.color {
		return colorRed + "\u2718" + colorReset
	}
	return "[X]"
}

func (r *MetricsRenderer) trendDownIcon() string {
	if r.color {
		return colorRed + "\u2193" + colorReset
	}
	return "[v]"
}

// --- formatting utilities ---

func formatDurationHuman(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB/op", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB/op", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B/op", b)
	}
}

func formatInt(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}
	return fmt.Sprintf("%d", n)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
