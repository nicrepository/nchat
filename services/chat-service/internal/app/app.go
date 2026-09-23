package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/linkfetch"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// defaultPresenceAwayTimeout is the duration of connection inactivity after
// which a user is considered away. Not configurable yet; adjust here if needed.
const defaultPresenceAwayTimeout = 5 * time.Minute

// dbBootstrapTimeout bounds the total retry window for the initial database
// connection. Keep it below the Kubernetes startupProbe budget (60s) so a
// failed bootstrap exits and the container is restarted before the kubelet
// intervenes.
const dbBootstrapTimeout = 30 * time.Second

// openDBWithRetry is swappable in tests so app bootstrap failure paths run
// without real network access or real sleeps.
var openDBWithRetry = storage.OpenDBWithRetry

// App is the fully assembled chat-service application.
//
// Lifecycle ownership:
//   - hub:     owned by App; shut down via Shutdown.
//   - presence: owned by App; stopped via Shutdown after hub exits.
//   - tracing: shut down via Shutdown after presence stops.
//
// Call Shutdown to release all resources cleanly.
type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc

	hub               *ws.Hub
	presence          *ws.PresenceTracker
	presenceDirectory *ws.ValkeyPresenceDirectory
	mentionCache      *storage.ValkeyMentionLabelCache
	reactionLimiter   *ws.ValkeyReactionLimiter
	typingLimiter     *ws.ValkeyReactionLimiter
	typingStore       *ws.ValkeyTypingStore
	callWorkerCancel  context.CancelFunc
	callWorkerWG      *sync.WaitGroup
	linkScanCancel    context.CancelFunc
	linkScanWG        *sync.WaitGroup
	closeDB           func()
	shutdownOnce      sync.Once
}

// Shutdown stops the WebSocket hub, presence tracker, and tracing exporter in
// the correct order. Safe to call multiple times — subsequent calls are no-ops.
//
// Shutdown order:
//  1. hub.Shutdown() — drains and closes all WebSocket connections.
//  2. presence.Stop() — stops the background away-check goroutine, then the
//     shared presence directory the hub was writing to.
//  3. closeDB() — closes the PostgreSQL pool after in-flight queries drain.
//  4. TracingShutdown(ctx) — flushes and closes the tracing exporter.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	a.shutdownOnce.Do(func() {
		err = a.shutdownComponents(ctx)
	})
	return err
}

// shutdownComponents stops everything in dependency order, bounded by ctx.
//
// Every wait here is for something that has just been cancelled, so the normal
// path is immediate. The deadline matters for the path that is not normal: a
// worker that will not return, or a hub goroutine wedged on a slow bus, used to
// hold the process open indefinitely while the kubelet's grace period ran out.
func (a *App) shutdownComponents(ctx context.Context) error {
	workerErr := a.stopWorkers(ctx)
	// Before the hub: the link-scan worker publishes through it, so stopping it
	// first is what keeps a promotion from racing the hub's shutdown.
	hubErr := a.hub.ShutdownContext(ctx)
	a.presence.Stop()
	a.closeResources()
	return firstShutdownError(workerErr, hubErr, a.TracingShutdown(ctx))
}

func (a *App) stopWorkers(ctx context.Context) error {
	var callErr, linkErr error
	if a.callWorkerCancel != nil {
		a.callWorkerCancel()
		callErr = awaitWaitGroup(ctx, a.callWorkerWG)
	}
	if a.linkScanCancel != nil {
		a.linkScanCancel()
		linkErr = awaitWaitGroup(ctx, a.linkScanWG)
	}
	return firstShutdownError(callErr, linkErr)
}

// closeResources releases what the hub was using, in the order that keeps the
// database open until nothing can still query it.
func (a *App) closeResources() {
	// After the hub, which is the only writer to it.
	if a.presenceDirectory != nil {
		a.presenceDirectory.Close()
	}
	if a.mentionCache != nil {
		a.mentionCache.Close()
	}
	if a.reactionLimiter != nil {
		a.reactionLimiter.Close()
	}
	if a.typingLimiter != nil {
		a.typingLimiter.Close()
	}
	if a.typingStore != nil {
		a.typingStore.Close()
	}
	// Close the DB pool only after the hub has drained connections that may
	// still be issuing queries.
	if a.closeDB != nil {
		a.closeDB()
	}
}

