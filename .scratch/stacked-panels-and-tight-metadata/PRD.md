# Stacked file/diff layout + tightened commit metadata

Status: needs-triage

## Problem Statement

When reviewing commits, the bottom of the screen splits horizontally between the files panel and the diff panel, so the diff only ever has ~60% of the terminal width. Long source lines wrap or get truncated even when there's free space on the other side of the screen.

The commit-message metadata block above the message body burns 7 rows on information that is mostly redundant: the email already implies the author, the committer and commit-date almost always match the author and author-date, parent SHAs and bare `HEAD` decorations are noise, and the date uses ISO-8601's `T` separator with a timezone offset that's identical to the system's offset in the common case. The metadata block routinely pushes the actual commit message below the fold.

Panels visually touch each other at their borders, making it hard to tell at a glance where one panel ends and the next begins.

`q` / `esc` already navigates back from the files panel to the log panel, but the back-nav path is tangled with small-mode "right-panel swap" mechanics that no longer earn their complexity.

## Solution

Stack the files and diff panels vertically — both full-width. The files panel takes only the rows it needs (one per file, plus a title row) up to a maximum of 8 rows; the diff panel gets every remaining row. Insert a 1-cell gap between adjacent panels (1 column between log and message, 1 row between top row and files, 1 row between files and diff). Leave no gap at the terminal edges, and treat the status row as terminal-edge chrome (no gap between diff and status).

Reduce the commit-message metadata block to two lines: a `<short-sha> (<refs>)` line and a `<name> <<email>>  <date>` line, with the `Tags:` block appearing only when tags exist. Drop the `Author:` / `AuthorDate:` labels (the formats are self-evident) and the `Commit:` / `CommitDate:` / `Parents:` lines entirely. Drop bare `HEAD` from refs and rewrite `HEAD -> X` as just `X`, keeping the branch information without the redundant arrow.

Use a space (not `T`) between date and time. Show the timezone offset (compact `-hhmm`) only when the commit's offset differs from the system's offset at the commit's instant. Keep the trailing `(2 days ago)` relative date.

Simplify small-mode (width < 100) so that only the top row collapses to a swap-via-`enter` single column; the bottom panels are already stacked full-width and need no swap. Delete the `bottomShowRight` field and every branch that depended on it.

## User Stories

