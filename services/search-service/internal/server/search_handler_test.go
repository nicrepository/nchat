package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type fakeSearchProvider struct {
	legacy   domain.LegacyMessagePage
	messages domain.MessagePage
	users    domain.UserPage
	channels domain.ChannelPage
	groups   domain.GroupPage
	files    domain.FilePage
	links    domain.LinkPage
	userID   *string
	linkReq  *searchRequest
	err      error
}

func (f fakeSearchProvider) SearchLinks(_ context.Context, userID, q string, limit int, cursor string) (domain.LinkPage, error) {
	if f.userID != nil {
		*f.userID = userID
	}
	if f.linkReq != nil {
		*f.linkReq = searchRequest{q: q, limit: limit, cursor: cursor}
	}
	return f.links, f.err
}

func (f fakeSearchProvider) SearchLegacyMessages(context.Context, string, string, int, string) (domain.LegacyMessagePage, error) {
	return f.legacy, f.err
}
func (f fakeSearchProvider) SearchMessages(context.Context, string, string, int, string) (domain.MessagePage, error) {
	return f.messages, f.err
}
func (f fakeSearchProvider) SearchUsers(context.Context, string, string, int, string) (domain.UserPage, error) {
	return f.users, f.err
}
func (f fakeSearchProvider) SearchChannels(context.Context, string, string, int, string) (domain.ChannelPage, error) {
	return f.channels, f.err
}
func (f fakeSearchProvider) SearchGroups(_ context.Context, userID, _ string, _ int, _ string) (domain.GroupPage, error) {
	if f.userID != nil {
		*f.userID = userID
	}
	return f.groups, f.err
}
func (f fakeSearchProvider) SearchFiles(_ context.Context, userID, _ string, _ int, _ string) (domain.FilePage, error) {
	if f.userID != nil {
		*f.userID = userID
	}
	return f.files, f.err
}

// The caller is the principal: user_id or workspace_id in the query string are
// ignored, never forwarded as authority.
func TestGroupsAndFilesUseOnlyThePrincipal(t *testing.T) {
	for _, tc := range []struct {
		path  string
		serve func(*SearchHandler, http.ResponseWriter, *http.Request)
	}{
		{"/api/search/groups?q=projeto&user_id=attacker&workspace_id=other", (*SearchHandler).Groups},
		{"/api/search/files?q=backup&user_id=attacker&workspace_id=other", (*SearchHandler).Files},
	} {
		var seen string
		h := NewSearchHandler(fakeSearchProvider{userID: &seen, groups: domain.GroupPage{Items: []domain.GroupResult{{ID: "g1", Title: "Projeto"}}}, files: domain.FilePage{Items: []domain.FileResult{{ID: "f1", Filename: "backup.pdf"}}}})
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{UserID: "user-1"}))
		res := httptest.NewRecorder()
		tc.serve(h, res, req)
		if res.Code != http.StatusOK || seen != "user-1" {
			t.Fatalf("%s status=%d caller=%q body=%s", tc.path, res.Code, seen, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "storage") {
			t.Fatalf("%s leaked storage detail: %s", tc.path, res.Body.String())
		}
	}
}

