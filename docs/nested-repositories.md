# Nested repositories

**A git repository inside your repository is not part of it, and atlas does
not index it.** A directory containing `.git` — whether that is a directory
(a clone or submodule) or a file (a worktree) — is a boundary the scan does
not cross.

Set `scan.include_nested_repos: true` in `.atlas.yaml` to index them anyway.

## Why this is the default

Skipping by name cannot see a repository boundary. Atlas skips `vendor` and
`node_modules` by name, plus whatever `scan.skip_dirs` adds — so a dependency
*copied* into `vendor/` was excluded, and the same dependency *cloned* under
any other name was indexed as though you had written it.

The inflated file count is not the damage. The damage is that every number
downstream is then computed over a codebase that is not yours:

- symbol and edge counts, so `codebase` and `hotspots` rank foreign code;
- **the coverage denominator**, diluted by files no test of yours could ever
  execute, which makes coverage look worse and patch coverage look arbitrary;
- `feature.linkage`, because annotations in the nested repository materialise
  features here that belong to another product;
- `sql capabilities`, and every advisory computed from it.

It is silent in the worst direction. Nothing errors, and `atlas doctor`
reports the index is fresh — which it faithfully is, for a tree that includes
somebody else's repository.

This is not a corner case. It was found on atlas's own repository: a dogfood
run reported **584 SQL operations over 102 tables** where a clean checkout
reports **145 over 27**, because three git worktrees were sitting under
`.claude/worktrees/`. Nothing was wrong with the SQL analyser.

## What you will see

The scan says what it did not index:

```
warning: 1 nested git repository was not indexed (vendorclone); its files
belong to another repository, so counting them would dilute every coverage
denominator here. Set include_nested_repos to index them anyway.
```

A repository skipped silently is indistinguishable, in every number that
follows, from one that was never there — so atlas names them rather than
quietly dropping them.

## Submodules

Submodules are the one genuinely arguable case: they are separate
repositories by construction, but some teams consider them part of the
product. The default is still to skip them, because a submodule's code cannot
be covered by *this* repository's test run, and counting it as uncovered is
worse than not counting it at all. If your submodule is built and tested as
part of this repo, set `include_nested_repos: true`.

## What is not affected

The scan root itself. Atlas is nearly always run at the top of a repository,
which by definition contains `.git`; treating that as a boundary would make
every scan return nothing. `TestNestedRepo_ScanRootIsNeverSkipped` pins it.
