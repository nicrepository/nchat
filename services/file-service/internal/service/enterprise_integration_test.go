package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

type enterpriseEnv struct {
	*integrationEnv
	sessionID, token string
	client           *http.Client
	server           *httptest.Server
	scans            *service.MalwareScanService
}

func enterpriseExec(t *testing.T, e *integrationEnv, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func newEnterpriseEnv(t *testing.T) *enterpriseEnv {
	t.Helper()
	address := os.Getenv("FILE_TEST_CLAMD_ADDRESS")
	if address == "" {
		t.Skip("FILE_TEST_CLAMD_ADDRESS is required for a real antimalware round-trip")
	}
	e := newIntegrationEnv(t)
	sid := uuid.NewString()
	enterpriseExec(t, e, `INSERT INTO auth.users(id,email,display_name,status,auth_source) VALUES($1,$2,'Synthetic tester','active','manual')`, e.uploaderID, e.uploaderID+"@example.test")
	enterpriseExec(t, e, `INSERT INTO auth.user_sessions(id,user_id,refresh_token_hash,idle_expires_at) VALUES($1::uuid,$2::uuid,$1::text,now()+interval '1 hour')`, sid, e.uploaderID)
	enterpriseExec(t, e, `WITH workspace AS (
 INSERT INTO chat.workspaces(id,slug,name) VALUES($1::uuid,$1::text,'Synthetic workspace') RETURNING id
 ) INSERT INTO chat.channels(id,workspace_id,slug,display_name,type,is_general)
 SELECT $2::uuid,id,'geral','Synthetic costs','public',true FROM workspace`, e.workspaceID, e.channelID)
	enterpriseExec(t, e, `INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES($1,$2)`, e.workspaceID, e.uploaderID)
	t.Cleanup(func() {
		enterpriseExec(t, e, `DELETE FROM chat.workspaces WHERE id=$1`, e.workspaceID)
		enterpriseExec(t, e, `DELETE FROM auth.users WHERE id=$1`, e.uploaderID)
	})
	secret := strings.Repeat("x", 32)
	validator, err := httpapi.NewTokenValidator(secret, "nchat-integration", "nchat")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := jwt.MapClaims{"iss": "nchat-integration", "aud": "nchat", "sub": e.uploaderID, "sid": sid, "jti": uuid.NewString(), "iat": now.Unix(), "nbf": now.Add(-time.Second).Unix(), "exp": now.Add(time.Hour).Unix()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	malware, err := scanner.New(address, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	scans := service.NewMalwareScanService(storage.NewPGXScanStore(e.pool), e.lifecycle, e.objects, e.keys, malware, nil, nil, 30*time.Second, discardLogger())
	scans.SetFormatProfiles(filepolicy.Profiles{Data: true, Office: true, Archives: true})
	result := &enterpriseEnv{integrationEnv: e, sessionID: sid, token: token, scans: scans}
	result.reopen(t, validator)
	return result
}

func (e *enterpriseEnv) reopen(t *testing.T, validator *httpapi.TokenValidator) {
	t.Helper()
	if e.server != nil {
		e.server.Close()
	}
	svc := service.NewAttachmentService(storage.NewPGXDestinationAuthorizer(e.pool), e.store, e.objects, e.keys, domain.DefaultMaxUploadBytes, true, nil, discardLogger())
	svc.SetFormatProfiles(filepolicy.Profiles{Data: true, Office: true, Archives: true})
	lockPool, ok := storage.LockConnPoolFrom(e.pool)
	if !ok {
		t.Fatal("admission needs a lock pool")
	}
	admission := storage.NewPGXUploadAdmission(lockPool, storage.UploadAdmissionLimits{Global: 4, PerUser: 2}, discardLogger())
	router := httpapi.NewRouter(config.Config{UploadsEnabled: true, MaxUploadBytes: domain.DefaultMaxUploadBytes, UploadRetryAfterSeconds: 30}, discardLogger(), httpapi.RouterDependencies{TokenValidator: validator, Attachments: svc, Admission: admission})
	e.server = httptest.NewServer(router)
	e.client = e.server.Client()
	t.Cleanup(e.server.Close)
}

func (e *enterpriseEnv) request(t *testing.T, method, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), method, e.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+e.token)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	response, err := e.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}

	return response
}

