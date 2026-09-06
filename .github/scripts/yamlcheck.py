#!/usr/bin/env python3
"""Parse every workflow and action file, and check the shape CI depends on.

The workflows cannot be executed from a laptop, so the properties that can
be checked without a runner are checked here and asserted by
scripts_test.sh. This catches the two failures that otherwise only surface
after a push: YAML that does not parse (the workflow silently never runs)
and a job with no steps.

Usage: yamlcheck.py <repo-root>
"""

import sys
import pathlib

try:
    import yaml
except ImportError:  # pragma: no cover - environment-dependent
    print("PyYAML not installed; cannot validate workflow YAML", file=sys.stderr)
    sys.exit(2)


def files(root: pathlib.Path):
    wf = root / ".github" / "workflows"
    if wf.is_dir():
        for p in sorted(wf.iterdir()):
            if p.suffix in (".yml", ".yaml"):
                yield p
    actions = root / ".github" / "actions"
    if actions.is_dir():
        yield from sorted(actions.rglob("action.yml"))
        yield from sorted(actions.rglob("action.yaml"))


def check(path: pathlib.Path) -> list[str]:
    problems: list[str] = []
    try:
        doc = yaml.safe_load(path.read_text())
    except yaml.YAMLError as exc:
        return [f"{path}: does not parse: {exc}"]
    if not isinstance(doc, dict):
        return [f"{path}: top level is {type(doc).__name__}, expected a mapping"]

    if path.name.startswith("action."):
        # A composite action needs runs.steps; an action whose steps list is
        # empty is a no-op that reports success, which is worse than a
        # missing action because it looks like it ran.
        runs = doc.get("runs")
        if not isinstance(runs, dict):
            problems.append(f"{path}: no 'runs:' block")
        elif runs.get("using") == "composite" and not runs.get("steps"):
            problems.append(f"{path}: composite action declares no steps")
        return problems

    # `on:` is parsed by PyYAML as the boolean True (YAML 1.1 treats `on` as
    # a boolean). Accept either spelling rather than pretending the file is
    # malformed.
    if "on" not in doc and True not in doc:
        problems.append(f"{path}: no trigger ('on:') block")
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict) or not jobs:
        problems.append(f"{path}: no jobs")
        return problems
    for name, job in jobs.items():
        if not isinstance(job, dict):
            problems.append(f"{path}: job {name} is not a mapping")
            continue
        if "uses" in job:  # reusable workflow call, has no steps of its own
            continue
        steps = job.get("steps")
        if not steps:
            problems.append(f"{path}: job {name} has no steps")
    return problems


def main() -> int:
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    seen = 0
    problems: list[str] = []
    for path in files(root):
        seen += 1
        problems.extend(check(path))
    if seen == 0:
        print("no workflow or action files found", file=sys.stderr)
        return 1
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        return 1
    print(f"{seen} workflow/action file(s) parse and are structurally sane")
    return 0


if __name__ == "__main__":
    sys.exit(main())
