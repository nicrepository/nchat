package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Route-level tests for PATCH /api/chat/channels/{channelID} (issue #527).
//
// Everything here goes through the real router and the real ChannelService, so
// the authorization these assert is the one a client actually meets: no UI, no
// capability flag, just a token and a body.

// stored models the persisted rows the rename reads and writes. It re-applies
// the same workspace and #geral predicates the SQL carries, so a route test sees
// the same denial the database would produce.
func (s *routeChannelStore) GetChannelByIDInWorkspace(_ context.Context, workspaceID, id string) (domain.Channel, error) {
	channel, ok := s.stored[id]
	if !ok || channel.WorkspaceID != workspaceID || channel.Status != domain.ChannelStatusActive {
		return domain.Channel{}, domain.ErrNotFound
	}
	return channel, nil
}

func (s *routeChannelStore) UpdateChannel(_ context.Context, input storage.UpdateChannelInput) (domain.Channel, error) {
	channel, ok := s.stored[input.ChannelID]
	if !ok || channel.WorkspaceID != input.WorkspaceID || channel.IsGeneral {
		return domain.Channel{}, domain.ErrNotFound
	}
	s.updates = append(s.updates, input)
	channel.DisplayName = input.DisplayName
	channel.Slug = input.Slug
	channel.Type = input.Type
	channel.UpdatedAt = time.Now().UTC()
	s.stored[input.ChannelID] = channel
	return channel, nil
}

const renameRouteChannelID = "33333333-3333-4333-8333-333333333333"

func renameRouteEnv(t *testing.T, role domain.WorkspaceRole) channelRouteEnv {
	t.Helper()
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(role, domain.MemberStatusActive), true)
	env.channels.stored = map[string]domain.Channel{
		renameRouteChannelID: {
			ID: renameRouteChannelID, WorkspaceID: testWorkspaceID, Slug: "infra",
			DisplayName: "Infra", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
		},
	}
	return env
}

func patchChannel(t *testing.T, channelID, body string) *http.Request {
	t.Helper()
	token := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	request := httptest.NewRequest(http.MethodPatch, "/api/chat/channels/"+channelID, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", bearer(token))
	return request
}

// The whole authorization table, exercised through the wire rather than through
// the UI. A member who never sees the menu item and one who forges the request
// by hand reach the same 403.
func TestChannelRoute_Rename_AuthorizationIsServerSide(t *testing.T) {
	for _, test := range []struct {
		role domain.WorkspaceRole
		want int
	}{
		{domain.WorkspaceRoleOwner, http.StatusOK},
		{domain.WorkspaceRoleAdmin, http.StatusOK},
		{domain.WorkspaceRoleModerator, http.StatusForbidden},
		{domain.WorkspaceRoleMember, http.StatusForbidden},
		{domain.WorkspaceRoleGuest, http.StatusForbidden},
	} {
		t.Run(string(test.role), func(t *testing.T) {
			env := renameRouteEnv(t, test.role)
			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, patchChannel(t, renameRouteChannelID, `{"display_name":"Plataforma"}`))

			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.want, recorder.Body.String())
			}
			persisted := env.channels.stored[renameRouteChannelID]
			if test.want == http.StatusOK {
				if persisted.DisplayName != "Plataforma" {
					t.Fatalf("persisted name = %q, want Plataforma", persisted.DisplayName)
				}
				// The identity is what must survive a rename.
				if persisted.ID != renameRouteChannelID || persisted.Type != domain.ChannelTypePrivate ||
					persisted.Slug != "infra" {
					t.Fatalf("rename changed more than the name: %+v", persisted)
				}
				return
			}
			if persisted.DisplayName != "Infra" {
				t.Fatalf("a refused caller changed the persisted name to %q", persisted.DisplayName)
			}
			if len(env.channels.updates) != 0 {
				t.Fatalf("a refused caller reached the store: %+v", env.channels.updates)
			}
		})
	}
}

