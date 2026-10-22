// Package filepolicy identifies untrusted attachments independently of storage,
// antivirus verdicts and preview rendering. An Allow decision is not a scan.
package filepolicy

import (
	"mime"
	"path"
	"slices"
	"strings"
)

const Version = 1

type Decision string

const (
	Allow         Decision = "allow"
	Restricted    Decision = "restricted"
	Reject        Decision = "reject"
	Indeterminate Decision = "indeterminate"
)

type Family string

const (
	Document Family = "document"
	Data     Family = "data"
	Office   Family = "office"
	Image    Family = "image"
	Media    Family = "media"
	Archive  Family = "archive"
	Email    Family = "email"
	CAD      Family = "cad"
	Unknown  Family = "unknown"
)

type Code string

const (
	Recognized     Code = "recognized"
	Unsupported    Code = "unsupported_format"
	Disabled       Code = "format_disabled"
	Mismatch       Code = "format_mismatch"
	Invalid        Code = "invalid_structure"
	Active         Code = "active_content"
	BudgetExceeded Code = "inspection_limit"
	Opaque         Code = "opaque_content"
)

type Strategy string

const (
	Signature   Strategy = "signature"
	Text        Strategy = "text"
	JSON        Strategy = "json"
	XML         Strategy = "xml"
	Delimited   Strategy = "delimited"
	ZIP         Strategy = "zip"
	OOXML       Strategy = "ooxml"
	ODF         Strategy = "odf"
	Unavailable Strategy = "unavailable"
	RFC822      Strategy = "rfc822"
)

type FormatID string

const (
	IDUnknown FormatID = "unknown"
	IDEML     FormatID = "eml"
	IDPDF     FormatID = "pdf"
	IDTXT     FormatID = "txt"
	IDMD      FormatID = "md"
	IDCSV     FormatID = "csv"
	IDTSV     FormatID = "tsv"
	IDJSON    FormatID = "json"
	IDXML     FormatID = "xml"
	IDDOCX    FormatID = "docx"
	IDXLSX    FormatID = "xlsx"
	IDPPTX    FormatID = "pptx"
	IDODT     FormatID = "odt"
	IDODS     FormatID = "ods"
	IDODP     FormatID = "odp"
	IDZIP     FormatID = "zip"
	IDJPEG    FormatID = "jpeg"
	IDPNG     FormatID = "png"
	IDGIF     FormatID = "gif"
	IDWEBP    FormatID = "webp"
	IDMP3     FormatID = "mp3"
	IDOGG     FormatID = "ogg"
	IDWAV     FormatID = "wav"
	IDMP4     FormatID = "mp4"
	IDWEBM    FormatID = "webm"
	IDMOV     FormatID = "mov"
	IDAVI     FormatID = "avi"
)

type Risk string

const (
	RiskDocument  Risk = "document"
	RiskEmail     Risk = "email"
	RiskMarkup    Risk = "markup"
	RiskContainer Risk = "container"
)

type Format struct {
	ID         FormatID
	Family     Family
	Extensions []string
	MIME       string
	Aliases    []string
	Strategy   Strategy
	Risk       Risk
	Preview    bool
	Enterprise bool
	Decision   Decision
}

// Profiles only enable implementations in this catalog. Neither switch can
// enable macros, encrypted content or a format without an inspector.
type Profiles struct{ Data, Office, Archives bool }

type Result struct {
	Format       Format
	Decision     Decision
	Code         Code
	Version      int
	MIMEMismatch bool
}

