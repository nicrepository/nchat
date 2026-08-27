package preview_test

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

// multiPagePDF builds a valid PDF with pageCount pages, each a distinctly
// shaded rectangle so a decoded page is never mistaken for another.
func multiPagePDF(pageCount int) []byte {
	const pagesObj = 2
	const firstPageObj = 3

	kids := ""
	for i := 0; i < pageCount; i++ {
		if i > 0 {
			kids += " "
		}
		kids += strconv.Itoa(firstPageObj+2*i) + " 0 R"
	}

	objects := []string{
		"<< /Type /Catalog /Pages " + strconv.Itoa(pagesObj) + " 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pageCount),
	}
	for i := 0; i < pageCount; i++ {
		pageObj := firstPageObj + 2*i
		contentObj := pageObj + 1
		shade := (i * 37) % 256
		content := fmt.Sprintf("%d %d %d rg\n0 0 200 100 re f\n", shade, 255-shade, 128)
		objects = append(objects, fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 200 100] /Contents %d 0 R /Resources << >> >>",
			pagesObj, contentObj))
		objects = append(objects, fmt.Sprintf(
			"<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}
	return assemblePDF(objects)
}

func TestRenderPDFPagesRendersEveryPageUpToTheDocument(t *testing.T) {
	const pageCount = 3
	pages, err := renderPages(t, "application/pdf", multiPagePDF(pageCount))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(pages) != pageCount {
		t.Fatalf("rendered %d pages, want %d", len(pages), pageCount)
	}
	for i, page := range pages {
		bounds := decodePreview(t, page).Bounds()
		if bounds.Dx() > domain.MaxPreviewDimension || bounds.Dy() > domain.MaxPreviewDimension {
			t.Fatalf("page %d is %dx%d, want both edges <= %d",
				i, bounds.Dx(), bounds.Dy(), domain.MaxPreviewDimension)
		}
	}
}

func TestRenderPDFPagesStaysAtOnePageForASinglePageDocument(t *testing.T) {
	pages, err := renderPages(t, "application/pdf", singlePagePDF())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("rendered %d pages, want 1", len(pages))
	}
}

// A document longer than the cap must still produce exactly the cap's worth
// of pages, not the document's real count: the ceiling bounds the render
// regardless of how much more content exists beyond it.
func TestRenderPDFPagesStopsAtTheConfiguredCap(t *testing.T) {
	pages, err := renderPages(t, "application/pdf", multiPagePDF(domain.MaxPreviewPDFPages+5))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(pages) != domain.MaxPreviewPDFPages {
		t.Fatalf("rendered %d pages, want the cap of %d", len(pages), domain.MaxPreviewPDFPages)
	}
}

func TestRenderPDFPagesReportsAZeroPageDocumentAsUnsupported(t *testing.T) {
	_, err := renderPages(t, "application/pdf", emptyPDF())
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderPDFPagesRefusesAnUnreadableDocument(t *testing.T) {
	_, err := renderPages(t, "application/pdf", []byte("%PDF-1.4\nbut nothing else at all"))
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}
