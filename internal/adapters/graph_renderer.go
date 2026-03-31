package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
)

// Additional ANSI color codes not defined in terminal_reporter.go.
const (
	colorMagenta = "\033[35m"
)

// GraphRenderer renders dependency graph trace results in multiple output
// formats: terminal tree, Graphviz DOT, Mermaid, and JSON.
type GraphRenderer struct {
	color bool
}

// NewGraphRenderer creates a renderer. TTY detection is performed via the
// shared isTTY helper from terminal_reporter.go so that color output is
// enabled only when stdout is a real terminal.
func NewGraphRenderer() *GraphRenderer {
	return &GraphRenderer{
		color: isTTY(),
	}
}

// ---------------------------------------------------------------------------
// Terminal tree rendering
// ---------------------------------------------------------------------------

// RenderTrace outputs the trace tree to the given writer using box-drawing
// characters. File:line references are right-aligned, and node kinds are
// color-coded (handler=cyan, service=green, repository=yellow, query=magenta,
// external=red). Cycle and ambiguous markers are shown inline. A summary of
// confidence percentage and warnings is printed at the bottom.
func (r *GraphRenderer) RenderTrace(result *domain.TraceResult, w io.Writer) {
	if result == nil || result.Root == nil {
		fmt.Fprintln(w, "  (empty trace)")
		return
	}

	fmt.Fprintln(w)

	// Render the root node.
	r.writeTraceNode(w, result.Root, "", true, true)

	// Summary footer.
	fmt.Fprintln(w)
	confidencePct := int(result.Confidence * 100)
	label := r.colorize(colorBold, fmt.Sprintf("  Confidence: %d%%", confidencePct))
	fmt.Fprintf(w, "%s  |  Nodes: %d  |  Depth: %d", label, result.TotalNodes, result.MaxDepth)
	if len(result.Cycles) > 0 {
		fmt.Fprintf(w, "  |  Cycles: %d", len(result.Cycles))
	}
	fmt.Fprintln(w)

	for _, warn := range result.Warnings {
		fmt.Fprintf(w, "  %s %s\n", r.colorize(colorYellow, "WARNING:"), warn)
	}
	fmt.Fprintln(w)
}

// writeTraceNode recursively renders a single node and its children.
// prefix carries the box-drawing continuation characters accumulated from
// parent levels. isLast indicates whether this node is the last sibling.
// isRoot indicates the root (no prefix connector).
func (r *GraphRenderer) writeTraceNode(w io.Writer, tn *domain.TraceNode, prefix string, isLast, isRoot bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	// Build the connector string.
	var connector string
	if isRoot {
		connector = "  "
	} else if isLast {
		connector = prefix + "└─ "
	} else {
		connector = prefix + "├─ "
	}

	// Node label: colored by kind.
	nodeLabel := r.colorizeKind(tn.Node.Kind, tn.Node.ID)

	// Markers.
	markers := ""
	if tn.IsCycle {
		markers += " " + r.colorize(colorRed, "[CYCLE]")
	}
	for _, e := range edgesFor(tn) {
		if e.Ambiguous {
			markers += " " + r.colorize(colorYellow, "[AMBIGUOUS]")
			break
		}
	}

	// File:line reference.
	fileRef := ""
	if tn.Node.File != "" {
		if tn.Node.Line > 0 {
			fileRef = fmt.Sprintf("%s:%d", tn.Node.File, tn.Node.Line)
		} else {
			fileRef = tn.Node.File
		}
	}

	// Write the line. We right-pad the node label + markers, then append
	// the file reference dimmed.
	leftPart := connector + nodeLabel + markers
	if fileRef != "" {
		// Use a fixed reference column so file references roughly align.
		visibleLen := visibleLength(leftPart)
		const refCol = 60
		padding := refCol - visibleLen
		if padding < 2 {
			padding = 2
		}
		fmt.Fprintf(w, "%s%s%s\n", leftPart, strings.Repeat(" ", padding), r.colorize(colorDim, fileRef))
	} else {
		fmt.Fprintln(w, leftPart)
	}

	// Recurse into children (skip if cycle to prevent infinite recursion).
	if tn.IsCycle {
		return
	}

	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix += "   "
		} else {
			childPrefix += "│  "
		}
	}

	for i, child := range tn.Children {
		last := i == len(tn.Children)-1
		r.writeTraceNode(w, child, childPrefix, last, false)
	}
}

