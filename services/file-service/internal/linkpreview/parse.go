package linkpreview

import (
	"bytes"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Field ceilings. Remote metadata is attacker-controlled, so "the site said so"
// is never a reason to carry a megabyte of it into a JSON response and into
// every cache entry.
const (
	maxTitleRunes       = 300
	maxDescriptionRunes = 1000
	maxSiteNameRunes    = 100
)

// extract reads the Open Graph metadata out of an HTML document.
//
// It tokenises rather than building a tree: nothing but <head> is of interest,
// so there is no reason to materialise a document, and stopping at <body> means
// a page whose interesting part is the first kilobyte costs the first kilobyte.
// Malformed markup is not an error — the tokeniser yields what it can and the
// missing fields are simply absent.
//
// Nothing here executes, resolves or fetches anything. The document is text.
func extract(base *url.URL, body []byte) Preview {
	var collector metadataCollector
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			// End of document, or bytes the tokeniser could not continue past.
			// Either way, what was collected is what there is.
			return collector.finalize(base)
		case html.TextToken:
			collector.appendTitleText(tokenizer)
		case html.EndTagToken:
			collector.closeTag(tokenizer)
		case html.StartTagToken, html.SelfClosingTagToken:
			if collector.openTag(tokenizer) {
				return collector.finalize(base)
			}
		}
	}
}

// metadataCollector accumulates what a document declares as it is tokenised.
//
// htmlTitle and htmlDescription are the non-Open-Graph fallbacks. They are kept
// apart from the Open Graph fields and applied last, because a page declaring
// both must be read as having declared the Open Graph one — a <meta
// name="description"> appearing first in the document must not win by position.
type metadataCollector struct {
	preview         Preview
	rawImage        string
	htmlTitle       string
	htmlDescription string
	inTitleTag      bool
}

// openTag handles a start tag and reports whether the part of the document
// worth reading has ended.
func (c *metadataCollector) openTag(tokenizer *html.Tokenizer) (done bool) {
	name, hasAttr := tokenizer.TagName()
	switch string(name) {
	case "body":
		// Open Graph is declared in <head>. Anything past this point is page
		// content, which this feature has no interest in.
		return true
	case "title":
		c.inTitleTag = c.htmlTitle == ""
	case "meta":
		if hasAttr {
			c.applyMeta(readMetaTag(tokenizer))
		}
	}
	return false
}

func (c *metadataCollector) closeTag(tokenizer *html.Tokenizer) {
	if name, _ := tokenizer.TagName(); string(name) == "title" {
		c.inTitleTag = false
	}
}

func (c *metadataCollector) appendTitleText(tokenizer *html.Tokenizer) {
	if c.inTitleTag {
		c.htmlTitle += string(tokenizer.Text())
	}
}

// applyMeta records one <meta> element under the field its key identifies. A
// key this feature does not use is ignored rather than stored.
func (c *metadataCollector) applyMeta(tag metaTag) {
	switch tag.key() {
	case "og:title":
		keepFirst(&c.preview.Title, tag.content)
	case "og:description":
		keepFirst(&c.preview.Description, tag.content)
	case "og:image", "og:image:url":
		keepFirst(&c.rawImage, tag.content)
	case "og:site_name":
		keepFirst(&c.preview.SiteName, tag.content)
	case "description":
		keepFirst(&c.htmlDescription, tag.content)
	}
}

// finalize normalises the collected values into the response.
func (c *metadataCollector) finalize(base *url.URL) Preview {
	preview := c.preview
	preview.Title = normalize(preview.Title, maxTitleRunes)
	if preview.Title == "" {
		preview.Title = normalize(c.htmlTitle, maxTitleRunes)
	}
	preview.Description = normalize(preview.Description, maxDescriptionRunes)
	if preview.Description == "" {
		preview.Description = normalize(c.htmlDescription, maxDescriptionRunes)
	}
	preview.SiteName = normalize(preview.SiteName, maxSiteNameRunes)
	preview.ImageURL = imageURL(base, c.rawImage)
	return preview
}

// metaTag is the part of a <meta> element this parser reads.
//
// The identifying attributes are kept separately rather than collapsed into one
// value while scanning, because collapsing them early is what made the meaning
// of a tag depend on the order its author happened to write the attributes in.
type metaTag struct {
	property string
	name     string
	content  string
}

// key is the identifier the element declares.
//
// property wins over name whenever both are present, in either textual order:
// property= is what Open Graph specifies, and a page carrying both is declaring
// an Open Graph value that also has a legacy name. Reading name= at all is what
// makes the <meta name="description"> fallback work.
func (t metaTag) key() string {
	if t.property != "" {
		return t.property
	}
	return t.name
}

// readMetaTag collects the attributes of the current <meta> element.
//
// Every attribute is read before any decision is made, so the result does not
// depend on which one appeared first. Duplicates keep the first occurrence,
// which is what HTML itself does with a repeated attribute.
func readMetaTag(tokenizer *html.Tokenizer) metaTag {
	var tag metaTag
	for {
		name, value, more := tokenizer.TagAttr()
		switch string(name) {
		case "property":
			keepFirst(&tag.property, attributeKey(value))
		case "name":
			keepFirst(&tag.name, attributeKey(value))
		case "content":
			keepFirst(&tag.content, string(value))
		}
		if !more {
			return tag
		}
	}
}

// attributeKey normalises an identifying attribute. These are matched
// case-insensitively, so "OG:Title" and "og:title" are the same declaration.
func attributeKey(value []byte) string {
	return strings.ToLower(strings.TrimSpace(string(value)))
}

// keepFirst assigns only while the field is still empty. A page repeating
// og:title keeps the one it declared first, so a later duplicate cannot
// override what the page already said.
func keepFirst(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

// normalize turns remote text into something safe to carry as data.
//
// Invalid UTF-8 is dropped rather than replaced, so the value stays encodable;
// whitespace — including the newlines that would matter if any of this ever
// reached a header — is collapsed to single spaces; and the result is cut to a
// rune count, never a byte count, so truncation cannot split a character.
//
// It does not escape anything. These values are data: they are delivered as
// JSON strings and the client renders them as text. Escaping here would only
// mean double-escaped titles downstream.
func normalize(value string, maxRunes int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes]))
}

// imageURL resolves and vets og:image.
//
// It is resolved against the document so a relative value is usable, and then
// held to the same scheme rule as everything else — which is what stops
// og:image from being a javascript:, data: or file: URL handed to a browser.
//
// It is not fetched. This service makes exactly one remote request per preview,
// and adding a second one driven by a value the remote page controls would
// reintroduce the SSRF the rest of this package removes. The client requests
// the image itself, as it would any other image on the web. A literal address
// that is not public is still dropped here, because that case costs nothing to
// check and there is no legitimate page whose image lives on the reader's own
// loopback.
func imageURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(strings.ToValidUTF8(raw, ""))
	if raw == "" || len(raw) > MaxURLLength {
		return ""
	}
	resolved, err := base.Parse(raw)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(resolved.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if resolved.User != nil || resolved.Hostname() == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(resolved.Hostname()); err == nil && !addrAllowed(addr) {
		return ""
	}
	resolved.Fragment = ""
	resolved.RawFragment = ""
	return resolved.String()
}
