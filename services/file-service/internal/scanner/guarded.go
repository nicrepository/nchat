package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

// ProvenScanner binds a completed scan to the immutable generation used.
type ProvenScanner interface {
	ScanWithEvidence(context.Context, io.Reader) (Verdict, signatures.Evidence, error)
}

var ErrGuardianUnavailable = errors.New("scanner: guardian unavailable")

type GuardedReply struct {
	Result   string              `json:"result"`
	Evidence signatures.Evidence `json:"evidence"`
}

type Guarded struct {
	endpoint string
	maxAge   time.Duration
	client   *http.Client
}

func NewGuarded(address string, timeout, maxAge time.Duration) (*Guarded, error) {
	if _, err := New(address, timeout); err != nil {
		return nil, err
	}
	if maxAge <= 0 {
		return nil, signatures.ErrUntrusted
	}
	return &Guarded{endpoint: "http://" + address + "/scan", maxAge: maxAge, client: &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 4096},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (g *Guarded) Scan(ctx context.Context, content io.Reader) (Verdict, error) {
	verdict, _, err := g.ScanWithEvidence(ctx, content)
	return verdict, err
}

func (g *Guarded) ScanWithEvidence(ctx context.Context, content io.Reader) (Verdict, signatures.Evidence, error) {
	started := time.Now().UTC()
	var reply GuardedReply
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, content)
	if err != nil {
		return VerdictInfected, reply.Evidence, ErrProtocol
	}
	response, err := g.client.Do(request)
	if err != nil {
		return VerdictInfected, reply.Evidence, fmt.Errorf("%w: %w", ErrGuardianUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if err = guardedStatusError(response.StatusCode); err != nil {
		return VerdictInfected, reply.Evidence, err
	}
	reply, err = decodeGuardedReply(response.Body)
	if err != nil {
		return VerdictInfected, reply.Evidence, err
	}
	if err = reply.Evidence.ValidCompletion(time.Now(), g.maxAge); err != nil {
		return VerdictInfected, reply.Evidence, err
	}
	if reply.Evidence.CompletedAt.Before(started) {
		return VerdictInfected, reply.Evidence, signatures.ErrUntrusted
	}
	if reply.Result == "infected" {
		return VerdictInfected, reply.Evidence, nil
	}
	if reply.Result != "clean" {
		return VerdictInfected, reply.Evidence, ErrProtocol
	}
	return VerdictClean, reply.Evidence, nil
}

func guardedStatusError(status int) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusRequestEntityTooLarge:
		return ErrStreamTooLarge
	case http.StatusGatewayTimeout:
		return context.DeadlineExceeded
	case http.StatusRequestTimeout:
		return context.Canceled
	case http.StatusServiceUnavailable:
		return ErrGuardianUnavailable
	default:
		return ErrProtocol
	}
}

// decodeGuardedReply requires the whole bounded HTTP message, including EOF.
func decodeGuardedReply(bodyReader io.Reader) (GuardedReply, error) {
	var reply GuardedReply
	body, err := io.ReadAll(io.LimitReader(bodyReader, 4097))
	if err != nil {
		return reply, fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	if len(body) > 4096 {
		return reply, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&reply); err != nil {
		return reply, ErrProtocol
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return reply, ErrProtocol
	}
	return reply, nil
}