// awaitWaitGroup waits, but not past the deadline.
//
// sync.WaitGroup has no context-aware Wait. The goroutine below outlives a
// timeout, which is acceptable precisely because the workers it waits on have
// already been cancelled: it ends when they do, rather than never.
func awaitWaitGroup(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func firstShutdownError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// New assembles the application. Bootstrap outcomes by state:
//
//   - DATABASE_URL configured but unreachable: retry with backoff, then
//     fail fast — New returns an error, the process exits non-zero and
//     Kubernetes restarts the container.
//   - DATABASE_URL absent: configuration choice, not a transient failure —
//     the process stays alive and /readyz reports 503.
//   - Invalid JWT config: the process stays alive and /readyz reports 503.
//
// In every degraded state the pod never becomes Ready, so the Service sends
// it no traffic.
func New(cfg config.Config) (*App, error) {
	// Before anything is built: a configuration that cannot be honoured must not
	// become a running service. Nothing is logged here — the error reaches the
	// caller, and it names the variable and never its value.
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	b := newBootstrap(cfg)
	if err := b.openDatabase(); err != nil {
		return nil, err
	}
	b.buildPresence()
	b.buildBus()
	b.buildLimiters()
	b.buildMessageHandler()
	b.buildConversationHandlers()
	b.buildHub()
	b.startCallWorker()
	b.startLinkWorkers()
	b.attachMessageBroadcasters()
	b.attachConversationBroadcasters()
	return b.app(), nil
}

// wireLinkSafety attaches the RF-21 Safe Browsing gate to message creation,
// editing and forwarding.
//
// It returns an error rather than logging one, and that is the whole point of
// the function's shape. A nil checker is read downstream as "the feature is
// off", so leaving it nil while the flag is on would mean the service runs
// believing links are checked while every link is accepted — the exact bypass
// the flag exists to prevent. Config.Validate already refuses missing
// credentials, and this is the second lock on the same door: if the constructor
// ever grows a new failure mode, it stops the process instead of quietly
// reopening that bypass.
//
// This is start-up only. A provider that becomes unreachable *later* is a
// different thing entirely: the queue exists, the worker keeps retrying, and the
// messages waiting on it stay withheld rather than being released.
//
// Two things are wired, and they are deliberately different objects. The gate the
// send path consults is the *store* — one indexed read of chat.link_scans, no
// network, so an interactive request can never wait on Cloudflare. The provider
// client goes to the worker instead, which is the only thing allowed to submit
// and poll.
//
// linkSafetyStore is what the bootstrap wires for links: the verdicts the send
// path reads, the queues the workers drain, and — since issue #807 — the
// per-link read model and the preview store. Narrow because it is also what a
// test has to provide.
type linkSafetyStore interface {
	service.URLSafetyChecker
	service.LinkScanQueue
	service.LinkReconcileQueue
	service.LinkEntityStore
	service.LinkTargetIndex
	service.LinkPreviewQueue
}

// linkSafetyWiring is the set of workers the link pipeline runs.
//
// Scan and Reconcile are deliberately separate objects with separate provider
// interfaces: the scan worker may submit, the reconcile worker may not. Since
// issue #807 Scan is always present — with the flag off it only sweeps, so
// nothing is ever stranded — and Preview drains the preview queue, fetching only
// when its own flag is on. Announcer is the convergence they share.
type linkSafetyWiring struct {
	Scan      *service.LinkScanService
	Reconcile *service.LinkReconcileService
	Preview   *service.LinkPreviewService
	Announcer *service.LinkTargetAnnouncer
}

func wireLinkSafety(
	cfg config.Config, messageSvc *service.MessageService,
	store linkSafetyStore, publisher service.MessageEventPublisher,
	metrics *observability.Metrics, logger *slog.Logger,
) (linkSafetyWiring, error) {
	if messageSvc == nil || store == nil {
		return linkSafetyWiring{}, errLinkSafetyUnwired
	}
	_ = publisher // attached after the hub exists; see SetPublisher below.
	// Link entities exist whether or not a provider does (issue #807): the
	// backend is the authority for what in a body is a link, and a deployment
	// with safety off still records targets — as unknown/disabled — so the client
	// draws interstitials rather than deciding for itself.
	messageSvc.SetLinkSafety(store)
	messageSvc.SetLinkEntities(store)
	messageSvc.SetLinkPreviewEnabled(cfg.LinkPreviewEnabled)
	pipeline := urlsafety.NewPipelineMetrics(metrics, cfg.ServiceName)
	messageSvc.SetAdmissionMetrics(pipeline)

	announcer := service.NewLinkTargetAnnouncer(store, nil, nil, logger)
	announcer.SetPreviewEnabled(cfg.LinkPreviewEnabled)

	safety, err := wireReputationProvider(cfg, metrics)
	if err != nil {
		return linkSafetyWiring{}, err
	}
	worker, reconcile := wireScanWorkers(cfg, messageSvc, store, safety, pipeline, announcer, logger)
	preview := wirePreviewWorker(cfg, store, pipeline, announcer, logger)
	return linkSafetyWiring{Scan: worker, Reconcile: reconcile, Preview: preview, Announcer: announcer}, nil
}

// wireReputationProvider builds the provider behind the abstraction, or nil
// with the flag off. Config.Validate already refuses missing credentials; this
// is the second lock on the same door.
//
// Since issue #928 the provider is a composition: Google Web Risk answers first
// and Cloudflare URL Scanner answers when it cannot. Both clients are built
// before either is used, so a deployment with a bad credential fails at
// start-up rather than at the first link somebody sends — the same rule the
// single-provider wiring had, applied to both halves.
func wireReputationProvider(cfg config.Config, metrics *observability.Metrics) (*urlsafety.Service, error) {
	if !cfg.LinkSafetyEnabled {
		return nil, nil
	}
	// Every constructor error below is flattened into one fixed value. Their own
	// messages name no credential, but a message that varies with which
	// credential was missing is itself a fact about the secrets, so nothing
	// about them can reach a log through this return.
	primary, err := urlsafety.NewWebRiskProvider(cfg.LinkSafetyGoogleWebRiskKey)
	if err != nil {
		return nil, errLinkSafetyUnwired
	}
	scanner, err := urlsafety.NewCloudflareScanner(
		cfg.LinkSafetyCloudflareAccount, cfg.LinkSafetyCloudflareToken,
	)
	if err != nil {
		return nil, errLinkSafetyUnwired
	}
	// The shared counters, registered on this service's own registry so
	// chat-service reports verdict outcomes exactly as file-service does. Their
	// labels are the closed sets the shared package defines; no URL, host, user,
	// message id or credential is ever one. The circuit breakers live inside this
	// service: one in front of the composition, one in front of the primary.
	return urlsafety.NewFallbackService(primary, scanner, urlsafety.NewMetrics(metrics)), nil
}

// wireScanWorkers builds the scan worker — always, so the deadline sweep runs —
// and the reconcile worker when a provider exists.
func wireScanWorkers(
	cfg config.Config, messageSvc *service.MessageService, store linkSafetyStore,
	safety *urlsafety.Service, pipeline *urlsafety.PipelineMetrics,
	announcer *service.LinkTargetAnnouncer, logger *slog.Logger,
) (*service.LinkScanService, *service.LinkReconcileService) {
	var provider service.LinkScanProvider
	if safety != nil {
		provider = safety
		// What a workspace, and this deployment, may spend on new provider work.
		// Applied at admission, before any job is queued, so a refusal costs the
		// provider nothing. With the flag off nothing is spent, so nothing is
		// capped.
		messageSvc.SetLinkScanCapacity(linkScanCapacity(cfg))
	}
	worker := service.NewLinkScanService(store, provider, nil, logger)
	worker.SetMetrics(pipeline)
	worker.SetAnnouncer(announcer)
	worker.SetHostResolver(linkfetch.LookupAddrs)
	worker.SetSafetyEnabled(cfg.LinkSafetyEnabled)
	worker.SetCapacity(service.LinkScanWorkerCapacity{
		ProviderSubmitLimit:  cfg.LinkSafetyProviderSubmitLimit,
		ProviderSubmitWindow: time.Duration(cfg.LinkSafetyProviderSubmitWindowSeconds) * time.Second,
		UncertainTimeout:     time.Duration(cfg.LinkSafetySubmitUncertainTimeoutSeconds) * time.Second,
	})
	if safety == nil {
		return worker, nil
	}
	// The recovery half (issue #135). It shares the provider client, and therefore
	// the same strict verdict rules, breaker and cache, but it is handed to a
	// narrower interface: LinkVerdictReconciler has exactly one method and no way
	// to submit.
	reconcile := service.NewLinkReconcileService(store, safety, logger)
	reconcile.SetMetrics(pipeline)
	reconcile.SetAnnouncer(announcer)
	return worker, reconcile
}

// wirePreviewWorker builds the preview worker. With the flag off it has no
// fetcher and only drains, so switching the flag off leaves no row waiting.
func wirePreviewWorker(
	cfg config.Config, store linkSafetyStore, pipeline *urlsafety.PipelineMetrics,
	announcer *service.LinkTargetAnnouncer, logger *slog.Logger,
) *service.LinkPreviewService {
	var fetcher service.LinkPreviewFetcher
	if cfg.LinkPreviewEnabled {
		fetcher = linkfetch.NewFetcher(service.LinkPreviewFetchTimeout)
	}
	preview := service.NewLinkPreviewService(store, fetcher, logger)
	preview.SetMetrics(pipeline)
	preview.SetAnnouncer(announcer)
	preview.SetEnabled(cfg.LinkPreviewEnabled)
	if cfg.LinkSafetyEnabled {
		preview.SetScanCapacity(linkScanCapacity(cfg))
	}
	return preview
}

func linkScanCapacity(cfg config.Config) storage.LinkScanCapacity {
	return storage.LinkScanCapacity{
		WorkspaceNewURLBudget: cfg.LinkSafetyWorkspaceBudget,
		BudgetWindow:          time.Duration(cfg.LinkSafetyBudgetWindowSeconds) * time.Second,
		MaxPendingJobs:        cfg.LinkSafetyMaxPendingJobs,
	}
}

// errLinkSafetyUnwired stops the bootstrap when RF-21 is switched on and the
// gate could not be installed. It carries no configuration value.
var errLinkSafetyUnwired = errors.New(
	"link safety is enabled but the checker could not be built; refusing to start unprotected",
)

// wireMentionLabelCache creates the Valkey-backed mention label cache and
// attaches it to messageSvc, using the configured TTL. Returns the cache
// (nil when disabled or the connection failed) so the caller can track it
// for Shutdown. A connection failure is logged and non-fatal: the mention
// label cache is simply skipped and messageSvc falls back to resolving
// labels directly from storage on every read.
func wireMentionLabelCache(valkeyURL string, ttlSeconds int, messageSvc *service.MessageService, logger *slog.Logger) *storage.ValkeyMentionLabelCache {
	if valkeyURL == "" {
		return nil
	}
	cache, err := storage.NewValkeyMentionLabelCache(valkeyURL)
	if err != nil {
		logger.Warn("mention label cache disabled", "reason", "invalid_valkey_config")
		return nil
	}
	messageSvc.SetMentionLabelCache(cache)
	messageSvc.SetMentionLabelCacheTTL(time.Duration(ttlSeconds) * time.Second)
	return cache
}

// wsHandlerConfig builds the WebSocket resource controls, and hands the socket
// the same session authority the HTTP routes are guarded with so a live
// connection can be re-checked against it. When no session store is configured
// the field stays nil and connections keep upgrade-time validation only, which
// is the same degradation the HTTP routes already have.
func wsHandlerConfig(cfg config.Config, sessions storage.SessionValidator, displayNames ws.UserDisplayNameResolver) ws.HandlerConfig {
	handlerCfg := ws.HandlerConfig{
		MaxConnectionsPerUser:    cfg.WSMaxConnectionsPerUser,
		InboundMessagesPerMinute: cfg.WSInboundMessagesPerMinute,
		InboundBurst:             cfg.WSInboundBurst,
		MaxInvalidMessages:       cfg.WSMaxInvalidMessages,
		SessionIDFromContext:     httpapi.GetContextSessionID,
	}
	if sessions != nil {
		handlerCfg.Sessions = sessions
	}
	if displayNames != nil {
		handlerCfg.DisplayNames = displayNames
	}
	return handlerCfg
}

// appWSWorkspaceResolver adapts storage.PGXWorkspaceStore to ws.WorkspaceResolver
// and to the canonical workspace resolver the RF-19 anti-spam guard consumes.
// The workspace ID is always resolved server-side; client-provided IDs are never accepted.
type appWSWorkspaceResolver struct {
	store interface {
		GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error)
	}
}

