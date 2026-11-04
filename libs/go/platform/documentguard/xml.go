package documentguard

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MaxXMLBytes     = 8 << 20
	MaxDepth        = 64
	MaxAttributes   = 64
	XLinkNS         = "http://www.w3.org/1999/xlink"
	RelationshipsNS = "http://schemas.openxmlformats.org/package/2006/relationships"
	SpreadsheetNS   = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	officeNS        = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
)

func RelationshipPart(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".rels") }

// Inspect validates decoded XML names/attributes, never their serialized spelling.
// Input, depth and attributes are bounded; encoding/xml has no external resolver.
func Inspect(ctx context.Context, body []byte, part string, exists func(string) bool) error {
	if len(body) > MaxXMLBytes || !utf8.Valid(body) {
		return ErrUnsafe
	}
	state := xmlState{part: part, exists: exists}
	if err := state.decode(ctx, xml.NewDecoder(bytes.NewReader(body))); err != nil {
		return err
	}
	if state.depth != 0 || state.roots != 1 {
		return ErrUnsafe
	}
	return nil
}

func (s *xmlState) decode(ctx context.Context, d *xml.Decoder) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrUnsafe
		}
		if err = s.token(token); err != nil {
			return err
		}
	}
}

type xmlState struct {
	part         string
	exists       func(string) bool
	depth, roots int
	scripts      bool
}

func (s *xmlState) token(token xml.Token) error {
	switch t := token.(type) {

	case xml.StartElement:
		return s.start(t)
	case xml.EndElement:
		s.depth--
		if t.Name.Local == "scripts" {
			s.scripts = false
		}
	case xml.CharData:
		if (s.scripts || s.depth == 0) && strings.TrimSpace(string(t)) != "" {
			return ErrUnsafe
		}
	}
	return s.declaration(token)
}

func (s *xmlState) declaration(token xml.Token) error {
	switch t := token.(type) {
	case xml.Directive:
		return ErrUnsafe
	case xml.ProcInst:
		if t.Target != "xml" || s.depth != 0 || s.roots != 0 {
			return ErrUnsafe
		}
	}
	return nil
}

func (s *xmlState) start(e xml.StartElement) error {
	if s.scripts {
		return ErrUnsafe
	}
	if s.depth == 0 {
		s.roots++
	}
	s.depth++
	if s.depth > MaxDepth || len(e.Attr) > MaxAttributes {
		return ErrUnsafe
	}
	if err := s.element(e); err != nil {
		return err
	}
	if err := s.attributes(e); err != nil {
		return err
	}
	if RelationshipPart(s.part) {
		return s.relationship(e)
	}
	return nil
}

var active = map[string]bool{"script": true, "object": true, "object-ole": true, "plugin": true, "oleobject": true, "activex": true, "applet": true, "event-listener": true, "event-listeners": true, "execute-macro": true, "encryption-data": true, "encrypted-package": true, "altchunk": true, "fldchar": true, "instrtext": true, "fldsimple": true, "ddelink": true, "externallink": true}

func (s *xmlState) element(e xml.StartElement) error {
	if active[strings.ToLower(e.Name.Local)] || e.Name.Space == "http://www.w3.org/2001/XInclude" {
		return ErrUnsafe
	}
	if e.Name.Local == "scripts" {
		if e.Name.Space != officeNS {
			return ErrUnsafe
		}
		s.scripts = true
	}

	return nil
}

func (s *xmlState) attributes(e xml.StartElement) error {
	seen := map[xml.Name]bool{}
	for _, a := range e.Attr {
		if seen[a.Name] {
			return ErrUnsafe
		}
		seen[a.Name] = true
		if err := s.attribute(a); err != nil {
			return err
		}
	}
	return nil
}

func (s *xmlState) attribute(a xml.Attr) error {
	name := strings.ToLower(a.Name.Local)
	if strings.HasPrefix(name, "on") {
		return ErrUnsafe
	}
	if name == "targetmode" && a.Value != "Internal" {
		return ErrUnsafe
	}
	if a.Name.Local == "base" && a.Name.Space == "http://www.w3.org/XML/1998/namespace" {
		return ErrUnsafe
	}
	// Unknown/unbound prefixes fail closed rather than being interpreted as xlink.
	if name == "href" {
		if a.Name.Space != XLinkNS {
			return ErrUnsafe
		}
		_, err := Resolve(s.part, a.Value, true, s.exists)
		return err
	}
	return nil
}

func (s *xmlState) relationship(e xml.StartElement) error {
	if e.Name.Space != RelationshipsNS {
		return ErrUnsafe
	}
	if s.depth == 1 {
		if e.Name.Local != "Relationships" {
			return ErrUnsafe
		}
		return nil
	}
	if s.depth != 2 || e.Name.Local != "Relationship" {
		return ErrUnsafe
	}
	if !relationshipAttributes(e) {
		return ErrUnsafe
	}
	if ActiveRelationship(Attribute(e, "Type")) {
		return ErrUnsafe
	}
	source, err := relationshipSource(s.part)
	if err != nil {
		return err
	}
	_, err = Resolve(source, Attribute(e, "Target"), false, s.exists)
	return err
}

func Attribute(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func ActiveRelationship(value string) bool {
	name, ok := relationshipTypeName(value)
	if !ok {
		return true
	}
	switch name {
	case "vbaproject", "vbaprojectsignature", "oleobject", "activex", "activexcontrol", "attachedtemplate", "externallink", "externallinkpath", "package":
		return true
	}
	return false
}

func relationshipSource(part string) (string, error) {
	if strings.EqualFold(part, "_rels/.rels") {
		return "", nil
	}
	dir := path.Dir(part)
	if path.Base(dir) != "_rels" {
		return "", ErrUnsafe
	}
	return path.Join(path.Dir(dir), path.Base(part)[:len(path.Base(part))-len(".rels")]), nil
}

func relationshipAttributes(e xml.StartElement) bool {
	if Attribute(e, "Id") == "" {
		return false
	}
	for _, a := range e.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			return false
		}
		switch a.Name.Local {
		case "Id", "Type", "Target", "TargetMode":
		default:
			return false
		}
	}
	return true
}

func relationshipTypeName(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || !canonicalTypeURI(u) {
		return "", false
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return "", false
	}
	return strings.ToLower(name), true
}

// Relationship types are identifiers, not fetch destinations. Permit canonical
// HTTP(S) identifiers only, with no escaping or ignored suffixes to reinterpret.
func canonicalTypeURI(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != "" && u.User == nil && u.Opaque == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.EscapedPath() == u.Path
}
