package preview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

func odsFixture(t *testing.T, content string) []byte {
	t.Helper()
	return zipFixture(t, map[string]string{
		"mimetype": "application/vnd.oasis.opendocument.spreadsheet",
		"content.xml": `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
 <office:body><office:spreadsheet>` + content + `</office:spreadsheet></office:body>
</office:document-content>`,
	})
}

func TestBoundedRepeatRejectsANegativeMaximum(t *testing.T) {
	if got := boundedRepeat("5", -1); got != 0 {
		t.Fatalf("boundedRepeat(%q, -1) = %d, want 0", "5", got)
	}
}

func TestBoundedRepeatCapsAtMaximumAndPassesThroughValidValues(t *testing.T) {
	if got := boundedRepeat("", 10); got != 1 {
		t.Fatalf("empty repeat = %d, want 1", got)
	}
	if got := boundedRepeat("3", 10); got != 3 {
		t.Fatalf("in-range repeat = %d, want 3", got)
	}
	if got := boundedRepeat("50", 10); got != 10 {
		t.Fatalf("over-max repeat = %d, want capped at 10", got)
	}
	if got := boundedRepeat("not-a-number", 10); got != 10 {
		t.Fatalf("unparseable repeat = %d, want capped at 10", got)
	}
}

func decodeSheet(t *testing.T, page []byte) sheetPreview {
	t.Helper()
	var got sheetPreview
	if err := json.Unmarshal(page, &got); err != nil {
		t.Fatalf("decode sheet preview: %v", err)
	}
	return got
}

func TestRenderODSReadsOnlyFirstSheetAndCachedFormulaValue(t *testing.T) {
	data := odsFixture(t, `<table:table table:name="First">
 <table:table-row>
  <table:table-cell office:value-type="string"><text:p>Nome</text:p></table:table-cell>
  <table:table-cell office:value-type="float" office:value="42"><text:p>42</text:p></table:table-cell>
  <table:table-cell table:formula="of:=1+1" office:value-type="float" office:value="2"><text:p>cached 2</text:p></table:table-cell>
 </table:table-row>
</table:table><table:table table:name="Ignored"><table:table-row><table:table-cell><text:p>secret</text:p></table:table-cell></table:table-row></table:table>`)

	pages, contentType, err := New().Render(context.Background(), "application/zip", strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if contentType != domain.PreviewContentTypeSheet || len(pages) != 1 {
		t.Fatalf("content type/pages = %q/%d", contentType, len(pages))
	}
	got := decodeSheet(t, pages[0])
	if len(got.Rows) != 1 || strings.Join(got.Rows[0], "|") != "Nome|42|cached 2" {
		t.Fatalf("rows = %#v", got.Rows)
	}
	if strings.Contains(string(pages[0]), "of:=") || strings.Contains(string(pages[0]), "secret") {
		t.Fatalf("formula or later sheet leaked: %s", pages[0])
	}
}

func TestRenderODSBoundsRepeatedRowsAndColumns(t *testing.T) {
	data := odsFixture(t, `<table:table table:name="First"><table:table-row table:number-rows-repeated="999999">
 <table:table-cell table:number-columns-repeated="999999" office:value-type="string"><text:p>x</text:p></table:table-cell>
</table:table-row></table:table>`)

	pages, _, err := New().Render(context.Background(), "application/zip", strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := decodeSheet(t, pages[0])
	if len(got.Rows) != domain.MaxPreviewSheetRows || len(got.Rows[0]) != domain.MaxPreviewSheetColumns {
		t.Fatalf("bounded dimensions = %dx%d", len(got.Rows), len(got.Rows[0]))
	}
	if !got.TruncatedRows || !got.TruncatedColumns {
		t.Fatalf("truncation flags = rows:%v columns:%v", got.TruncatedRows, got.TruncatedColumns)
	}
}
