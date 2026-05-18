package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sosalejandro/atlas/internal/domain"
)

// ContractRenderer renders ContractOutput in terminal, JSON, or markdown formats.
type ContractRenderer struct {
	out   io.Writer
	color bool
}

// NewContractRenderer creates a new ContractRenderer with TTY auto-detection.
func NewContractRenderer() *ContractRenderer {
	return &ContractRenderer{
		out:   os.Stdout,
		color: isTTY(),
	}
}

// NewContractRendererToWriter creates a renderer writing to a specific writer.
func NewContractRendererToWriter(w io.Writer, color bool) *ContractRenderer {
	return &ContractRenderer{
		out:   w,
		color: color,
	}
}

// RenderTerminal renders the contract in layered terminal format with ANSI colors.
func (r *ContractRenderer) RenderTerminal(output *domain.ContractOutput, maxLayer int) {
	w := r.out

	// Header
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", r.c(colorBold, fmt.Sprintf("Feature: %s (%s)", output.FeatureName, output.FeatureID)))
	if output.EntryPoint != "" {
		fmt.Fprintf(w, "  Entry:   %s\n", r.c(colorCyan, output.EntryPoint))
	}

	if len(output.Layers) == 0 {
		fmt.Fprintln(w)
		if output.TraceExempt {
			fmt.Fprintf(w, "  %s\n", r.c(colorYellow, "⚑  TRACE EXEMPT — intentionally untraceable (contract_exempt: true)"))
			if output.ExemptReason != "" {
				fmt.Fprintf(w, "  %s\n", r.c(colorDim, "   Reason: "+output.ExemptReason))
			}
		} else {
			fmt.Fprintf(w, "  %s\n", r.c(colorDim, "No layers found — feature has no traceable API surfaces."))
		}
		fmt.Fprintln(w)
		return
	}

	for _, layer := range output.Layers {
		if maxLayer > 0 && layer.Number > maxLayer {
			break
		}

		r.renderLayerSeparator(w)
		r.renderLayer(w, &layer)
	}

	// Test coverage section.
	if len(output.TestFiles) > 0 {
		r.renderLayerSeparator(w)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, "Test Coverage for this chain:"))
		for _, tf := range output.TestFiles {
			icon := r.c(colorGreen, "\u2713")
			if tf.Status == "untested" {
				icon = r.c(colorRed, "\u2718")
			}
			layerLabel := ""
			if tf.Layer != "" {
				layerLabel = " " + r.c(colorDim, "("+tf.Layer+")")
			}
			fmt.Fprintf(w, "  %s %s%s\n", icon, tf.File, layerLabel)
		}
	}

	fmt.Fprintln(w)
}

