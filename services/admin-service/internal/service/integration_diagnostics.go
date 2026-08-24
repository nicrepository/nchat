package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The diagnostic adapters (issue #582).
//
// One adapter per integration, and no generic executor. That is the whole point
// of the shape: an endpoint that could run "a diagnostic" against "a target" is
// an endpoint whose blast radius nobody can review, so each function below
// knows exactly one protocol and reaches exactly one dependency, whose address
// came from this pod's environment.
//
// Every adapter obeys the same two output rules as the health probes of issue
// #581, because the console renders them side by side:
//
//   - nothing a dependency says comes back out. Bodies are drained or read
//     under a byte cap and reduced to known fields; errors are classified into
//     domain's closed category set and the original text is dropped;
//   - a stage that did not run is reported as skipped. It is never reported as
//     passing, and never as failing either — both would be a claim this build
//     did not make.

// stageRecorder accumulates one run's steps and times them.
//
// It exists so an adapter reads as a sequence of stages rather than as a
// sequence of append calls with timing arithmetic in between, and so the
// latency of every stage is measured the same way.
type stageRecorder struct {
	now   func() time.Time
	steps []domain.DiagnosticStep
	// version is an optional sanitized version string a stage learned.
	version string
}

func newStageRecorder(now func() time.Time) *stageRecorder {
	return &stageRecorder{now: now, steps: make([]domain.DiagnosticStep, 0, 8)}
}

// begin returns a function that records one stage with its measured duration.
//
// The closure captures the start instant, so a caller writes the stage once and
// cannot forget to time it or time the wrong span.
func (r *stageRecorder) begin(stage domain.DiagnosticStage) func(domain.DiagnosticStatus, domain.HealthErrorCategory, string) {
	started := r.now()
	return func(status domain.DiagnosticStatus, category domain.HealthErrorCategory, detail string) {
		latency := r.now().Sub(started).Milliseconds()
		r.steps = append(r.steps, domain.DiagnosticStep{
			Stage: stage, Status: status, Category: category, Detail: detail, LatencyMS: &latency,
		})
	}
}

// note records a stage with no measured duration.
//
// Absence of a duration is the honest answer for a stage nothing was attempted
// for: zero milliseconds reads as instantaneous, which is a measurement, and
// none was taken.
func (r *stageRecorder) note(
	stage domain.DiagnosticStage,
	status domain.DiagnosticStatus,
	category domain.HealthErrorCategory,
	detail string,
) {
	r.steps = append(r.steps, domain.DiagnosticStep{
		Stage: stage, Status: status, Category: category, Detail: detail,
	})
}

// skip records a stage that did not run, with the reason it did not.
func (r *stageRecorder) skip(stage domain.DiagnosticStage, detail string) {
	r.note(stage, domain.DiagnosticSkipped, domain.HealthErrorNone, detail)
}

// warn records a stage that did not run and whose absence is itself a finding.
//
// Distinct from skip because the two produce different overall verdicts:
// DeriveDiagnosticStatus ignores a skipped stage and lets a warning pull the
// whole run to DiagnosticWarning. A stage nobody attempted *because the
// deployment turned the protection off* must not leave the run reading as a
// clean pass.
//
// No category: the closed set of issue #581 describes ways a dependency failed,
// and nothing failed here. The detail carries the finding.
func (r *stageRecorder) warn(stage domain.DiagnosticStage, detail string) {
	r.note(stage, domain.DiagnosticWarning, domain.HealthErrorNone, detail)
}

// recorded reports whether this stage already has a result.
func (r *stageRecorder) recorded(stage domain.DiagnosticStage) bool {
	for _, step := range r.steps {
		if step.Stage == stage {
			return true
		}
	}
	return false
}

// skipRemaining fills in every stage the descriptor declared that never ran.
//
// Without it, a run that failed at the second stage would return two steps and
// the console would have to guess what happened to the rest. With it, the
// report always describes the whole declared plan, which is what makes "TLS:
// falhou, autenticação: não executada" possible to render at all.
func (r *stageRecorder) skipRemaining(stages []domain.DiagnosticStage, detail string) {
	for _, stage := range stages {
		if !r.recorded(stage) {
			r.skip(stage, detail)
		}
	}
}

// ordered returns the steps in the order the descriptor declared its stages.
//
// A run does not always record stages in plan order — a target reached over
// plain HTTP never performs the TLS handshake, so that stage is filled in
// afterwards — and a report whose rows move depending on how the run failed is
// a report nobody can read twice. Anything recorded that the plan did not
// declare keeps its position at the end rather than being dropped: hiding a
// stage that ran would be worse than showing one out of order.
func (r *stageRecorder) ordered(stages []domain.DiagnosticStage) []domain.DiagnosticStep {
	remaining := append([]domain.DiagnosticStep{}, r.steps...)
	sorted := make([]domain.DiagnosticStep, 0, len(remaining))
	for _, stage := range stages {
		for index, step := range remaining {
			if step.Stage == stage {
				sorted = append(sorted, step)
				remaining = append(remaining[:index], remaining[index+1:]...)
				break
			}
		}
	}
	return append(sorted, remaining...)
}

// notExecuted is the sentence a stage carries when an earlier one failed.
const notExecuted = "Não executada porque uma etapa anterior falhou."

// dialStages performs the resolve, connect and TLS stages of one diagnostic.
//
// It is shared by every adapter, HTTP or not, so "DNS: OK, TCP: OK, TLS: falha"
// means the same thing on every card. The TLS stage is only recorded for a
// target that is encrypted from the first byte; STARTTLS records it later, from
// inside the SMTP session, because that is when the handshake actually happens.
func dialStages(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	target diagnosticTarget,
) (net.Conn, bool) {
	if !resolveStage(ctx, recorder, policy, target) {
		return nil, false
	}
	conn, ok := connectStage(ctx, recorder, policy, target)
	if !ok {
		return nil, false
	}
	if !target.secure {
		return conn, true
	}
	return tlsStage(ctx, recorder, conn, target)
}

// resolveStage answers DNS, and answers it against the policy.
//
// The resolution here is for the operator's benefit — it is what turns "não
// conectou" into "o nome não resolve" — and not the security control: the
// address the connection actually uses is checked again in the dialer's
// Control, which is the check with no window between it and connect(2).
func resolveStage(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	target diagnosticTarget,
) bool {
	done := recorder.begin(domain.StageResolve)
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, target.host)
	if err != nil {
		done(domain.DiagnosticFailed, domain.HealthErrorInvalidConfiguration,
			"O nome configurado para esta integração não resolve neste cluster.")
		return false
	}
	for _, address := range addresses {
		if allowedAddress(policy, address.IP) {
			done(domain.DiagnosticPassed, domain.HealthErrorNone, "O nome resolve para um endereço utilizável.")
			return true
		}
	}
	done(domain.DiagnosticFailed, domain.HealthErrorInvalidConfiguration,
		"O nome resolve apenas para faixas que a plataforma não contata (link-local ou metadata de nuvem).")
	return false
}

