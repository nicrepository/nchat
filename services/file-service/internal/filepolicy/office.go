package filepolicy

import (
	"context"
	"encoding/xml"
	"path"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/documentguard"
)

const (
	contentTypesNS = "http://schemas.openxmlformats.org/package/2006/content-types"
	packageRelsNS  = "http://schemas.openxmlformats.org/package/2006/relationships"
	officeRelsNS   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	odfOfficeNS    = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
	odfManifestNS  = "urn:oasis:names:tc:opendocument:xmlns:manifest:1.0"
)

type officeMain struct{ part, contentType, root, namespace string }

var officeMains = map[FormatID]officeMain{
	IDDOCX: {"word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml", "document", "http://schemas.openxmlformats.org/wordprocessingml/2006/main"},
	IDXLSX: {"xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml", "workbook", "http://schemas.openxmlformats.org/spreadsheetml/2006/main"},
	IDPPTX: {"ppt/presentation.xml", "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml", "presentation", "http://schemas.openxmlformats.org/presentationml/2006/main"},
}

func (z *zipInspection) part(ctx context.Context, name string) ([]byte, error) {
	f := z.files[strings.ToLower(name)]
	if f == nil || f.Name != name {
		return nil, errStructure
	}
	return z.readEntry(ctx, f)
}

func (z *zipInspection) validateOOXML(ctx context.Context, f Format) error {
	main := officeMains[f.ID]
	if z.files["mimetype"] != nil {
		return errStructure
	}
	for id, other := range officeMains {
		if id != f.ID && z.files[other.part] != nil {
			return errStructure
		}
	}
	if err := z.contentTypes(ctx, main); err != nil {
		return err
	}
	if err := z.relationships(ctx, main); err != nil {
		return err
	}
	b, err := z.part(ctx, main.part)
	if err != nil {
		return err
	}
	if err = xmlRoot(ctx, b, main.root, main.namespace); err != nil {
		return err
	}
	return z.mainBody(ctx, b, f.ID, main)
}

type mainBodyIndex struct {
	main     officeMain
	expected string
	body     bool
	refs     map[string]bool
}

func (z *zipInspection) mainBody(ctx context.Context, b []byte, id FormatID, main officeMain) error {
	index := mainBodyIndex{main: main, expected: map[FormatID]string{IDDOCX: "body", IDXLSX: "sheets", IDPPTX: "sldIdLst"}[id], refs: map[string]bool{}}
	if err := validateXML(ctx, b, index.visit); err != nil {
		return err
	}
	if !index.body {
		return errStructure
	}
	if id == IDDOCX {
		return nil
	}
	return z.mainReferences(ctx, main, id, index.refs)
}

func (m *mainBodyIndex) visit(e xml.StartElement, depth int) error {
	if depth == 2 && e.Name == (xml.Name{Space: m.main.namespace, Local: m.expected}) {
		if m.body {
			return errStructure
		}
		m.body = true
	}
	if e.Name.Local != "sheet" && e.Name.Local != "sldId" {
		return nil
	}
	if depth != 3 || e.Name.Space != m.main.namespace {
		return errStructure
	}
	ref := nsAttr(e, strings.TrimSuffix(officeRelsNS, "/"), "id")
	if ref == "" || m.refs[ref] {
		return errStructure
	}
	m.refs[ref] = true
	return nil
}

func (z *zipInspection) mainReferences(ctx context.Context, main officeMain, id FormatID, refs map[string]bool) error {
	if len(refs) == 0 {
		return errStructure
	}
	name := path.Join(path.Dir(main.part), "_rels", path.Base(main.part)+".rels")
	b, err := z.part(ctx, name)
	if err != nil {
		return err
	}
	expected := map[FormatID]string{IDXLSX: "worksheet", IDPPTX: "slide"}[id]
	err = validateXML(ctx, b, func(e xml.StartElement, _ int) error {
		ref := attr(e, "Id")
		if !refs[ref] {
			return nil
		}
		if attr(e, "Type") != officeRelsNS+expected {
			return errStructure
		}
		delete(refs, ref)
		return nil
	})
	if err != nil {
		return err
	}
	if len(refs) != 0 {
		return errStructure
	}
	return nil
}