// edgesFor is a placeholder that returns an empty slice. The actual ambiguous
// state is not carried on TraceNode children today; it lives on domain.Edge.
// We inspect the IsCycle and the node itself; for ambiguous detection we rely
// on the graph edges passed through TraceResult.Cycles or future extensions.
// For now, this returns nil — the ambiguous marker is only shown when the
// TraceNode's parent edge is explicitly marked.
func edgesFor(_ *domain.TraceNode) []domain.Edge {
	return nil
}

// ---------------------------------------------------------------------------
// Graphviz DOT rendering
// ---------------------------------------------------------------------------

// RenderDOT outputs the graph in Graphviz DOT format suitable for rendering
// with `dot -Tpng` or `dot -Tsvg`. Node shapes and edge colors encode the
// graph semantics: handler=box, service=ellipse, repository=cylinder,
// query=note, external=diamond.
func (r *GraphRenderer) RenderDOT(result *domain.TraceResult, w io.Writer) {
	if result == nil || result.Root == nil {
		fmt.Fprintln(w, "digraph trace { }")
		return
	}

	fmt.Fprintln(w, "digraph trace {")
	fmt.Fprintln(w, "  rankdir=TB;")
	fmt.Fprintln(w, "  node [fontname=\"Helvetica\" fontsize=10];")
	fmt.Fprintln(w, "  edge [fontname=\"Helvetica\" fontsize=9];")
	fmt.Fprintln(w)

	visited := make(map[string]bool)
	r.writeDOTNode(w, result.Root, visited)

	fmt.Fprintln(w, "}")
}

// writeDOTNode recursively emits a node declaration and its outgoing edges.
func (r *GraphRenderer) writeDOTNode(w io.Writer, tn *domain.TraceNode, visited map[string]bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	nodeID := dotSafe(tn.Node.ID)

	if !visited[tn.Node.ID] {
		visited[tn.Node.ID] = true

		shape := dotShape(tn.Node.Kind)
		tooltip := tn.Node.Doc
		if tooltip == "" {
			tooltip = tn.Node.Signature
		}

		label := tn.Node.ID
		if tn.Node.File != "" {
			label += fmt.Sprintf("\\n%s", tn.Node.File)
			if tn.Node.Line > 0 {
				label += fmt.Sprintf(":%d", tn.Node.Line)
			}
		}

		fmt.Fprintf(w, "  %s [label=%q shape=%s tooltip=%q];\n",
			nodeID, label, shape, tooltip)
	}

	if tn.IsCycle {
		return
	}

	for _, child := range tn.Children {
		childID := dotSafe(child.Node.ID)

		attrs := []string{}
		if child.IsCycle {
			attrs = append(attrs, "color=red", "style=dashed", `label="cycle"`)
		}
		// Check for ambiguous (simple heuristic: flagged on the child node or edge).
		// Since TraceNode doesn't carry edge metadata today, we skip this for now.

		attrStr := ""
		if len(attrs) > 0 {
			attrStr = " [" + strings.Join(attrs, " ") + "]"
		}

		fmt.Fprintf(w, "  %s -> %s%s;\n", nodeID, childID, attrStr)
		r.writeDOTNode(w, child, visited)
	}
}

// dotShape returns the Graphviz shape for a node kind.
func dotShape(kind domain.NodeKind) string {
	switch kind {
	case domain.NodeHandler:
		return "box"
	case domain.NodeService:
		return "ellipse"
	case domain.NodeRepository:
		return "cylinder"
	case domain.NodeQuery:
		return "note"
	case domain.NodeExternal:
		return "diamond"
	case domain.NodeComponent:
		return "component"
	case domain.NodeHook:
		return "cds"
	case domain.NodeEndpoint:
		return "box3d"
	default:
		return "ellipse"
	}
}

// dotSafe converts a node ID to a valid DOT identifier by quoting it.
func dotSafe(id string) string {
	return fmt.Sprintf("%q", id)
}

// ---------------------------------------------------------------------------
// Mermaid rendering
// ---------------------------------------------------------------------------