func connectStage(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	target diagnosticTarget,
) (net.Conn, bool) {
	done := recorder.begin(domain.StageConnect)
	conn, err := newGuardedDialer(policy).DialContext(ctx, "tcp", target.address())
	if err != nil {
		category, detail := classifyDialError(err)
		done(domain.DiagnosticFailed, category, detail)
		return nil, false
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "A dependência aceitou a conexão.")
	return conn, true
}

// tlsStage wraps an established connection in TLS, with verification on.
//
// Nothing here can be told to skip verification: there is no InsecureSkipVerify
// in this package and no configuration that reaches this function. A
// certificate the pod does not trust fails the stage, which is the honest
// answer and the one an operator can fix.
func tlsStage(
	ctx context.Context,
	recorder *stageRecorder,
	conn net.Conn,
	target diagnosticTarget,
) (net.Conn, bool) {
	done := recorder.begin(domain.StageTLS)
	secure := tls.Client(conn, &tls.Config{ServerName: target.host, MinVersion: tls.VersionTLS12})
	if err := secure.HandshakeContext(ctx); err != nil {
		category, detail := classifyDialError(err)
		if category == domain.HealthErrorDependencyUnavailable {
			category, detail = domain.HealthErrorTLSError, "Não foi possível estabelecer TLS com a dependência."
		}
		done(domain.DiagnosticFailed, category, detail)
		_ = conn.Close()
		return nil, false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "TLS estabelecido e certificado validado.")
	return secure, true
}

