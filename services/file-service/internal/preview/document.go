package preview

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	maxDocumentEntries       = 2048
	maxDocumentExpandedBytes = 32 << 20
	maxDocumentEntryBytes    = 8 << 20
	maxDocumentExpansionRate = 100
)

var ErrUnsafeDocument = errors.New("unsafe document container")

// InspectDocumentContainer identifies an OOXML/OpenDocument archive from its
// contents and rejects container features that must never reach a converter.
// The filename and client-declared MIME are deliberately not inputs.
func InspectDocumentContainer(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: invalid zip", ErrUnsafeDocument)
	}
	if len(reader.File) == 0 || len(reader.File) > maxDocumentEntries {
		return "", fmt.Errorf("%w: entry count", ErrUnsafeDocument)
	}
	var total uint64
	names := make(map[string]struct{}, len(reader.File))
	var odfMIME string
	for _, entry := range reader.File {
		name := strings.ReplaceAll(entry.Name, "\\", "/")
		clean := path.Clean(name)
		lower := strings.ToLower(clean)
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(name, "/") || strings.Contains(name, "\x00") {
			return "", fmt.Errorf("%w: invalid entry path", ErrUnsafeDocument)
		}
		if entry.UncompressedSize64 > maxDocumentEntryBytes {
			return "", fmt.Errorf("%w: entry too large", ErrUnsafeDocument)
		}
		total += entry.UncompressedSize64
		if total > maxDocumentExpandedBytes || (entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > maxDocumentExpansionRate) {
			return "", fmt.Errorf("%w: expansion limit", ErrUnsafeDocument)
		}
		if strings.HasSuffix(lower, "vbaproject.bin") || strings.Contains(lower, "/activex/") || strings.Contains(lower, "/embeddings/") || strings.Contains(lower, "/externallinks/") || strings.HasPrefix(lower, "object ") || strings.HasPrefix(lower, "scripts/") || strings.HasPrefix(lower, "basic/") {
			return "", fmt.Errorf("%w: active content", ErrUnsafeDocument)
		}
		names[lower] = struct{}{}
		if lower == "mimetype" {
			body, readErr := readZipEntry(entry, 256)
			if readErr != nil {
				return "", readErr
			}
			odfMIME = strings.TrimSpace(string(body))
		}
		if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") {
			body, readErr := readZipEntry(entry, maxDocumentEntryBytes)
			if readErr != nil {
				return "", readErr
			}
			folded := strings.ToLower(string(body))
			if strings.Contains(folded, "<!doctype") || strings.Contains(folded, "<!entity") || strings.Contains(folded, `targetmode="external"`) || strings.Contains(folded, "targetmode='external'") || hasNonEmptyScriptsElement(body) || strings.Contains(folded, "<script:") || strings.Contains(folded, "<draw:object") || strings.Contains(folded, "<draw:plugin") || hasExternalODFReference(folded) {
				return "", fmt.Errorf("%w: active xml", ErrUnsafeDocument)
			}
		}
	}
	switch {
	case hasName(names, "word/document.xml"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case hasName(names, "xl/workbook.xml"):
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case hasName(names, "ppt/presentation.xml"):
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	case odfMIME == "application/vnd.oasis.opendocument.text", odfMIME == "application/vnd.oasis.opendocument.spreadsheet", odfMIME == "application/vnd.oasis.opendocument.presentation":
		return odfMIME, nil
	default:
		return "", fmt.Errorf("%w: unsupported package", ErrUnsafeDocument)
	}
}

func hasNonEmptyScriptsElement(body []byte) bool {
	if !bytes.Contains(bytes.ToLower(body), []byte("scripts")) {
		return false
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	inScripts := false
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		switch value := token.(type) {
		case xml.StartElement:
			if inScripts {
				return true
			}
			if strings.EqualFold(value.Name.Local, "scripts") {
				inScripts, depth = true, 1
			}
		case xml.EndElement:
			if inScripts {
				depth--
				if depth == 0 {
					inScripts = false
				}
			}
		case xml.CharData:
			if inScripts && strings.TrimSpace(string(value)) != "" {
				return true
			}
		}
	}
}

func hasExternalODFReference(xml string) bool {
	for _, quote := range []string{`xlink:href="`, "xlink:href='"} {
		for _, scheme := range []string{"http:", "https:", "file:", "ftp:"} {
			if strings.Contains(xml, quote+scheme) {
				return true
			}
		}
	}
	return false
}

func hasName(names map[string]struct{}, name string) bool { _, ok := names[name]; return ok }

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