func TestSearchMessagesPublishesExistingPaginatedEnvelope(t *testing.T) {
	provider := fakeSearchProvider{messages: domain.MessagePage{Items: []domain.MessageResult{{ID: "m1", BodyText: "resultado", CreatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}}, NextCursor: "next"}}
	h := NewSearchHandler(provider)
	req := httptest.NewRequest(http.MethodGet, "/api/search/messages?q=resultado&limit=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{UserID: "user-1"}))
	res := httptest.NewRecorder()
	h.Messages(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Data struct {
			Data       []domain.MessageResult `json:"data"`
			Pagination struct {
				Limit      int     `json:"limit"`
				NextCursor *string `json:"next_cursor"`
				HasMore    bool    `json:"has_more"`
			} `json:"pagination"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Data) != 1 || body.Data.Pagination.Limit != 1 || body.Data.Pagination.NextCursor == nil || !body.Data.Pagination.HasMore {
		t.Fatalf("unexpected response: %+v", body.Data)
	}
}

func TestSearchHandlerRejectsInvalidInputsAndMissingPrincipal(t *testing.T) {
	h := NewSearchHandler(fakeSearchProvider{})
	tests := []struct {
		name, target  string
		authenticated bool
		want          int
	}{
		{"missing auth", "/api/search/users?q=ana", false, http.StatusUnauthorized},
		{"empty query", "/api/search/users?q=%20", true, http.StatusBadRequest},
		{"zero limit", "/api/search/users?q=ana&limit=0", true, http.StatusBadRequest},
		{"non-numeric limit", "/api/search/users?q=ana&limit=abc", true, http.StatusBadRequest},
		{"too large", "/api/search/users?q=ana&limit=101", true, http.StatusBadRequest},
		{"duplicate query", "/api/search/users?q=ana&q=bia", true, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.authenticated {
				req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{UserID: "user-1"}))
			}
			res := httptest.NewRecorder()
			h.Users(res, req)
			if res.Code != tt.want {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestSearchHandlersMapProviderErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{{"invalid input", domain.ErrInvalidInput, http.StatusBadRequest}, {"invalid cursor", domain.ErrInvalidCursor, http.StatusBadRequest}, {"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized}, {"internal", errors.New("secret database detail"), http.StatusInternalServerError}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSearchHandler(fakeSearchProvider{err: tt.err})
			for _, call := range []func(http.ResponseWriter, *http.Request){h.Messages, h.Users, h.Channels} {
				req := httptest.NewRequest(http.MethodGet, "/?q=x", nil)
				req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{UserID: "user"}))
				res := httptest.NewRecorder()
				call(res, req)
				if res.Code != tt.want {
					t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
				}
				if strings.Contains(res.Body.String(), "secret database detail") {
					t.Fatal("internal detail leaked")
				}
			}
		})
	}
}

func postLinks(h *SearchHandler, body string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/search/links", strings.NewReader(body))
	if authenticated {
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{UserID: "user-1"}))
	}
	res := httptest.NewRecorder()
	h.Links(res, req)
	return res
}

// The query of a link search may be a whole URL with a token in it: it is read
// from the body only, and the caller is still the principal alone.
func TestSearchLinksReadsTheBodyAndOnlyThePrincipal(t *testing.T) {
	var seen string
	var got searchRequest
	created := time.Date(2026, 9, 1, 9, 41, 0, 0, time.UTC)
	h := NewSearchHandler(fakeSearchProvider{userID: &seen, linkReq: &got, links: domain.LinkPage{
		Items:      []domain.LinkResult{{MessageID: "m1", URL: "https://docs.example.com/runbook", Hostname: "docs.example.com", CreatedAt: created, Rank: 2}},
		NextCursor: "next",
	}})
	res := postLinks(h, `{"q":"  https://docs.example.com/runbook?ref=fixture ","limit":5,"cursor":"c1"}`, true)
	if res.Code != http.StatusOK || seen != "user-1" {
		t.Fatalf("status=%d caller=%q body=%s", res.Code, seen, res.Body.String())
	}
	if got != (searchRequest{q: "https://docs.example.com/runbook?ref=fixture", limit: 5, cursor: "c1"}) {
		t.Fatalf("request=%+v", got)
	}
	if !strings.Contains(res.Body.String(), `"has_more":true`) || strings.Contains(res.Body.String(), `"rank"`) {
		t.Fatalf("body=%s", res.Body.String())
	}
	if res := postLinks(h, `{"q":"runbook"}`, true); res.Code != http.StatusOK || got.limit != defaultLimit || got.cursor != "" {
		t.Fatalf("defaults status=%d request=%+v", res.Code, got)
	}
}

func TestSearchLinksRejectsInvalidBodies(t *testing.T) {
	h := NewSearchHandler(fakeSearchProvider{})
	tests := []struct {
		name, body    string
		authenticated bool
		want          int
	}{
		{"missing auth", `{"q":"docs"}`, false, http.StatusUnauthorized},
		{"no body", ``, true, http.StatusBadRequest},
		{"not json", `q=docs`, true, http.StatusBadRequest},
		{"missing query", `{"limit":5}`, true, http.StatusBadRequest},
		{"empty query", `{"q":""}`, true, http.StatusBadRequest},
		{"whitespace query", `{"q":" 	 "}`, true, http.StatusBadRequest},
		{"query over 512 bytes", `{"q":"` + strings.Repeat("a", 513) + `"}`, true, http.StatusBadRequest},
		{"zero limit", `{"q":"docs","limit":0}`, true, http.StatusBadRequest},
		{"limit over max", `{"q":"docs","limit":101}`, true, http.StatusBadRequest},
		{"fractional limit", `{"q":"docs","limit":1.5}`, true, http.StatusBadRequest},
		{"wrong type", `{"q":5}`, true, http.StatusBadRequest},
		{"unknown field", `{"q":"docs","user_id":"attacker"}`, true, http.StatusBadRequest},
		{"trailing data", `{"q":"docs"}{"q":"x"}`, true, http.StatusBadRequest},
		{"oversized body", `{"q":"docs","cursor":"` + strings.Repeat("a", maxSearchBodyBytes) + `"}`, true, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if res := postLinks(h, tt.body, tt.authenticated); res.Code != tt.want {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
	if res := postLinks(h, `{"q":"`+strings.Repeat("a", 512)+`"}`, true); res.Code != http.StatusOK {
		t.Fatalf("512-byte query status=%d", res.Code)
	}
	for _, err := range []error{domain.ErrInvalidInput, errors.New("secret database detail")} {
		res := postLinks(NewSearchHandler(fakeSearchProvider{err: err}), `{"q":"docs"}`, true)
		if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "secret") {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
	}
}
