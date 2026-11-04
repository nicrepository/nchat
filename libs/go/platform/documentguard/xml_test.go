package documentguard

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestReferences(t *testing.T) {
	exists := func(s string) bool { return s == "Pictures/a b.png" || s == "content.xml" || s == "styles.xml" }
	for _, good := range []string{"#section", "content.xml", "./styles.xml", "Pictures/a%20b.png"} {
		if _, err := Resolve("content.xml", good, true, exists); err != nil {
			t.Errorf("valid %q: %v", good, err)
		}
	}
	for _, bad := range []string{"/etc/passwd", `C:\Windows\win.ini`, "../secret", "%2e%2e/secret", "file:///a", "http://example.test/a", "https://example.test/a", "//example.test/a", "content.xml?q=a", "content.xml?", "missing.png", "%252e%252e/secret", "%ZZ", "Pictures%2fa.png", "https:opaque", " a", "a%00"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := Resolve("content.xml", bad, true, exists); err == nil {
				t.Fatal("unsafe URI accepted")
			}
		})
	}
	if _, err := Resolve("", "styles.xml", true, nil); err == nil {
		t.Fatal("standalone XML resolves package reference")
	}
}

func TestRelationshipXML(t *testing.T) {
	exists := func(s string) bool { return s == "word/document.xml" }
	for _, suffix := range []string{".rels", ".RELS", ".Rels"} {
		for _, attrs := range []string{
			`Type="https://schemas.test/attachedTemplate" Target="word/document.xml"`,
			`Type="https://schemas.test/vbaProject" Target="word/document.xml"`,
			`Type="https://schemas.test/vbaProject " Target="word/document.xml"`,
			`Type="https://schemas.test/%76baProject" Target="word/document.xml"`,
			`Type="https://schemas.test/vbaProject?ignored" Target="word/document.xml"`,
			`Type="https://schemas.test/oleObject" Target="word/document.xml"`,
			`Type="https://schemas.test/activeX" Target="word/document.xml"`,
			`Type="https://schemas.test/externalLink" Target="word/document.xml"`,
			`Type="https://schemas.test/document" Target="https://example.test/a"`,
			`Type="https://schemas.test/document" Target="/word/document.xml"`,
			`Type="https://schemas.test/document" Target="word/document.xml" TargetMode = 'Ext&#101;rnal'`,
			`Type="https://schemas.test/document" Target="missing.xml"`,
			`Type="https://schemas.test/document" Target="#fragment"`,
			`Type="https://schemas.test/document" Target="word/document.xml" xmlns:extra="urn:bad" extra:Type="https://schemas.test/vbaProject"`,
		} {
			t.Run(suffix+attrs, func(t *testing.T) {
				body := `<r:Relationships xmlns:r="` + RelationshipsNS + `"><r:Relationship Id="r1" ` + attrs + `/></r:Relationships>`
				if err := Inspect(context.Background(), []byte(body), "_rels/"+suffix, exists); err == nil {
					t.Fatal("unsafe relationship accepted")
				}
			})
		}
	}
	good := `<Relationships xmlns="` + RelationshipsNS + `"><Relationship Id="r1" Type="https://schemas.test/document" Target="word/document.xml" TargetMode = 'Int&#101;rnal'/></Relationships>`
	if err := Inspect(context.Background(), []byte(good), "_rels/.rels", exists); err != nil {
		t.Fatal(err)
	}
}

func TestODFNamespacesAndRepresentations(t *testing.T) {
	exists := func(s string) bool { return s == "Pictures/image.png" }
	for _, ref := range []string{"#section", "Pictures/image.png", "/etc/passwd", "%2e%2e/secret", "missing.png", "http&#58;//example.test/a"} {
		t.Run(ref, func(t *testing.T) {
			body := `<office xmlns:other="` + XLinkNS + `"><image other:href = '` + ref + `'/></office>`
			err := Inspect(context.Background(), []byte(body), "content.xml", exists)
			want := ref == "#section" || ref == "Pictures/image.png"
			if (err == nil) != want {
				t.Fatalf("ref %s: %v", ref, err)
			}
		})
	}
	for _, body := range []string{`<x>`, `<!DOCTYPE x><x/>`, `<x xml:base="file:///"/>`, `<x a="1" a="2"/>`, `<x xmlns:r="` + XLinkNS + `" r:href="%FF"/>`} {
		if err := Inspect(context.Background(), []byte(body), "content.xml", exists); err == nil {
			t.Fatalf("unsafe XML accepted: %s", body)
		}
	}
}

func TestXMLBudgetsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Inspect(ctx, []byte(`<x/>`), "data.xml", nil); err != context.Canceled {
		t.Fatal(err)
	}
	for _, n := range []int{MaxDepth, MaxDepth + 1} {
		body := strings.Repeat("<x>", n) + strings.Repeat("</x>", n)
		if err := Inspect(context.Background(), []byte(body), "data.xml", nil); (err == nil) != (n == MaxDepth) {
			t.Fatalf("depth %d: %v", n, err)
		}
	}
	for _, n := range []int{MaxAttributes, MaxAttributes + 1} {
		var b strings.Builder
		b.WriteString("<x ")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, `a%d="x" `, i)
		}
		b.WriteString("/>")
		if err := Inspect(context.Background(), []byte(b.String()), "data.xml", nil); (err == nil) != (n == MaxAttributes) {
			t.Fatalf("attrs %d: %v", n, err)
		}
	}
}

func FuzzInspect(f *testing.F) {
	f.Add(`<root/>`)
	f.Add(`<x xmlns:l="` + XLinkNS + `" l:href="%2e%2e/secret"/>`)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) <= MaxXMLBytes {
			_ = Inspect(context.Background(), []byte(body), "content.xml", func(string) bool { return false })
		}
	})
}

func TestXMLRejectsMalformedActiveAndAmbiguousInputs(t *testing.T) {
	for _, body := range []string{
		"", `outside<root/>`, `<a/><b/>`, `<a/>outside`, `<?other x?><root/>`, `<a><?xml version="1.0"?></a>`,
		`<a xmlns:i="http://www.w3.org/2001/XInclude"><i:include/></a>`, `<object/>`,
		`<office:scripts xmlns:office="` + officeNS + `"><x/></office:scripts>`,
		`<office:scripts xmlns:office="` + officeNS + `">active</office:scripts>`, `<scripts xmlns="urn:other"/>`,
		`<x onclick="x"/>`, `<x href="relative"/>`, `<x xmlns:q="urn:other" q:href="file:///x"/>`,
	} {
		if err := Inspect(context.Background(), []byte(body), "content.xml", nil); err == nil {
			t.Fatalf("unsafe XML: %s", body)
		}
	}
	if err := Inspect(context.Background(), bytes.Repeat([]byte("x"), MaxXMLBytes+1), "content.xml", nil); err == nil {
		t.Fatal("byte budget exceeded")
	}
	good := `<?xml version="1.0"?><office:document xmlns:office="` + officeNS + `"><office:scripts/></office:document>`
	if err := Inspect(context.Background(), []byte(good), "content.xml", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRelationshipPartStructureAndRelativeTargets(t *testing.T) {
	exists := func(name string) bool { return name == "word/document.xml" || name == "word/styles.xml" }
	good := `<Relationships xmlns="` + RelationshipsNS + `"><Relationship Id="r1" Type="http://schemas.test/styles" Target="styles.xml"/></Relationships>`
	if err := Inspect(context.Background(), []byte(good), "word/_rels/document.xml.RELS", exists); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`<Relationships/>`, `<Other xmlns="` + RelationshipsNS + `"/>`, `<Relationships xmlns="` + RelationshipsNS + `"><Other/></Relationships>`, `<Relationships xmlns="` + RelationshipsNS + `"><Relationship/></Relationships>`} {
		if err := Inspect(context.Background(), []byte(body), "word/_rels/document.xml.rels", exists); err == nil {
			t.Fatalf("malformed relationship part: %s", body)
		}
	}
	if err := Inspect(context.Background(), []byte(good), "word/document.xml.rels", exists); err == nil {
		t.Fatal("invalid relationship location accepted")
	}
}
