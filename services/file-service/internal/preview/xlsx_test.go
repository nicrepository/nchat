package preview_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

// xlsxMIME is what Render is actually called with in production: the coarse
// sniff net/http.DetectContentType produces for any zip-shaped upload, XLSX
// included — never the OOXML-specific string, which that sniffer cannot
// produce at all. See domain.previewableMIMEs' own comment.
const xlsxMIME = "application/zip"

// xlsxZipFixture builds a raw, hand-assembled OOXML zip — not a real
// workbook — so a test can put arbitrary (including hostile) entries in it
// without excelize's own writer refusing to produce them.
func xlsxZipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func buildXLSX(t *testing.T, populate func(f *excelize.File)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	populate(f)
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	return buf.Bytes()
}

func TestRenderXLSXReadsOnlyTheFirstSheet(t *testing.T) {
	data := buildXLSX(t, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "first-sheet-value")
		if _, err := f.NewSheet("Sheet2"); err != nil {
			t.Fatalf("new sheet: %v", err)
		}
		_ = f.SetCellValue("Sheet2", "A1", "second-sheet-value")
	})

	pages, contentType, err := renderPagesWithType(t, xlsxMIME, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if contentType != domain.PreviewContentTypeSheet {
		t.Fatalf("content type = %q, want %q", contentType, domain.PreviewContentTypeSheet)
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	row0, _ := rows[0].([]any)
	if row0[0] != "first-sheet-value" {
		t.Fatalf("first cell = %v, want the first sheet's value, not the second's", row0[0])
	}
	body := string(pages[0])
	if strings.Contains(body, "second-sheet-value") {
		t.Fatal("the second sheet's data must never be read")
	}
}

// A cell holding only a formula, with no cached value, must render as empty
// — never the formula text, and never a value this service computed by
// evaluating it. excelize's GetCellValue only ever returns a cached <v>; it
// has no evaluator, so this also proves the render path never reaches for
// one.
func TestRenderXLSXNeverExposesAFormulasText(t *testing.T) {
	data := buildXLSX(t, func(f *excelize.File) {
		if err := f.SetCellFormula("Sheet1", "A1", "=1+1"); err != nil {
			t.Fatalf("set formula: %v", err)
		}
		_ = f.SetCellValue("Sheet1", "B1", "plain value")
	})

	pages, _, err := renderPagesWithType(t, xlsxMIME, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(pages[0])
	if strings.Contains(body, "1+1") {
		t.Fatalf("the formula's own text leaked into the preview: %s", body)
	}
	sheet := decodeSheet(t, pages[0])
	rows, _ := sheet["rows"].([]any)
	row0, _ := rows[0].([]any)
	if row0[1] != "plain value" {
		t.Fatalf("second cell = %v, want the plain value unaffected", row0[1])
	}
}

func TestRenderXLSXRejectsAContainerWithAMacro(t *testing.T) {
	data := xlsxZipFixture(t, map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"xl/workbook.xml":     `<workbook/>`,
		"xl/vbaProject.bin":   "macro payload",
	})
	_, _, err := renderPagesWithType(t, xlsxMIME, data)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported (rejected by the container gate before excelize opens it)", err)
	}
}

func TestRenderXLSXRejectsAnExpansionBomb(t *testing.T) {
	data := xlsxZipFixture(t, map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"xl/workbook.xml":     strings.Repeat("A", 33<<20), // past the container gate's expansion limit
	})
	_, _, err := renderPagesWithType(t, xlsxMIME, data)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported (rejected by the container gate)", err)
	}
}

func TestRenderXLSXRejectsACorruptZip(t *testing.T) {
	_, _, err := renderPagesWithType(t, xlsxMIME, []byte("PK\x03\x04not actually a zip"))
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported (an unreadable zip is refused by the container gate)", err)
	}
}

func TestRenderXLSXTruncatesExcessRowsAndColumns(t *testing.T) {
	data := buildXLSX(t, func(f *excelize.File) {
		for row := 1; row <= domain.MaxPreviewSheetRows+20; row++ {
			cell, err := excelize.CoordinatesToCellName(1, row)
			if err != nil {
				t.Fatalf("coordinates: %v", err)
			}
			_ = f.SetCellValue("Sheet1", cell, row)
		}
		for col := 1; col <= domain.MaxPreviewSheetColumns+10; col++ {
			cell, err := excelize.CoordinatesToCellName(col, 1)
			if err != nil {
				t.Fatalf("coordinates: %v", err)
			}
			_ = f.SetCellValue("Sheet1", cell, col)
		}
	})

	pages, _, err := renderPagesWithType(t, xlsxMIME, data)
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
	row0, _ := rows[0].([]any)
	if len(row0) != domain.MaxPreviewSheetColumns {
		t.Fatalf("columns in row = %d, want the cap of %d", len(row0), domain.MaxPreviewSheetColumns)
	}
	if truncated, _ := sheet["truncatedColumns"].(bool); !truncated {
		t.Fatal("truncatedColumns must be true")
	}
}

func TestRenderXLSXRefusesAContainerThatIsNotASpreadsheet(t *testing.T) {
	// A well-formed OOXML container, but a Word document — the wrong MIME for
	// this renderer, which must refuse it rather than guessing.
	data := xlsxZipFixture(t, map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"word/document.xml":   `<document/>`,
	})
	_, _, err := renderPagesWithType(t, xlsxMIME, data)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}
