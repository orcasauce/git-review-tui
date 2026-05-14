# 02 — Panel margins

Status: done

## Parent

PRD: `.scratch/stacked-panels-and-tight-metadata/PRD.md`

## What to build

Insert 1-cell gaps between adjacent panels so the panel boundaries are visually distinct.

Vertical gaps (both modes): 1 blank row between the top row and the files panel; 1 blank row between the files panel and the diff panel.

Horizontal gap (full mode only): 1 blank column between `TopLeft` (log) and `TopRight` (message). Small mode has no horizontal split, so no horizontal gap there.

No gap at any terminal edge (top, sides, bottom). No gap between the diff panel and the status row — the status row sits flush against the diff and reads as terminal-edge chrome.

`layout.Compute` is the single place to handle this: the returned rectangles already account for the inserted blank cells, so `View()` composition naturally renders gaps where the rectangles don't touch. `View()` does not need to inject blank rows/columns at composition time; it just renders each rectangle in place.

`MinCols` (80) and `MinRows` (24) are unchanged. The new gaps fit within the existing minimums (status 1 + topH 5 + gap 1 + files 2 + gap 1 + diff 14 = 24).

## Acceptance criteria

- [x] In full mode, `TopLeft.W + 1 + TopRight.W == w` (1 column gap between the two)
- [x] In full mode, `TopLeft.X == 0` and `TopRight.X == TopLeft.W + 1`
- [x] In both modes, `Files.Y == topH + 1` (1-row gap between top row and files)
- [x] In both modes, `Diff.Y == Files.Y + Files.H + 1` (1-row gap between files and diff)
- [x] Status row sits at `h - 1`, flush against `Diff` (no gap between diff bottom and status)
- [x] In small mode, no horizontal gap is introduced (top row is a single full-width panel)
- [x] Panels do not render into the gap cells; the cells are visually blank
- [x] `MinCols` (80) and `MinRows` (24) unchanged; the minimum-size case still produces non-empty `Diff`
- [x] `layout_test.go` covers: full-mode horizontal-gap math; vertical-gap math (files and diff Y offsets); small-mode absence of horizontal gap; flush diff/status boundary

## Blocked by

- `.scratch/stacked-panels-and-tight-metadata/issues/01-stacked-file-diff-layout.md` — the gap math is defined against the new stacked geometry.
