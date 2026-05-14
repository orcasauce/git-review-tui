# 03 — Tightened commit metadata

Status: done

## Parent

PRD: `.scratch/stacked-panels-and-tight-metadata/PRD.md`

## What to build

Shrink the commit-message metadata block from 7 rows down to 2 (or 3+ when tags are present), drop redundant fields, rewrite HEAD-ref decorations, and switch the date format to a space-separated form with a conditional timezone offset.

Extract a new `metadata/` package as a deep module that owns the entire header block. The public interface is:

```
metadata.Lines(d gitcmd.CommitDetail, loc *time.Location) []string
```

`Lines` returns, in order: the short-sha + refs line, the author/date line, the optional `Tags:` block, a blank separator, then the body lines. `*time.Location` is injected by the caller so tests can pass a fixed zone (production passes `time.Local`).

**Line 1 — short sha + refs.** Render `d.ShortSHA`, then a space, then `(` + comma-separated refs + `)` when any survive the filter. Omit the parens entirely when no refs remain. The SHA itself keeps its current `msgSHAStyle` orange coloring; the refs reuse the same per-kind coloring as the log row (the existing `formatRefs` machinery). When no refs survive, the line is just the bare short SHA.

**Ref filter (metadata-only — log row is unchanged):** drop tags (they go in the dedicated Tags block); drop bare `HEAD`; rewrite `HEAD -> X` as just `X`, with the remainder classified and colored as a local branch. Preserve token order from `git log --decorate=short`.

**Line 2 — author + date.** `<name> <<email>>  <date>` — two spaces between the email's closing `>` and the date. No `Author:` or `AuthorDate:` label. The date format uses `YYYY-MM-DD HH:MM:SS` with a space (not `T`) between date and time. Append ` -hhmm` (compact, no colon) only when the commit's offset at the commit's instant differs from `loc`'s offset at that same instant — DST-aware comparison. Trailing ` (2 days ago)` (or whatever `d.AuthorDateRel` says) is always appended.

**Tags block.** Only rendered when `d.Tags` is non-empty. Preserves the existing format: `Tags: <name>` (or `<name> (annotated)`), with annotated-tag bodies indented underneath. The `Tags:` label is kept (the content isn't self-evident the way `Name <email>` is).

**Dropped from the metadata block:** `commit ` SHA prefix, `Commit:` line, `CommitDate:` line, `Parents:` line, the old standalone `Refs:` line.

**Body separator.** Blank line, then the commit body lines, identical to today.

**Call-site updates in `main.go`:**
- `messageLines(d)` becomes `metadata.Lines(d, time.Local)`.
- `messageLineCount(d)` becomes `len(metadata.Lines(d, time.Local))`.
- `styleMessageLine` simplified: the only remaining label-style line is `Tags:`. SHA styling and ref coloring are handled on the first metadata line directly (use `formatRefs` for refs; `msgSHAStyle` for the short sha; the date and author render plain or with the existing dim treatment, whichever matches today's look closest).

`metadata_test.go` covers:
- Date with commit offset equal to injected location offset → no `-hhmm` appended
- Date with commit offset different from location → `-hhmm` appended (compact, no colon)
- Date across a DST boundary — offset comparison uses location offset *at the commit's instant*, not "now"
- Negative offsets (`-0700`), positive offsets, half-hour offsets (`+0530`), UTC (`+0000`)
- Ref filter: empty refs → no parens; bare `HEAD` only → no parens; `HEAD -> main` → `(main)`; `HEAD -> main, origin/main` → `(main, origin/main)`; refs with a tag mixed in → tag filtered out
- End-to-end `Lines()` shape: typical commit with branch and no tags; commit with no refs and no tags; commit with annotated tag (multi-line tag body indented); commit with empty body; merge commit (parents count > 1 — only the 2-line header should appear, no `Parents:` row)

## Acceptance criteria

- [ ] New `metadata/` package compiles and is importable from `main`
- [ ] `metadata.Lines(d, loc)` is the single public entry point; no other exported helpers needed
- [ ] Metadata block is 2 lines (sha + refs / author + date) for a typical commit
- [ ] `Tags:` block appears only when `d.Tags` is non-empty
- [ ] `Parents`, `Commit:`, `CommitDate:`, and the standalone `Refs:` line are gone from the metadata block
- [ ] Short sha appears bare on line 1 (no `commit ` prefix), followed by refs in parens when any survive the filter
- [ ] Bare `HEAD` is dropped from metadata refs; `HEAD -> main` renders as `main` with branch coloring
- [ ] Log row refs are unaffected — HEAD still decorates commit rows
- [ ] Date format uses a space (not `T`) between date and time
- [ ] Offset suffix appears only when commit offset ≠ location offset at the commit's instant
- [ ] Relative date `(<rel>)` always appears as a trailing parenthetical
- [ ] `main.go`'s `messageLines` / `messageLineCount` / `styleMessageLine` delegate to / consume the `metadata` package
- [ ] `metadata_test.go` covers same-TZ, different-TZ, DST-boundary, half-hour offset cases for the date formatter
- [ ] `metadata_test.go` covers ref-filter cases (empty, bare HEAD, `HEAD -> X`, `HEAD -> X` with siblings, tag interleaved)
- [ ] `metadata_test.go` covers end-to-end `Lines()` for typical / no-refs / annotated-tag / empty-body / merge-commit fixtures

## Blocked by

None — independent of the layout slices.
