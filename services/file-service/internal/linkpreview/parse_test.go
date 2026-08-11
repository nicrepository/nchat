package linkpreview

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return parsed
}

func TestExtractReadsOpenGraph(t *testing.T) {
	document := `<html><head>
		<meta property="og:title" content="A title">
		<meta property="og:description" content="A description">
		<meta property="og:image" content="https://cdn.example.com/card.png">
		<meta property="og:site_name" content="Example">
		<meta property="og:type" content="article">
		<title>Ignored because og:title won</title>
	</head><body><h1>page</h1></body></html>`

	preview := extract(mustURL(t, "https://example.com/page"), []byte(document))

	if preview.Title != "A title" {
		t.Fatalf("title: %q", preview.Title)
	}
	if preview.Description != "A description" {
		t.Fatalf("description: %q", preview.Description)
	}
	if preview.ImageURL != "https://cdn.example.com/card.png" {
		t.Fatalf("image: %q", preview.ImageURL)
	}
	if preview.SiteName != "Example" {
		t.Fatalf("site name: %q", preview.SiteName)
	}
}

func TestExtractFallsBackToHTMLTitleAndDescription(t *testing.T) {
	document := `<html><head>
		<title>  The   page   title </title>
		<meta name="description" content="Plain description">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "The page title" {
		t.Fatalf("expected the whitespace-normalised html title, got %q", preview.Title)
	}
	if preview.Description != "Plain description" {
		t.Fatalf("description: %q", preview.Description)
	}
}

// TestExtractPrefersOpenGraphOverFallbacks pins the precedence, which is what
// stops a page's <title> from overriding what it explicitly declared.
func TestExtractPrefersOpenGraphOverFallbacks(t *testing.T) {
	document := `<html><head>
		<title>HTML title</title>
		<meta name="description" content="HTML description">
		<meta property="og:title" content="OG title">
		<meta property="og:description" content="OG description">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "OG title" || preview.Description != "OG description" {
		t.Fatalf("expected the Open Graph values to win, got %+v", preview)
	}
}

// TestExtractKeepsTheFirstOfDuplicateTags: a later duplicate must not be able
// to override what the page already declared.
func TestExtractKeepsTheFirstOfDuplicateTags(t *testing.T) {
	document := `<html><head>
		<meta property="og:title" content="first">
		<meta property="og:title" content="second">
		<meta property="og:image" content="https://cdn.example.com/first.png">
		<meta property="og:image" content="https://cdn.example.com/second.png">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "first" {
		t.Fatalf("title: %q", preview.Title)
	}
	if preview.ImageURL != "https://cdn.example.com/first.png" {
		t.Fatalf("image: %q", preview.ImageURL)
	}
}

func TestExtractHandlesMissingMetadata(t *testing.T) {
	preview := extract(mustURL(t, "https://example.com/"), []byte(`<html><head></head><body>hi</body></html>`))
	if preview != (Preview{}) {
		t.Fatalf("expected an empty preview, got %+v", preview)
	}
}

// TestExtractSurvivesMalformedHTML: a tokeniser yields what it can, and a
// broken page is a page with fewer fields — never an error and never a hang.
func TestExtractSurvivesMalformedHTML(t *testing.T) {
	for name, document := range map[string]string{
		"unclosed head tag": `<html><head><meta property="og:title" content="still read">`,
		"stray brackets":    `<<<>>><meta property="og:title" content="still read"><`,
		"no head":           `<meta property="og:title" content="still read">`,
		"nested garbage":    `<html><head><div><span><meta property="og:title" content="still read">`,
		"unquoted attr":     `<meta property=og:title content="still read">`,
		"binary noise":      "\x00\x01\x02<meta property=\"og:title\" content=\"still read\">",
		"truncated entity":  `<meta property="og:title" content="still read">&amp`,
		"duplicate html":    `<html><html><head><meta property="og:title" content="still read">`,
	} {
		t.Run(name, func(t *testing.T) {
			preview := extract(mustURL(t, "https://example.com/"), []byte(document))
			if preview.Title != "still read" {
				t.Fatalf("title: %q", preview.Title)
			}
		})
	}
}

func TestExtractHandlesEmptyDocument(t *testing.T) {
	if preview := extract(mustURL(t, "https://example.com/"), nil); preview != (Preview{}) {
		t.Fatalf("expected an empty preview, got %+v", preview)
	}
}

// TestExtractDiscardsATagCutOffAtEOF pins what happens to the document the
// body limit produces: a read stopped mid-tag ends with a tag that was never
// terminated, and the tokeniser discards it. Everything already parsed is kept.
//
// That is the behaviour worth having — the alternative is acting on an
// attribute whose value may have been cut in half.
func TestExtractDiscardsATagCutOffAtEOF(t *testing.T) {
	document := `<html><head><meta property="og:title" content="complete">` +
		`<meta property="og:description" content="cut in ha`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "complete" {
		t.Fatalf("expected the complete tag to survive, got %q", preview.Title)
	}
	if preview.Description != "" {
		t.Fatalf("expected the truncated tag to be discarded, got %q", preview.Description)
	}
}