1. As a reviewer, I want the diff panel to span the full terminal width, so that long source lines don't wrap or get truncated when there's screen space to spare.
2. As a reviewer, I want the files panel above the diff to take only as many rows as the file list needs (up to a cap), so that the diff receives every spare row.
3. As a reviewer of a 3-file commit, I want the files panel to be exactly 4 rows tall (title + 3 files), so that I don't see wasted empty rows.
4. As a reviewer of a 30-file commit, I want the files panel to cap at 8 rows and scroll internally, so that the diff still has room to read.
5. As a reviewer of a clean merge with no conflicts resolved, I want the files panel to still render at least 2 rows (title + placeholder), so that the "Clean merge" message is visible.
6. As a reviewer waiting for files to load on a new selection, I want the files panel to hold its previously-rendered height, so that the layout doesn't jump as data streams in.
7. As a reviewer on a narrow terminal (<100 columns), I want the bottom files+diff panels to remain stacked and both visible, so that I don't have to swap between them.
8. As a reviewer on a narrow terminal, I want `enter` to still swap log ↔ message in the top row, so that I can read either side without horizontal cramping.
9. As a reviewer, I want a 1-cell gap between the log and message panels, so that the column boundary is visually obvious.
10. As a reviewer, I want a 1-row gap between the top row and the files panel, and between the files panel and the diff panel, so that adjacent panels are visually separable.
11. As a reviewer, I want no gap between any panel and the terminal edges, so that all available screen real estate is used.
12. As a reviewer, I want no gap between the diff panel and the status row, so that the status row reads as terminal-edge chrome rather than a separate panel.
13. As a reviewer focused on the files panel, I want `q` or `esc` to return focus to the log panel, so that I can keep browsing commits without exiting the program.
14. As a reviewer, I want the commit metadata to show the short SHA on its own line (not the full 40-character hash), so that the identifier is concise.
15. As a reviewer, I want branch and remote-tracking refs at the commit to appear in parentheses on the short-SHA line, so that I can see at a glance which branches point at this commit.
16. As a reviewer, I want bare `HEAD` decorations dropped from the metadata refs, so that I don't see redundant clutter when HEAD points at a branch already visible.
17. As a reviewer, I want `HEAD -> main` rewritten as just `main` in the metadata refs, so that I keep the branch information without the redundant arrow.
18. As a reviewer, I want the metadata refs to use the same per-kind coloring as the log row, so that the visual language is consistent.
19. As a reviewer, I want the author name, email, and commit date on a single line, so that I don't waste a row on a separate `AuthorDate:` line.
20. As a reviewer, I don't want to see `Author:` or `AuthorDate:` labels, because `Name <email>` and `YYYY-MM-DD HH:MM:SS` are self-evident.
21. As a reviewer, I don't want to see `Commit:`, `CommitDate:`, or `Parents:` lines, because they rarely add information beyond what's already in the author line.
22. As a reviewer, I want the commit date formatted with a space separating date and time (`2026-05-13 10:42:11`), so that the date is easier to read than ISO-`T` notation.
23. As a reviewer in the same timezone as the commit's author, I want no timezone offset displayed, so that the line is shorter when the offset would be redundant.
24. As a reviewer in a different timezone than the commit's author, I want the commit's original offset appended as compact `-hhmm`, so that I can tell when the author actually committed on their clock.
25. As a reviewer, I want the relative date `(2 days ago)` preserved alongside the absolute date, so that I get chronological context without doing arithmetic.
26. As a reviewer of a commit with no tags, I want no `Tags:` line in the metadata, so that the block stays as short as possible.
27. As a reviewer of a tagged commit, I want the existing `Tags:` block (with annotated-tag bodies indented below) preserved, so that release commits remain clearly marked.
28. As a reviewer, I want a blank separator line between the metadata block and the commit message body, so that the body is visually distinct from the header.
29. As a reviewer on the minimum supported terminal (80×24), I want the new layout to still fit (no "too small" fallback), so that the documented minimum remains usable.
30. As a reviewer resizing the terminal live, I want the gaps and panel heights to recompute correctly on every resize, so that the layout adapts smoothly.
31. As a reviewer, I want pressing `q`/`esc` while in the files panel to leave the panel viewport (scroll, file selection) untouched when I return later, so that I don't lose my place.
32. As a maintainer, I want the date formatter living in a focused package with a `*time.Location` injection point, so that I can unit-test same-TZ, different-TZ, and DST-boundary behavior without depending on the system clock.
33. As a maintainer, I want the ref-filter and metadata line-assembly logic out of `main.go`, so that I can refactor message-panel rendering without touching unrelated UI code.
34. As a maintainer, I want `layout`'s `BottomLeft` / `BottomRight` fields renamed to `Files` / `Diff`, so that future readers don't have to mentally translate "right" into "below".
35. As a maintainer, I want `bottomShowRight` and every code path that branches on it to be deleted, so that the small-mode mechanics shrink to just the top-row swap.

## Implementation Decisions

**Module: `layout/` (modified)**

