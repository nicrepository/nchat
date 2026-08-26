package preview

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// sheetPreview is the one bounded shape every spreadsheet/CSV preview
// produces, JSON-encoded and stored exactly like a rendered page — see
// domain.PreviewContentTypeSheet's own comment for why this is a private,
// versioned type rather than bare JSON.
//
// Cell values are never interpreted: no HTML entity decoding, no formula
// evaluation (CSV has none; a cell beginning with =, +, - or @ is passed
// through verbatim, because whether that string is safe to reopen in a
// spreadsheet application is a decision for whatever the user does with the
// download, not for this preview). This struct's own field names, chosen
// deliberately, are the whole safety contract: it can hold only string cells
// in a bounded grid, never a formula, a macro or a reference to anything
// external.
type sheetPreview struct {
	Columns       []string   `json:"columns"`
	Rows          [][]string `json:"rows"`
	TruncatedRows bool       `json:"truncatedRows"`
	// TruncatedColumns is set when any row had more than
	// domain.MaxPreviewSheetColumns columns, or any cell was longer than
	// domain.MaxPreviewCellBytes.
	TruncatedColumns bool `json:"truncatedColumns"`
	// TotalRowsRead is len(Rows) — how many data rows this preview actually
	// holds, never the source's true row count. Reading far enough to count
	// what was truncated away would undo the row cap as a cost bound, so once
	// TruncatedRows is true this is simply domain.MaxPreviewSheetRows.
	TotalRowsRead int `json:"totalRowsRead"`
}

// utf16BOM matches either byte order of a UTF-16 BOM. This service has no
// transcoder in its dependency graph, and adding one for a preview feature
// would be disproportionate — a UTF-16 CSV is an expected absence, not a
// failure.
var (
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
)

// renderCSV validates and parses a CSV file into the bounded sheetPreview
// shape, returned as its single JSON page.
//
// Nothing here executes anything in the file: encoding/csv only splits text
// on a delimiter, and every cell it yields is taken as an opaque string.
func renderCSV(data []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(data, utf16LEBOM), bytes.HasPrefix(data, utf16BEBOM):
		return nil, fmt.Errorf("%w: UTF-16 CSV is not supported", ErrUnsupported)
	case bytes.HasPrefix(data, utf8BOM):
		data = data[len(utf8BOM):]
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: CSV is not valid UTF-8", ErrUnsupported)
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = sniffDelimiter(data)
	// A hostile or simply irregular file's ragged rows are a data fact to
	// show, not a parse error — encoding/csv's default (reject on width
	// mismatch) is turned off deliberately.
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = false
	reader.ReuseRecord = false

	preview := sheetPreview{Rows: make([][]string, 0, domain.MaxPreviewSheetRows)}
	maxColumns := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(preview.Rows) > 0 {
				// A later row failing to parse does not invalidate the rows
				// already read — the same "keep partial, honest work" choice
				// renderPDFPages makes for a page that runs out of budget.
				break
			}
			return nil, fmt.Errorf("%w: CSV could not be parsed", ErrRender)
		}
		if len(preview.Rows) >= domain.MaxPreviewSheetRows {
			// Stop reading entirely rather than scanning the rest of the file
			// just to count it — the row cap is a cost ceiling, and counting
			// every row past it would defeat that.
			preview.TruncatedRows = true
			break
		}
		row, truncatedRow := boundRow(record)
		if truncatedRow {
			preview.TruncatedColumns = true
		}
		if len(row) > maxColumns {
			maxColumns = len(row)
		}
		preview.Rows = append(preview.Rows, row)
	}
	if len(preview.Rows) == 0 {
		return nil, fmt.Errorf("%w: empty CSV", ErrRender)
	}
	preview.TotalRowsRead = len(preview.Rows)
	preview.Columns = columnLabels(maxColumns)

	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, fmt.Errorf("%w: encode sheet preview", ErrRender)
	}
	return encoded, nil
}

// boundRow truncates one record to domain.MaxPreviewSheetColumns columns and
// each cell to domain.MaxPreviewCellBytes, reporting whether it cut anything.
func boundRow(record []string) (row []string, truncatedColumns bool) {
	if len(record) > domain.MaxPreviewSheetColumns {
		record = record[:domain.MaxPreviewSheetColumns]
		truncatedColumns = true
	}
	row = make([]string, len(record))
	for i, cell := range record {
		if len(cell) > domain.MaxPreviewCellBytes {
			cell = cell[:domain.MaxPreviewCellBytes]
			truncatedColumns = true
		}
		row[i] = cell
	}
	return row, truncatedColumns
}

// sniffDelimiter reads the first non-empty line and picks whichever of the
// common delimiters appears most often outside quotes. Ties, and a line with
// none of them, default to comma.
func sniffDelimiter(data []byte) rune {
	line := firstNonEmptyLine(data)
	candidates := []rune{',', ';', '\t', '|'}
	best, bestCount := ',', 0
	for _, candidate := range candidates {
		count := countOutsideQuotes(line, candidate)
		if count > bestCount {
			best, bestCount = candidate, count
		}
	}
	return best
}

func firstNonEmptyLine(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) != "" {
			return trimmed
		}
	}
	return ""
}

func countOutsideQuotes(line string, delimiter rune) int {
	count := 0
	inQuotes := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case r == delimiter && !inQuotes:
			count++
		}
	}
	return count
}

// columnLabels builds spreadsheet-style positional column names — A, B, ...,
// Z, AA, AB, ... — for a CSV, which has no header concept this service should
// assume: guessing row one is a header and guessing wrong is worse than not
// guessing.
func columnLabels(count int) []string {
	labels := make([]string, count)
	for i := range labels {
		labels[i] = columnLabel(i)
	}
	return labels
}

func columnLabel(index int) string {
	var label []byte
	for {
		label = append([]byte{byte('A' + index%26)}, label...)
		index = index/26 - 1
		if index < 0 {
			break
		}
	}
	return string(label)
}
