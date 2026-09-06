package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

// renamedVerbs maps a retired verb name to the verb that replaced it
// (issue #112). Both names dispatch to the same command; the old one is a
// cobra alias, so scripts that already say `atlas trace` keep working.
//
// Why the old names survive at all: docs/annotations.md already promises
// that an unknown annotation kind degrades to a one-time advisory warning
// rather than an error, and a CLI verb is a harder dependency than an
// annotation -- it is in someone's CI file, not in their source. The
// aliases are the same promise applied to the command surface, and they
// last one minor version, which is what the warning below says out loud so
// nobody has to guess.
//
// Why the renames were worth making anyway:
//
//   - trace -> chain. "Trace" means a distributed trace to every engineer
//     who has ever opened Jaeger, and atlas is about to ingest real OTel
//     spans (#94). Two things called trace, one of them static and one of
//     them runtime, is a permanent tax on every conversation about either.
//     "Chain" is what the command actually walks; "trace" is now reserved
//     for the runtime thing.
//
//   - audit -> health. The command said audit, the type said FeatureHealth,
//     and the docs said score. Three words for one number is how a reader
//     concludes there are three numbers. `health` matches the type; `audit`
//     survives as the alias because the compliance reading of the word is
//     an asset, not an accident.
var renamedVerbs = map[string]string{
	"trace": "chain",
	"audit": "health",
}

// aliasesFor returns the retired names that now dispatch to canonical, for
// use as a cobra command's Aliases. Sorted so the generated help text is
// stable.
func aliasesFor(canonical string) []string {
	var out []string
	for old, now := range renamedVerbs {
		if now == canonical {
			out = append(out, old)
		}
	}
	sort.Strings(out)
	return out
}

// noteIfRenamed prints the one-line deprecation notice when the command was
// invoked under a retired name.
//
// It goes to STDERR, always, including under --json. A structured consumer
// must be able to pipe stdout into jq without a courtesy message corrupting
// the envelope; a human running the old verb in a terminal still sees the
// line. That split is the same one stdoutOrJSON draws for every other verb.
func noteIfRenamed(cmd *cobra.Command) {
	calledAs := cmd.CalledAs()
	canonical, renamed := renamedVerbs[calledAs]
	if !renamed {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"note: `atlas %s` has been renamed to `atlas %s`; %s keeps working for one minor version\n",
		calledAs, canonical, calledAs)
}
