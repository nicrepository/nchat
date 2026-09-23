package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The per-link contract (issue #807).
//
// A message carries `links`, one entry per URL occurrence in its body. The
// client draws exactly what it is given: text it may match against the body,
// a safety state, what the policy lets the reader do, and — only when the
// backend authorised it — an href. A pending or condemned link has no href and
// a condemned one has no URL or text at all; its span in body_text is the
// blocked marker.

// linkJSON is one link occurrence as the client sees it.
type linkJSON struct {
	Ordinal int `json:"ordinal"`
	// TargetKey is the target's stable identity, present on every occurrence
	// including a condemned one. Never a URL.
	TargetKey string `json:"target_key"`
	// Text is the URL as written, for matching the rendered span. Absent for a
	// condemned link, whose text is withheld from the body too.
	Text string `json:"text,omitempty"`
	// URL is the canonical destination. Absent for a condemned link.
	URL      string `json:"url,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Safety   string `json:"safety"`
	Click    string `json:"click"`
	// Href is present only when Click is direct. Its presence is the
	// authorisation to draw an anchor; nothing else is.
	Href      string           `json:"href,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
	Preview   *linkPreviewJSON `json:"preview,omitempty"`
}

// linkPreviewJSON is the card. Every string is remote text carried as data;
// image_id names a derived asset on this service, never a remote URL.
type linkPreviewJSON struct {
	State       string `json:"state"`
	Hostname    string `json:"hostname"`
	SiteName    string `json:"site_name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageID     string `json:"image_id,omitempty"`
	ImageWidth  int    `json:"image_width,omitempty"`
	ImageHeight int    `json:"image_height,omitempty"`
}

func mapLinksJSON(links []domain.MessageLink) []linkJSON {
	if len(links) == 0 {
		return nil
	}
	out := make([]linkJSON, len(links))
	for i, link := range links {
		out[i] = mapLinkJSON(link)
	}
	return out
}

func mapLinkJSON(link domain.MessageLink) linkJSON {
	return linkJSON{
		Ordinal: link.Ordinal, TargetKey: link.TargetKey, Text: link.Text, URL: link.URL, Hostname: link.Hostname,
		Safety: string(link.Safety), Click: string(link.Click), Href: link.Href,
		UpdatedAt: link.UpdatedAt, Preview: mapLinkPreviewJSON(link.Preview),
	}
}

func mapLinkPreviewJSON(preview *domain.LinkPreview) *linkPreviewJSON {
	if preview == nil {
		return nil
	}
	return &linkPreviewJSON{
		State: string(preview.State), Hostname: preview.Hostname, SiteName: preview.SiteName,
		Title: preview.Title, Description: preview.Description, ImageID: preview.ImageID,
		ImageWidth: preview.ImageWidth, ImageHeight: preview.ImageHeight,
	}
}

// LinkPreviewImageReader serves a derived thumbnail to a workspace member.
type LinkPreviewImageReader interface {
	LinkPreviewImage(ctx context.Context, workspaceID, userID, previewID string) (storage.LinkPreviewImage, error)
}

// linkPreviewImageCacheControl lets the browser keep a thumbnail privately for
// a day — the preview TTL — without any shared cache holding it: the bytes are
// authorised per member, so a shared cache would serve them to the next caller.
const linkPreviewImageCacheControl = "private, max-age=86400"

// GetLinkPreviewImage serves GET /api/chat/link-previews/{previewID}/image.
//
// The image is a derivative this service produced from a page it fetched
// itself; the browser never contacts the remote host. Authorisation is inside
// the store's statement — active membership of the owning workspace, a ready
// preview, a target still safe — and every refusal is the same 404, so the
// route is not an oracle for preview ids.
func (h *MessageHandler) GetLinkPreviewImage(w http.ResponseWriter, r *http.Request) {
	if !h.checkDeps(w) || h.previewImages == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	previewID := r.PathValue("previewID")
	if _, err := uuid.Parse(previewID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
		return
	}
	workspaceID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	image, err := h.previewImages.LinkPreviewImage(r.Context(), workspaceID, userID, previewID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
			return
		}
		mapServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(image.Data)))
	w.Header().Set("Cache-Control", linkPreviewImageCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image.Data)
}