// TestExtractStopsAtBody proves the parser does not walk the whole document:
// metadata declared after <body> is page content and is not read.
func TestExtractStopsAtBody(t *testing.T) {
	document := `<html><head><meta property="og:title" content="head title"></head>
		<body><meta property="og:description" content="should not be read"></body></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "head title" {
		t.Fatalf("title: %q", preview.Title)
	}
	if preview.Description != "" {
		t.Fatalf("expected metadata after <body> to be ignored, got %q", preview.Description)
	}
}

// TestExtractTreatsScriptPayloadsAsText is the XSS case. The backend's job is
// to not interpret this and to not become a place where it is turned into
// markup: the payload survives as data, exactly as written, and nothing here
// escapes, strips or executes it.
func TestExtractTreatsScriptPayloadsAsText(t *testing.T) {
	document := `<html><head>
		<meta property="og:title" content="&lt;script&gt;alert(1)&lt;/script&gt;">
		<meta property="og:description" content="&lt;img src=x onerror=alert(1)&gt;">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Title != "<script>alert(1)</script>" {
		t.Fatalf("title: %q", preview.Title)
	}
	if preview.Description != "<img src=x onerror=alert(1)>" {
		t.Fatalf("description: %q", preview.Description)
	}
}

// TestExtractCollapsesNewlines matters beyond tidiness: a value carrying CR or
// LF is what a header-injection attempt looks like if any of this ever reaches
// a header. It cannot, and it also cannot carry the characters.
func TestExtractCollapsesNewlines(t *testing.T) {
	document := "<html><head><meta property=\"og:title\" content=\"line\r\nSet-Cookie: a=b\r\n\tmore\">" +
		"</head></html>"

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if strings.ContainsAny(preview.Title, "\r\n\t") {
		t.Fatalf("title kept a control character: %q", preview.Title)
	}
	if preview.Title != "line Set-Cookie: a=b more" {
		t.Fatalf("title: %q", preview.Title)
	}
}