var catalog = []Format{
	{ID: IDEML, Family: Email, Extensions: []string{".eml"}, MIME: "message/rfc822", Aliases: []string{"text/plain"}, Strategy: RFC822, Risk: RiskEmail, Enterprise: true, Decision: Allow},
	{ID: IDPDF, Family: Document, Extensions: []string{".pdf"}, MIME: "application/pdf", Strategy: Signature, Risk: RiskDocument, Preview: true, Decision: Allow},
	{ID: IDTXT, Family: Document, Extensions: []string{".txt"}, MIME: "text/plain", Strategy: Text, Decision: Allow},
	{ID: IDMD, Family: Document, Extensions: []string{".md", ".markdown"}, MIME: "text/markdown", Aliases: []string{"text/plain"}, Strategy: Text, Enterprise: true, Decision: Allow},
	{ID: IDCSV, Family: Data, Extensions: []string{".csv"}, MIME: "text/csv", Aliases: []string{"text/plain", "application/vnd.ms-excel"}, Strategy: Delimited, Preview: true, Decision: Allow},
	{ID: IDTSV, Family: Data, Extensions: []string{".tsv"}, MIME: "text/tab-separated-values", Aliases: []string{"text/plain"}, Strategy: Delimited, Enterprise: true, Decision: Allow},
	{ID: IDJSON, Family: Data, Extensions: []string{".json"}, MIME: "application/json", Aliases: []string{"text/plain", "text/json"}, Strategy: JSON, Enterprise: true, Decision: Allow},
	{ID: IDXML, Family: Data, Extensions: []string{".xml"}, MIME: "application/xml", Aliases: []string{"text/xml", "text/plain"}, Strategy: XML, Risk: RiskMarkup, Enterprise: true, Decision: Allow},
	{ID: IDDOCX, Family: Office, Extensions: []string{".docx"}, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Strategy: OOXML, Risk: RiskContainer, Preview: true, Enterprise: true, Decision: Allow},
	{ID: IDXLSX, Family: Office, Extensions: []string{".xlsx"}, MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Strategy: OOXML, Risk: RiskContainer, Preview: true, Enterprise: true, Decision: Allow},
	{ID: IDPPTX, Family: Office, Extensions: []string{".pptx"}, MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Strategy: OOXML, Risk: RiskContainer, Preview: true, Enterprise: true, Decision: Allow},
	{ID: IDODT, Family: Office, Extensions: []string{".odt"}, MIME: "application/vnd.oasis.opendocument.text", Strategy: ODF, Risk: RiskContainer, Preview: true, Enterprise: true, Decision: Allow},
	{ID: IDODS, Family: Office, Extensions: []string{".ods"}, MIME: "application/vnd.oasis.opendocument.spreadsheet", Strategy: ODF, Risk: RiskContainer, Preview: true, Enterprise: true, Decision: Allow},
	{ID: IDODP, Family: Office, Extensions: []string{".odp"}, MIME: "application/vnd.oasis.opendocument.presentation", Strategy: ODF, Risk: RiskContainer, Preview: false, Enterprise: true, Decision: Allow},
	{ID: IDZIP, Family: Archive, Extensions: []string{".zip"}, MIME: "application/zip", Aliases: []string{"application/x-zip-compressed"}, Strategy: ZIP, Risk: RiskContainer, Enterprise: true, Decision: Allow},
	{ID: IDJPEG, Family: Image, Extensions: []string{".jpg", ".jpeg"}, MIME: "image/jpeg", Aliases: []string{"image/pjpeg"}, Strategy: Signature, Preview: true, Decision: Allow},
	{ID: IDPNG, Family: Image, Extensions: []string{".png"}, MIME: "image/png", Strategy: Signature, Preview: true, Decision: Allow},
	{ID: IDGIF, Family: Image, Extensions: []string{".gif"}, MIME: "image/gif", Strategy: Signature, Preview: true, Decision: Allow},
	{ID: IDWEBP, Family: Image, Extensions: []string{".webp"}, MIME: "image/webp", Strategy: Signature, Decision: Allow},
	{ID: IDMP3, Family: Media, Extensions: []string{".mp3"}, MIME: "audio/mpeg", Aliases: []string{"audio/mp3"}, Strategy: Signature, Decision: Allow},
	{ID: IDOGG, Family: Media, Extensions: []string{".ogg", ".oga", ".opus"}, MIME: "application/ogg", Aliases: []string{"audio/ogg"}, Strategy: Signature, Decision: Allow},
	{ID: IDWAV, Family: Media, Extensions: []string{".wav"}, MIME: "audio/wave", Aliases: []string{"audio/wav", "audio/x-wav"}, Strategy: Signature, Decision: Allow},
	{ID: IDMP4, Family: Media, Extensions: []string{".mp4", ".m4a"}, MIME: "video/mp4", Aliases: []string{"audio/mp4"}, Strategy: Signature, Decision: Allow},
	{ID: IDWEBM, Family: Media, Extensions: []string{".webm"}, MIME: "video/webm", Aliases: []string{"audio/webm"}, Strategy: Signature, Decision: Allow},
	{ID: IDMOV, Family: Media, Extensions: []string{".mov"}, MIME: "video/quicktime", Strategy: Signature, Decision: Allow},
	{ID: IDAVI, Family: Media, Extensions: []string{".avi"}, MIME: "video/x-msvideo", Strategy: Signature, Decision: Allow},
}

