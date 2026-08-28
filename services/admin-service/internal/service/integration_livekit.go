package service

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The LiveKit diagnostic (issue #582).
//
// It proves the two things an operator needs to know and nothing else: the
// media server is reachable, and the API credential this deployment holds is
// the one it accepts. It does that by listing rooms, which is the cheapest
// authenticated call LiveKit has.
//
// What it deliberately does not do is create anything. An earlier shape of this
// check would open a diagnostic room to prove a participant can join; that
// leaves an orphan room behind whenever the request is cancelled at the wrong
// moment, and it proves nothing that listing does not. There is no room to
// clean up here because none is created.

// diagnosticTokenTTL is how long the diagnostic credential is valid.
//
// Thirty seconds: long enough for one request against a slow server, short
// enough that a token captured from a log that should not have it — and it is
// never logged — is expired before it can be replayed. It is not the
// deployment's configured token TTL, which governs real call tokens and is
// minutes long.
const diagnosticTokenTTL = 30 * time.Second

// liveKitGrant is the minimum authority the diagnostic needs.
//
// roomList and nothing else: no roomCreate, no roomJoin, no roomAdmin, no
// canPublish. A token that leaked would be able to enumerate room names for
// thirty seconds, which is the smallest possible blast radius for a check that
// has to authenticate at all.
type liveKitGrant struct {
	RoomList bool `json:"roomList"`
}

type liveKitClaims struct {
	Video liveKitGrant `json:"video"`
	jwt.RegisteredClaims
}

// signLiveKitDiagnosticToken mints the short-lived credential.
//
// The returned token is handed straight to the HTTP request and never stored,
// never returned to a caller and never written to a log or an audit row.
func signLiveKitDiagnosticToken(apiKey, apiSecret string, now time.Time) (string, error) {
	claims := liveKitClaims{
		Video: liveKitGrant{RoomList: true},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    apiKey,
			Subject:   apiKey,
			NotBefore: jwt.NewNumericDate(now.Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(diagnosticTokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(apiSecret))
}

// liveKitCredentials is what the environment says about the media server.
type liveKitCredentials struct {
	endpoint  string
	apiKey    string
	apiSecret string
}

// runLiveKitDiagnostic checks reachability and the API credential.
func runLiveKitDiagnostic(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	credentials liveKitCredentials,
	now time.Time,
) {
	target, err := parseIntegrationURL(policy, credentials.endpoint)
	if err != nil {
		recorder.skip(domain.StageResolve, "A URL configurada do LiveKit não é utilizável para uma verificação http(s).")
		return
	}
	conn, ok := dialStages(ctx, recorder, policy, target)
	if !ok {
		return
	}
	_ = conn.Close()

	token, ok := liveKitCredentialToken(recorder, credentials, now)
	if !ok {
		return
	}
	liveKitAPIStages(ctx, recorder, policy, target, token)
}

// liveKitCredentialToken mints the diagnostic token, refusing early when the
// deployment has no credential to mint it with.
func liveKitCredentialToken(recorder *stageRecorder, credentials liveKitCredentials, now time.Time) (string, bool) {
	if credentials.apiKey == "" || credentials.apiSecret == "" {
		recorder.skip(domain.StageCredential,
			"Este serviço não observa a chave de API do LiveKit, então a credencial não pôde ser verificada.")
		recorder.skip(domain.StageReady, notExecuted)
		return "", false
	}
	token, err := signLiveKitDiagnosticToken(credentials.apiKey, credentials.apiSecret, now)
	if err != nil {
		done := recorder.begin(domain.StageCredential)
		done(domain.DiagnosticFailed, domain.HealthErrorInvalidConfiguration,
			"A credencial configurada não permite assinar um token de diagnóstico.")
		recorder.skip(domain.StageReady, notExecuted)
		return "", false
	}
	return token, true
}

// liveKitAPIStages performs the one authenticated call and reads two facts from
// it: whether the credential was accepted, and whether the server is serving.
func liveKitAPIStages(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	target diagnosticTarget,
	token string,
) {
	credential := recorder.begin(domain.StageCredential)
	response, err := liveKitListRooms(ctx, newDiagnosticHTTPClient(policy), target.endpoint, token)
	if err != nil {
		category, detail := classifyDialError(err)
		credential(domain.DiagnosticFailed, category, detail)
		recorder.skip(domain.StageReady, notExecuted)
		return
	}
	defer drainAndClose(response)

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		credential(domain.DiagnosticFailed, domain.HealthErrorAuthenticationFailed,
			"O LiveKit respondeu, mas recusou a credencial de API deste deployment.")
		recorder.skip(domain.StageReady, notExecuted)
		return
	}
	credential(domain.DiagnosticPassed, domain.HealthErrorNone,
		"O LiveKit aceitou um token de diagnóstico assinado com a credencial configurada.")

	ready := recorder.begin(domain.StageReady)
	status, category, detail := readinessFromStatus(response.StatusCode)
	ready(status, category, detail)
}

// liveKitListRooms issues the Twirp call.
//
// The response body is drained and discarded by the caller: the diagnostic
// answers "did it accept the credential", and the names of the rooms currently
// in progress are not information the Admin API relays to a browser.
func liveKitListRooms(ctx context.Context, client *http.Client, endpoint, token string) (*http.Response, error) {
	url := trimTrailingSlash(endpoint) + "/twirp/livekit.RoomService/ListRooms"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	return client.Do(request)
}

func trimTrailingSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}