func (r *appWSWorkspaceResolver) GetDefaultWorkspaceID(ctx context.Context) (string, error) {
	workspace, err := r.store.GetDefaultWorkspace(ctx)
	if err != nil {
		return "", err
	}
	return workspace.ID, nil
}

// ResolveWorkspaceID is the single server-side answer to "which workspace does
// this authenticated request belong to", shared by the WebSocket session bind
// and by the anti-spam guard so the two cannot disagree.
//
// In this MVP the chat surface is one workspace: no chat route carries a
// workspace segment, and every handler (messages, DMs, channels, categories,
// sidebar) resolves it the same way. That makes this resolution canonical, not
// a fallback — when workspace-scoped routing arrives, only this method changes
// and the guard, its cache and its counter keys follow automatically.
func (r *appWSWorkspaceResolver) ResolveWorkspaceID(ctx context.Context) (string, error) {
	return r.GetDefaultWorkspaceID(ctx)
}

// presenceReporter adapts ws.PresenceTracker to the lookup the HTTP layer
// declares, so httpapi never imports the ws package. A nil tracker answers no
// online users, and the details payload then carries an empty online preview
// rather than members whose presence nothing vouches for.
type presenceReporter struct{ tracker *ws.PresenceTracker }

func (p presenceReporter) OnlineUserIDs(workspaceID string) []string {
	if p.tracker == nil {
		return nil
	}
	return p.tracker.OnlineUserIDs(workspaceID)
}