// runOIDCDiagnostic checks the identity provider end to end, short of a login.
//
// It never performs a user authentication: a diagnostic that signed somebody in
// would need a real credential and would put a login event in the identity
// provider's audit trail every time an operator pressed a button.
//
// The security-relevant step is jwks. That URL comes from the provider's own
// response rather than from this deployment's configuration, so following it
// blindly would let whatever answers the issuer nominate the next address this
// pod connects to. It is required to share the issuer's scheme and host, and no
// second request is made when it does not.
func runOIDCDiagnostic(ctx context.Context, recorder *stageRecorder, policy domain.IntegrationNetworkPolicy, issuer string) {
	target, err := parseIntegrationURL(policy, issuer)
	if err != nil {
		recorder.skip(domain.StageResolve, "O issuer configurado não é uma URL http(s) utilizável.")
		return
	}
	conn, ok := dialStages(ctx, recorder, policy, target)
	if !ok {
		return
	}
	_ = conn.Close()

	client := newDiagnosticHTTPClient(policy)
	base := strings.TrimSuffix(target.endpoint, "/")
	discovery, ok := oidcDiscoveryStage(ctx, recorder, client, base)
	if !ok {
		return
	}
	if !oidcIssuerStage(recorder, discovery, base) {
		return
	}
	oidcJWKSStage(ctx, recorder, client, policy, discovery, target)
	recorder.skip(domain.StageCredential,
		"O provedor não expõe forma de validar o client sem executar uma autenticação real, "+
			"que este diagnóstico deliberadamente não faz.")
}

func oidcDiscoveryStage(
	ctx context.Context,
	recorder *stageRecorder,
	client *http.Client,
	base string,
) (oidcDiscovery, bool) {
	done := recorder.begin(domain.StageDiscovery)
	response, err := diagnosticGet(ctx, client, base+"/.well-known/openid-configuration")
	if err != nil {
		category, detail := classifyDialError(err)
		done(domain.DiagnosticFailed, category, detail)
		return oidcDiscovery{}, false
	}
	defer drainAndClose(response)
	if response.StatusCode != http.StatusOK {
		done(domain.DiagnosticFailed, domain.HealthErrorDependencyUnavailable,
			"O provedor respondeu, mas não entregou o documento de discovery.")
		return oidcDiscovery{}, false
	}
	var discovery oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(response.Body, maxDiagnosticBodyBytes)).Decode(&discovery); err != nil {
		done(domain.DiagnosticFailed, domain.HealthErrorProtocolError,
			"O documento de discovery não pôde ser interpretado.")
		return oidcDiscovery{}, false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "Documento de discovery obtido.")
	return discovery, true
}

func oidcIssuerStage(recorder *stageRecorder, discovery oidcDiscovery, base string) bool {
	done := recorder.begin(domain.StageIssuer)
	if strings.TrimSuffix(discovery.Issuer, "/") != base {
		done(domain.DiagnosticFailed, domain.HealthErrorInvalidConfiguration,
			"O provedor declara um issuer diferente do configurado; tokens emitidos por ele serão recusados.")
		recorder.skip(domain.StageJWKS, notExecuted)
		recorder.skip(domain.StageCredential, notExecuted)
		return false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "O issuer declarado é idêntico ao configurado.")
	return true
}

func oidcJWKSStage(
	ctx context.Context,
	recorder *stageRecorder,
	client *http.Client,
	policy domain.IntegrationNetworkPolicy,
	discovery oidcDiscovery,
	issuer diagnosticTarget,
) {
	done := recorder.begin(domain.StageJWKS)
	jwks, err := parseIntegrationURL(policy, discovery.JWKSURI)
	// Scheme, host *and* port. Two services on one host are two origins, and a
	// provider that could nominate a different port on the same name would be
	// choosing an address this deployment never configured — which is the whole
	// thing this check exists to refuse.
	if err != nil || jwks.scheme != issuer.scheme || jwks.host != issuer.host || jwks.port != issuer.port {
		done(domain.DiagnosticFailed, domain.HealthErrorInvalidConfiguration,
			"O provedor apontou o conjunto de chaves para fora da própria origem; o diagnóstico não segue esse endereço.")
		return
	}
	response, err := diagnosticGet(ctx, client, jwks.endpoint)
	if err != nil {
		category, detail := classifyDialError(err)
		done(domain.DiagnosticFailed, category, detail)
		return
	}
	defer drainAndClose(response)
	if response.StatusCode != http.StatusOK {
		done(domain.DiagnosticFailed, domain.HealthErrorDependencyUnavailable,
			"O conjunto de chaves não foi entregue; a validação de tokens vai falhar.")
		return
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "Conjunto de chaves carregado.")
}

