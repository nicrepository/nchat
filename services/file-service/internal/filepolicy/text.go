package filepolicy

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicrepository/nchat/libs/go/platform/documentguard"
)

func inspectText(ctx context.Context, r io.Reader, size int64, f Format) error {
	if f.Strategy == Text {
		return inspectPlainText(ctx, r, size)
	}
	if size > MaxTextBytes {
		return errBudget
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, r}, MaxTextBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errStructure
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !passiveText(data) {
		return errActive
	}
	switch f.Strategy {
	case JSON:
		if !json.Valid(data) {
			return errStructure
		}
	case XML:
		return inspectXML(ctx, data)
	case Delimited:
		return validateDelimited(data, f.ID)
	}
	return nil
}

// Plain text uses bounded streaming instead of a retained parser input. The
// extension selects a passive text grammar, never an executable exception.
func inspectPlainText(ctx context.Context, source io.Reader, size int64) error {
	r := contextReader{ctx, source}
	buffer := make([]byte, 64<<10)
	var count int64
	var runeTail, tokenTail []byte
	for {
		n, err := r.Read(buffer)
		count += int64(n)
		runeTail = append(runeTail, buffer[:n]...)
		window := runeTail
		tail, validationErr := validateTextRunes(window)
		if validationErr != nil {
			return validationErr
		}
		runeTail = append([]byte(nil), tail...)
		tokens := bytes.ToLower(append(tokenTail, buffer[:n]...))
		if forbiddenTextWindow(tokens) {
			return errActive
		}
		tokenTail = append(tokenTail[:0], tokens[max(0, len(tokens)-32):]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	return completeText(count, size, runeTail)
}

func validateTextRunes(b []byte) ([]byte, error) {
	for len(b) > 0 {
		ch, n := utf8.DecodeRune(b)
		if ch == utf8.RuneError && n == 1 {
			if !utf8.FullRune(b) {
				return b, nil
			}
			return nil, errActive
		}
		if forbiddenTextRune(ch) {
			return nil, errActive
		}
		b = b[n:]
	}
	return nil, nil
}

func forbiddenTextRune(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
}

func forbiddenTextWindow(b []byte) bool {
	return containsToken(b, [][]byte{[]byte("<!doctype"), []byte("<!entity"), []byte("<?xml"), []byte("<html"), []byte("<script"), []byte("<svg"), []byte("#!"), []byte("-----begin private key"), []byte("-----begin rsa private key")})
}

func passiveText(data []byte) bool {
	if !utf8.Valid(data) || dangerousHead(bytes.TrimSpace(data)) {
		return false
	}
	for _, r := range string(data) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	lower := bytes.ToLower(data)
	return !containsToken(lower, [][]byte{[]byte("<!doctype"), []byte("<!entity"), []byte("<html"), []byte("<script"), []byte("<svg"), []byte("-----begin private key"), []byte("-----begin rsa private key")})
}

func validateDelimited(data []byte, id FormatID) error {
	r := csv.NewReader(bytes.NewReader(data))
	if id == IDTSV {
		r.Comma = '\t'
	}
	first, err := passiveCSVRecord(r)
	if errors.Is(err, io.EOF) {
		return errStructure
	}
	if err != nil {
		return err
	}
	if len(first) < 2 {
		return errStructure
	}
	rows := 1
	for {
		_, err = passiveCSVRecord(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		rows++
	}
	if rows < 2 {
		return errStructure
	}
	return nil
}

func passiveCSVRecord(r *csv.Reader) ([]string, error) {
	record, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}
	if err != nil {
		return nil, errStructure
	}
	if activeRecord(record) {
		return nil, errActive
	}
	return record, nil
}

func activeRecord(record []string) bool {
	for _, cell := range record {
		if formulaCell(cell) {
			return true
		}
	}
	return false
}

func formulaCell(cell string) bool {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return false
	}
	switch cell[0] {
	case '=', '@':
		return true
	case '+', '-':
		_, err := strconv.ParseFloat(cell, 64)
		return err != nil
	default:
		return strings.HasPrefix(cell, "＝") || strings.HasPrefix(cell, "＠")
	}
}

type xmlVisit func(xml.StartElement, int) error

// encoding/xml has no external resolver. Directives (including DTD/entity
// declarations), non-XML processing instructions and active elements are refused.
func validateXML(ctx context.Context, data []byte, visit xmlVisit) error {
	d := xml.NewDecoder(contextReader{ctx, bytes.NewReader(data)})
	depth, roots := 0, 0
	scripts := scriptSection{}
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errStructure
		}
		if err = xmlToken(token, &depth, &roots, visit, &scripts); err != nil {
			return err
		}
	}
	if depth != 0 || roots != 1 {
		return errStructure
	}
	return ctx.Err()
}