func xmlRoot(ctx context.Context, b []byte, local, namespace string) error {
	first := true
	return validateXML(ctx, b, func(e xml.StartElement, depth int) error {
		if first && (e.Name.Local != local || e.Name.Space != namespace) {
			return errStructure
		}
		first = false
		return nil
	})
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

type contentTypeIndex struct {
	archive   *zipInspection
	main      officeMain
	seen      map[string]bool
	mainFound bool
}

func (z *zipInspection) contentTypes(ctx context.Context, main officeMain) error {
	f := z.files["[content_types].xml"]
	if f == nil || f.Name != "[Content_Types].xml" {
		return errStructure
	}
	b, err := z.readEntry(ctx, f)
	if err != nil {
		return err
	}
	index := contentTypeIndex{archive: z, main: main, seen: map[string]bool{}}
	if err = validateXML(ctx, b, index.visit); err != nil {
		return err
	}
	if !index.mainFound || !index.seen["xml"] || !index.seen["rels"] {
		return errStructure
	}
	return nil
}

func (c *contentTypeIndex) visit(e xml.StartElement, depth int) error {
	if e.Name.Space != contentTypesNS {
		return errStructure
	}
	if depth == 1 {
		if e.Name.Local != "Types" {
			return errStructure
		}
		return nil
	}
	if depth != 2 {
		return errStructure
	}
	key, err := c.archive.contentTypeEntry(e)
	if err != nil {
		return err
	}
	if c.seen[key] {
		return errStructure
	}
	c.seen[key] = true
	if key == "/"+c.main.part && attr(e, "ContentType") == c.main.contentType {
		c.mainFound = true
	}
	return nil
}

func (z *zipInspection) contentTypeEntry(e xml.StartElement) (string, error) {
	t := strings.ToLower(attr(e, "ContentType"))
	if activeContentType(t) {
		return "", errActive
	}
	switch e.Name.Local {
	case "Default":
		return defaultContentType(e, t)
	case "Override":
		return z.overrideContentType(e)
	default:
		return "", errStructure
	}
}

func activeContentType(t string) bool {
	for _, bad := range []string{"macro", "vba", "oleobject", "activex"} {
		if strings.Contains(t, bad) {
			return true
		}
	}
	return t == ""
}

func defaultContentType(e xml.StartElement, t string) (string, error) {
	ext := attr(e, "Extension")
	expected := map[string]string{"xml": "application/xml", "rels": "application/vnd.openxmlformats-package.relationships+xml"}
	if expected[ext] != "" && t != expected[ext] {
		return "", errStructure
	}
	if ext == "" || strings.ContainsAny(ext, "/\\.") {
		return "", errStructure
	}
	return strings.ToLower(ext), nil
}

func (z *zipInspection) overrideContentType(e xml.StartElement) (string, error) {
	name := attr(e, "PartName")
	file := z.files[strings.ToLower(strings.TrimPrefix(name, "/"))]
	if !strings.HasPrefix(name, "/") || file == nil {
		return "", errStructure
	}
	if "/"+file.Name != name {
		return "", errStructure
	}
	return name, nil
}

func (z *zipInspection) relationships(ctx context.Context, main officeMain) error {
	rootFound := false
	for _, f := range z.order {
		if !documentguard.RelationshipPart(f.Name) {
			continue
		}
		b, err := z.readEntry(ctx, f)
		if err != nil {
			return err
		}
		root, err := z.relationshipPart(ctx, f.Name, b, main)
		if err != nil {
			return err
		}
		rootFound = rootFound || root
	}
	if !rootFound {
		return errStructure
	}
	return nil
}

type relationshipIndex struct {
	ctx     context.Context
	archive *zipInspection
	name    string
	main    officeMain
	seen    map[string]bool
	root    bool
}

func (z *zipInspection) relationshipPart(ctx context.Context, name string, b []byte, main officeMain) (bool, error) {
	index := relationshipIndex{ctx: ctx, archive: z, name: name, main: main, seen: map[string]bool{}}
	err := validateXML(ctx, b, index.visit)
	return index.root, err
}

func (r *relationshipIndex) visit(e xml.StartElement, depth int) error {
	if e.Name.Space != packageRelsNS {
		return errStructure
	}
	if depth == 1 {
		if e.Name.Local != "Relationships" {
			return errStructure
		}
		return nil
	}
	if depth != 2 || e.Name.Local != "Relationship" {
		return errStructure
	}
	id := attr(e, "Id")
	if id == "" || r.seen[id] {
		return errStructure
	}
	r.seen[id] = true
	target, err := r.archive.relationshipTarget(r.name, e)
	if err != nil {
		return err
	}
	return r.inspectTarget(e, target)
}

func (r *relationshipIndex) inspectTarget(e xml.StartElement, target string) error {
	if attr(e, "Type") == officeRelsNS+"worksheet" {
		body, err := r.archive.part(r.ctx, target)
		if err != nil {
			return err
		}
		if err = validateWorksheet(r.ctx, body); err != nil {
			return err
		}
	}
	return r.rootRelationship(e, target)
}

func (r *relationshipIndex) rootRelationship(e xml.StartElement, target string) error {
	if r.name != "_rels/.rels" || attr(e, "Type") != officeRelsNS+"officeDocument" {
		return nil
	}
	if r.root || target != r.main.part {
		return errStructure
	}
	r.root = true
	return nil
}

func activeRelationship(t string) bool { return documentguard.ActiveRelationship(t) }

func (z *zipInspection) relationshipTarget(name string, e xml.StartElement) (string, error) {
	if activeRelationship(attr(e, "Type")) {
		return "", errActive
	}
	base := path.Join(path.Dir(path.Dir(name)), "source.xml")
	if strings.EqualFold(name, "_rels/.rels") {
		base = ""
	}
	target, err := documentguard.Resolve(base, attr(e, "Target"), false, z.hasPart)
	if err != nil {
		return "", errActive
	}
	return target, nil
}

func (z *zipInspection) hasPart(name string) bool {
	f := z.files[strings.ToLower(name)]
	return f != nil && f.Name == name
}

func (z *zipInspection) validateODF(ctx context.Context, f Format) error {
	m := z.files["mimetype"]
	if m == nil || z.order[0] != m || m.Method != 0 {
		return errStructure
	}
	b, err := z.part(ctx, "mimetype")
	if err != nil {
		return err
	}
	if string(b) != f.MIME {
		return errStructure
	}
	b, err = z.part(ctx, "content.xml")
	if err != nil {
		return err
	}
	if err = odfBody(ctx, b, f.ID); err != nil {
		return err
	}
	return z.odfManifest(ctx, f)
}

func odfBody(ctx context.Context, b []byte, id FormatID) error {
	expected := map[FormatID]string{IDODT: "text", IDODS: "spreadsheet", IDODP: "presentation"}[id]
	found := false
	err := validateXML(ctx, b, func(e xml.StartElement, depth int) error {
		if depth == 1 && e.Name != (xml.Name{Space: odfOfficeNS, Local: "document-content"}) {
			return errStructure
		}
		if depth == 3 && e.Name == (xml.Name{Space: odfOfficeNS, Local: expected}) {
			found = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return errStructure
	}
	return nil
}

type odfManifestIndex struct {
	archive *zipInspection
	mime    string
	root    bool
	seen    map[string]bool
}

func (z *zipInspection) odfManifest(ctx context.Context, f Format) error {
	b, err := z.part(ctx, "META-INF/manifest.xml")
	if err != nil {
		return err
	}
	index := odfManifestIndex{archive: z, mime: f.MIME, seen: map[string]bool{}}
	if err = validateXML(ctx, b, index.visit); err != nil {
		return err
	}
	if !index.root || !index.seen["content.xml"] {
		return errStructure
	}
	return nil
}

func (m *odfManifestIndex) visit(e xml.StartElement, depth int) error {
	if depth == 1 {
		if e.Name != (xml.Name{Space: odfManifestNS, Local: "manifest"}) {
			return errStructure
		}
		return nil
	}
	if depth != 2 || e.Name != (xml.Name{Space: odfManifestNS, Local: "file-entry"}) {
		return errActive
	}
	name := nsAttr(e, odfManifestNS, "full-path")
	if m.seen[name] {
		return errStructure
	}
	m.seen[name] = true
	if name == "/" {
		m.root = nsAttr(e, odfManifestNS, "media-type") == m.mime
		return nil
	}
	return m.archive.odfManifestEntry(name)
}

func nsAttr(e xml.StartElement, namespace, name string) string {
	for _, a := range e.Attr {
		if a.Name.Space == namespace && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (z *zipInspection) odfManifestEntry(name string) error {
	if !safeEntryName(name) {
		return errStructure
	}
	if strings.HasSuffix(name, "/") {
		return nil
	}
	if z.files[strings.ToLower(name)] == nil {
		return errStructure
	}
	return nil
}

// Only Transitional worksheets are supported. Namespace rebinding cannot hide
// formulas; legacy preview renderers independently read cached values only.
func validateWorksheet(ctx context.Context, b []byte) error {
	return validateXML(ctx, b, func(e xml.StartElement, depth int) error {
		if e.Name.Space != documentguard.SpreadsheetNS {
			return errStructure
		}
		if depth == 1 && e.Name.Local != "worksheet" {
			return errStructure
		}
		if e.Name.Local == "f" {
			return errActive
		}
		return nil
	})
}