func (e *enterpriseEnv) uploadHTTP(t *testing.T, name string, b []byte) service.AttachmentView {
	t.Helper()
	return e.uploadHTTPAt(t, "/channels/"+e.channelID+"/attachments", name, b)
}

func (e *enterpriseEnv) uploadHTTPAt(t *testing.T, path, name string, b []byte) service.AttachmentView {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(b); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := e.request(t, http.MethodPost, path, &body, map[string]string{"Content-Type": w.FormDataContentType()})
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(r.Body)
		t.Fatalf("upload status %d: %s", r.StatusCode, data)
	}
	var envelope struct {
		Data service.AttachmentView `json:"data"`
	}
	if err = json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Status != "pending_scan" {
		t.Fatalf("upload escaped quarantine: %+v", envelope.Data)
	}
	return envelope.Data
}

func TestIntegrationEnterpriseTenantAndGuestBoundaries(t *testing.T) {
	e := newEnterpriseEnv(t)
	other := newEnterpriseEnv(t)
	b := enterpriseCorpus(t, "data.json")
	view := e.uploadHTTP(t, "costs.json", b)
	e.scanClean(t, view.ID)
	originalToken := e.token
	e.token = other.token
	e.assertEndpointStatus(t, view.ID, http.StatusNotFound)
	e.token = originalToken
	enterpriseExec(t, e.integrationEnv, `UPDATE chat.workspace_members SET role='guest' WHERE workspace_id=$1 AND user_id=$2`, e.workspaceID, e.uploaderID)
	enterpriseExec(t, e.integrationEnv, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, e.channelID, e.uploaderID)
	e.assertEndpointStatus(t, view.ID, http.StatusNotFound)
	enterpriseExec(t, e.integrationEnv, `INSERT INTO chat.channel_members(channel_id,user_id) VALUES($1,$2)`, e.channelID, e.uploaderID)
	e.assertOriginal(t, view.ID, "costs.json", b)
	enterpriseExec(t, e.integrationEnv, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, e.channelID, e.uploaderID)
	e.assertEndpointStatus(t, view.ID, http.StatusNotFound)
}

func TestIntegrationEnterpriseDMAndGroupOriginals(t *testing.T) {
	for _, kind := range []string{"direct", "group"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnterpriseEnv(t)
			id := uuid.NewString()
			enterpriseExec(t, e.integrationEnv, `WITH conversation AS (
 INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by,direct_pair_key)
 VALUES($1::uuid,$2::uuid,$3::text,$4::uuid,CASE WHEN $3::text='direct' THEN $1::text ELSE NULL END) RETURNING id
 ) INSERT INTO chat.dm_members(conversation_id,user_id,ownership_role) SELECT id,$4,'owner' FROM conversation`, id, e.workspaceID, kind, e.uploaderID)
			b := enterpriseCorpus(t, "document.docx")
			view := e.uploadHTTPAt(t, "/dm/"+id+"/attachments", "contract.docx", b)
			e.assertEndpointStatus(t, view.ID, http.StatusForbidden)
			e.scanClean(t, view.ID)
			e.assertOriginal(t, view.ID, "contract.docx", b)
			enterpriseExec(t, e.integrationEnv, `UPDATE chat.dm_conversations SET status='archived' WHERE id=$1`, id)
			e.assertEndpointStatus(t, view.ID, http.StatusNotFound)
		})
	}
}

func (e *enterpriseEnv) scanClean(t *testing.T, id string) {
	t.Helper()
	if _, err := e.scans.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ := e.row(t, id)
	if status != "clean" {
		t.Fatalf("real scan did not approve synthetic corpus: %s", status)
	}
}

