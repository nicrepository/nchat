package preview_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

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
// without any writer refusing to produce them.
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
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

const xlsxMainNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

// xlsxWorkbookParts returns the package parts of a minimal but genuine
// workbook: sheets are declared in workbook.xml in the given order, each
// wired through workbook.xml.rels to xl/worksheets/sheetN.xml holding the
// given <sheetData> body. A nil shared table omits sharedStrings.xml, as a
// workbook with no string cells does.
func xlsxWorkbookParts(shared []string, sheetData ...string) map[string]string {
	var sheets, rels strings.Builder
	files := map[string]string{"[Content_Types].xml": `<Types/>`}
	for i, body := range sheetData {
		fmt.Fprintf(&sheets, `<sheet name="Sheet%d" sheetId="%d" r:id="rId%d"/>`, i+1, i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		files[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = `<worksheet xmlns="` + xlsxMainNS + `"><sheetData>` + body + `</sheetData></worksheet>`
	}
	files["xl/workbook.xml"] = `<workbook xmlns="` + xlsxMainNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` + sheets.String() + `</sheets></workbook>`
	files["xl/_rels/workbook.xml.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + `</Relationships>`
	if shared != nil {
		var table strings.Builder
		for _, text := range shared {
			fmt.Fprintf(&table, `<si><t>%s</t></si>`, text)
		}
		files["xl/sharedStrings.xml"] = `<sst xmlns="` + xlsxMainNS + `" count="1" uniqueCount="1">` + table.String() + `</sst>`
	}
	return files
}

func buildXLSX(t *testing.T, shared []string, sheetData ...string) []byte {
	t.Helper()
	return xlsxZipFixture(t, xlsxWorkbookParts(shared, sheetData...))
}

func renderXLSXRows(t *testing.T, data []byte) (map[string]any, [][]any) {
	t.Helper()
	pages, contentType, err := renderPagesWithType(t, xlsxMIME, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if contentType != domain.PreviewContentTypeSheet {
		t.Fatalf("content type = %q, want %q", contentType, domain.PreviewContentTypeSheet)
	}
	sheet := decodeSheet(t, pages[0])
	raw, _ := sheet["rows"].([]any)
	rows := make([][]any, len(raw))
	for i, row := range raw {
		rows[i], _ = row.([]any)
	}
	return sheet, rows
}

func TestRenderXLSXReadsOnlyTheFirstSheet(t *testing.T) {
	data := buildXLSX(t, []string{"first-sheet-value", "second-sheet-value"},
		`<row r="1"><c r="A1" t="s"><v>0</v></c></row>`,
		`<row r="1"><c r="A1" t="s"><v>1</v></c></row>`,
	)

	pages, _, err := renderPagesWithType(t, xlsxMIME, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	_, rows := renderXLSXRows(t, data)
	if rows[0][0] != "first-sheet-value" {
		t.Fatalf("first cell = %v, want the first sheet's value, not the second's", rows[0][0])
	}
	if strings.Contains(string(pages[0]), "second-sheet-value") {
		t.Fatal("the second sheet's data must never be read")
	}
}

// The first sheet is the first one workbook.xml declares, wherever its part
// lives in the package and whatever its relationship is called.
func TestRenderXLSXFollowsWorkbookOrderThroughRelationships(t *testing.T) {
	files := xlsxWorkbookParts(nil, `<row><c t="inlineStr"><is><t>rId1 sheet</t></is></c></row>`)
	files["xl/workbook.xml"] = `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
		`<sheet name="Declared first" sheetId="7" r:id="rId9"/><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	files["xl/_rels/workbook.xml.rels"] = `<Relationships>` +
		`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/>` +
		`<Relationship Id="rId9" Target="/xl/worksheets/elsewhere.xml"/></Relationships>`
	files["xl/worksheets/elsewhere.xml"] = `<worksheet><sheetData><row><c t="inlineStr"><is><t>declared first</t></is></c></row></sheetData></worksheet>`

	_, rows := renderXLSXRows(t, xlsxZipFixture(t, files))
	if rows[0][0] != "declared first" {
		t.Fatalf("first cell = %v, want the sheet workbook.xml declares first", rows[0][0])
	}
}

// A cell holding only a formula, with no cached value, must render as empty
// — never the formula text, and never a value this service computed by
// evaluating it. The parser reads <v> and <is> only; there is no evaluator
// to reach for.
func TestRenderXLSXNeverExposesAFormulasText(t *testing.T) {
	data := buildXLSX(t, nil, `<row r="1">`+
		`<c r="A1"><f>1+1</f></c>`+
		`<c r="B1" t="inlineStr"><is><t>plain value</t></is></c>`+
		`<c r="C1" t="str"><f>CONCAT("a","b")</f><v>ab</v></c>`+
		`</row>`)

	pages, _, err := renderPagesWithType(t, xlsxMIME, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(pages[0])
	if strings.Contains(body, "1+1") || strings.Contains(body, "CONCAT") {
		t.Fatalf("a formula's own text leaked into the preview: %s", body)
	}
	_, rows := renderXLSXRows(t, data)
	if rows[0][0] != "" || rows[0][1] != "plain value" || rows[0][2] != "ab" {
		t.Fatalf("row = %v, want [\"\" \"plain value\" \"ab\"] (formula empty, plain and cached values kept)", rows[0])
	}
}

func TestRenderXLSXDecodesEveryCellTypeItDisplays(t *testing.T) {
	data := buildXLSX(t, []string{"shared &lt;one&gt;", "rich"},
		`<row r="1">`+
			`<c r="A1" t="s"><v>0</v></c>`+
			`<c r="B1" t="s"><v> 1 </v></c>`+
			`<c r="C1"><v>42.5</v></c>`+
			`<c r="D1" t="b"><v>1</v></c>`+
			`<c r="E1" t="b"><v>0</v></c>`+
			`<c r="F1" t="b"/>`+
			`<c r="G1" t="e"><v>#DIV/0!</v></c>`+
			`<c r="H1" t="s"/>`+
			`<c r="I1" t="inlineStr"><is><r><t>in</t></r><r><t>line</t></r><rPh sb="0" eb="1"><t>phonetic</t></rPh></is></c>`+
			`</row>`)

	_, rows := renderXLSXRows(t, data)
	want := []any{"shared <one>", "rich", "42.5", "TRUE", "FALSE", "", "#DIV/0!", "", "inline"}
	if fmt.Sprint(rows[0]) != fmt.Sprint(want) {
		t.Fatalf("row = %v, want %v", rows[0], want)
	}
}

// GO-2026-6452: a shared-string index the file controls must be checked on
// both bounds. A negative, oversized or non-numeric index is a malformed
// cell refused as ErrRender — never an out-of-range slice access.
func TestRenderXLSXRefusesAMalformedSharedStringIndex(t *testing.T) {
	for _, index := range []string{"-1", "1", "999999999999999999999", "x"} {
		t.Run(index, func(t *testing.T) {
			data := buildXLSX(t, []string{"only"}, `<row r="1"><c r="A1" t="s"><v>`+index+`</v></c></row>`)
			_, _, err := renderPagesWithType(t, xlsxMIME, data)
			if !errors.Is(err, preview.ErrRender) {
				t.Fatalf("error = %v, want ErrRender", err)
			}
		})
	}
}

func TestRenderXLSXRefusesASharedStringCellWithoutATable(t *testing.T) {
	data := buildXLSX(t, nil, `<row r="1"><c r="A1" t="s"><v>0</v></c></row>`)
	_, _, err := renderPagesWithType(t, xlsxMIME, data)
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}

// A malformed row after good ones keeps what was read, as the earlier
// library-backed renderer did; a malformed first row is a render failure.
func TestRenderXLSXKeepsRowsReadBeforeAMalformedOne(t *testing.T) {
	data := buildXLSX(t, []string{"ok"},
		`<row r="1"><c r="A1" t="s"><v>0</v></c></row><row r="2"><c r="A2" t="s"><v>-1</v></c></row>`)
	sheet, rows := renderXLSXRows(t, data)
	if len(rows) != 1 || rows[0][0] != "ok" {
		t.Fatalf("rows = %v, want just the first, valid row", rows)
	}
	if got, _ := sheet["totalRowsRead"].(float64); got != 1 {
		t.Fatalf("totalRowsRead = %v, want 1", got)
	}
}

func TestRenderXLSXPlacesSparseCellsAndGapRows(t *testing.T) {
	data := buildXLSX(t, nil,
		`<row r="2"><c r="C2"><v>c2</v></c><c r="A2"><v>a2</v></c></row>`+
			`<row r="5"><c><v>next</v></c><c r="B5"><v>b5</v></c></row>`)

	sheet, rows := renderXLSXRows(t, data)
	want := "[[] [a2  c2] [] [] [next b5]]"
	if fmt.Sprint(rows) != want {
		t.Fatalf("rows = %v, want %s", rows, want)
	}
	if columns, _ := sheet["columns"].([]any); fmt.Sprint(columns) != "[A B C]" {
		t.Fatalf("columns = %v, want [A B C]", columns)
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
		t.Fatalf("error = %v, want ErrUnsupported (rejected by the container gate before the workbook is parsed)", err)
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

func TestRenderXLSXRejectsBrokenPackages(t *testing.T) {
	cases := map[string]struct {
		mutate func(files map[string]string)
		want   error
	}{
		"workbook without sheets": {
			mutate: func(files map[string]string) { files["xl/workbook.xml"] = `<workbook><sheets/></workbook>` },
			want:   preview.ErrUnsupported,
		},
		"workbook that is not XML": {
			mutate: func(files map[string]string) { files["xl/workbook.xml"] = `<workbook><sheets>` },
			want:   preview.ErrUnsupported,
		},
		"missing relationships part": {
			mutate: func(files map[string]string) { delete(files, "xl/_rels/workbook.xml.rels") },
			want:   preview.ErrRender,
		},
		"relationship missing for the first sheet": {
			mutate: func(files map[string]string) { files["xl/_rels/workbook.xml.rels"] = `<Relationships/>` },
			want:   preview.ErrRender,
		},
		"relationship pointing at a missing part": {
			mutate: func(files map[string]string) { delete(files, "xl/worksheets/sheet1.xml") },
			want:   preview.ErrRender,
		},
		"sheet that is not XML": {
			mutate: func(files map[string]string) { files["xl/worksheets/sheet1.xml"] = `<worksheet><sheetData><row>` },
			want:   preview.ErrRender,
		},
		"shared strings that are not XML": {
			mutate: func(files map[string]string) { files["xl/sharedStrings.xml"] = `<sst><si><t>x` },
			want:   preview.ErrRender,
		},
		"sheet with no rows": {
			mutate: func(files map[string]string) {
				files["xl/worksheets/sheet1.xml"] = `<worksheet><sheetData/></worksheet>`
			},
			want: preview.ErrUnsupported,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			files := xlsxWorkbookParts([]string{"x"}, `<row r="1"><c r="A1" t="s"><v>0</v></c></row>`)
			tc.mutate(files)
			_, _, err := renderPagesWithType(t, xlsxMIME, xlsxZipFixture(t, files))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRenderXLSXTruncatesExcessRowsAndColumns(t *testing.T) {
	var sheet strings.Builder
	sheet.WriteString(`<row r="1">`)
	for col := 1; col <= domain.MaxPreviewSheetColumns+10; col++ {
		fmt.Fprintf(&sheet, `<c><v>%d</v></c>`, col)
	}
	sheet.WriteString(`</row>`)
	for row := 2; row <= domain.MaxPreviewSheetRows+20; row++ {
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d"><v>%d</v></c></row>`, row, row, row)
	}

	got, rows := renderXLSXRows(t, buildXLSX(t, nil, sheet.String()))
	if len(rows) != domain.MaxPreviewSheetRows {
		t.Fatalf("rows = %d, want the cap of %d", len(rows), domain.MaxPreviewSheetRows)
	}
	if truncated, _ := got["truncatedRows"].(bool); !truncated {
		t.Fatal("truncatedRows must be true")
	}
	if len(rows[0]) != domain.MaxPreviewSheetColumns {
		t.Fatalf("columns in row = %d, want the cap of %d", len(rows[0]), domain.MaxPreviewSheetColumns)
	}
	if truncated, _ := got["truncatedColumns"].(bool); !truncated {
		t.Fatal("truncatedColumns must be true")
	}
}

// Row numbers and cell references are the file's own claims. A row numbered
// far past the cap, or a cell referenced far past the last column, must cost
// no more than the cap itself and be reported as truncation.
func TestRenderXLSXBoundsHostileRowNumbersAndCellReferences(t *testing.T) {
	data := buildXLSX(t, nil,
		`<row r="1"><c r="A1"><v>a1</v></c><c r="ZZZZZZZZ1"><v>far</v></c><c r="XFD1"><v>last</v></c></row>`+
			`<row r="18446744073709551615"><c><v>never shown</v></c></row>`)

	got, rows := renderXLSXRows(t, data)
	if len(rows) != domain.MaxPreviewSheetRows {
		t.Fatalf("rows = %d, want exactly the cap", len(rows))
	}
	if truncated, _ := got["truncatedRows"].(bool); !truncated {
		t.Fatal("truncatedRows must be true")
	}
	if len(rows[0]) != domain.MaxPreviewSheetColumns || rows[0][0] != "a1" {
		t.Fatalf("row 1 = %v, want a1 padded to the column cap", rows[0])
	}
	if truncated, _ := got["truncatedColumns"].(bool); !truncated {
		t.Fatal("truncatedColumns must be true")
	}
	if body := fmt.Sprint(rows); strings.Contains(body, "far") || strings.Contains(body, "last") || strings.Contains(body, "never") {
		t.Fatalf("a cell past the caps leaked into the preview: %s", body)
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
