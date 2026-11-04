package service_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
)

func TestPolicyRefusalsCompensateEncryptedQuarantine(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       error
	}{
		{"notes.unknown", "business notes", domain.ErrUnsupportedMedia},
		{"notes.json", `{"cost":12}`, domain.ErrFormatBlocked},
		{"notes.csv", "a,b\n1,2,3\n", domain.ErrUnrecognizedContent},
		{"notes.csv", "a,b\n" + strings.Repeat("a", int(filepolicy.MaxTextBytes)), domain.ErrInspectionLimit},
	} {
		t.Run(tt.name+tt.want.Error(), func(t *testing.T) {
			f := newFixture(t)
			_, err := f.upload(context.Background(), strings.NewReader(tt.body), tt.name)
			if !errors.Is(err, tt.want) {
				t.Fatalf("policy error = %v, want %v", err, tt.want)
			}
			assertCompensated(t, f)
		})
	}
}

func TestEnterpriseProfileCannotSkipAntimalwareInLocalMode(t *testing.T) {
	f := newFixture(t, fixtureOptions{scanRequired: false})
	f.service.SetFormatProfiles(filepolicy.Profiles{Data: true})
	view, err := f.upload(context.Background(), strings.NewReader(`{"cost":12}`), "costs.json")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != string(domain.StatusPendingScan) {
		t.Fatalf("enterprise escaped quarantine: %s", view.Status)
	}
}

func TestClassifiedUploadPreviewFollowsCatalog(t *testing.T) {
	for _, tt := range []struct {
		name string
		want domain.PreviewStatus
	}{
		{"notes.txt", domain.PreviewStatusUnsupported}, {"bundle.zip", domain.PreviewStatusUnsupported},
		{"notes.md", domain.PreviewStatusUnsupported}, {"document.odp", domain.PreviewStatusUnsupported},
		{"data.csv", domain.PreviewStatusPending}, {"document.xlsx", domain.PreviewStatusPending}, {"document.docx", domain.PreviewStatusPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.service.SetFormatProfiles(filepolicy.Profiles{Data: true, Office: true, Archives: true})
			view, err := f.upload(context.Background(), bytes.NewReader(enterpriseCorpus(t, tt.name)), tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if view.PreviewStatus != string(tt.want) {
				t.Fatalf("preview status=%s, want %s", view.PreviewStatus, tt.want)
			}
			_, uploaded, _ := f.store.snapshot()
			if uploaded[0].PreviewStatus != tt.want {
				t.Fatalf("persisted preview=%s", uploaded[0].PreviewStatus)
			}
		})
	}
}
