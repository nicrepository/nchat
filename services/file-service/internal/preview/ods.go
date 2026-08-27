package preview

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

const odsSpreadsheetMIME = "application/vnd.oasis.opendocument.spreadsheet"

// renderODS streams the first table in content.xml. It never interprets a
// formula: only persisted text (or, when absent, the persisted office value)
// is copied into the preview.
func renderODS(data []byte) ([]byte, error) {
	detected, err := InspectDocumentContainer(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	if detected != odsSpreadsheetMIME {
		return nil, fmt.Errorf("%w: container is not an ODS spreadsheet", ErrUnsupported)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid ODS", ErrRender)
	}
	var content *zip.File
	for _, file := range zr.File {
		if strings.EqualFold(file.Name, "content.xml") {
			content = file
			break
		}
	}
	if content == nil {
		return nil, fmt.Errorf("%w: ODS has no content.xml", ErrRender)
	}
	r, err := content.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open ODS content", ErrRender)
	}
	defer func() { _ = r.Close() }()
	preview, err := parseODSContent(xml.NewDecoder(io.LimitReader(r, maxDocumentEntryBytes+1)))
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, fmt.Errorf("%w: encode ODS preview", ErrRender)
	}
	return encoded, nil
}

func parseODSContent(decoder *xml.Decoder) (sheetPreview, error) {
	decoder.Strict = true
	preview := sheetPreview{Rows: make([][]string, 0, domain.MaxPreviewSheetRows)}
	inFirstTable := false
	seenTable := false
	maxColumns := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return preview, fmt.Errorf("%w: invalid ODS XML", ErrRender)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "table":
			if !seenTable {
				seenTable, inFirstTable = true, true
			} else if inFirstTable {
				return finishODSPreview(preview, maxColumns)
			}
		case "table-row":
			if !inFirstTable {
				continue
			}
			row, repeat, truncated, err := readODSRow(decoder, start)
			if err != nil {
				return preview, err
			}
			if truncated {
				preview.TruncatedColumns = true
			}
			if len(row) > maxColumns {
				maxColumns = len(row)
			}
			remaining := domain.MaxPreviewSheetRows - len(preview.Rows)
			if repeat > remaining {
				preview.TruncatedRows = true
				repeat = remaining
			}
			for i := 0; i < repeat; i++ {
				preview.Rows = append(preview.Rows, append([]string(nil), row...))
			}
			if len(preview.Rows) == domain.MaxPreviewSheetRows {
				preview.TruncatedRows = true
				return finishODSPreview(preview, maxColumns)
			}
		}
	}
	return finishODSPreview(preview, maxColumns)
}

func readODSRow(decoder *xml.Decoder, start xml.StartElement) ([]string, int, bool, error) {
	repeat := boundedRepeat(attribute(start, "number-rows-repeated"), domain.MaxPreviewSheetRows+1)
	row := make([]string, 0, domain.MaxPreviewSheetColumns)
	truncated := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, 0, false, fmt.Errorf("%w: invalid ODS row", ErrRender)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local != "table-cell" && element.Name.Local != "covered-table-cell" {
				continue
			}
			cell, cellTruncated, err := readODSCell(decoder, element)
			if err != nil {
				return nil, 0, false, err
			}
			cellRepeat := boundedRepeat(attribute(element, "number-columns-repeated"), domain.MaxPreviewSheetColumns+1)
			room := domain.MaxPreviewSheetColumns - len(row)
			if cellRepeat > room {
				truncated, cellRepeat = true, room
			}
			for i := 0; i < cellRepeat; i++ {
				row = append(row, cell)
			}
			truncated = truncated || cellTruncated
		case xml.EndElement:
			if element.Name == start.Name {
				return row, repeat, truncated, nil
			}
		}
	}
}

func readODSCell(decoder *xml.Decoder, start xml.StartElement) (string, bool, error) {
	var text strings.Builder
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return "", false, fmt.Errorf("%w: invalid ODS cell", ErrRender)
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth > 1 {
				text.Write([]byte(value))
			}
		}
	}
	cell := strings.TrimSpace(text.String())
	if cell == "" {
		for _, name := range []string{"string-value", "value", "date-value", "time-value", "boolean-value"} {
			if value := attribute(start, name); value != "" {
				cell = value
				break
			}
		}
	}
	if len(cell) > domain.MaxPreviewCellBytes {
		return cell[:domain.MaxPreviewCellBytes], true, nil
	}
	return cell, false, nil
}

func attribute(element xml.StartElement, local string) string {
	for _, attr := range element.Attr {
		if attr.Name.Local == local {
			return attr.Value
		}
	}
	return ""
}

// boundedRepeat caps an untrusted ODS "repeated" attribute at maximum, which
// is always one of the package's own small, positive preview-size constants
// (see the two call sites) — never derived from the document itself, which is
// what makes the int/uint64 conversions below safe despite gosec's G115
// flagging any such conversion on its own.
func boundedRepeat(raw string, maximum int) int {
	if raw == "" {
		return 1
	}
	if maximum < 0 {
		return 0
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 || n > uint64(maximum) { // #nosec G115 -- maximum is validated non-negative above
		return maximum
	}
	return int(n) // #nosec G115 -- n <= uint64(maximum) was just checked, and maximum is always small
}

func finishODSPreview(preview sheetPreview, maxColumns int) (sheetPreview, error) {
	if len(preview.Rows) == 0 {
		return preview, fmt.Errorf("%w: ODS sheet has no rows", ErrUnsupported)
	}
	preview.TotalRowsRead = len(preview.Rows)
	preview.Columns = columnLabels(maxColumns)
	return preview, nil
}
