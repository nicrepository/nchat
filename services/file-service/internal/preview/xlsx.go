package preview

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// xlsxSpreadsheetMIME is InspectDocumentContainer's own returned MIME for an
// OOXML spreadsheet — see internal/preview/document.go. Compared literally
// rather than re-detected, so a container that identifies as anything else
// (a Word or PowerPoint file with an .xlsx-shaped upload, an ODS package)
// never reaches the sheet parser at all.
const xlsxSpreadsheetMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const (
	xlsxWorkbookEntry      = "xl/workbook.xml"
	xlsxWorkbookRelsEntry  = "xl/_rels/workbook.xml.rels"
	xlsxSharedStringsEntry = "xl/sharedStrings.xml"
)

// renderXLSX validates an XLSX container, reads only its first sheet's cached
// cell values, and returns the same bounded sheetPreview JSON shape CSV and
// ODS do.
//
// # Why the container gate runs first
//
// InspectDocumentContainer (document.go) already rejects zip bombs, path
// traversal, excessive entry counts/expansion ratios and active-content
// markers (vbaProject.bin, ActiveX, embeddings, external links) in an
// OOXML/ODF zip — exactly the non-goals this preview must not touch
// (macros, external connections, embedded content). It also caps every entry
// at maxDocumentEntryBytes, which is what bounds the shared-string table and
// the sheet XML read below.
//
// # Why this is a stdlib parser and not a spreadsheet library
//
// The preview only ever needs the cached <v> of each cell in the first
// sheet, the same thing renderODS reads from content.xml. A full spreadsheet
// library brings an evaluator, image and pivot-table handling and its own
// parsing surface along with it (GO-2026-6452 was a panic on a negative
// shared-string index in exactly the cell-value path this preview used), so
// the read is done with archive/zip and encoding/xml, the same way ODS is:
// every index and repeat count taken from the file is bounds-checked here,
// formulas (<f>) are never read, and nothing can be evaluated because there
// is no evaluator.
func renderXLSX(data []byte) ([]byte, error) {
	detected, err := InspectDocumentContainer(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	if detected != xlsxSpreadsheetMIME {
		return nil, fmt.Errorf("%w: container is not an XLSX spreadsheet", ErrUnsupported)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid XLSX", ErrRender)
	}
	sheet, err := firstXLSXSheet(zr)
	if err != nil {
		return nil, err
	}
	shared, err := readXLSXSharedStrings(zr)
	if err != nil {
		return nil, err
	}
	preview, err := parseXLSXSheet(xml.NewDecoder(bytes.NewReader(sheet)), shared)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, fmt.Errorf("%w: encode sheet preview", ErrRender)
	}
	return encoded, nil
}

// firstXLSXSheet returns the XML of the first sheet in workbook.xml's declared
// order — exactly as a spreadsheet application would open it — never the
// "active sheet", which can differ and would mean trusting more of the file's
// own claims about itself.
func firstXLSXSheet(zr *zip.Reader) ([]byte, error) {
	workbook, err := readXLSXEntry(zr, xlsxWorkbookEntry)
	if err != nil {
		return nil, err
	}
	relID := firstXLSXAttribute(workbook, "sheet", "id")
	if relID == "" {
		return nil, fmt.Errorf("%w: workbook has no sheets", ErrUnsupported)
	}
	rels, err := readXLSXEntry(zr, xlsxWorkbookRelsEntry)
	if err != nil {
		return nil, err
	}
	target := xlsxRelationshipTarget(rels, relID)
	if target == "" {
		return nil, fmt.Errorf("%w: first sheet has no relationship", ErrRender)
	}
	// A relationship target is relative to xl/ unless it is package-absolute.
	if strings.HasPrefix(target, "/") {
		target = strings.TrimPrefix(target, "/")
	} else {
		target = path.Join("xl", target)
	}
	sheet, err := readXLSXEntry(zr, target)
	if err != nil {
		return nil, fmt.Errorf("%w: sheet could not be read", ErrRender)
	}
	return sheet, nil
}

// readXLSXEntry reads one named package part, bounded by the container gate's
// own per-entry limit; a part the gate did not see cannot appear here.
func readXLSXEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, file := range zr.File {
		if strings.EqualFold(strings.ReplaceAll(file.Name, "\\", "/"), name) {
			body, err := readZipEntry(file, maxDocumentEntryBytes)
			if err != nil {
				return nil, fmt.Errorf("%w: %s could not be read", ErrRender, name)
			}
			return body, nil
		}
	}
	return nil, fmt.Errorf("%w: XLSX has no %s", ErrRender, name)
}

// firstXLSXAttribute returns the named attribute of the first element with
// the given local name, or "" when the document has none or is not XML.
func firstXLSXAttribute(document []byte, element, attr string) string {
	decoder := xml.NewDecoder(bytes.NewReader(document))
	decoder.Strict = true
	start, ok, err := nextXLSXElement(decoder, element)
	if err != nil || !ok {
		return ""
	}
	return attribute(start, attr)
}