- `Compute` signature extended to take the current file count: `Compute(w, h, numFiles int) Layout`. File count drives the height of the files panel; layout stays a pure function.
- `Layout` struct: `BottomLeft` / `BottomRight` renamed to `Files` / `Diff` to reflect the new stacked geometry. `TopLeft` / `TopRight` retained for the unchanged top row.
- Vertical gaps: 1 blank row between top-row bottom and files top, 1 blank row between files bottom and diff top. No gap between diff bottom and status row.
- Horizontal gap: 1 blank column between `TopLeft` and `TopRight` in non-small mode only.
- Files panel height: `min(1 + numFiles, 8)` with a floor of 2. The remainder of the bottom region goes to the diff panel.
- Small-mode (`w < SmallModeMinCols`) trigger unchanged at 100 columns. In small mode `TopRight` is zero-sized (caller swaps `TopLeft` content via `topShowRight`); `Files` and `Diff` rectangles are populated identically to full-mode.
- `MinCols` (80) and `MinRows` (24) unchanged; the new gaps fit within existing minimums (status 1 + topH 5 + gap 1 + files 2 + gap 1 + diff 14 = 24).
- `SmallMode` and `TooSmall` flags retained with their existing semantics.

**Module: `metadata/` (new, deep)**

- Encapsulates the entire commit-message header block. Single primary public function:
  - `Lines(d gitcmd.CommitDetail, loc *time.Location) []string` — returns the ordered list of lines for the message panel: short-sha + refs line, author/date line, optional `Tags:` block, blank separator, then the body lines.
- Internal helpers (not exported):
  - Ref filter: drop tags, drop bare `HEAD`, rewrite `HEAD -> X` to `X`. The remainder is fed through the existing `formatRefs`-style coloring (which the caller can apply, or the helper can return a `[]gitcmd.Ref` analog).
  - Date formatter: parses `AuthorDateISO`, computes the commit's offset at that instant, compares against `loc.Offset(commitInstant)`. Emits `"YYYY-MM-DD HH:MM:SS"`, conditionally appends `" -hhmm"`, then `" (<relative>)"`.
- Pure function — no global state, no I/O. `*time.Location` is injected at the call site (`time.Local` in production, fixed `time.FixedZone` values in tests).

**Module: `main.go` (modified)**

- Field `bottomShowRight` removed. Every branch reading or writing it is deleted in: `q` / `esc` handler, `enter` handler, mouse-click handler, mouse-wheel routing, panel-rect helpers (`diffPanelRect`, `filesPanelRect`, etc.), and `View()`.
- `View()` composition rebuilt: in full mode, render `TopLeft` ↔ 1-col-gap ↔ `TopRight` as `topRow`; then 1 blank row; then `Files`; then 1 blank row; then `Diff`; then status. In small mode, render the active top side (log or message based on `topShowRight`), then 1 blank row, then `Files`, then 1 blank row, then `Diff`, then status.
- `messageLines` and `messageLineCount` become thin wrappers around `metadata.Lines(m.detail, time.Local)`.
- `styleMessageLine`'s label list reduced to `Tags:` only (plus the SHA / refs styling which moves to a new dedicated path because the SHA is now bare and refs are inline parens with `formatRefs` coloring).
- Help modal text: drop the `(small mode: swap to right panel)` parenthetical from the `enter` description.

**Out-of-package considerations**

- The new metadata block changes the message panel's renderable line count, which feeds into `messagePanelBodyHeight` and the `j`/`k`/`d`/`u`/`gg`/`G` scroll clamps. These continue to work because they read `len(messageLines(...))`.
- The new `Files` field name propagates to every call site in `main.go` that previously read `BottomLeft`/`BottomRight`. The renames are mechanical.

## Testing Decisions

A good test verifies the externally observable behavior of a module — the rectangles a layout returns for given inputs, the line strings a metadata builder returns for a given commit. It does not pin internal helper signatures, internal field names, or step counts.

**`metadata/` (new tests)**

