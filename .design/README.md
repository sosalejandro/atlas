# Design sources

Working files for the published design canvases. Each `.dc.html` is one
artboard; `canvas.json` lays them out. The single-file `atlas-*.html` build in
each directory is a 3 MB artifact and is not tracked — it is regenerated from
these sources.

| Directory | Canvas | What it covers |
| --- | --- | --- |
| `.design/` | Atlas Desktop Console (v1) | 12 artboards: four decision-first screens plus the eight pages from `docs/GUI_WIREFRAMES.md`, rebuilt on the SQLite model |
| `.design/v2/` | Atlas Console v2 | 13 artboards on two pages: the verification-first workflow, and the diagram surfaces (sequence, flow, ERD, state machine, authoring, specs) |

v2 supersedes v1's information architecture — see issue #124. Both are kept
because v1 documents the screens v2 folded away, and the argument for folding
them is easier to check with both in front of you.

Tokens come from the implemented app (`internal/server/templates/base.html`,
Material-3 dark + Inter/JetBrains Mono), not from `docs/GUI_WIREFRAMES.md`,
which still specifies a GitHub-style palette the implementation moved past.
