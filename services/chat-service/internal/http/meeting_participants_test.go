package httpapi_test

import (
	"context"
	"errors"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"strings"
	"testing"
)

func (s *meetingRouteProvider) ListMeetingParticipants(_ context.Context, scope storage.MeetingScope, q storage.MeetingPageQuery) (storage.MeetingParticipantPage, error) {
	s.participantCalls++
	return storage.MeetingParticipantPage{}, s.err
}
func (s *meetingRouteProvider) ListMeetingCandidates(_ context.Context, scope storage.MeetingCandidateScope, q storage.MeetingPageQuery) (storage.MeetingCandidatePage, error) {
	s.participantCalls++
	return storage.MeetingCandidatePage{}, s.err
}
func (s *meetingRouteProvider) AddMeetingParticipant(_ context.Context, scope storage.MeetingScope, id string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	s.participantCalls++
	return domain.MeetingParticipant{UserID: id, Role: role}, s.err
}
func (s *meetingRouteProvider) ChangeMeetingParticipantRole(_ context.Context, scope storage.MeetingScope, id string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	s.participantCalls++
	return domain.MeetingParticipant{UserID: id, Role: role}, s.err
}
func (s *meetingRouteProvider) RemoveMeetingParticipant(_ context.Context, scope storage.MeetingScope, id string) error {
	s.participantCalls++
	return s.err
}
func (s *meetingRouteProvider) InviteAllMeetingParticipants(_ context.Context, scope storage.MeetingScope) (int64, error) {
	s.participantCalls++
	return 2, s.err
}

func TestMeetingParticipantRoutes(t *testing.T) {
	base := meetingRoutePath + "/participants"
	user := "22222222-2222-4222-8222-222222222222"
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", base, "", 200},
		{"GET", "/api/chat/meeting-candidates/dm/" + meetingRouteID, "", 200},
		{"POST", base, `{"user_id":"` + user + `"}`, 201},
		{"PATCH", base + "/" + user, `{"role":"cohost"}`, 200},
		{"DELETE", base + "/" + user, "", 204},
		{"POST", base + "/invite-all", `{}`, 200},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			r := env.request(tt.method, tt.path, tt.body)
			r.Header.Del("If-Match")
			if tt.method == "GET" || tt.method == "DELETE" {
				r.Header.Del("Content-Type")
			}
			w := env.serve(r)
			if w.Code != tt.status || env.provider.participantCalls != 1 {
				t.Fatalf("%d %s calls %d", w.Code, w.Body, env.provider.participantCalls)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
				t.Fatal("invalid private roster headers")
			}
		})
	}
}

func TestMeetingParticipantRouteRefusals(t *testing.T) {
	base := meetingRoutePath + "/participants"
	for _, tt := range []struct{ method, path, body string }{
		{"POST", base, `null`}, {"POST", base, `{"user_id":"bad"}`},
		{"POST", base, `{"user_id":"22222222-2222-4222-8222-222222222222","workspace_id":"other"}`},
		{"POST", base, `{`}, {"POST", base, strings.Repeat(" ", 65537) + `{}`},
		{"PATCH", base + "/bad", `{"role":"cohost"}`}, {"PATCH", base + "/" + meetingRouteID, `null`},
		{"PATCH", base + "/" + meetingRouteID, `{"organizer_user_id":"x"}`},
		{"DELETE", base + "/bad", ``}, {"POST", base + "/invite-all", `null`}, {"POST", base + "/invite-all", `{"all":true}`},
		{"GET", base + "?limit=101", ``}, {"GET", base + "?limit=0", ``}, {"GET", base + "?limit=x", ``},
		{"GET", base + "?cursor=bad", ``}, {"GET", base + "?limit=1&limit=2", ``}, {"GET", base + "?q=a", ``},
		{"GET", base + "?workspace_id=x", ``}, {"GET", base + "?q=%ZZ", ``},
		{"GET", "/api/chat/meeting-candidates/channel/bad", ``},
		{"GET", "/api/chat/meeting-candidates/dm/" + meetingRouteID + "?cursor=bad", ``},
	} {
		t.Run(tt.method+tt.path+tt.body[:min(len(tt.body), 20)], func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			w := env.serve(env.request(tt.method, tt.path, tt.body))
			if w.Code != 400 || env.provider.participantCalls != 0 {
				t.Fatalf("%d %s calls %d", w.Code, w.Body, env.provider.participantCalls)
			}
		})
	}
}

func TestMeetingParticipantPrivateErrors(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{{domain.ErrNotFound, 404}, {domain.ErrForbidden, 403}, {domain.ErrInvalidInput, 400}, {domain.ErrMeetingParticipantExists, 409}, {errors.New("private SQL detail"), 500}} {
		env := newMeetingRouteEnv(t, allowAllSessionValidator{})
		env.provider.err = tt.err
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE", "ALL", "CANDIDATES"} {
			path, body, verb := meetingRoutePath+"/participants", `{"user_id":"22222222-2222-4222-8222-222222222222"}`, method
			switch method {
			case "PATCH":
				path += "/" + meetingRouteID
				body = `{"role":"attendee"}`
			case "DELETE":
				path += "/" + meetingRouteID
			case "ALL":
				verb = "POST"
				path += "/invite-all"
				body = `{}`
			case "CANDIDATES":
				verb = "GET"
				path = "/api/chat/meeting-candidates/dm/" + meetingRouteID
			}
			w := env.serve(env.request(verb, path, body))
			if w.Code != tt.status || strings.Contains(w.Body.String(), "private SQL") {
				t.Fatalf("%s: %d %s", method, w.Code, w.Body)
			}
		}
	}
	for _, valid := range []bool{true, false} {
		var sessions httpapi.SessionValidator = denySessionValidator{err: domain.ErrInvalidToken}
		if valid {
			sessions = allowAllSessionValidator{}
		}
		env := newMeetingRouteEnv(t, sessions)
		r := env.request("GET", meetingRoutePath+"/participants", "")
		if valid {
			r.Header.Del("Authorization")
		}
		if w := env.serve(r); w.Code != 401 {
			t.Fatalf("auth %d", w.Code)
		}
	}
}