// hubBroadcaster adapts ws.Hub to service.MessageEventPublisher.
// It converts the string targetType to ws.TargetType and domain.Message to
// ws.MessagePayload, keeping the service package free of a direct ws import.
type hubBroadcaster struct{ hub *ws.Hub }

// PublishCall adapts the hub for service.CallEventPublisher (issue #835
// realtime follow-up's CallEventPublisher now needs both this and
// PublishConversationEvent below, and hub.PublishCall already matches this
// signature exactly — no conversion needed, unlike the ws.TargetType one).
func (b *hubBroadcaster) PublishCall(ctx context.Context, call domain.Call) {
	b.hub.PublishCall(ctx, call)
}

type reactionHandlerAdapter struct{ service *service.ReactionService }

func (a *reactionHandlerAdapter) ToggleReaction(ctx context.Context, workspaceID, userID, messageID, emoji string) (ws.ReactionUpdate, error) {
	result, err := a.service.ToggleReaction(ctx, service.ToggleReactionInput{
		WorkspaceID: workspaceID, UserID: userID, MessageID: messageID, Emoji: emoji,
	})
	if err != nil {
		return ws.ReactionUpdate{}, err
	}
	targetType, targetID := ws.TargetTypeChannel, result.ChannelID
	if result.DMID != "" {
		targetType, targetID = ws.TargetTypeDM, result.DMID
	}
	reactions := make([]ws.ReactionPayload, len(result.Reactions))
	for i, reaction := range result.Reactions {
		reactions[i] = ws.ReactionPayload{
			Emoji: reaction.Emoji, Count: reaction.Count, Users: reactionUserPayloads(reaction.Users),
		}
	}
	return ws.ReactionUpdate{
		MessageID: result.MessageID, TargetType: targetType, TargetID: targetID,
		Added: result.Added, Reactions: reactions,
	}, nil
}