var restricted = map[string]Family{
	".doc": Office, ".xls": Office, ".ppt": Office, ".msg": Email,
	".xlsb": Office, ".rtf": Document,
	".yaml": Data, ".yml": Data, ".bmp": Image, ".tif": Image, ".tiff": Image,
	".tar": Archive, ".gz": Archive, ".tgz": Archive, ".rar": Archive, ".7z": Archive,
	".dwg": CAD, ".dxf": CAD, ".step": CAD, ".stp": CAD, ".iges": CAD, ".igs": CAD, ".dwf": CAD,
	".pages": Office, ".numbers": Office, ".key": Office,
}

var blocked = []string{".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".sh", ".js", ".mjs", ".vbs", ".jar", ".apk", ".html", ".htm", ".svg", ".docm", ".xlsm", ".pptm", ".dotm", ".xlam", ".ppam", ".pem", ".p12", ".pfx"}

func byName(filename string) (Format, bool) {
	ext := strings.ToLower(path.Ext(filename))
	for _, f := range catalog {
		if slices.Contains(f.Extensions, ext) {
			return f, true
		}
	}
	return Format{ID: IDUnknown, Family: Unknown}, false
}

func enabled(f Format, p Profiles) bool {
	if !f.Enterprise {
		return true
	}
	switch f.Family {
	case Office:
		return p.Office
	case Archive:
		return p.Archives
	default:
		return p.Data
	}
}

func outcome(f Format, d Decision, c Code, declared string) Result {
	m, _, _ := mime.ParseMediaType(declared)
	mismatch := !plausibleMIME(f, m)
	return Result{Format: f, Decision: d, Code: c, Version: Version, MIMEMismatch: mismatch}
}

// PolicyPreviewMIME applies the persisted classification, without granting a
// future/unknown format a renderer through a legacy MIME fallback.
func PolicyPreviewMIME(m string, id FormatID, version int) string {
	if version == 0 {
		return PreviewMIME(m)
	}
	if version != Version {
		return ""
	}
	for _, f := range catalog {
		if f.ID == id && f.MIME == m && f.Preview {
			return PreviewMIME(m)
		}
	}
	return ""
}

// PreviewMIME adapts canonical classifications to the existing renderers. The
// renderer still validates the container and never replaces the original.
func PreviewMIME(m string) string {
	for _, f := range catalog {
		if f.MIME != m || !f.Preview {
			continue
		}
		switch f.Strategy {
		case OOXML, ODF:
			return "application/zip"
		case Delimited:
			return "text/plain"
		}
	}
	return m
}

func plausibleMIME(f Format, m string) bool {
	if m == "" || m == "application/octet-stream" || m == f.MIME || slices.Contains(f.Aliases, m) {
		return true
	}
	return m == "application/zip" && (f.Strategy == OOXML || f.Strategy == ODF)
}
