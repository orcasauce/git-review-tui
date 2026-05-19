package filelist

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormat_ColumnAlignmentAndCounts(t *testing.T) {
	files := []File{
		{Status: "M", Path: "a.txt", Added: 1, Deleted: 2},
		{Status: "M", Path: "b.txt", Added: 100, Deleted: 50},
		{Status: "A", Path: "c.txt", Added: 5, Deleted: 0},
	}
	rows := Format(files, 80)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	// Plus column width should be 4 ("+100"). Minus column width should
	// be 3 ("-50"). All rows must share the same plus/minus widths.
	if w := utf8.RuneCountInString(rows[0].Plus); w != 4 {
		t.Errorf("rows[0].Plus width = %d (%q), want 4", w, rows[0].Plus)
	}
	if w := utf8.RuneCountInString(rows[1].Plus); w != 4 {
		t.Errorf("rows[1].Plus width = %d (%q), want 4", w, rows[1].Plus)
	}
	if w := utf8.RuneCountInString(rows[2].Plus); w != 4 {
		t.Errorf("rows[2].Plus width = %d (%q), want 4", w, rows[2].Plus)
	}
	if w := utf8.RuneCountInString(rows[0].Minus); w != 3 {
		t.Errorf("rows[0].Minus width = %d (%q), want 3", w, rows[0].Minus)
	}
	// Row 2's deleted=0 must render as all spaces in the minus column.
	if got := rows[2].Minus; strings.TrimSpace(got) != "" {
		t.Errorf("rows[2].Minus = %q, want all spaces", got)
	}
	// Plus / minus columns should be right-aligned.
	if got, want := rows[0].Plus, "  +1"; got != want {
		t.Errorf("rows[0].Plus = %q, want %q", got, want)
	}
	if got, want := rows[1].Plus, "+100"; got != want {
		t.Errorf("rows[1].Plus = %q, want %q", got, want)
	}
	if got, want := rows[1].Minus, "-50"; got != want {
		t.Errorf("rows[1].Minus = %q, want %q", got, want)
	}
}

func TestFormat_BarScaling(t *testing.T) {
	// Commit-max total is 100 (file two). Bar caps at MaxBarCells (10).
	files := []File{
		{Status: "M", Path: "one", Added: 50, Deleted: 0},
		{Status: "M", Path: "two", Added: 80, Deleted: 20},
	}
	rows := Format(files, 80)
	if rows[1].Pluses+rows[1].Minuses != MaxBarCells {
		t.Errorf("biggest row bar = %d cells, want %d", rows[1].Pluses+rows[1].Minuses, MaxBarCells)
	}
	if rows[1].Pluses != 8 || rows[1].Minuses != 2 {
		t.Errorf("biggest row = (%d,%d), want (8,2)", rows[1].Pluses, rows[1].Minuses)
	}
	// Half-size row should be roughly half the bar.
	if got := rows[0].Pluses + rows[0].Minuses; got != 5 {
		t.Errorf("half row bar = %d, want 5", got)
	}
}

func TestFormat_BarMinimumOneCell(t *testing.T) {
	// A tiny change next to a huge one should still get at least one cell.
	files := []File{
		{Status: "M", Path: "huge", Added: 1000, Deleted: 0},
		{Status: "M", Path: "tiny", Added: 1, Deleted: 0},
	}
	rows := Format(files, 80)
	if rows[1].Pluses+rows[1].Minuses < 1 {
		t.Errorf("tiny row should have at least 1 bar cell, got %d", rows[1].Pluses+rows[1].Minuses)
	}
}

func TestFormat_RenameLabel(t *testing.T) {
	files := []File{
		{Status: "R", Path: "new.txt", OldPath: "old.txt", Added: 1, Deleted: 1},
	}
	rows := Format(files, 80)
	if rows[0].Status != "R" {
		t.Errorf("Status = %q, want %q", rows[0].Status, "R")
	}
	if !strings.Contains(rows[0].Path, "old.txt → new.txt") {
		t.Errorf("Path = %q, want it to contain 'old.txt → new.txt'", rows[0].Path)
	}
}

