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
	messages domain.MessagePage
	users    domain.UserPage
	channels domain.ChannelPage
	err      error
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
