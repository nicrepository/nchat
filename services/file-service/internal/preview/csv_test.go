package preview_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

func decodeSheet(t *testing.T, page []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(page, &decoded); err != nil {
		t.Fatalf("decode sheet preview: %v", err)
	}
	return decoded
}

func TestRenderCSVParsesCommaDelimitedFiles(t *testing.T) {
	pages, contentType, err := renderPagesWithType(t, "text/csv", []byte("a,b,c\n1,2,3\n4,5,6\n"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if contentType != domain.PreviewContentTypeSheet {
		t.Fatalf("content type = %q, want %q", contentType, domain.PreviewContentTypeSheet)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	columns, _ := sheet["columns"].([]any)
	if len(columns) != 3 || columns[0] != "A" || columns[1] != "B" || columns[2] != "C" {
		t.Fatalf("columns = %v, want [A B C]", columns)
	}
}

func TestRenderCSVSniffsSemicolonAndTabDelimiters(t *testing.T) {
	for name, source := range map[string]string{
		"semicolon": "a;b;c\n1;2;3\n",
		"tab":       "a\tb\tc\n1\t2\t3\n",
	} {
		t.Run(name, func(t *testing.T) {
			pages, _, err := renderPagesWithType(t, "text/csv", []byte(source))
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			sheet := decodeSheet(t, pages[0])
			row0, _ := sheet["rows"].([]any)[0].([]any)
			if len(row0) != 3 {
				t.Fatalf("first row has %d cells, want 3: %v", len(row0), row0)
			}
		})
	}
}

func TestRenderCSVStripsAUTF8BOM(t *testing.T) {
	source := append([]byte{0xEF, 0xBB, 0xBF}, []byte("a,b\n1,2\n")...)
	pages, _, err := renderPagesWithType(t, "text/csv", source)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	columns, _ := sheet["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("columns = %v, want 2 entries (BOM must not leak into the first cell)", columns)
	}
}

func TestRenderCSVRejectsUTF16(t *testing.T) {
	source := append([]byte{0xFF, 0xFE}, []byte("a\x00,\x00b\x00\n")...)
	_, _, err := renderPagesWithType(t, "text/csv", source)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderCSVRejectsInvalidUTF8(t *testing.T) {
	_, _, err := renderPagesWithType(t, "text/csv", []byte("a,b\n\xff\xfe,2\n"))
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderCSVAcceptsRaggedRows(t *testing.T) {
	pages, _, err := renderPagesWithType(t, "text/csv", []byte("a,b,c\n1,2\n3,4,5,6\n"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (ragged rows must not fail the parse)", len(rows))
	}
}

func TestRenderCSVTruncatesExcessRows(t *testing.T) {
	var b strings.Builder
	for i := 0; i < domain.MaxPreviewSheetRows+50; i++ {
		b.WriteString("x\n")
	}
	pages, _, err := renderPagesWithType(t, "text/csv", []byte(b.String()))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	if len(rows) != domain.MaxPreviewSheetRows {
		t.Fatalf("rows = %d, want the cap of %d", len(rows), domain.MaxPreviewSheetRows)
	}
	if truncated, _ := sheet["truncatedRows"].(bool); !truncated {
		t.Fatal("truncatedRows must be true")
	}
}

func TestRenderCSVTruncatesExcessColumns(t *testing.T) {
	var row strings.Builder
	for i := 0; i < domain.MaxPreviewSheetColumns+10; i++ {
		if i > 0 {
			row.WriteByte(',')
		}
		row.WriteByte('x')
	}
	pages, _, err := renderPagesWithType(t, "text/csv", []byte(row.String()+"\n"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	firstRow, _ := sheet["rows"].([]any)[0].([]any)
	if len(firstRow) != domain.MaxPreviewSheetColumns {
		t.Fatalf("columns in row = %d, want the cap of %d", len(firstRow), domain.MaxPreviewSheetColumns)
	}
	if truncated, _ := sheet["truncatedColumns"].(bool); !truncated {
		t.Fatal("truncatedColumns must be true")
	}
}

func TestRenderCSVTruncatesAnOversizedCell(t *testing.T) {
	huge := strings.Repeat("x", domain.MaxPreviewCellBytes+100)
	pages, _, err := renderPagesWithType(t, "text/csv", []byte(huge+"\n"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	firstRow, _ := sheet["rows"].([]any)[0].([]any)
	cell, _ := firstRow[0].(string)
	if len(cell) != domain.MaxPreviewCellBytes {
		t.Fatalf("cell length = %d, want the cap of %d", len(cell), domain.MaxPreviewCellBytes)
	}
	if truncated, _ := sheet["truncatedColumns"].(bool); !truncated {
		t.Fatal("truncatedColumns must be true for an oversized cell")
	}
}

func TestRenderCSVRefusesAnEmptyFile(t *testing.T) {
	// readBounded already refuses a zero-length source before the CSV parser
	// is ever reached — see preview.go's own "empty source" check, shared by
	// every renderer.
	_, _, err := renderPagesWithType(t, "text/csv", []byte(""))
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender (from the shared empty-source guard)", err)
	}
}

// A cell that looks like a spreadsheet formula-injection payload must survive
// as an inert string: CSV has no formulas of its own, and this preview never
// decides what happens if the file is later reopened in a spreadsheet
// application — only that it renders the text verbatim here.
func TestRenderCSVNeverInterpretsFormulaLikeOrHTMLCells(t *testing.T) {
	source := "note\n=CMD|'/c calc'!A1\n<script>alert(1)</script>\n"
	pages, _, err := renderPagesWithType(t, "text/csv", []byte(source))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	row1, _ := rows[1].([]any)
	row2, _ := rows[2].([]any)
	if row1[0] != "=CMD|'/c calc'!A1" {
		t.Fatalf("formula-like cell was altered: %v", row1[0])
	}
	if row2[0] != "<script>alert(1)</script>" {
		t.Fatalf("HTML-like cell was altered: %v", row2[0])
	}
}