func TestFormat_BinaryFile(t *testing.T) {
	files := []File{
		{Status: "M", Path: "code.go", Added: 10, Deleted: 5},
		{Status: "A", Path: "image.png", IsBinary: true},
	}
	rows := Format(files, 80)
	// Binary rows render with status + path only — no numeric counts and
	// no glyph bar. The plus/minus columns are blanked out (still padded
	// to the per-commit column widths so the path column aligns).
	if strings.TrimSpace(rows[1].Plus) != "" {
		t.Errorf("binary row Plus = %q, expected all spaces", rows[1].Plus)
	}
	if strings.TrimSpace(rows[1].Minus) != "" {
		t.Errorf("binary row Minus = %q, expected all spaces", rows[1].Minus)
	}
	if rows[1].Pluses != 0 || rows[1].Minuses != 0 {
		t.Errorf("binary row bar = (%d,%d), want (0,0)", rows[1].Pluses, rows[1].Minuses)
	}
	// Sibling non-binary row on the same commit must still show its
	// numeric counts; binary rows must not collapse the plus/minus column
	// widths for the whole commit.
	if strings.TrimSpace(rows[0].Plus) == "" || strings.TrimSpace(rows[0].Minus) == "" {
		t.Errorf("non-binary sibling row missing counts: plus=%q minus=%q",
			rows[0].Plus, rows[0].Minus)
	}
	// Plus/Minus columns must remain the same width across all rows so
	// the path column lines up.
	if utf8.RuneCountInString(rows[0].Plus) != utf8.RuneCountInString(rows[1].Plus) {
		t.Errorf("plus column widths differ: %d vs %d",
			utf8.RuneCountInString(rows[0].Plus), utf8.RuneCountInString(rows[1].Plus))
	}
}

func TestFormat_PathLeftTruncation(t *testing.T) {
	// Total width 20. Status(1) + gap(2) + path + gap(2) + plus(2) + gap(2) + minus(2) + gap(2) + bar
	// With Added=1/Deleted=1, plusColW=2 ("+1"), minusColW=2 ("-1"), barW=2.
	// fixed = 1 + 2 + 2 + 2 + 2 + 2 + 2 + 2 = 15. pathW = 5.
	files := []File{
		{Status: "M", Path: "a/very/long/path/to/file.go", Added: 1, Deleted: 1},
	}
	rows := Format(files, 20)
	path := strings.TrimRight(rows[0].Path, " ")
	if !strings.HasPrefix(path, "…") {
		t.Errorf("expected leading ellipsis on truncated path, got %q", path)
	}
	if utf8.RuneCountInString(rows[0].Path) > 20 {
		t.Errorf("path column wider than panel: %q (%d cells)", rows[0].Path, utf8.RuneCountInString(rows[0].Path))
	}
}

func TestFormat_NoCountsWhenAllZero(t *testing.T) {
	// All-zero commit (clean merge-style): plus / minus columns should
	// collapse and the bar shouldn't reserve any cells.
	files := []File{
		{Status: "M", Path: "a.txt"},
	}
	rows := Format(files, 80)
	if rows[0].Plus != "" || rows[0].Minus != "" {
		t.Errorf("expected empty plus/minus columns, got %q/%q", rows[0].Plus, rows[0].Minus)
	}
	if rows[0].Pluses != 0 || rows[0].Minuses != 0 {
		t.Errorf("expected empty bar, got (%d,%d)", rows[0].Pluses, rows[0].Minuses)
	}
}

func TestFormat_EmptyInput(t *testing.T) {
	if rows := Format(nil, 80); rows != nil {
		t.Errorf("Format(nil) = %+v, want nil", rows)
	}
	if rows := Format([]File{{Path: "a"}}, 0); rows != nil {
		t.Errorf("Format(_, 0) = %+v, want nil", rows)
	}
}