// RenderMermaid outputs the graph in Mermaid flowchart syntax suitable for
// embedding in Markdown. Different node shapes represent different kinds,
// and cycle edges use dotted lines.
func (r *GraphRenderer) RenderMermaid(result *domain.TraceResult, w io.Writer) {
	if result == nil || result.Root == nil {
		fmt.Fprintln(w, "flowchart TD")
		return
	}

	fmt.Fprintln(w, "flowchart TD")

	visited := make(map[string]bool)
	r.writeMermaidNode(w, result.Root, visited)

	// Style cycle edges.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  %% Style definitions")
	fmt.Fprintln(w, "  classDef handler fill:#e0f7fa,stroke:#00acc1")
	fmt.Fprintln(w, "  classDef service fill:#e8f5e9,stroke:#43a047")
	fmt.Fprintln(w, "  classDef repository fill:#fff8e1,stroke:#f9a825")
	fmt.Fprintln(w, "  classDef query fill:#f3e5f5,stroke:#8e24aa")
	fmt.Fprintln(w, "  classDef external fill:#ffebee,stroke:#e53935")

	// Assign classes to nodes.
	nodeClasses := make(map[string]string)
	collectMermaidClasses(result.Root, nodeClasses, make(map[string]bool))
	for id, cls := range nodeClasses {
		fmt.Fprintf(w, "  class %s %s\n", mermaidSafe(id), cls)
	}
}

// writeMermaidNode recursively emits nodes and edges.
func (r *GraphRenderer) writeMermaidNode(w io.Writer, tn *domain.TraceNode, visited map[string]bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	nodeID := mermaidSafe(tn.Node.ID)

	if !visited[tn.Node.ID] {
		visited[tn.Node.ID] = true
		shape := mermaidShape(tn.Node.Kind, tn.Node.ID)
		fmt.Fprintf(w, "  %s\n", shape)
	}

	if tn.IsCycle {
		return
	}

	for _, child := range tn.Children {
		childID := mermaidSafe(child.Node.ID)

		if child.IsCycle {
			// Dotted line for cycles.
			fmt.Fprintf(w, "  %s -.->|cycle| %s\n", nodeID, childID)
		} else {
			fmt.Fprintf(w, "  %s --> %s\n", nodeID, childID)
		}

		r.writeMermaidNode(w, child, visited)
	}
}

// mermaidShape returns the Mermaid node declaration with the appropriate shape.
func mermaidShape(kind domain.NodeKind, id string) string {
	safe := mermaidSafe(id)
	label := mermaidLabel(id)
	switch kind {
	case domain.NodeHandler:
		return fmt.Sprintf("%s[%s]", safe, label) // rectangle
	case domain.NodeService:
		return fmt.Sprintf("%s(%s)", safe, label) // rounded
	case domain.NodeRepository:
		return fmt.Sprintf("%s[(%s)]", safe, label) // cylinder
	case domain.NodeQuery:
		return fmt.Sprintf("%s>%s]", safe, label) // asymmetric (flag)
	case domain.NodeExternal:
		return fmt.Sprintf("%s{%s}", safe, label) // diamond
	case domain.NodeComponent:
		return fmt.Sprintf("%s(%s)", safe, label) // rounded
	case domain.NodeHook:
		return fmt.Sprintf("%s([%s])", safe, label) // stadium
	case domain.NodeEndpoint:
		return fmt.Sprintf("%s[[%s]]", safe, label) // subroutine
	default:
		return fmt.Sprintf("%s(%s)", safe, label)
	}
}

// mermaidSafe converts an ID to a valid Mermaid identifier by replacing
// dots and slashes with underscores.
func mermaidSafe(id string) string {
	replacer := strings.NewReplacer(
		".", "_",
		"/", "_",
		"-", "_",
		" ", "_",
		":", "_",
	)
	return replacer.Replace(id)
}

// mermaidLabel wraps the label in quotes to handle special characters.
func mermaidLabel(id string) string {
	return fmt.Sprintf("%q", id)
}

