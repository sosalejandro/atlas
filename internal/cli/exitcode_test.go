package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The property this whole file exists for: a command that COULD NOT CHECK
// must not be indistinguishable from one that checked and found a problem.
// Asserting the constants differ is not enough -- the assertions below drive
// the real command tree and compare the status a CI job would actually see.

func TestExitCodeFor_Mapping(t *testing.T) {
	sentinel := errors.New("boom")
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil is success", nil, ExitOK},
		{"an unclassified error stays a finding", sentinel, ExitFinding},
		{"an explicit code is honoured", &ExitError{Code: ExitUndetermined, Err: sentinel}, ExitUndetermined},
		{"usage is distinct", usagef("bad flag"), ExitUsage},
		{"undetermined is distinct", undeterminedf("cannot tell"), ExitUndetermined},
		{"a wrapped ExitError is still found", fmt.Errorf("ctx: %w",
			&ExitError{Code: ExitUndetermined, Err: sentinel}), ExitUndetermined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCodeFor(tc.err); got != tc.want {
				t.Errorf("ExitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// Wrapping must not sever errors.Is/As, or classifying an error would cost
// the caller the ability to inspect what it wrapped.
func TestExitError_PreservesTheWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("read index: %w", os.ErrNotExist)
	err := &ExitError{Code: ExitUndetermined, Err: wrapped}
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("errors.Is no longer reaches through ExitError")
	}
	if !strings.Contains(err.Error(), "read index") {
		t.Errorf("Error() = %q, want the wrapped message", err.Error())
	}
}

// The four codes must be four codes. A refactor that collapsed any pair
// would silently restore the bug this contract removes.
func TestExitCodes_AreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ExitOK": ExitOK, "ExitFinding": ExitFinding,
		"ExitUsage": ExitUsage, "ExitUndetermined": ExitUndetermined,
	} {
		if prev, dup := seen[code]; dup {
			t.Errorf("%s and %s are both %d; the distinction is the contract", prev, name, code)
		}
		seen[code] = name
	}
}

// doctor: a --fail-on value it cannot parse is a bad invocation. Reporting it
// as 1 would say "your codebase has a problem" about a typo.
func TestDoctor_UnparseableFailOnIsUsageNotFinding(t *testing.T) {
	f := newDoctorFixture(t)
	_, _, err := runDoctorCmd(t, f, "--fail-on", "catastrophic")
	if err == nil {
		t.Fatal("an unparseable --fail-on must not be accepted")
	}
	if got := ExitCodeFor(err); got != ExitUsage {
		t.Errorf("exit = %d, want %d (usage): %v", got, ExitUsage, err)
	}
}

// A flag cobra itself rejects takes the same route, via the root's
// FlagErrorFunc, so subcommands do not each have to remember.
func TestRoot_UnknownFlagIsUsageNotFinding(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"doctor", "--not-a-real-flag"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("an unknown flag must not be accepted")
	}
	if got := ExitCodeFor(err); got != ExitUsage {
		t.Errorf("exit = %d, want %d (usage): %v", got, ExitUsage, err)
	}
}

func TestCovDiff_MissingBaseIsUsageNotFinding(t *testing.T) {
	f := newCovDiffFixture(t)
	_, err := execCovDiff(t, f)
	if err == nil {
		t.Fatal("cov diff without --base must not be accepted")
	}
	if got := ExitCodeFor(err); got != ExitUsage {
		t.Errorf("exit = %d, want %d (usage): %v", got, ExitUsage, err)
	}
}

// The case the contract is for, stated as one comparison. Same command, same
// flag, two situations that a caller must be able to tell apart:
//
//	fresh index, coverage genuinely below the floor -> 1, act on it
//	stale index, coverage unknowable                -> 3, go re-scan
//
// Before ExitCodeFor both were 1, so a pipeline that re-scanned and retried
// on a real shortfall was indistinguishable from one that ignored a broken
// index -- and the second is the one that ships untested code.
func TestCovDiff_StaleIndexUnderAGateIsUndeterminedNotAFinding(t *testing.T) {
	t.Run("fresh index, real shortfall, is a finding", func(t *testing.T) {
		f := newCovDiffFixture(t)
		f.seedSymbolsAndCoverage(t)
		f.touchBothSymbols(t) // re-indexes at HEAD, so nothing is stale
		_, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "80")
		if err == nil {
			t.Fatal("66.7% under a target of 80 must fail")
		}
		if got := ExitCodeFor(err); got != ExitFinding {
			t.Errorf("exit = %d, want %d (finding): %v", got, ExitFinding, err)
		}
	})

	t.Run("stale index under a gate is undetermined", func(t *testing.T) {
		f := newCovDiffFixture(t)
		f.seedSymbolsAndCoverage(t)
		// Index BEFORE the branch commit -- the ordinary CI mistake -- and
		// do not re-index after it.
		f.indexFilesAtHEAD(t, "pkg/a.go")
		changed := map[int]bool{}
		for i := 5; i <= 14; i++ {
			changed[i] = true
		}
		f.write(t, "pkg/a.go", numberedGo(40, "touched", changed))
		f.git(t, "add", "-A")
		f.git(t, "commit", "-q", "-m", "touch a symbol")

		_, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "80")
		if err == nil {
			t.Fatal("a gate asked of a stale index must not silently pass")
		}
		if got := ExitCodeFor(err); got != ExitUndetermined {
			t.Errorf("exit = %d, want %d (undetermined): %v", got, ExitUndetermined, err)
		}
		if !strings.Contains(err.Error(), "atlas scan") {
			t.Errorf("the message must name the remedy, got: %v", err)
		}
	})

	// Without a gate the same staleness is a caveat, not a refusal. Exiting
	// non-zero for a report nobody is deciding anything with is how a team
	// learns to ignore the exit code.
	t.Run("stale index without a gate only reports", func(t *testing.T) {
		f := newCovDiffFixture(t)
		f.seedSymbolsAndCoverage(t)
		f.indexFilesAtHEAD(t, "pkg/a.go")
		changed := map[int]bool{}
		for i := 5; i <= 14; i++ {
			changed[i] = true
		}
		f.write(t, "pkg/a.go", numberedGo(40, "touched", changed))
		f.git(t, "add", "-A")
		f.git(t, "commit", "-q", "-m", "touch a symbol")

		if _, err := execCovDiff(t, f, "--base", "main~1"); err != nil {
			t.Errorf("a stale index with no gate must still report: %v", err)
		}
	})
}