func xlsxRelationshipTarget(rels []byte, id string) string {
	decoder := xml.NewDecoder(bytes.NewReader(rels))
	decoder.Strict = true
	for {
		start, ok, err := nextXLSXElement(decoder, "Relationship")
		if err != nil || !ok {
			return ""
		}
		if attribute(start, "Id") == id {
			return attribute(start, "Target")
		}
	}
}

// nextXLSXElement advances to the next start element with the given local
// name. ok is false at the end of the document; err reports malformed XML.
func nextXLSXElement(decoder *xml.Decoder, local string) (xml.StartElement, bool, error) {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return xml.StartElement{}, false, nil
		}
		if err != nil {
			return xml.StartElement{}, false, err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == local {
			return start, true, nil
		}
	}
}

// eachToken feeds visit every token inside start, up to but not including
// start's own end tag. visit may itself consume a nested element in full.
func eachToken(decoder *xml.Decoder, start xml.StartElement, visit func(xml.Token) error) error {
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			return nil
		}
		if err := visit(token); err != nil {
			return err
		}
	}
}

// readXLSXSharedStrings loads the shared-string table, which a workbook with
// no string cells legitimately lacks. Each <si> is the concatenation of its
// <t> runs; phonetic <rPh> runs are skipped, as a spreadsheet application
// does not display them either.
func readXLSXSharedStrings(zr *zip.Reader) ([]string, error) {
	table, err := readXLSXEntry(zr, xlsxSharedStringsEntry)
	if err != nil {
		return nil, nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(table))
	decoder.Strict = true
	var shared []string
	for {
		start, ok, err := nextXLSXElement(decoder, "si")
		if err != nil {
			return nil, fmt.Errorf("%w: invalid shared strings", ErrRender)
		}
		if !ok {
			return shared, nil
		}
		text, err := readXLSXText(decoder, start)
		if err != nil {
			return nil, err
		}
		shared = append(shared, text)
	}
}

