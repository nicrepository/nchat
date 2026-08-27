package preview

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xuri/excelize/v2"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// xlsxSpreadsheetMIME is InspectDocumentContainer's own returned MIME for an
// OOXML spreadsheet — see internal/preview/document.go. Compared literally
// rather than re-detected, so a container that identifies as anything else
// (a Word or PowerPoint file with an .xlsx-shaped upload, an ODS package)
// never reaches excelize at all.
const xlsxSpreadsheetMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// renderXLSX validates an XLSX container, reads only its first sheet's cached
// cell values, and returns the same bounded sheetPreview JSON shape CSV does.
//
// # Why the container gate runs first
//
// InspectDocumentContainer (document.go) already rejects zip bombs, path
// traversal, excessive entry counts/expansion ratios and active-content
// markers (vbaProject.bin, ActiveX, embeddings, external links) in an
// OOXML/ODF zip — exactly the non-goals this preview must not touch
// (macros, external connections, embedded content). Running it before
// excelize ever opens the bytes means a hostile file is refused at the
// cheap, already-tested layer, not discovered by excelize's own much weaker
// limits.
//
// # Why "never evaluate anything" needs no code here
//
// excelize is a data library, not a spreadsheet engine: GetCellValue always
// returns the cached value the workbook's own XML stored the last time a
// real spreadsheet application saved it — it never recalculates a formula.
// GetCellFormula exists on the API and is simply never called; nothing here
// ever touches pictures, embedded objects or external references either. The
// non-evaluation guarantee comes from the library's architecture, not from a
// flag this code has to remember to set.
func renderXLSX(data []byte) ([]byte, error) {
	detected, err := InspectDocumentContainer(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	if detected != xlsxSpreadsheetMIME {
		return nil, fmt.Errorf("%w: container is not an XLSX spreadsheet", ErrUnsupported)
	}

	workbook, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: XLSX could not be opened", ErrRender)
	}
	defer func() { _ = workbook.Close() }()

	sheets := workbook.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("%w: workbook has no sheets", ErrUnsupported)
	}
	// The first sheet in declared order — workbook.xml's own order, exactly
	// as a spreadsheet application would open it — never the "active sheet",
	// which can differ and would mean trusting more of the file's own claims
	// about itself.
	firstSheet := sheets[0]

	rows, err := workbook.Rows(firstSheet)
	if err != nil {
		return nil, fmt.Errorf("%w: sheet could not be read", ErrRender)
	}
	defer func() { _ = rows.Close() }()

	preview := sheetPreview{Rows: make([][]string, 0, domain.MaxPreviewSheetRows)}
	maxColumns := 0
	for rows.Next() {
		if len(preview.Rows) >= domain.MaxPreviewSheetRows {
			preview.TruncatedRows = true
			break
		}
		record, err := rows.Columns()
		if err != nil {
			if len(preview.Rows) > 0 {
				break
			}
			return nil, fmt.Errorf("%w: row could not be read", ErrRender)
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
		return nil, fmt.Errorf("%w: sheet has no rows", ErrUnsupported)
	}
	preview.TotalRowsRead = len(preview.Rows)
	preview.Columns = columnLabels(maxColumns)

	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, fmt.Errorf("%w: encode sheet preview", ErrRender)
	}
	return encoded, nil
}