func reactionUserPayloads(users []domain.ReactionUser) []ws.ReactionUserPayload {
	payloads := make([]ws.ReactionUserPayload, len(users))
	for i, user := range users {
		payloads[i] = ws.ReactionUserPayload{UserID: user.UserID, DisplayName: user.DisplayName}
	}
	return payloads
}

// PublishMessageBlocked forwards the RF-21 refusal to its author.
//
// targetID is the recipient's user id: the outbox row for a blocked message
// records the sender as the audience, which is what keeps the announcement off
// the conversation.
func (b *hubBroadcaster) PublishMessageBlocked(ctx context.Context, workspaceID, recipientUserID, messageID, reason string) {
	b.hub.PublishMessageBlocked(ctx, workspaceID, recipientUserID, messageID, reason)
}

func (b *hubBroadcaster) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	payload := domainMessageToWSPayload(msg)
	b.hub.PublishMessageCreated(ctx, workspaceID, ws.TargetType(targetType), targetID, payload)
}

func (b *hubBroadcaster) PublishMessageUpdated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	b.hub.PublishMessageUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, domainMessageToWSUpdatedPayload(msg))
}

func domainMessageToWSUpdatedPayload(msg domain.Message) ws.MessageUpdatedPayload {
	removed := msg.Status == domain.MessageStatusDeleted || !msg.DeletedAt.IsZero()
	var deletedAt *time.Time
	if !msg.DeletedAt.IsZero() {
		t := msg.DeletedAt
		deletedAt = &t
	}
	body := msg.BodyText
	if removed {
		body = ""
	}
	return ws.MessageUpdatedPayload{
		MessageID: msg.ID, ChannelID: msg.ChannelID, DMID: msg.DMConversationID,
		Body: body, BodyFormat: string(msg.BodyFormat), LinkSafetyState: string(msg.LinkSafety),
		Links: domainLinksToWSPayload(msg.Links), EditedAt: msg.EditedAt,
		EditCount: msg.EditCount, IsEdited: msg.EditCount > 0,
		Status: string(msg.Status), IsRemoved: removed, DeletedAt: deletedAt, UpdatedAt: msg.UpdatedAt,
	}
}