// collectMermaidClasses walks the trace tree and maps node IDs to their
// Mermaid class name for styling.
func collectMermaidClasses(tn *domain.TraceNode, classes map[string]string, visited map[string]bool) {
	if tn == nil || tn.Node == nil {
		return
	}
	if visited[tn.Node.ID] {
		return
	}
	visited[tn.Node.ID] = true

	switch tn.Node.Kind {
	case domain.NodeHandler:
		classes[tn.Node.ID] = "handler"
	case domain.NodeService:
		classes[tn.Node.ID] = "service"
	case domain.NodeRepository:
		classes[tn.Node.ID] = "repository"
	case domain.NodeQuery:
		classes[tn.Node.ID] = "query"
	case domain.NodeExternal:
		classes[tn.Node.ID] = "external"
	}

	for _, child := range tn.Children {
		collectMermaidClasses(child, classes, visited)
	}
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

// jsonTrace is the JSON-serializable representation of a TraceResult.
type jsonTrace struct {
	Root       *jsonTraceNode `json:"root"`
	TotalNodes int            `json:"total_nodes"`
	MaxDepth   int            `json:"max_depth"`
	Confidence float64        `json:"confidence"`
	Cycles     []domain.Edge  `json:"cycles,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}

// jsonTraceNode is the JSON-serializable representation of a TraceNode.
type jsonTraceNode struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	File     string           `json:"file,omitempty"`
	Line     int              `json:"line,omitempty"`
	Doc      string           `json:"doc,omitempty"`
	IsCycle  bool             `json:"is_cycle,omitempty"`
	Children []*jsonTraceNode `json:"children,omitempty"`
}

// RenderJSON outputs the trace as pretty-printed JSON.
func (r *GraphRenderer) RenderJSON(result *domain.TraceResult, w io.Writer) {
	if result == nil {
		fmt.Fprintln(w, "{}")
		return
	}

	jt := jsonTrace{
		Root:       toJSONTraceNode(result.Root),
		TotalNodes: result.TotalNodes,
		MaxDepth:   result.MaxDepth,
		Confidence: result.Confidence,
		Cycles:     result.Cycles,
		Warnings:   result.Warnings,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(jt)
}

// toJSONTraceNode recursively converts a domain TraceNode to the JSON form.
func toJSONTraceNode(tn *domain.TraceNode) *jsonTraceNode {
	if tn == nil || tn.Node == nil {
		return nil
	}

	jn := &jsonTraceNode{
		ID:      tn.Node.ID,
		Kind:    string(tn.Node.Kind),
		File:    tn.Node.File,
		Line:    tn.Node.Line,
		Doc:     tn.Node.Doc,
		IsCycle: tn.IsCycle,
	}

	for _, child := range tn.Children {
		jn.Children = append(jn.Children, toJSONTraceNode(child))
	}

	return jn
}

// ---------------------------------------------------------------------------
// Diagnosis rendering
// ---------------------------------------------------------------------------

// RenderDiagnosis outputs the result of a diagnose command, showing the
// feature being diagnosed, the matched symptom rule, and the relevant
// portion of the dependency trace.
func (r *GraphRenderer) RenderDiagnosis(feature string, symptom string, rule *domain.SymptomRule, trace *domain.TraceResult, w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", r.colorize(colorBold, "Diagnosis Report"))
	fmt.Fprintf(w, "  Feature:  %s\n", feature)
	fmt.Fprintf(w, "  Symptom:  %s\n", symptom)
	fmt.Fprintln(w)

	if rule != nil {
		fmt.Fprintf(w, "  %s\n", r.colorize(colorBold, "Matched Rule"))
		fmt.Fprintf(w, "  Pattern:      %s\n", rule.Pattern)
		fmt.Fprintf(w, "  Layer:        %s\n", rule.Layer)
		fmt.Fprintf(w, "  Description:  %s\n", r.colorize(colorGreen, rule.Description))
		fmt.Fprintf(w, "  Check order:  %s\n", strings.Join(rule.CheckOrder, " -> "))
		fmt.Fprintln(w)
	} else {
		fmt.Fprintf(w, "  %s\n", r.colorize(colorYellow, "No matching diagnostic rule found for this symptom."))
		fmt.Fprintln(w)
	}

	if trace != nil && trace.Root != nil {
		fmt.Fprintf(w, "  %s\n", r.colorize(colorBold, "Dependency Trace"))
		r.RenderTrace(trace, w)
	} else {
		fmt.Fprintf(w, "  %s\n", r.colorize(colorDim, "No dependency trace available."))
		fmt.Fprintln(w)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// colorize wraps text with an ANSI color code if color output is enabled.
func (r *GraphRenderer) colorize(color, text string) string {
	if !r.color {
		return text
	}
	return color + text + colorReset
}

// colorizeKind returns the node ID colored according to its kind.
func (r *GraphRenderer) colorizeKind(kind domain.NodeKind, id string) string {
	if !r.color {
		return id
	}
	switch kind {
	case domain.NodeHandler, domain.NodeEndpoint:
		return colorCyan + id + colorReset
	case domain.NodeService:
		return colorGreen + id + colorReset
	case domain.NodeRepository:
		return colorYellow + id + colorReset
	case domain.NodeQuery:
		return colorMagenta + id + colorReset
	case domain.NodeExternal:
		return colorRed + id + colorReset
	case domain.NodeComponent:
		return colorCyan + id + colorReset
	case domain.NodeHook:
		return colorGreen + id + colorReset
	default:
		return id
	}
}

// visibleLength returns the length of a string excluding ANSI escape sequences.
func visibleLength(s string) int {
	length := 0
	inEscape := false
	for _, ch := range s {
		if ch == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				inEscape = false
			}
			continue
		}
		length++
	}
	return length
}