func (e *enterpriseEnv) assertOriginal(t *testing.T, id, name string, b []byte) {
	t.Helper()
	r := e.request(t, http.MethodGet, "/attachments/"+id+"/content", nil, nil)
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("original status %d", r.StatusCode)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) != r.ContentLength || !bytes.Equal(got, b) || sha256.Sum256(got) != sha256.Sum256(b) {
		t.Fatal("native original changed after encryption/scan/download")
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatal("unsafe original headers")
	}
	_, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
	if err != nil || parameters["filename"] != name {
		t.Fatalf("native filename changed: %q", parameters["filename"])
	}
	t.Logf("synthetic %s size=%d sha256=%x", name, len(b), sha256.Sum256(b))
}

func enterpriseCorpus(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("../filepolicy/testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIntegrationEnterpriseNativeOriginalRoundTrip(t *testing.T) {
	e := newEnterpriseEnv(t)
	names := []string{"notes.txt", "notes.md", "data.csv", "data.tsv", "data.json", "data.xml", "message.eml", "document.pdf", "document.docx", "document.xlsx", "document.pptx", "document.odt", "document.ods", "document.odp", "bundle.zip", "image.png", "image.jpg", "image.gif", "image.webp", "silence.mp3", "silence.ogg", "silence.wav", "video.mp4", "video.webm", "video.mov", "video.avi"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			b := enterpriseCorpus(t, name)
			filename := "Relatório.Custos " + name
			view := e.uploadHTTP(t, filename, b)
			e.assertEndpointStatus(t, view.ID, http.StatusForbidden)
			_, err := e.pool.Exec(context.Background(), `UPDATE files.attachments SET status='clean' WHERE id=$1`, view.ID)
			if err == nil {
				t.Fatal("a pre-policy worker can approve a new attachment")
			}
			e.scanClean(t, view.ID)
			e.assertOriginal(t, view.ID, filename, b)
		})
	}
}

func enterprisePreviewWorker(t *testing.T, e *enterpriseEnv) *service.PreviewService {
	t.Helper()
	converterURL := os.Getenv("FILE_TEST_CONVERTER_URL")
	if converterURL == "" {
		t.Skip("FILE_TEST_CONVERTER_URL is required for real Office preview conversion")
	}
	client, err := converter.NewClient(converterURL, 35*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return service.NewPreviewService(storage.NewPGXPreviewStore(e.pool), e.objects, e.keys, preview.NewWithDocumentConverter(client), e.fence, storage.NewPGXObjectCleanupStore(e.pool), nil, discardLogger())
}

func TestIntegrationEnterprisePreviewKeepsNativeOriginal(t *testing.T) {
	e := newEnterpriseEnv(t)
	worker := enterprisePreviewWorker(t, e)
	for _, name := range []string{"image.png", "document.pdf", "data.csv", "document.xlsx", "document.ods", "document.docx", "document.odt", "document.pptx"} {
		t.Run(name, func(t *testing.T) {
			b := enterpriseCorpus(t, name)
			view := e.uploadHTTP(t, name, b)
			e.scanClean(t, view.ID)
			e.assertOriginal(t, view.ID, name, b)
			e.generatePreview(t, worker, view.ID, name, b)
			e.expireAndRegeneratePreview(t, view.ID)
			e.generatePreview(t, worker, view.ID, name, b)

		})
	}
}

func TestIntegrationEnterpriseRangeAndRevocation(t *testing.T) {
	e := newEnterpriseEnv(t)
	b := bytes.Repeat([]byte("ação sintética\n"), 16000)
	view := e.uploadHTTP(t, "Ação.Custos.TXT", b)
	e.scanClean(t, view.ID)
	var rebuilt []byte
	const segment = 65513
	for start := 0; start < len(b); start += segment {
		end := min(start+segment, len(b)) - 1
		r := e.request(t, http.MethodGet, "/attachments/"+view.ID+"/content", nil, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", start, end)})
		defer func() { _ = r.Body.Close() }()
		if r.StatusCode != http.StatusPartialContent {
			t.Fatalf("range status %d", r.StatusCode)
		}
		part, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(part, b[start:end+1]) {
			t.Fatal("range differs from original")
		}
		rebuilt = append(rebuilt, part...)
	}
	if sha256.Sum256(rebuilt) != sha256.Sum256(b) {
		t.Fatal("range reconstruction changed original")
	}
	enterpriseExec(t, e.integrationEnv, `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, e.workspaceID, e.uploaderID)
	e.assertEndpointStatus(t, view.ID, http.StatusNotFound)

}

func TestIntegrationEnterpriseScannerOutageAndEICAR(t *testing.T) {
	e := newEnterpriseEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	offline, err := scanner.New(address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	worker := service.NewMalwareScanService(storage.NewPGXScanStore(e.pool), e.lifecycle, e.objects, e.keys, offline, nil, nil, time.Second, discardLogger())
	view := e.uploadHTTP(t, "notes.txt", enterpriseCorpus(t, "notes.txt"))
	if _, err = worker.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ := e.row(t, view.ID)
	if status != "pending_scan" {
		t.Fatalf("scanner outage escaped quarantine: %s", status)
	}
	// Advance this fixture's durable schedule without a wall-clock sleep.
	enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET scan_next_attempt_at=now() WHERE id=$1`, view.ID)
	e.scanClean(t, view.ID)
	eicar := []byte("X5O!P%@AP[4" + "\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*")
	infected := e.uploadHTTP(t, "eicar-test.txt", eicar)
	if _, err = e.scans.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ = e.row(t, infected.ID)
	if status != "rejected" {
		t.Fatalf("real EICAR scan: %s", status)
	}
	r := e.request(t, http.MethodGet, "/attachments/"+infected.ID+"/content", nil, nil)
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("malware original delivered: %d", r.StatusCode)
	}
}

func (e *enterpriseEnv) generatePreview(t *testing.T, worker *service.PreviewService, id, name string, b []byte) {
	t.Helper()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := e.request(t, http.MethodGet, "/attachments/"+id+"/preview", nil, nil)
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("real preview %s: %d", name, r.StatusCode)
	}
	e.assertOriginal(t, id, name, b)
}

