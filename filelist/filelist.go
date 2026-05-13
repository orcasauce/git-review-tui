// Package filelist is a pure formatter from gitcmd.FileStat-style
// records into rendered file list rows. It computes per-commit column
// widths, scales the +/- glyph bar, formats renames as "old → new",
// marks binary files, and left-truncates paths with a leading "…"
// when they overflow the available width.
package filelist

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxBarCells caps the width of the +/- glyph bar so very large commits
// don't squeeze the path column.
const MaxBarCells = 10

// File is the input record: a single file's status, paths, and counts.
// Mirrors gitcmd.FileStat shape without taking a dependency on it.
type File struct {
	Status   string // "A", "M", "D", "R", "C", "T", ...
	Path     string
	OldPath  string // empty unless rename/copy
	Added    int
	Deleted  int
	IsBinary bool
}

// Row is the pre-styling structured layout for one file list row.
// Path is already padded and left-truncated to fit; Plus and Minus are
// already right-padded to the per-commit column widths (or empty when
// the whole commit has zero of that kind). Pluses + Minuses sum to the
// glyph-bar cell count for this row.
type Row struct {
	Status  string
	Path    string
	Plus    string
	Minus   string
	Pluses  int
	Minuses int
}

// Format computes Row data for the given files at a given total panel
// width. Pure: no I/O, no styling. The caller is responsible for
// applying colors and joining the columns with the same gap used here.
func Format(files []File, width int) []Row {
	if len(files) == 0 || width <= 0 {
		return nil
	}
	var maxAdded, maxDeleted, maxTotal int
	for _, f := range files {
		if f.IsBinary {
			continue
		}
		if f.Added > maxAdded {
			maxAdded = f.Added
		}
		if f.Deleted > maxDeleted {
			maxDeleted = f.Deleted
		}
		if t := f.Added + f.Deleted; t > maxTotal {
			maxTotal = t
		}
	}
	plusColW := 0
	if maxAdded > 0 {
		plusColW = 1 + digits(maxAdded) // '+' + digits
	}
	minusColW := 0
	if maxDeleted > 0 {
		minusColW = 1 + digits(maxDeleted)
	}
	barW := MaxBarCells
	if maxTotal < barW {
		barW = maxTotal
	}

	const gap = "  "
	// status(1) + gap + path + [gap + plus] + [gap + minus] + [gap + bar]
	fixed := 1 + len(gap)
	if plusColW > 0 {
		fixed += len(gap) + plusColW
	}
	if minusColW > 0 {
		fixed += len(gap) + minusColW
	}
	if barW > 0 {
		fixed += len(gap) + barW
	}
	pathW := width - fixed
	if pathW < 1 {
		pathW = 1
	}

	rows := make([]Row, 0, len(files))
	for _, f := range files {
		path := pathLabel(f)
		path = truncateLeft(path, pathW)
		path = padRight(path, pathW)

		var plus, minus string
		var pluses, minuses int
		switch {
		case f.IsBinary:
			// Binary files render with status + path only — no numeric
			// counts and no glyph bar. The plus/minus columns are filled
			// with spaces so the path column still aligns with non-binary
			// rows on the same commit.
			if plusColW > 0 {
				plus = strings.Repeat(" ", plusColW)
			}
			if minusColW > 0 {
				minus = strings.Repeat(" ", minusColW)
			}
		default:
			if plusColW > 0 {
				if f.Added > 0 {
					plus = padLeft(fmt.Sprintf("+%d", f.Added), plusColW)
				} else {
					plus = strings.Repeat(" ", plusColW)
				}
			}
			if minusColW > 0 {
				if f.Deleted > 0 {
					minus = padLeft(fmt.Sprintf("-%d", f.Deleted), minusColW)
				} else {
					minus = strings.Repeat(" ", minusColW)
				}
			}
			if barW > 0 && maxTotal > 0 {
				pluses, minuses = scaleBar(f.Added, f.Deleted, maxTotal, barW)
			}
		}
		rows = append(rows, Row{
			Status:  f.Status,
			Path:    path,
			Plus:    plus,
			Minus:   minus,
			Pluses:  pluses,
			Minuses: minuses,
		})
	}
	return rows
}

func pathLabel(f File) string {
	if f.OldPath != "" {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}

func digits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

func padLeft(s string, w int) string {
	rw := utf8.RuneCountInString(s)
	if rw >= w {
		return s
	}
	return strings.Repeat(" ", w-rw) + s
}

func padRight(s string, w int) string {
	rw := utf8.RuneCountInString(s)
	if rw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-rw)
}

// truncateLeft truncates s from the left with a leading "…" so the
// tail (typically the basename) stays visible.
func truncateLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	rw := utf8.RuneCountInString(s)
	if rw <= w {
		return s
	}
	runes := []rune(s)
	drop := rw - (w - 1)
	return "…" + string(runes[drop:])
}

// scaleBar splits a (added+deleted) total across barW cells in
// proportion to maxTotal, with at least one cell when total > 0.
func scaleBar(added, deleted, maxTotal, barW int) (int, int) {
	total := added + deleted
	if total == 0 || maxTotal == 0 || barW == 0 {
		return 0, 0
	}
	cells := total * barW / maxTotal
	if cells == 0 && total > 0 {
		cells = 1
	}
	if cells > barW {
		cells = barW
	}
	pluses := added * cells / total
	if pluses == 0 && added > 0 && cells > 0 {
		pluses = 1
	}
	if pluses > cells {
		pluses = cells
	}
	return pluses, cells - pluses
}