// PublishAcknowledgementUpdated adapts the hub for the issue #824 broadcaster,
// converting the string targetType the HTTP layer speaks into the hub's own
// typed one — the same conversion every other broadcaster here performs.
func (b *hubBroadcaster) PublishAcknowledgementUpdated(
	ctx context.Context, workspaceID, targetType, targetID, messageID string,
) {
	b.hub.PublishAcknowledgementUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, messageID)
}

// PublishPinUpdated adapts the hub for the RF-05 pin broadcaster interface.
func (b *hubBroadcaster) PublishPinUpdated(ctx context.Context, workspaceID, targetType, targetID, messageID, actorUserID string, pinned bool) {
	b.hub.PublishPinUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, messageID, actorUserID, pinned)
}

// PublishMembersAdded adapts the hub for the issue #398 members broadcaster,
// converting the string targetType so the HTTP layer keeps no ws import.
func (b *hubBroadcaster) PublishMembersAdded(ctx context.Context, workspaceID, targetType, targetID, actorUserID string, addedCount, memberCount int) {
	b.hub.PublishMembersAdded(ctx, workspaceID, ws.TargetType(targetType), targetID, actorUserID, addedCount, memberCount)
}

// PublishMessageLinkSafetyChanged adapts the hub for the issue #135 link-safety
// correction, converting the string targetType so the service layer keeps no ws
// import.
func (b *hubBroadcaster) PublishMessageLinkSafetyChanged(
	ctx context.Context, workspaceID, targetType, targetID, messageID, state string, updatedAt time.Time,
) {
	b.hub.PublishMessageLinkSafetyChanged(
		ctx, workspaceID, ws.TargetType(targetType), targetID, messageID, state, updatedAt)
}

