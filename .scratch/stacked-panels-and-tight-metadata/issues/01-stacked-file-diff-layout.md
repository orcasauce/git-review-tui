# 01 — Stacked file/diff layout

Status: done

## Parent

PRD: `.scratch/stacked-panels-and-tight-metadata/PRD.md`

## What to build

Replace the side-by-side files/diff bottom row with a stacked, full-width files-above-diff layout. The files panel takes only the rows it needs (1 title row + 1 row per file) up to a maximum of 8 rows, with a floor of 2 rows. The diff panel takes every remaining row in the bottom region. The top row (log + message) is unchanged.

In small mode (width < 100 columns), the top row continues to collapse to a single full-width panel that swaps log ↔ message via `enter` (controlled by the existing `topShowRight` field). The bottom panels remain stacked full-width in small mode — they're already full-width, so no swap is needed.

Delete the `bottomShowRight` field and every code path that depended on it: the small-mode branch of the `q`/`esc` handler, the small-mode branch of the `enter` handler, the mouse-click handlers that toggled it, and the panel-rect helpers' branches. The `q`/`esc` back-nav semantics (files → log → quit) survive unchanged; only the small-mode pre-step that un-swapped the bottom is removed. Update the help modal's `enter` description to drop the `(small mode: swap to right panel)` parenthetical.

Rename `Layout.BottomLeft` / `Layout.BottomRight` to `Layout.Files` / `Layout.Diff` so the field names reflect the new stacked geometry instead of the old horizontal split. Extend `layout.Compute` to take a `numFiles int` parameter that drives the files-panel height; every call site in `main.go` is updated to pass the current file count. During files loading, callers pass the previously-rendered file count so the panel height doesn't jump while data streams in.

`MinCols` (80) and `MinRows` (24) are unchanged; the math still fits (status 1 + topH 5 + files 2 + diff 16 = 24 with no gaps yet — inter-panel gaps come in slice 02). The 20% `TopRowFraction` is unchanged.

## Acceptance criteria

- [x] `Layout` struct exposes `Files` and `Diff` rectangles (not `BottomLeft`/`BottomRight`)
- [x] `layout.Compute` signature is `Compute(w, h, numFiles int) Layout`
- [x] Files panel height equals `min(1+numFiles, 8)` with a floor of 2 in both small and full modes
- [x] Diff panel takes the remainder of the bottom region
- [x] In small mode, `TopRight.W == 0`; `Files.W` and `Diff.W` both equal the terminal width
- [x] `enter` in small mode swaps the top row only; the bottom is always shown stacked
- [x] `q`/`esc` ladder is files → log → quit; no small-mode pre-step
- [x] `bottomShowRight` field and all references to it are removed
- [x] Help modal's `enter` description no longer mentions the bottom-panel swap
- [x] During files loading after a commit selection, the panel height holds the previous count rather than collapsing to 2 (callers pass `len(m.files)`, which is not cleared on selection change)
- [x] `layout_test.go` covers: variable `numFiles` driving `Files.H`; files floor (2 rows when `numFiles == 0`); files cap (8 rows when `numFiles >= 7`); small-mode `TopRight` zero-sized but `Files`/`Diff` full-width; `TooSmall` boundary unchanged at 80×24

## Blocked by

None — can start immediately.