func (e *enterpriseEnv) assertEndpointStatus(t *testing.T, id string, status int) {
	t.Helper()
	for _, suffix := range []string{"/content", "/preview", "/document-preview", "/audio.mp3"} {
		r := e.request(t, http.MethodGet, "/attachments/"+id+suffix, nil, map[string]string{"Range": "bytes=0-10"})
		defer func() { _ = r.Body.Close() }()
		if r.StatusCode != status {
			t.Fatalf("endpoint %s: status %d, expected %d", suffix, r.StatusCode, status)
		}
	}
}

// Simulate the existing expiry contract, then use the real regeneration route.
// Delete only this fixture's expired derived object, never its original.
func (e *enterpriseEnv) expireAndRegeneratePreview(t *testing.T, id string) {
	t.Helper()
	var previewID string
	if err := e.pool.QueryRow(context.Background(), `SELECT preview_object_id::text FROM files.attachments WHERE id=$1`, id).Scan(&previewID); err != nil {
		t.Fatal(err)
	}
	if err := e.objects.Delete(context.Background(), domain.PreviewObjectKey(uuid.MustParse(previewID))); err != nil {
		t.Fatal(err)
	}
	enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET preview_lifecycle_status='expired' WHERE id=$1`, id)
	r := e.request(t, http.MethodPost, "/attachments/"+id+"/document-preview/regenerate", nil, nil)
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("regeneration route: %d", r.StatusCode)
	}
}

func TestIntegrationEnterpriseReopensOriginal(t *testing.T) {
	e := newEnterpriseEnv(t)
	b := enterpriseCorpus(t, "document.docx")
	view := e.uploadHTTP(t, "Custos.Nativos.DOCX", b)
	e.scanClean(t, view.ID)
	validator, err := httpapi.NewTokenValidator(strings.Repeat("x", 32), "nchat-integration", "nchat")
	if err != nil {
		t.Fatal(err)
	}
	e.reopen(t, validator)
	e.assertOriginal(t, view.ID, "Custos.Nativos.DOCX", b)
}

func TestIntegrationEnterprisePreviewPolicyDoesNotQueueUnsupported(t *testing.T) {
	e := newEnterpriseEnv(t)
	for _, name := range []string{"notes.txt", "bundle.zip", "notes.md", "document.odp"} {
		t.Run(name, func(t *testing.T) {
			b := enterpriseCorpus(t, name)
			view := e.uploadHTTP(t, name, b)
			if view.PreviewStatus != "unsupported" {
				t.Fatalf("initial preview=%s", view.PreviewStatus)
			}
			e.scanClean(t, view.ID)
			e.assertNoPreviewJob(t, view.ID)
			e.assertOriginal(t, view.ID, name, b)
		})
	}
}

func (e *enterpriseEnv) assertNoPreviewJob(t *testing.T, id string) {
	t.Helper()

	var status, lifecycle string
	var attempts int
	if err := e.pool.QueryRow(context.Background(), `SELECT preview_status,preview_lifecycle_status,preview_attempts FROM files.attachments WHERE id=$1`, id).Scan(&status, &lifecycle, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "unsupported" || lifecycle != "blocked" || attempts != 0 {
		t.Fatalf("queued disabled preview: %s/%s/%d", status, lifecycle, attempts)
	}
	jobs, err := storage.NewPGXPreviewStore(e.pool).ClaimDuePreviews(context.Background(), 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatal("unsupported preview entered worker queue")
	}
}

func TestIntegrationEnterpriseLegacyPreviewCompatibility(t *testing.T) {
	e := newEnterpriseEnv(t)
	worker := enterprisePreviewWorker(t, e)
	for _, tt := range []struct{ name, mime string }{{"data.csv", "text/plain; charset=utf-8"}, {"document.docx", "application/zip"}} {
		t.Run(tt.name, func(t *testing.T) {
			b := enterpriseCorpus(t, tt.name)
			view := e.uploadHTTP(t, tt.name, b)
			e.scanClean(t, view.ID)
			enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET format_policy_version=0,format_id='',detected_mime=$2 WHERE id=$1`, view.ID, tt.mime)
			e.generatePreview(t, worker, view.ID, tt.name, b)
		})
	}
}