func xmlToken(token xml.Token, depth, roots *int, visit xmlVisit, scripts *scriptSection) error {
	if err := scripts.inspect(token); err != nil {
		return err
	}
	switch t := token.(type) {
	case xml.Directive:
		return errActive
	case xml.ProcInst:
		if !validXMLDeclaration(t, *depth, *roots) {
			return errActive
		}
	case xml.StartElement:
		return startXML(t, depth, roots, visit)
	case xml.EndElement:
		*depth--
	case xml.CharData:
		if *depth == 0 && strings.TrimSpace(string(t)) != "" {
			return errStructure
		}
	}
	return nil
}

func startXML(t xml.StartElement, depth, roots *int, visit xmlVisit) error {
	if *depth == 0 {
		*roots++
	}
	*depth++
	if *depth > MaxXMLDepth || len(t.Attr) > MaxXMLAttributes {
		return errBudget
	}
	if err := passiveElement(t); err != nil {
		return err
	}
	if visit != nil {
		return visit(t, *depth)
	}
	return nil
}

var activeElements = map[string]bool{
	"script": true, "html": true, "svg": true, "iframe": true, "object": true,
	"oleobject": true, "activex": true, "altchunk": true, "fldchar": true, "instrtext": true, "fldsimple": true,
	"ddelink": true, "externallink": true, "applet": true, "plugin": true, "encryption-data": true, "encrypted-package": true,
	"event-listener": true, "event-listeners": true, "execute-macro": true,
}

func passiveElement(t xml.StartElement) error {
	if activeElements[strings.ToLower(t.Name.Local)] || t.Name.Space == "http://www.w3.org/2001/XInclude" {
		return errActive
	}
	seen := map[xml.Name]bool{}
	for _, a := range t.Attr {
		if seen[a.Name] {
			return errStructure
		}
		seen[a.Name] = true
		if activeAttribute(a) {
			return errActive
		}
	}
	return nil
}

func activeAttribute(a xml.Attr) bool {
	name := strings.ToLower(a.Name.Local)
	if strings.HasPrefix(name, "on") || name == "formula" {
		return true
	}
	if name == "targetmode" && a.Value != "Internal" {
		return true
	}

	return false
}

func validXMLDeclaration(t xml.ProcInst, depth, roots int) bool {
	return t.Target == "xml" && depth == 0 && roots == 0
}

func completeText(count, size int64, tail []byte) error {
	if count != size || len(tail) > 0 {
		return errStructure
	}
	return nil
}

// ODF commonly emits an empty office:scripts marker. Contents, including child
// elements, are rejected; an empty marker is not evidence of a macro.
type scriptSection struct{ inside bool }

func (s *scriptSection) inspect(token xml.Token) error {
	switch t := token.(type) {
	case xml.StartElement:
		if s.inside {
			return errActive
		}
		if !strings.EqualFold(t.Name.Local, "scripts") {
			return nil
		}
		if t.Name.Space != odfOfficeNS {
			return errActive
		}
		s.inside = true
	case xml.CharData:
		if s.inside && strings.TrimSpace(string(t)) != "" {
			return errActive
		}
	case xml.EndElement:
		s.inside = false
	}
	return nil
}

func inspectXML(ctx context.Context, data []byte) error {
	if err := validateXML(ctx, data, nil); err != nil {
		return err
	}
	if err := documentguard.Inspect(ctx, data, "", nil); err != nil {
		return errActive
	}
	return nil
}