// runStorageDiagnostic checks the attachment store's filer.
//
// Capacity is deliberately absent. SeaweedFS exposes volume statistics only on
// the master, which this pod does not receive, and reporting a number derived
// from anything else would be inventing a fact an operator would then plan
// around.
func runStorageDiagnostic(ctx context.Context, recorder *stageRecorder, policy domain.IntegrationNetworkPolicy, endpoint string) {
	target, err := parseIntegrationURL(policy, endpoint)
	if err != nil {
		recorder.skip(domain.StageResolve, "O endpoint configurado não é uma URL http(s) utilizável.")
		return
	}
	conn, ok := dialStages(ctx, recorder, policy, target)
	if !ok {
		return
	}
	_ = conn.Close()

	done := recorder.begin(domain.StageReady)
	response, err := diagnosticGet(ctx, newDiagnosticHTTPClient(policy), target.endpoint)
	if err != nil {
		category, detail := classifyDialError(err)
		done(domain.DiagnosticFailed, category, detail)
		return
	}
	defer drainAndClose(response)
	status, category, detail := readinessFromStatus(response.StatusCode)
	done(status, category, detail)
}

// readinessFromStatus judges an HTTP answer without relaying it.
//
// A redirect is a warning rather than a pass: the client deliberately does not
// follow it, so reachability is confirmed and nothing else, and calling that
// healthy would be claiming a check that did not happen.
func readinessFromStatus(status int) (domain.DiagnosticStatus, domain.HealthErrorCategory, string) {
	switch {
	case status >= 500:
		return domain.DiagnosticFailed, domain.HealthErrorDependencyUnavailable,
			"A dependência respondeu com erro interno."
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return domain.DiagnosticFailed, domain.HealthErrorAuthenticationFailed,
			"A dependência respondeu, mas recusou a credencial deste serviço."
	case status >= 300 && status < 400:
		return domain.DiagnosticWarning, domain.HealthErrorProtocolError,
			"A dependência respondeu com um redirecionamento, que o diagnóstico não segue."
	case status >= 400:
		return domain.DiagnosticWarning, domain.HealthErrorProtocolError,
			"A dependência respondeu, mas não da forma esperada para esta verificação."
	default:
		return domain.DiagnosticPassed, domain.HealthErrorNone, "A dependência respondeu e está pronta."
	}
}

// runClamAVDiagnostic pings the antimalware daemon and reads its version.
//
// It sends PING and VERSION and nothing else. In particular it never sends an
// EICAR sample: writing a known-malicious pattern to the daemon would put a
// signature hit in the deployment's security log every time an operator pressed
// a button, and teaching operators to ignore that log line is worse than not
// having the test.
func runClamAVDiagnostic(ctx context.Context, recorder *stageRecorder, policy domain.IntegrationNetworkPolicy, address string) {
	target, err := parseIntegrationAddress(address, false)
	if err != nil {
		recorder.skip(domain.StageResolve, "O endereço configurado não está no formato host:porta.")
		return
	}
	conn, ok := dialStages(ctx, recorder, policy, target)
	if !ok {
		return
	}
	defer func() { _ = conn.Close() }()

	done := recorder.begin(domain.StageReady)
	reader := boundedReader(conn)
	reply, err := clamdCommand(conn, reader, "zPING\x00")
	if err != nil {
		category, detail := classifyDialError(err)
		done(domain.DiagnosticFailed, category, detail)
		return
	}
	if !strings.HasPrefix(reply, "PONG") {
		done(domain.DiagnosticFailed, domain.HealthErrorProtocolError,
			"O antimalware respondeu algo que este diagnóstico não interpreta.")
		return
	}
	if version, versionErr := clamdCommand(conn, reader, "zVERSION\x00"); versionErr == nil {
		recorder.version = sanitizeVersion(version)
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "O antimalware respondeu ao ping e está aceitando verificações.")
}

// boundedReader caps how much a dependency may make this pod read from a raw
// socket. A daemon that answers with megabytes is broken, and finding that out
// must not cost memory.
func boundedReader(conn net.Conn) *bufio.Reader {
	return bufio.NewReader(io.LimitReader(conn, maxDiagnosticBodyBytes))
}

// diagnosticGet issues one bounded GET.
func diagnosticGet(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}