func TestExtractTruncatesOversizedFields(t *testing.T) {
	huge := strings.Repeat("x", 10_000)
	document := `<html><head>
		<meta property="og:title" content="` + huge + `">
		<meta property="og:description" content="` + huge + `">
		<meta property="og:site_name" content="` + huge + `">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if got := utf8.RuneCountInString(preview.Title); got != maxTitleRunes {
		t.Fatalf("title kept %d runes, limit is %d", got, maxTitleRunes)
	}
	if got := utf8.RuneCountInString(preview.Description); got != maxDescriptionRunes {
		t.Fatalf("description kept %d runes, limit is %d", got, maxDescriptionRunes)
	}
	if got := utf8.RuneCountInString(preview.SiteName); got != maxSiteNameRunes {
		t.Fatalf("site name kept %d runes, limit is %d", got, maxSiteNameRunes)
	}
}

// TestExtractTruncatesOnRuneBoundaries: cutting by bytes would produce invalid
// UTF-8, which is unencodable as JSON.
func TestExtractTruncatesOnRuneBoundaries(t *testing.T) {
	document := `<html><head><meta property="og:title" content="` +
		strings.Repeat("é", 10_000) + `"></head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if !utf8.ValidString(preview.Title) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if got := utf8.RuneCountInString(preview.Title); got != maxTitleRunes {
		t.Fatalf("kept %d runes, limit is %d", got, maxTitleRunes)
	}
}

func TestExtractDropsInvalidUTF8(t *testing.T) {
	document := []byte("<html><head><meta property=\"og:title\" content=\"ok\xff\xfe end\">" +
		"</head></html>")

	preview := extract(mustURL(t, "https://example.com/"), document)

	if !utf8.ValidString(preview.Title) {
		t.Fatalf("title is not valid UTF-8: %q", preview.Title)
	}
}

func TestImageURLResolvesAndVets(t *testing.T) {
	base := mustURL(t, "https://example.com/articles/one")
	cases := map[string]struct {
		raw  string
		want string
	}{
		"absolute https": {"https://cdn.example.com/a.png", "https://cdn.example.com/a.png"},
		"absolute http":  {"http://cdn.example.com/a.png", "http://cdn.example.com/a.png"},
		"root relative":  {"/img/a.png", "https://example.com/img/a.png"},
		"path relative":  {"a.png", "https://example.com/articles/a.png"},
		"protocol rel":   {"//cdn.example.com/a.png", "https://cdn.example.com/a.png"},
		"fragment cut":   {"https://cdn.example.com/a.png#x", "https://cdn.example.com/a.png"},
		"nondefault port": {
			"https://cdn.example.com:8443/a.png", "https://cdn.example.com:8443/a.png",
		},

		// Everything a browser must never be handed.
		"javascript": {"javascript:alert(1)", ""},
		"data":       {"data:image/png;base64,AAAA", ""},
		"file":       {"file:///etc/passwd", ""},
		"ftp":        {"ftp://example.com/a.png", ""},
		"userinfo":   {"https://user:pass@cdn.example.com/a.png", ""},
		"empty":      {"", ""},
		"whitespace": {"   ", ""},
		"too long":   {"https://cdn.example.com/" + longPath(MaxURLLength), ""},

		// A literal private address: no fetch happens here, but there is no
		// legitimate page whose card image lives on the reader's loopback.
		"loopback literal": {"http://127.0.0.1/a.png", ""},
		"metadata literal": {"http://169.254.169.254/a.png", ""},
		"ipv6 loopback":    {"http://[::1]/a.png", ""},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := imageURL(base, testCase.raw); got != testCase.want {
				t.Fatalf("imageURL(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
		})
	}
}

// TestImageURLAcceptsOgImageURLAlias covers the property some sites use.
func TestExtractAcceptsOgImageURLAlias(t *testing.T) {
	document := `<html><head><meta property="og:image:url" content="/card.png"></head></html>`

	preview := extract(mustURL(t, "https://example.com/page"), []byte(document))

	if preview.ImageURL != "https://example.com/card.png" {
		t.Fatalf("image: %q", preview.ImageURL)
	}
}

// --- attribute order ------------------------------------------------------
//
// A <meta> element's meaning must come from which attributes it carries, never
// from the order the author wrote them in. These cases exist in pairs for that
// reason: each pair is the same element with the attributes swapped, and both
// halves must produce the same preview.

func TestExtractIsIndependentOfAttributeOrder(t *testing.T) {
	cases := map[string]struct {
		tag  string
		want Preview
	}{
		"property before name": {
			`<meta property="og:title" name="description" content="OG title">`,
			Preview{Title: "OG title"},
		},
		"name before property": {
			`<meta name="description" property="og:title" content="OG title">`,
			Preview{Title: "OG title"},
		},
		"content first, property last": {
			`<meta content="OG title" name="description" property="og:title">`,
			Preview{Title: "OG title"},
		},

		// The same pairing for description, so the precedence is not a rule
		// that happens to hold only for the title.
		"description property first": {
			`<meta property="og:description" name="description" content="OG description">`,
			Preview{Description: "OG description"},
		},
		"description name first": {
			`<meta name="description" property="og:description" content="OG description">`,
			Preview{Description: "OG description"},
		},

		// And for the image, whose value goes through URL resolution afterwards.
		"image property first": {
			`<meta property="og:image" name="thumbnail" content="/card.png">`,
			Preview{ImageURL: "https://example.com/card.png"},
		},
		"image name first": {
			`<meta name="thumbnail" property="og:image" content="/card.png">`,
			Preview{ImageURL: "https://example.com/card.png"},
		},

		// property alone, and name alone for the fallback that depends on it.
		"property only": {
			`<meta property="og:site_name" content="Example">`,
			Preview{SiteName: "Example"},
		},
		"name only falls back": {
			`<meta name="description" content="Plain description">`,
			Preview{Description: "Plain description"},
		},

		// Attributes this parser does not read must not disturb the ones it does.
		"irrelevant attributes interleaved": {
			`<meta charset="utf-8" property="og:title" data-x="1" content="OG title" lang="en">`,
			Preview{Title: "OG title"},
		},

		// Identifying attributes are matched case-insensitively; a tag with no
		// content declares nothing.
		"uppercase property": {
			`<meta property="OG:Title" content="OG title">`,
			Preview{Title: "OG title"},
		},
		"padded property": {
			`<meta property="  og:title  " content="OG title">`,
			Preview{Title: "OG title"},
		},
		"no content attribute": {
			`<meta property="og:title">`,
			Preview{},
		},
		"empty content": {
			`<meta property="og:title" content="">`,
			Preview{},
		},
		"unknown property ignored": {
			`<meta property="og:audio" content="https://example.com/a.mp3">`,
			Preview{},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			document := "<html><head>" + testCase.tag + "</head></html>"

			got := extract(mustURL(t, "https://example.com/page"), []byte(document))

			if got != testCase.want {
				t.Fatalf("extract(%s) = %+v, want %+v", testCase.tag, got, testCase.want)
			}
		})
	}
}

// TestExtractPrecedenceHoldsAcrossSeparateTags: property winning over name is
// about one element's own attributes, and must not disturb the document-order
// rule that applies between two separate elements.
func TestExtractPrecedenceHoldsAcrossSeparateTags(t *testing.T) {
	document := `<html><head>
		<meta name="description" content="plain, declared first">
		<meta property="og:description" content="open graph, declared second">
	</head></html>`

	preview := extract(mustURL(t, "https://example.com/"), []byte(document))

	if preview.Description != "open graph, declared second" {
		t.Fatalf("description: %q", preview.Description)
	}
}