// RenderJSON writes the contract output as indented JSON.
func (r *ContractRenderer) RenderJSON(output *domain.ContractOutput) error {
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// RenderMarkdown renders the contract as markdown documentation.
func (r *ContractRenderer) RenderMarkdown(output *domain.ContractOutput, maxLayer int) {
	w := r.out

	fmt.Fprintf(w, "# API Contract: %s\n\n", output.FeatureName)
	fmt.Fprintf(w, "**Feature ID:** `%s`\n", output.FeatureID)
	if output.Priority != "" {
		fmt.Fprintf(w, "**Priority:** %s\n", output.Priority)
	}
	if output.EntryPoint != "" {
		fmt.Fprintf(w, "**Entry Point:** `%s`\n", output.EntryPoint)
	}
	fmt.Fprintln(w)

	if len(output.Layers) == 0 {
		if output.TraceExempt {
			fmt.Fprintf(w, "> **⚑ TRACE EXEMPT** — intentionally untraceable (`contract_exempt: true`)\n")
			if output.ExemptReason != "" {
				fmt.Fprintf(w, ">\n> **Reason:** %s\n", output.ExemptReason)
			}
		} else {
			fmt.Fprintf(w, "> No layers found — feature has no traceable API surfaces.\n")
		}
		fmt.Fprintln(w)
		return
	}

	for _, layer := range output.Layers {
		if maxLayer > 0 && layer.Number > maxLayer {
			break
		}
		r.renderMarkdownLayer(w, &layer)
	}

	// Call chain summary.
	if len(output.Layers) > 0 {
		fmt.Fprint(w, "## Call Chain\n\n")
		for _, layer := range output.Layers {
			if maxLayer > 0 && layer.Number > maxLayer {
				break
			}
			loc := ""
			if layer.File != "" {
				loc = layer.File
				if layer.Line > 0 {
					loc = fmt.Sprintf("%s:%d", layer.File, layer.Line)
				}
			}
			if layer.NodeID != "" {
				fmt.Fprintf(w, "%d. `%s`", layer.Number, layer.NodeID)
				if loc != "" {
					fmt.Fprintf(w, " -- %s", loc)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "%d. %s\n", layer.Number, layer.Name)
			}
		}
		fmt.Fprintln(w)
	}

	// Test coverage.
	if len(output.TestFiles) > 0 {
		fmt.Fprint(w, "## Test Coverage\n\n")
		for _, tf := range output.TestFiles {
			status := "\u2713"
			if tf.Status == "untested" {
				status = "\u2718 NO TEST"
			}
			fmt.Fprintf(w, "- %s `%s`", status, tf.File)
			if tf.Layer != "" {
				fmt.Fprintf(w, " (%s)", tf.Layer)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}
}

// ---------------------------------------------------------------------------
// Terminal rendering helpers
// ---------------------------------------------------------------------------

func (r *ContractRenderer) renderLayerSeparator(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", strings.Repeat("\u2550", 67))
}

func (r *ContractRenderer) renderLayer(w io.Writer, layer *domain.ContractLayer) {
	fmt.Fprintln(w)

	// Layer header.
	header := fmt.Sprintf("  Layer %d: %s", layer.Number, layer.Name)
	fmt.Fprintf(w, "  %s\n", r.c(colorBold, fmt.Sprintf("Layer %d: %s", layer.Number, layer.Name)))
	fmt.Fprintf(w, "  %s\n", strings.Repeat("\u2500", visibleLength(header)))

	// File reference.
	if layer.File != "" {
		loc := layer.File
		if layer.Line > 0 {
			loc = fmt.Sprintf("%s:%d", layer.File, layer.Line)
		}
		fmt.Fprintf(w, "  File: %s\n", r.c(colorDim, loc))
	}

	// Signature.
	if layer.Signature != "" {
		fmt.Fprintln(w)
		// Indent each line of multi-line signatures.
		for _, line := range strings.Split(layer.Signature, "\n") {
			fmt.Fprintf(w, "  %s\n", r.c(colorCyan, line))
		}
	}

	// Input type with table.
	if layer.InputType != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s %s\n", r.c(colorBold, "Input:"), layer.InputType.Name)
		r.renderFieldTable(w, layer.InputType.Fields, true)
	}

	// Output type with table.
	if layer.OutputType != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s %s\n", r.c(colorBold, "Response:"), layer.OutputType.Name)
		r.renderFieldTable(w, layer.OutputType.Fields, false)
	}

	// Delegate info.
	if layer.DelegateTo != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Delegates to: %s\n", r.c(colorGreen, layer.DelegateTo))
	}

	// Notes.
	for _, note := range layer.Notes {
		fmt.Fprintf(w, "  %s %s\n", r.c(colorDim, "\u2192"), r.c(colorDim, note))
	}
}