- Prior art: `gitcmd_test.go` constructs `CommitDetail` and `Commit` values to verify parsing; `layout_test.go` tests a pure function across boundary conditions. `metadata_test.go` follows the same pattern — construct `CommitDetail` values in code (no fixture repo needed), call `Lines`, assert the resulting `[]string`.
- Test cases to cover:
  - Date formatting with commit offset equal to the injected location's offset → no `-hhmm` appended.
  - Date formatting with commit offset different from the injected location → `-hhmm` appended.
  - Date formatting across a DST boundary (commit instant where the location's offset has changed) — verify comparison uses the location's offset *at the commit's instant*, not at "now".
  - Negative offsets (`-0700`), positive offsets (`+0530`, half-hour case), zero offset.
  - Refs: empty refs → no parens. Bare `HEAD` only → empty parens dropped (so no parens). `HEAD -> main` → `(main)`. `HEAD -> main, origin/main` → `(main, origin/main)`. With a tag in the list → tag filtered out.
  - End-to-end `Lines()` shape for: typical commit with one branch and no tags; commit with no refs and no tags; commit with annotated tag (multi-line body preserved with indentation); commit with empty body; merge commit (parents count > 1 — should still produce only the new header lines because `Parents` is no longer rendered).

**`layout/` (updated tests)**

- Existing `layout_test.go` exercises `Compute` across `SmallMode` and `TooSmall` boundaries. The same suite expands to cover:
  - Various `numFiles` values → `Files.H == min(1+numFiles, 8)` with floor 2, and `Diff.H == bottomRegionH - 1(gap) - filesH`.
  - Vertical gap rows are not included in any panel's rectangle (rectangle Y values reflect the inserted gaps).
  - Horizontal gap column not included in `TopLeft.W` + `TopRight.W` (they should sum to `w - 1` in full mode).
  - Small-mode geometry: `TopRight.W == 0`; `Files` and `Diff` still populated full-width.
  - `TooSmall` boundary unchanged at 80×24.
- Field-rename note: tests that read `BottomLeft` / `BottomRight` migrate to `Files` / `Diff` in the same diff.

**`main.go` (no new tests)**

- Manual verification in the TUI: View composition at multiple terminal sizes (80×24, 100×30, 200×60), key-handler simplifications (`q`/`esc` back-nav still works; `enter` in small mode swaps top row but not bottom; `enter` in full mode activates bottom section as before), and help-modal text. Consistent with the project's existing convention — `main.go` is integration-tested by running the binary.

## Out of Scope

- Changing the log + message side-by-side arrangement in the top row.
- Changing `TopRowFraction` (20%) or `MinTopRows` (5).
- Changing `MinCols` (80), `MinRows` (24), or `SmallModeMinCols` (100).
- Converting commit timestamps to the viewer's local clock time. The displayed time remains the commit's original-zone clock time; only the *offset* is conditionally hidden.
- Configuration knobs for the files-panel cap, gap size, date format, or any of the constants. All values are baked in.
- Changing the relative-date string (`"2 days ago"`); it's passed through verbatim from `gitcmd.CommitDetail.AuthorDateRel`.
- Date formatting in the log row (it stays on `c.RelDate`).
- Any change to the worktree picker, search, syntax highlighting, mouse handling, error status row, or other unrelated features.
- An ADR documenting the layout shift (4-panel grid → 3-band stacked-bottom layout). Could be written later; not blocking implementation.

## Further Notes

- The grilling pass that produced this PRD resolved every decision branch — no open questions for triage.
- Renaming `BottomLeft` / `BottomRight` to `Files` / `Diff` touches around a dozen call sites in `main.go`; the change is mechanical but worth doing in the same change to avoid stale names lingering after the geometry shift.
- The `metadata/` package needs `*time.Location` injected at every call site. Production code passes `time.Local`. Tests pass `time.FixedZone(...)` values to make offset comparisons deterministic.
- For the `HEAD -> X` rewrite, the simplest implementation is to detect the prefix on a `RefHEAD`-classified token, strip it, and re-classify the remainder for coloring purposes (it will be a local branch). The token order from `git log --decorate=short` is preserved.
- The new metadata block changes the message-panel scroll target (fewer lines), so `gg` / `G` clamps will be tighter by default — no special handling needed because they read `len(messageLines(...))` dynamically.