// readXLSXText collects the text of every <t> under start (an <si> or <is>),
// ignoring phonetic runs, until start's own end tag.
func readXLSXText(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var text strings.Builder
	inText := false
	err := eachToken(decoder, start, func(token xml.Token) error {
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "rPh" {
				return decoder.Skip()
			}
			inText = value.Name.Local == "t"
		case xml.EndElement:
			inText = false
		case xml.CharData:
			if inText {
				text.Write(value)
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: invalid XLSX text", ErrRender)
	}
	return text.String(), nil
}

// parseXLSXSheet streams <sheetData>, copying at most
// domain.MaxPreviewSheetRows rows. A row numbered past the ones already seen
// yields the empty rows in between, as a spreadsheet application shows them,
// but never past the row cap: the file's own row numbers are untrusted. A
// malformed row after good ones keeps what was read; a malformed first row
// is a render failure.
func parseXLSXSheet(decoder *xml.Decoder, shared []string) (sheetPreview, error) {
	decoder.Strict = true
	preview := sheetPreview{Rows: make([][]string, 0, domain.MaxPreviewSheetRows)}
	maxColumns := 0
	for {
		start, ok, err := nextXLSXElement(decoder, "row")
		if err != nil {
			return finishXLSXPreview(preview, maxColumns, fmt.Errorf("%w: invalid sheet XML", ErrRender))
		}
		if !ok {
			return finishXLSXPreview(preview, maxColumns, nil)
		}
		// Gap rows come first, so a row numbered past the cap still counts as
		// the row that overflowed it.
		preview.Rows = appendXLSXGapRows(preview.Rows, attribute(start, "r"))
		if len(preview.Rows) >= domain.MaxPreviewSheetRows {
			preview.TruncatedRows = true
			return finishXLSXPreview(preview, maxColumns, nil)
		}
		record, err := readXLSXRow(decoder, start, shared)
		if err != nil {
			return finishXLSXPreview(preview, maxColumns, err)
		}
		row, truncatedRow := boundRow(record)
		preview.TruncatedColumns = preview.TruncatedColumns || truncatedRow
		maxColumns = max(maxColumns, len(row))
		preview.Rows = append(preview.Rows, row)
	}
}

func finishXLSXPreview(preview sheetPreview, maxColumns int, err error) (sheetPreview, error) {
	if len(preview.Rows) == 0 {
		if err != nil {
			return preview, err
		}
		return preview, fmt.Errorf("%w: sheet has no rows", ErrUnsupported)
	}
	preview.TotalRowsRead = len(preview.Rows)
	preview.Columns = columnLabels(maxColumns)
	return preview, nil
}

// appendXLSXGapRows pads rows with empty ones up to (not including) the
// 1-based row number in raw, capped at the preview's row limit. A missing or
// malformed number means "the next row".
func appendXLSXGapRows(rows [][]string, raw string) [][]string {
	number, err := strconv.ParseUint(raw, 10, 64)
	if raw == "" || err != nil {
		return rows
	}
	// One past the cap is enough to make the caller mark the truncation.
	limit := uint64(domain.MaxPreviewSheetRows) + 1
	if number > limit {
		number = limit
	}
	for uint64(len(rows))+1 < number { // #nosec G115 -- len is non-negative
		rows = append(rows, []string{})
	}
	return rows
}

// readXLSXRow reads one <row>'s cells into their column positions until the
// row's end tag.
func readXLSXRow(decoder *xml.Decoder, start xml.StartElement, shared []string) ([]string, error) {
	record := make([]string, 0, domain.MaxPreviewSheetColumns)
	err := eachToken(decoder, start, func(token xml.Token) error {
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "c" {
			return nil
		}
		var err error
		record, err = placeXLSXCell(decoder, element, shared, record)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: row could not be read", ErrRender)
	}
	return record, nil
}

// placeXLSXCell reads one <c> and stores its value at the column its
// reference names (or the next column when it has none). A cell referenced
// past the column cap is consumed unread and reported through the record's
// length, which boundRow then truncates and flags; its value is never stored.
func placeXLSXCell(decoder *xml.Decoder, element xml.StartElement, shared []string, record []string) ([]string, error) {
	column := xlsxColumn(attribute(element, "r"))
	if column == 0 {
		column = len(record) + 1
	}
	if column > domain.MaxPreviewSheetColumns {
		return padXLSXRecord(record, domain.MaxPreviewSheetColumns+1), decoder.Skip()
	}
	value, err := readXLSXCell(decoder, element, shared)
	if err != nil {
		return nil, err
	}
	record = padXLSXRecord(record, column)
	record[column-1] = value
	return record, nil
}

// padXLSXRecord grows record with empty cells until it has at least width
// entries; width is already capped by the caller, so this never allocates
// more than the column limit plus one.
func padXLSXRecord(record []string, width int) []string {
	for len(record) < width {
		record = append(record, "")
	}
	return record
}

// readXLSXCell reads one <c> until its end tag and returns its cached value.
// Only <v> and an inline <is> are read; a formula's <f> is never looked at,
// so a cell holding only a formula renders empty rather than as its text.
func readXLSXCell(decoder *xml.Decoder, start xml.StartElement, shared []string) (string, error) {
	var cached, inline string
	err := eachToken(decoder, start, func(token xml.Token) error {
		element, ok := token.(xml.StartElement)
		if !ok {
			return nil
		}
		var err error
		switch element.Name.Local {
		case "v":
			cached, err = readXLSXElementText(decoder, element)
		case "is":
			inline, err = readXLSXText(decoder, element)
		default:
			err = decoder.Skip()
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("%w: cell could not be read", ErrRender)
	}
	return xlsxCellValue(attribute(start, "t"), cached, inline, shared)
}

func readXLSXElementText(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var text strings.Builder
	err := eachToken(decoder, start, func(token xml.Token) error {
		if value, ok := token.(xml.CharData); ok {
			text.Write(value)
		}
		return nil
	})
	return text.String(), err
}

// xlsxCellValue resolves a cell's displayed text from its type.
func xlsxCellValue(cellType, cached, inline string, shared []string) (string, error) {
	switch cellType {
	case "s":
		return xlsxSharedString(cached, shared)
	case "inlineStr":
		return inline, nil
	case "b":
		return xlsxBool(cached), nil
	default:
		// "n", "str", "d", "e" and an absent type all display their cached
		// text as stored.
		return cached, nil
	}
}

func xlsxBool(cached string) string {
	switch strings.TrimSpace(cached) {
	case "":
		return ""
	case "1":
		return "TRUE"
	default:
		return "FALSE"
	}
}

// xlsxSharedString resolves a shared-string cell. The index comes straight
// from the file and is checked on both bounds before it indexes anything —
// a negative or out-of-range index is a malformed cell, refused as ErrRender,
// never a panic (GO-2026-6452). A cell with no <v> at all is simply empty.
func xlsxSharedString(cached string, shared []string) (string, error) {
	raw := strings.TrimSpace(cached)
	if raw == "" {
		return "", nil
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 || index >= len(shared) {
		return "", fmt.Errorf("%w: invalid shared string index", ErrRender)
	}
	return shared[index], nil
}

// xlsxColumn converts the column letters of a cell reference such as "C12"
// into a 1-based index, or 0 when the reference has none. More than three
// letters is past any spreadsheet's last column and is reported as one past
// the preview's column cap, so the caller neither trusts nor allocates for it.
func xlsxColumn(ref string) int {
	column := 0
	letters := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		letters++
		if letters > 3 {
			return domain.MaxPreviewSheetColumns + 1
		}
		column = column*26 + int(r-'A') + 1
	}
	return column
}