// renderFieldTable draws a box-drawing character table for struct fields.
func (r *ContractRenderer) renderFieldTable(w io.Writer, fields []domain.ContractField, showRequired bool) {
	if len(fields) == 0 {
		return
	}

	// Calculate column widths.
	nameW := len("Field")
	typeW := len("Type")
	for _, f := range fields {
		if len(f.Name) > nameW {
			nameW = len(f.Name)
		}
		if len(f.Type) > typeW {
			typeW = len(f.Type)
		}
	}
	nameW += 2
	typeW += 2

	if showRequired {
		reqW := len("Required") + 2

		// Top border.
		fmt.Fprintf(w, "  \u250c%s\u252c%s\u252c%s\u2510\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW), strings.Repeat("\u2500", reqW))

		// Header row.
		fmt.Fprintf(w, "  \u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502\n",
			nameW-2, "Field", typeW-2, "Type", reqW-2, "Required")

		// Header separator.
		fmt.Fprintf(w, "  \u251c%s\u253c%s\u253c%s\u2524\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW), strings.Repeat("\u2500", reqW))

		// Data rows.
		for _, f := range fields {
			reqStr := "no"
			if f.Required {
				reqStr = "yes"
			}
			fmt.Fprintf(w, "  \u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502\n",
				nameW-2, f.Name, typeW-2, f.Type, reqW-2, reqStr)
		}

		// Bottom border.
		fmt.Fprintf(w, "  \u2514%s\u2534%s\u2534%s\u2518\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW), strings.Repeat("\u2500", reqW))
	} else {
		// Two-column table (no Required column).

		// Top border.
		fmt.Fprintf(w, "  \u250c%s\u252c%s\u2510\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW))

		// Header row.
		fmt.Fprintf(w, "  \u2502 %-*s\u2502 %-*s\u2502\n",
			nameW-2, "Field", typeW-2, "Type")

		// Header separator.
		fmt.Fprintf(w, "  \u251c%s\u253c%s\u2524\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW))

		// Data rows.
		for _, f := range fields {
			fmt.Fprintf(w, "  \u2502 %-*s\u2502 %-*s\u2502\n",
				nameW-2, f.Name, typeW-2, f.Type)
		}

		// Bottom border.
		fmt.Fprintf(w, "  \u2514%s\u2534%s\u2518\n",
			strings.Repeat("\u2500", nameW), strings.Repeat("\u2500", typeW))
	}
}

// ---------------------------------------------------------------------------
// Markdown rendering helpers
// ---------------------------------------------------------------------------

func (r *ContractRenderer) renderMarkdownLayer(w io.Writer, layer *domain.ContractLayer) {
	fmt.Fprintf(w, "## Layer %d: %s\n\n", layer.Number, layer.Name)

	if layer.File != "" {
		loc := layer.File
		if layer.Line > 0 {
			loc = fmt.Sprintf("%s:%d", layer.File, layer.Line)
		}
		fmt.Fprintf(w, "**File:** `%s`\n\n", loc)
	}

	if layer.Signature != "" {
		fmt.Fprintln(w, "```")
		fmt.Fprintln(w, layer.Signature)
		fmt.Fprintln(w, "```")
		fmt.Fprintln(w)
	}

	if layer.InputType != nil {
		fmt.Fprintf(w, "### Input: %s\n\n", layer.InputType.Name)
		fmt.Fprintln(w, "| Field | Type | Required |")
		fmt.Fprintln(w, "|-------|------|----------|")
		for _, f := range layer.InputType.Fields {
			req := "no"
			if f.Required {
				req = "yes"
			}
			fmt.Fprintf(w, "| %s | %s | %s |\n", f.Name, f.Type, req)
		}
		fmt.Fprintln(w)
	}

	if layer.OutputType != nil {
		fmt.Fprintf(w, "### Response: %s\n\n", layer.OutputType.Name)
		fmt.Fprintln(w, "| Field | Type |")
		fmt.Fprintln(w, "|-------|------|")
		for _, f := range layer.OutputType.Fields {
			fmt.Fprintf(w, "| %s | %s |\n", f.Name, f.Type)
		}
		fmt.Fprintln(w)
	}

	if layer.DelegateTo != "" {
		fmt.Fprintf(w, "**Delegates to:** `%s`\n\n", layer.DelegateTo)
	}

	if len(layer.Notes) > 0 {
		for _, note := range layer.Notes {
			fmt.Fprintf(w, "- %s\n", note)
		}
		fmt.Fprintln(w)
	}
}

// ---------------------------------------------------------------------------
// Color helpers (reusing constants from terminal_reporter.go)
// ---------------------------------------------------------------------------

func (r *ContractRenderer) c(ansiColor, text string) string {
	if !r.color {
		return text
	}
	return ansiColor + text + colorReset
}