// PublishConversationUpdated adapts the hub for the issue #527 rename signal,
// converting the string targetType so the HTTP layer keeps no ws import.
// PublishMessageLinkUpdated satisfies service.LinkUpdatePublisher (issue #807).
func (b *hubBroadcaster) PublishMessageLinkUpdated(
	ctx context.Context, workspaceID, targetType, targetID, messageID string, link domain.MessageLink,
) {
	b.hub.PublishMessageLinkUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, messageID, domainLinkToWSPayload(link))
}

func domainLinksToWSPayload(links []domain.MessageLink) []ws.LinkPayload {
	if len(links) == 0 {
		return nil
	}
	out := make([]ws.LinkPayload, len(links))
	for i, link := range links {
		out[i] = domainLinkToWSPayload(link)
	}
	return out
}

func domainLinkToWSPayload(link domain.MessageLink) ws.LinkPayload {
	payload := ws.LinkPayload{
		Ordinal: link.Ordinal, TargetKey: link.TargetKey, Text: link.Text, URL: link.URL, Hostname: link.Hostname,
		Safety: string(link.Safety), Click: string(link.Click), Href: link.Href, UpdatedAt: link.UpdatedAt,
	}
	if link.Preview != nil {
		payload.Preview = &ws.LinkPreviewPayload{
			State: string(link.Preview.State), Hostname: link.Preview.Hostname,
			SiteName: link.Preview.SiteName, Title: link.Preview.Title, Description: link.Preview.Description,
			ImageID: link.Preview.ImageID, ImageWidth: link.Preview.ImageWidth, ImageHeight: link.Preview.ImageHeight,
		}
	}
	return payload
}

func (b *hubBroadcaster) PublishConversationUpdated(ctx context.Context, workspaceID, targetType, targetID string) {
	b.hub.PublishConversationUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID)
}

// PublishConversationEvent adapts the hub for the issue #527 system-message
// signal, converting the string targetType so the HTTP layer keeps no ws import.
func (b *hubBroadcaster) PublishConversationEvent(ctx context.Context, workspaceID, targetType, targetID, messageID string) {
	b.hub.PublishConversationEvent(ctx, workspaceID, ws.TargetType(targetType), targetID, messageID)
}

// PublishConversationAvailable adapts the hub's user-scoped signal (issue #398).
func (b *hubBroadcaster) PublishConversationAvailable(ctx context.Context, workspaceID, targetType, targetID string, userIDs []string) {
	b.hub.PublishConversationAvailable(ctx, workspaceID, ws.TargetType(targetType), targetID, userIDs)
}

// wireAcknowledgements attaches the issue #824 endpoints when a database was
// available to build them from, and leaves them answering 503 when it was not.
//
// The nil test lives here rather than at the seam because the seam takes an
// interface: handing it a nil *AcknowledgementService would produce a non-nil
// interface holding a nil pointer, which passes the handler's readiness check
// and then panics on the first request.
//
// Wired after the hub, beside the pins, because a committed acknowledgement is
// announced to the conversation it happened in. Persistence remains the source
// of truth — the event carries a route and a message id, and a subscriber that
// cares re-reads the authorised summary — so a deployment whose bus is down
// loses only the immediacy, not the state.
func wireAcknowledgements(
	handler *httpapi.MessageHandler, acknowledgements *service.AcknowledgementService, hub *ws.Hub,
) *httpapi.MessageHandler {
	if acknowledgements == nil {
		return handler
	}
	return handler.WithAcknowledgements(acknowledgements, &hubBroadcaster{hub: hub})
}

// wireLinkPreviewImages serves derived link-preview thumbnails (issue #807)
// from the store that re-authorises each read; without a store there is no
// route.
func wireLinkPreviewImages(handler *httpapi.MessageHandler, store *storage.PGXMessageStore) *httpapi.MessageHandler {
	if store == nil {
		return handler
	}
	return handler.WithLinkPreviewImages(store)
}

