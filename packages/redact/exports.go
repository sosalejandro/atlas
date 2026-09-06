package redact

// This file answers the second half of the question a security review asks.
// Take() says what the database holds; this says what leaves it, and where
// each thing goes.
//
// Nothing here describes a network transfer, because atlas performs none.
// Every entry is a local file or a stream handed to a process the operator
// started. That is the whole of the egress story today, and the point of
// writing it down as data rather than prose is that internal/cli's
// security_test.go can check the list against the actual command tree: a
// verb added without a decision about what it discloses fails the build.

// Export is one surface through which indexed content leaves the state
// database.
type Export struct {
	// Verb is the cobra command path ("report sarif"), or "" for a surface
	// that is not one command -- the --json envelope, say, which every verb
	// has.
	Verb string `json:"verb,omitempty"`

	// Surface is how a person refers to it.
	Surface string `json:"surface"`

	// Destination is where the bytes go. Every value is local: a file, a
	// standard stream, or a pipe to a process the operator launched.
	Destination string `json:"destination"`

	// Classes are the content classes this surface can carry. The list is
	// what it CAN carry, not what a particular invocation did.
	Classes []Class `json:"classes"`

	// Note says what a reader would otherwise have to read the code to
	// learn -- usually the reason a class is or is not in the list.
	Note string `json:"note"`
}

// Exports returns the catalogue of surfaces that emit indexed content.
func Exports() []Export { return exports }

// ExportsFor returns the catalogue entries whose verb path starts with the
// given verb, or every entry when verb is empty. It is what backs
// `atlas security --export <verb>`: "what would THIS command disclose".
func ExportsFor(verb string) []Export {
	if verb == "" {
		return exports
	}
	var out []Export
	for _, e := range exports {
		if e.Verb == verb || hasVerbPrefix(e.Verb, verb) {
			out = append(out, e)
		}
	}
	return out
}

// hasVerbPrefix reports whether path is verb or a subcommand of it, matching
// whole path segments so "cov" does not match a hypothetical "coverage".
func hasVerbPrefix(path, verb string) bool {
	if len(path) <= len(verb) {
		return false
	}
	return path[:len(verb)] == verb && path[len(verb)] == ' '
}

// allClasses is every class, for the surfaces that can carry anything the
// database holds.
var allClasses = []Class{
	ClassPath, ClassIdentifier, ClassSourceText, ClassUserText, ClassEnum, ClassDigest,
}

// structuralClasses is paths, names and closed vocabularies: the shape of
// the system without any verbatim source text.
var structuralClasses = []Class{ClassPath, ClassIdentifier, ClassEnum}

var exports = []Export{
	{
		Verb:        "onboard",
		Surface:     "the provisional capability map",
		Destination: "a local file, .atlas/provisional/capabilities.json",
		Classes:     structuralClasses,
		Note: "package and directory names, symbol names, route paths and the " +
			"table names a capability touches. It carries no verbatim source " +
			"text -- the inference reads structure, not bodies -- but a route " +
			"path and a table name are still proprietary shape, and this file " +
			"is the one onboarding artifact a user is most likely to paste " +
			"into an issue or a chat while asking what atlas found.",
	},
	{
		Verb:        "",
		Surface:     "the state database",
		Destination: "a local file, .atlas/atlas.db by default",
		Classes:     allClasses,
		Note: "written by init, scan, snapshot, cov, sql, flow and trend. " +
			"Copying this file copies everything `atlas security` lists, " +
			"including the verbatim source text columns.",
	},
	{
		Verb:        "",
		Surface:     "--json envelope",
		Destination: "stdout",
		Classes:     allClasses,
		Note: "every verb accepts --json. What a given envelope carries is " +
			"whatever that verb reads: `atlas sql --json` includes query " +
			"text, `atlas flow --json` includes branch conditions.",
	},
	{
		Verb:        "report sarif",
		Surface:     "SARIF 2.1.0",
		Destination: "stdout, or the file named by --out",
		Classes:     structuralClasses,
		Note: "findings carry a path, a line span, a rule id and a message " +
			"naming features, symbols and scores. The audit, coverage and " +
			"dead-code producers quote no source text, so no query text or " +
			"doc comment reaches this file today.",
	},
	{
		Verb:        "report github",
		Surface:     "GitHub Actions workflow annotations",
		Destination: "stdout, which the runner copies into the job log",
		Classes:     structuralClasses,
		Note: "the same findings as SARIF. Job logs are readable by anyone " +
			"who can read the repository's Actions tab, which is a wider " +
			"audience than the repository on some plans.",
	},
	{
		Verb:        "report pr",
		Surface:     "sticky pull-request comment markdown",
		Destination: "stdout; the operator pipes it to `gh pr comment`",
		Classes:     structuralClasses,
		Note: "atlas never calls the GitHub API itself. The comment becomes " +
			"public the moment it is posted on a public repository.",
	},
	{
		Verb:        "cov run",
		Surface:     "per-test Go coverage profiles",
		Destination: "one <test symbol>.out file per test, in the directory named by --out",
		Classes:     []Class{ClassPath, ClassIdentifier},
		Note: "coverprofiles: file paths, line ranges and execution counts. " +
			"The identifier disclosure is in the FILENAMES, which are the " +
			"tests' qualified symbol names. No source text.",
	},
	{
		Verb:        "cov run",
		Surface:     "coverage counter snapshots",
		Destination: "the directory named by --work, kept only with --keep",
		Classes:     []Class{ClassPath, ClassIdentifier},
		Note: "Go coverage meta and counter files plus atlas's own plan and " +
			"report JSON. A temporary directory by default, discarded after " +
			"the run unless --keep is passed.",
	},
	{
		Verb:        "mcp",
		Surface:     "MCP tool results",
		Destination: "stdout, as JSON-RPC to the client process that spawned it",
		Classes:     structuralClasses,
		Note: "read-only by construction and capped per tool. The client is " +
			"usually an editor talking to a model provider, so treat this " +
			"as the one surface where indexed content routinely reaches a " +
			"third party -- through the client, never through atlas.",
	},
	{
		Verb:        "snapshot",
		Surface:     "serialised index blob",
		Destination: "the snapshots table of the state database",
		Classes:     allClasses,
		Note: "the largest single disclosure atlas creates: the whole index " +
			"as JSON, including every symbol's doc comment and signature. " +
			"Nothing else in the database holds doc comments.",
	},
	{
		Verb:        "migrate-annotations",
		Surface:     "rewritten source files",
		Destination: "the repository's own files, in place",
		Classes:     nil,
		Note: "the only command that writes to the working tree. It moves " +
			"annotations between comment grammars; it emits nothing and " +
			"discloses nothing.",
	},
}
