package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nicrepository/nchat/libs/go/platform/documentguard"
)

const (
	maxDocumentExpandedBytes = documentguard.MaxExpandedBytes
	maxDocumentEntryBytes    = documentguard.MaxXMLBytes
)

var ErrUnsafeDocument = errors.New("unsafe document container")

// InspectDocumentContainer identifies an OOXML/OpenDocument archive from its
// contents and rejects container features that must never reach a converter.
// The filename and client-declared MIME are deliberately not inputs.
func InspectDocumentContainer(data []byte) (string, error) {
	return inspectDocumentContainer(context.Background(), data)
}
func inspectDocumentContainer(ctx context.Context, data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: invalid zip", ErrUnsafeDocument)
	}
	index, err := documentguard.ArchiveXML(ctx, reader)
	if err != nil {
		return "", fmt.Errorf("%w: package inspection", ErrUnsafeDocument)
	}
	odfMIME := index.MIME

	switch {
	case index.Has("word/document.xml"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case index.Has("xl/workbook.xml"):
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case index.Has("ppt/presentation.xml"):
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	case odfMIME == "application/vnd.oasis.opendocument.text", odfMIME == "application/vnd.oasis.opendocument.spreadsheet", odfMIME == "application/vnd.oasis.opendocument.presentation":
		return odfMIME, nil
	default:
		return "", fmt.Errorf("%w: unsupported package", ErrUnsafeDocument)
	}
}

func readZipEntry(entry *zip.File, limit int) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open entry", ErrUnsafeDocument)
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil, fmt.Errorf("%w: read entry", ErrUnsafeDocument)
	}
	return body, nil
}