func domainMessageToWSPayload(msg domain.Message) ws.MessagePayload {
	var editedAt, deletedAt *time.Time
	if !msg.EditedAt.IsZero() {
		t := msg.EditedAt
		editedAt = &t
	}
	if !msg.DeletedAt.IsZero() {
		t := msg.DeletedAt
		deletedAt = &t
	}
	removed := msg.Status == domain.MessageStatusDeleted || deletedAt != nil
	body := msg.BodyText
	quoted := domainQuoteToWSPayload(msg.Quoted)
	attachments := domainAttachmentsToWSPayload(msg.Attachments)
	if removed {
		body, quoted, attachments = "", nil, nil
	}
	return ws.MessagePayload{
		ID:                msg.ID,
		WorkspaceID:       msg.WorkspaceID,
		ChannelID:         msg.ChannelID,
		DMConversationID:  msg.DMConversationID,
		SenderID:          msg.SenderID,
		SenderDisplayName: msg.SenderDisplayName,
		SenderAvatarURL:   msg.SenderAvatarURL,
		Kind:              string(msg.Kind),
		BodyText:          body,
		BodyFormat:        string(msg.BodyFormat),
		Status:            string(msg.Status),
		Priority:          string(msg.Priority.OrStandard()),
		// Carried on a removed message too, like Priority above: what a message
		// asked for is not content, and the removal path blanks content.
		AcknowledgementRequired: msg.AcknowledgementRequired,
		// PersistentNotifications mirrors the HTTP message contract's field of
		// the same name (issue #825), for the same reason AcknowledgementRequired
		// does: a message inserted from this event and the same message after a
		// reload must render identically.
		PersistentNotifications: msg.PersistentNotifications,
		LinkSafetyState:         string(msg.LinkSafety),
		Links:                   domainLinksToWSPayload(msg.Links),
		IsRemoved:               removed,
		CreatedAt:               msg.CreatedAt,
		UpdatedAt:               msg.UpdatedAt,
		EditedAt:                editedAt,
		DeletedAt:               deletedAt,
		Quoted:                  quoted,
		// Carried whatever `removed` did to the preview above: who was answered
		// is not a presentation detail, and a deleted message still answered
		// somebody (issue #136).
		ReplyToSenderID:    msg.ReplyToSenderID,
		Attachments:        attachments,
		IsForwarded:        msg.ForwardedFromMessageID != "",
		NotificationPolicy: notificationPolicyFor(msg, removed, recipientFacts{}),
		HasReference:       msg.ReferencedMessageID != "",
	}
}

func domainAttachmentsToWSPayload(attachments []domain.MessageAttachment) []ws.MessageAttachmentPayload {
	if len(attachments) == 0 {
		return nil
	}
	payload := make([]ws.MessageAttachmentPayload, len(attachments))
	for i, attachment := range attachments {
		payload[i] = ws.MessageAttachmentPayload{
			ID: attachment.ID, Filename: attachment.Filename,
			ContentType: attachment.ContentType, Size: attachment.SizeBytes,
			Status: attachment.Status, PreviewStatus: attachment.PreviewStatus,
			AudioKind: attachment.AudioKind, DurationMs: attachment.DurationMs,
		}
	}
	return payload
}

func domainQuoteToWSPayload(q *domain.QuotedMessage) *ws.QuotePayload {
	if q == nil {
		return nil
	}
	var deletedAt *time.Time
	if !q.DeletedAt.IsZero() {
		t := q.DeletedAt
		deletedAt = &t
	}
	payload := &ws.QuotePayload{
		ID:              q.ID,
		AuthorID:        q.AuthorID,
		BodyFormat:      string(q.BodyFormat),
		LinkSafetyState: string(q.LinkSafety),
		IsRemoved:       q.Status == domain.MessageStatusDeleted || deletedAt != nil,
		DeletedAt:       deletedAt,
		CreatedAt:       q.CreatedAt,
		UpdatedAt:       q.UpdatedAt,
	}
	if !payload.IsRemoved {
		payload.Body = q.BodyText
	}
	return payload
}