func TestIntegrationEnterpriseRegenerationCooldown(t *testing.T) {
	e := newEnterpriseEnv(t)
	view := e.uploadHTTP(t, "image.png", enterpriseCorpus(t, "image.png"))
	e.scanClean(t, view.ID)
	enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET preview_lifecycle_status='failed',preview_attempts=3,updated_at=now() WHERE id=$1`, view.ID)
	response := e.request(t, http.MethodPost, "/attachments/"+view.ID+"/document-preview/regenerate", nil, nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("failure cooldown: %d", response.StatusCode)
	}
	enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET updated_at=now()-interval '25 hours' WHERE id=$1`, view.ID)
	store := storage.NewPGXAttachmentStore(e.pool)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- store.RegenerateDocumentPreview(context.Background(), view.ID) }()
	}
	accepted := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			accepted++
		} else if err != domain.ErrPreviewUnavailable {
			t.Fatal(err)
		}
	}
	if accepted == 0 {
		t.Fatal("no concurrent regeneration accepted")
	}
	var attempts int
	var lifecycle string
	if err := e.pool.QueryRow(context.Background(), `SELECT preview_attempts,preview_lifecycle_status FROM files.attachments WHERE id=$1`, view.ID).Scan(&attempts, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || lifecycle != "pending" {
		t.Fatalf("reset: %d/%s", attempts, lifecycle)
	}
	enterpriseExec(t, e.integrationEnv, `UPDATE files.attachments SET preview_lifecycle_status='failed',preview_attempts=3,updated_at=now() WHERE id=$1`, view.ID)
	if err := store.RegenerateDocumentPreview(context.Background(), view.ID); err != domain.ErrPreviewUnavailable {
		t.Fatalf("repeated failures bypass cooldown: %v", err)
	}
	e.assertOriginal(t, view.ID, "image.png", enterpriseCorpus(t, "image.png"))
}
