package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func listInput(limit int) service.ListChannelAttachmentsInput {
	return service.ListChannelAttachmentsInput{
		ChannelID: testChannelID,
		UserID:    testUserID,
		SessionID: testSessionID,
		Limit:     limit,
	}
}

func TestListChannelAttachments_ProjectsTheStoredRows(t *testing.T) {
	f := newFixture(t)
	createdAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	f.store.listed = []service.ListedAttachment{
		{
			ID: "a-1", Status: domain.StatusClean, Filename: "relatorio.pdf",
			DetectedMIME: "application/pdf", Size: 2048, CreatedAt: createdAt,
		},
		// No detected type: the view must fall back to the inert default rather
		// than to whatever the uploader declared.
		{ID: "a-2", Status: domain.StatusPendingScan, Filename: "topologia", Size: 10, CreatedAt: createdAt},
	}

	views, err := f.service.ListChannelAttachments(context.Background(), listInput(0))
	if err != nil {
		t.Fatalf("ListChannelAttachments: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected two views, got %d", len(views))
	}
	if views[0].ID != "a-1" || views[0].ContentType != "application/pdf" || views[0].Size != 2048 {
		t.Fatalf("unexpected first view: %+v", views[0])
	}
	if views[0].Status != string(domain.StatusClean) || views[0].DestinationKind != "channel" {
		t.Fatalf("unexpected first view state: %+v", views[0])
	}
	if views[1].ContentType != domain.DefaultContentType {
		t.Fatalf("expected the inert default content type, got %q", views[1].ContentType)
	}
	if views[1].Status != string(domain.StatusPendingScan) {
		t.Fatalf("the scan state must survive the projection, got %q", views[1].Status)
	}
}

func TestListChannelAttachments_QueriesTheAuthorizedWorkspaceOnly(t *testing.T) {
	f := newFixture(t)
	// The authorizer answers with the destination's own canonical workspace and
	// channel; the query must use those, never the request's channel string.
	f.authorizer.result = service.AuthorizedDestination{
		ID:               testChannelID,
		WorkspaceID:      testWorkspaceID,
		SessionExpiresAt: time.Now().Add(time.Hour),
	}

	if _, err := f.service.ListChannelAttachments(context.Background(), listInput(0)); err != nil {
		t.Fatalf("ListChannelAttachments: %v", err)
	}
	if f.store.listCalled != 1 {
		t.Fatalf("expected exactly one store query, got %d", f.store.listCalled)
	}
	if f.store.listQuery.WorkspaceID != testWorkspaceID || f.store.listQuery.ChannelID != testChannelID {
		t.Fatalf("query must be bound to the authorized destination: %+v", f.store.listQuery)
	}
	if len(f.authorizer.calls) != 1 || f.authorizer.calls[0].Destination.Kind != domain.DestinationKindChannel {
		t.Fatalf("expected one channel authorization, got %+v", f.authorizer.calls)
	}
}

func TestListChannelAttachments_ClampsTheLimit(t *testing.T) {
	for name, tt := range map[string]struct{ asked, want int }{
		"unspecified":  {asked: 0, want: domain.DefaultAttachmentListLimit},
		"within range": {asked: 5, want: 5},
		"above ceiling": {
			asked: domain.MaxAttachmentListLimit * 10,
			want:  domain.MaxAttachmentListLimit,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.service.ListChannelAttachments(context.Background(), listInput(tt.asked)); err != nil {
				t.Fatalf("ListChannelAttachments: %v", err)
			}
			if f.store.listQuery.Limit != tt.want {
				t.Fatalf("expected limit %d, got %d", tt.want, f.store.listQuery.Limit)
			}
		})
	}
}

func TestListChannelAttachments_RefusesUnreachableChannels(t *testing.T) {
	for name, tt := range map[string]struct {
		input   service.ListChannelAttachmentsInput
		authErr error
		want    error
	}{
		"channel not visible": {input: listInput(0), authErr: domain.ErrNotFound, want: domain.ErrNotFound},
		"session invalid":     {input: listInput(0), authErr: domain.ErrUnauthorized, want: domain.ErrUnauthorized},
		// A malformed UUID is answered exactly like an invisible channel, so the
		// route cannot be used to tell one from the other.
		"malformed channel id": {
			input: service.ListChannelAttachmentsInput{ChannelID: "../../etc", UserID: testUserID, SessionID: testSessionID},
			want:  domain.ErrNotFound,
		},
		"malformed principal": {
			input: service.ListChannelAttachmentsInput{ChannelID: testChannelID, UserID: "nope", SessionID: testSessionID},
			want:  domain.ErrUnauthorized,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.authorizer.err = tt.authErr

			_, err := f.service.ListChannelAttachments(context.Background(), tt.input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
			if f.store.listCalled != 0 {
				t.Fatal("an unauthorised listing must never reach the store")
			}
		})
	}
}

func TestListChannelAttachments_RequiresAWiredService(t *testing.T) {
	unwired := service.NewAttachmentService(nil, nil, nil, nil, 0, true, nil, discardLogger())
	_, err := unwired.ListChannelAttachments(context.Background(), listInput(0))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestStatusListable_ExcludesIncompleteAndAbandonedUploads(t *testing.T) {
	for status, want := range map[domain.Status]bool{
		domain.StatusPendingUpload: false,
		domain.StatusFailed:        false,
		domain.StatusDeleted:       false,
		domain.StatusPendingScan:   true,
		domain.StatusClean:         true,
		domain.StatusRejected:      true,
	} {
		if got := status.Listable(); got != want {
			t.Fatalf("%s: expected listable=%v, got %v", status, want, got)
		}
	}
}