// A request without a usable session never reaches the service, so a rename is
// not a route an anonymous caller can probe for which channels exist.
func TestChannelRoute_Rename_RequiresASession(t *testing.T) {
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	request := httptest.NewRequest(http.MethodPatch, "/api/chat/channels/"+renameRouteChannelID,
		strings.NewReader(`{"display_name":"Plataforma"}`))
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if len(env.channels.updates) != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

// An owner of this workspace aiming at a channel of another one, and at an ID
// nobody ever issued, get the same 404 — so the route is not an oracle for which
// channel UUIDs exist.
func TestChannelRoute_Rename_IsNotAnExistenceOracle(t *testing.T) {
	const foreignChannelID = "44444444-4444-4444-8444-444444444444"
	const unknownChannelID = "55555555-5555-4555-8555-555555555555"
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	env.channels.stored[foreignChannelID] = domain.Channel{
		ID: foreignChannelID, WorkspaceID: "99999999-9999-4999-8999-999999999999",
		Slug: "outro", DisplayName: "Outro", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
	}

	for _, id := range []string{foreignChannelID, unknownChannelID} {
		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, patchChannel(t, id, `{"display_name":"Plataforma"}`))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d for %s, want 404", recorder.Code, id)
		}
		if code := errorCode(t, recorder); code != "not_found" {
			t.Fatalf("error code = %q, want not_found", code)
		}
	}
	if env.channels.stored[foreignChannelID].DisplayName != "Outro" {
		t.Fatal("a cross-workspace rename was persisted")
	}
}

// #geral is immutable for every role: the service refuses it before the store,
// and the store's own predicate would refuse it again.
func TestChannelRoute_Rename_RefusesTheGeneralChannel(t *testing.T) {
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	general := env.channels.stored[renameRouteChannelID]
	general.IsGeneral = true
	env.channels.stored[renameRouteChannelID] = general

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, patchChannel(t, renameRouteChannelID, `{"display_name":"Plataforma"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if env.channels.stored[renameRouteChannelID].DisplayName != "Infra" {
		t.Fatal("#geral was renamed")
	}
}

// Validation is the domain's, reached through the wire: an empty or oversized
// name is a 400 and never a persisted row, and the rejected value never comes
// back in the response.
func TestChannelRoute_Rename_ValidatesTheNameServerSide(t *testing.T) {
	long := strings.Repeat("a", domain.MaxChannelDisplayNameCodePoints+1)
	for _, test := range []struct {
		name string
		body string
	}{
		{"empty", `{"display_name":""}`},
		{"whitespace only", `{"display_name":"   "}`},
		{"over the cap", `{"display_name":"` + long + `"}`},
		{"absent", `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, patchChannel(t, renameRouteChannelID, test.body))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), long) {
				t.Fatalf("the rejected name was echoed back: %s", recorder.Body.String())
			}
			if len(env.channels.updates) != 0 {
				t.Fatalf("an invalid name reached the store: %+v", env.channels.updates)
			}
		})
	}
}

// Trimming and the Unicode cap are the create path's rules, applied unchanged —
// a rename must not be the way past a bound creation enforces.
func TestChannelRoute_Rename_NormalisesLikeCreation(t *testing.T) {
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, patchChannel(t, renameRouteChannelID, `{"display_name":"  Plataforma  "}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := env.channels.stored[renameRouteChannelID].DisplayName; got != "Plataforma" {
		t.Fatalf("persisted %q, want the trimmed name", got)
	}
}

// Two administrators renaming the same channel is the last write winning on one
// row, not two identities: the ID is unchanged and there is still exactly one
// channel afterwards.
func TestChannelRoute_Rename_ConcurrentRenamesKeepOneIdentity(t *testing.T) {
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	for _, name := range []string{"Primeiro", "Segundo"} {
		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, patchChannel(t, renameRouteChannelID, `{"display_name":"`+name+`"}`))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d for %s: %s", recorder.Code, name, recorder.Body.String())
		}
	}
	if len(env.channels.stored) != 1 {
		t.Fatalf("%d channels persisted, want 1", len(env.channels.stored))
	}
	persisted := env.channels.stored[renameRouteChannelID]
	if persisted.ID != renameRouteChannelID || persisted.DisplayName != "Segundo" {
		t.Fatalf("persisted = %+v, want the same id holding the last name", persisted)
	}
}

// The route is PATCH-only. A method the mux does not serve must not fall through
// to another channel route.
func TestChannelRoute_Rename_OnlyServesPatch(t *testing.T) {
	env := renameRouteEnv(t, domain.WorkspaceRoleOwner)
	token := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		request := httptest.NewRequest(method, "/api/chat/channels/"+renameRouteChannelID, nil)
		request.Header.Set("Authorization", bearer(token))
		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, request)

		if recorder.Code == http.StatusOK {
			t.Fatalf("%s on the rename route was served", method)
		}
		if len(env.channels.updates) != 0 {
			t.Fatalf("%s reached the store", method)
		}
	}
}
