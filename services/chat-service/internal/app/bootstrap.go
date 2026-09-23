package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// bootstrap is the assembly New runs, one stage at a time. Each stage reads
// what the previous ones built and adds its own components; New only orders
// them. The groups below are the dependency layers: storage, then the services
// over it, then the realtime substrate, then the HTTP handlers, then the
// workers that need the hub to exist.
type bootstrap struct {
	cfg       config.Config
	logger    *slog.Logger
	shutdown  observability.ShutdownFunc
	metrics   *observability.Metrics
	validator *httpapi.TokenValidator

	stores   bootstrapStores
	services bootstrapServices
	realtime bootstrapRealtime
	handlers bootstrapHandlers
	workers  bootstrapWorkers
}

type bootstrapStores struct {
	closeDB               func()
	ready                 bool
	sessions              storage.SessionValidator
	workspaces            *storage.PGXWorkspaceStore
	userDisplayNames      *storage.PGXUserDisplayNameStore
	channels              *storage.PGXChannelStore
	members               *storage.PGXMemberStore
	dms                   *storage.PGXDMStore
	messages              *storage.PGXMessageStore
	sidebarPins           *storage.PGXSidebarPinStore
	conversationReadState *storage.PGXConversationReadStateStore
	notificationPrefs     *storage.PGXNotificationPrefStore
	mentionCache          *storage.ValkeyMentionLabelCache
}

type bootstrapServices struct {
	sidebar         *service.SidebarService
	dm              *service.DMService
	message         *service.MessageService
	mention         *service.MentionService
	reaction        *service.ReactionService
	favorite        *service.FavoriteService
	pin             *service.PinService
	acknowledgement *service.AcknowledgementService
	permission      *service.PermissionService
	channel         *service.ChannelService
	channelCategory *service.ChannelCategoryService
	member          *service.MemberService
	call            *service.CallService
	links           linkSafetyWiring
}

type bootstrapRealtime struct {
	presence            *ws.PresenceTracker
	presenceDirectory   *ws.ValkeyPresenceDirectory
	authorizer          ws.SubscriptionAuthorizer
	workspaces          ws.WorkspaceResolver
	canonicalWorkspaces *appWSWorkspaceResolver
	displayNames        ws.UserDisplayNameResolver
	instanceID          string
	presenceInstanceID  string
	bus                 ws.BroadcastBus
	options             []ws.HubOption
	reactionLimiter     *ws.ValkeyReactionLimiter
	typingLimiter       *ws.ValkeyReactionLimiter
	typingStore         *ws.ValkeyTypingStore
	hub                 *ws.Hub
	wsHandler           http.Handler
}

type bootstrapHandlers struct {
	sidebar           *httpapi.SidebarHandler
	message           *httpapi.MessageHandler
	directMessages    *httpapi.DMHandler
	channels          *httpapi.ChannelHandler
	channelCategories *httpapi.ChannelCategoryHandler
	antiSpam          *httpapi.AntiSpamGuard
}

type bootstrapWorkers struct {
	callCancel context.CancelFunc
	callWG     *sync.WaitGroup
	linkCancel context.CancelFunc
	linkWG     *sync.WaitGroup
}

// newBootstrap builds the process-wide primitives every stage shares.
func newBootstrap(cfg config.Config) *bootstrap {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)
	// One registry for the whole process, built here rather than inside the
	// router because RF-21's counter is registered during service wiring, which
	// happens first. The router serves this exact object.
	metrics := observability.NewMetrics(obsCfg)
	// JWT token validator — nil when secret is not configured.
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		logger.Warn("sidebar auth disabled", "reason", "invalid_jwt_config")
	}
	return &bootstrap{cfg: cfg, logger: logger, shutdown: shutdown, metrics: metrics, validator: validator}
}

// abort releases what was opened before a fatal bootstrap error.
func (b *bootstrap) abort() {
	if b.stores.closeDB != nil {
		b.stores.closeDB()
	}
	_ = b.shutdown(context.Background())
}

// openDatabase connects, then wires the stores and services over the pool. A
// configured but unreachable database is fatal: Kubernetes restarts the
// container and the retry window resets. An absent DATABASE_URL is a
// configuration choice — the process stays alive and /readyz reports 503.
func (b *bootstrap) openDatabase() error {
	if b.cfg.DatabaseURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbBootstrapTimeout)
	pool, err := openDBWithRetry(ctx, b.cfg.DatabaseURL, b.cfg.DBConnectTimeoutSeconds, b.logger)
	cancel()
	if err != nil {
		// Fail fast: a half-wired server must never start serving.
		b.logger.Error("database bootstrap failed; refusing degraded start", "reason", "open_db_failed")
		b.abort()
		return err
	}
	b.stores.ready = true
	if closer, ok := pool.(interface{ Close() }); ok {
		b.stores.closeDB = closer.Close
	}
	if b.validator == nil {
		return nil
	}
	b.wireStores(pool)
	b.wireServices(pool)
	return b.wireLinks()
}

func (b *bootstrap) wireStores(pool storage.Pool) {
	b.stores.sessions = storage.NewPGXSessionValidator(pool)
	b.stores.workspaces = storage.NewPGXWorkspaceStore(pool)
	b.stores.userDisplayNames = storage.NewPGXUserDisplayNameStore(pool)
	b.stores.channels = storage.NewPGXChannelStore(pool)
	b.stores.members = storage.NewPGXMemberStore(pool)
	b.stores.dms = storage.NewPGXDMStore(pool)
	b.stores.messages = storage.NewPGXMessageStore(pool)
	b.stores.sidebarPins = storage.NewPGXSidebarPinStore(pool)
	b.stores.conversationReadState = storage.NewPGXConversationReadStateStore(pool)
	b.stores.notificationPrefs = storage.NewPGXNotificationPrefStore(pool)
}

func (b *bootstrap) wireServices(pool storage.Pool) {
	cfg, st := b.cfg, b.stores
	b.services.dm = service.NewDMService(st.dms, st.members)
	b.services.reaction = service.NewReactionService(storage.NewPGXReactionStore(pool))
	b.services.favorite = service.NewFavoriteService(storage.NewPGXFavoriteStore(pool))
	b.services.pin = service.NewPinService(storage.NewPGXPinStore(pool))
	b.services.acknowledgement = service.NewAcknowledgementService(storage.NewPGXAcknowledgementStore(pool))
	b.services.call = service.NewCallService(storage.NewPGXCallStore(pool), time.Duration(cfg.CallRingTimeoutSeconds)*time.Second, nil, nil)
	b.services.permission = service.NewPermissionService(st.members, st.channels)
	b.services.channel = service.NewChannelService(st.workspaces, st.channels, st.members)
	// channelStore is both the category store and the visible-channel read
	// side, so RF-17 groups channels through the same query the sidebar uses.
	b.services.channelCategory = service.NewChannelCategoryService(st.workspaces, st.members, st.channels, st.channels)
	b.services.sidebar = service.NewSidebarService(st.workspaces, st.channels, st.members, st.dms).
		WithPins(st.sidebarPins).
		WithReadState(st.conversationReadState).
		WithNotificationPrefs(st.notificationPrefs).
		// Issue #136's rollout gate. Off unless a deployment asks, and asked
		// for in one place so the write path and the capability the payload
		// publishes cannot disagree.
		WithConversationNotificationLevels(cfg.ConversationNotificationLevelsEnabled)
	b.services.message = service.NewMessageService(st.channels, st.dms, st.messages).
		WithMessageAttachmentLimits(cfg.MaxMessageAttachments, cfg.MaxMessageAttachmentBytes)
	// One MemberService instance for both consumers: mention autocomplete
	// reads channel members through it, and issue #398 writes them. Two
	// instances would only be two paths to the same stores.
	b.services.member = service.NewMemberService(st.members, st.channels, st.workspaces)
	b.services.mention = service.NewMentionService(b.services.member, b.services.permission, st.dms)
}

// wireLinks attaches RF-21 / issue #807 to the message service. Fatal on
// failure: starting with the flag on and no gate would accept links nobody
// checked. The publishers are attached once the hub exists (startLinkWorkers).
func (b *bootstrap) wireLinks() error {
	links, err := wireLinkSafety(b.cfg, b.services.message, b.stores.messages, nil, b.metrics, b.logger)
	if err != nil {
		b.abort()
		return err
	}
	b.services.links = links
	b.stores.mentionCache = wireMentionLabelCache(b.cfg.ValkeyURL, b.cfg.MentionLabelCacheTTLSeconds, b.services.message, b.logger)
	return nil
}

// buildPresence creates the hub's presence tracker and the authorities a
// socket binds to. Always created so Shutdown always manages them: without a
// database NopAuthorizer denies every subscription and the workspace resolver
// is nil, so ServeWS answers 503 before any client connects.
func (b *bootstrap) buildPresence() {
	rt := &b.realtime
	rt.presence = ws.NewPresenceTracker(defaultPresenceAwayTimeout)
	rt.authorizer = ws.NopAuthorizer{}
	if b.stores.workspaces != nil {
		rt.authorizer = ws.NewServiceAuthorizer(b.stores.channels, b.stores.dms)
		// Held concretely as well: the same adapter is the canonical workspace
		// resolver for the RF-19 guard, so WebSocket sessions and HTTP sends bind
		// to the same workspace by construction rather than by two lookups.
		rt.canonicalWorkspaces = &appWSWorkspaceResolver{store: b.stores.workspaces}
		rt.workspaces = rt.canonicalWorkspaces
		rt.displayNames = b.stores.userDisplayNames
	}
	// Two identities, because they answer two different questions.
	//
	// instanceID is the logical one: configured through WS_INSTANCE_ID,
	// meaningful to operators, and used by the bus to suppress its own echo.
	// Nothing guarantees it is unique, which is why it also needs a fallback.
	//
	// presenceInstanceID is the physical one: this execution of this process.
	// The presence directory names the field it owns by it, so its uniqueness
	// cannot be left to configuration. Generated here, never persisted, and
	// different on every restart — two pods sharing WS_INSTANCE_ID still write
	// two different fields.
	rt.instanceID = b.cfg.WSInstanceID
	if rt.instanceID == "" {
		rt.instanceID = uuid.New().String()
	}
	rt.presenceInstanceID = uuid.NewString()
	rt.options = []ws.HubOption{ws.WithPresence(rt.presence), ws.WithPresenceInstanceID(rt.presenceInstanceID)}
}

// buildBus wires the cross-instance broadcast and the shared presence state
// (RF-58). The directory only earns its keep when events already cross
// instances: with no bus this process is the whole cluster.
func (b *bootstrap) buildBus() {
	rt := &b.realtime
	rt.bus = ws.NopBus{}
	if !b.cfg.ValkeyWSBroadcastEnabled {
		return
	}
	if valkeyBus, err := ws.NewValkeyBus(b.cfg.ValkeyURL, rt.instanceID, b.logger); err != nil {
		b.logger.Warn("distributed ws broadcast disabled", "reason", "invalid_valkey_config")
	} else {
		rt.bus = valkeyBus
	}
	if directory, err := ws.NewValkeyPresenceDirectory(b.cfg.ValkeyURL, rt.presenceInstanceID); err != nil {
		b.logger.Warn("shared presence directory disabled", "reason", "invalid_valkey_config")
	} else {
		rt.presenceDirectory = directory
		rt.options = append(rt.options, ws.WithPresenceDirectory(directory))
	}
}

// buildLimiters dials the Valkey-backed budgets. Each ws subsystem owns its own
// client rather than sharing one — the established pattern in this package.
// Absent VALKEY_URL each feature fails closed (SECURITY.md's WS rate-limit
// requirement) rather than degrading to a per-process limiter.
func (b *bootstrap) buildLimiters() {
	cfg, rt := b.cfg, &b.realtime
	if b.services.reaction != nil {
		if limiter, err := ws.NewValkeyReactionLimiter(
			cfg.ValkeyURL, cfg.ReactionRateLimitMaxActions, cfg.ReactionRateLimitWindowSeconds,
		); err != nil {
			b.logger.Warn("message reactions disabled", "reason", "invalid_valkey_config")
		} else {
			rt.reactionLimiter = limiter
			rt.options = append(rt.options, ws.WithReactionHandler(&reactionHandlerAdapter{service: b.services.reaction}), ws.WithReactionLimiter(limiter))
			rt.options = b.withCallOptions(rt.options, limiter)
		}
	}
	// Typing indicator: independent of the reaction feature, so it gets its own
	// limiter and TTL backstop. Delivery itself does not depend on Valkey.
	if limiter, err := ws.NewValkeyReactionLimiter(
		cfg.ValkeyURL, cfg.TypingRateLimitMaxActions, cfg.TypingRateLimitWindowSeconds,
	); err != nil {
		b.logger.Warn("typing indicator rate limiting disabled", "reason", "invalid_valkey_config")
	} else {
		rt.typingLimiter = limiter
		rt.options = append(rt.options, ws.WithTypingLimiter(limiter, cfg.TypingRateLimitMaxActions, cfg.TypingRateLimitWindowSeconds))
	}
	if store, err := ws.NewValkeyTypingStore(cfg.ValkeyURL); err != nil {
		b.logger.Warn("typing ttl backstop disabled", "reason", "invalid_valkey_config")
	} else {
		rt.typingStore = store
		rt.options = append(rt.options, ws.WithTypingStore(store))
	}
}

func (b *bootstrap) withCallOptions(options []ws.HubOption, limiter *ws.ValkeyReactionLimiter) []ws.HubOption {
	if b.services.call == nil {
		return options
	}
	return append(options, ws.WithCallHandler(&callHandlerAdapter{service: b.services.call}),
		ws.WithCallLimiter(limiter, b.cfg.CallStartRateLimitMaxActions, b.cfg.CallStartRateLimitWindowSeconds))
}

// buildHub assembles the hub from everything the realtime stages collected.
func (b *bootstrap) buildHub() {
	rt := &b.realtime
	rt.options = withRecipientPolicyOption(rt.options, b.stores.notificationPrefs)
	rt.hub = ws.NewHub(rt.authorizer, b.logger, rt.bus, rt.instanceID, rt.options...)
	rt.wsHandler = ws.ServeWSWithConfig(rt.hub, b.logger, rt.workspaces, httpapi.GetContextUserID,
		wsHandlerConfig(b.cfg, b.stores.sessions, rt.displayNames))
}

// broadcaster is the adapter every service-to-hub publication goes through.
func (b *bootstrap) broadcaster() *hubBroadcaster { return &hubBroadcaster{hub: b.realtime.hub} }

// buildMessageHandler assembles the message routes and the RF-19 anti-spam
// guard in front of them.
//
// The guard is given three distinct things on purpose: who decides the
// workspace (canonicalWorkspaces), where the policy for a given workspace is
// read (workspaceStore), and what counts (reactionLimiter). The nil checks are
// on the concrete pointers: a nil *ws.ValkeyReactionLimiter assigned to an
// interface parameter is a non-nil interface holding a nil pointer.
func (b *bootstrap) buildMessageHandler() {
	cfg, st, rt := b.cfg, b.stores, &b.realtime
	b.handlers.sidebar = httpapi.NewSidebarHandler(b.services.sidebar).
		WithMessageAttachmentLimits(cfg.MaxMessageAttachments, cfg.MaxMessageAttachmentBytes)
	handler := httpapi.NewMessageHandler(st.workspaces, b.services.message, nil)
	if b.services.mention != nil {
		handler = httpapi.NewMessageHandler(st.workspaces, b.services.message, b.services.mention)
	}
	if b.services.favorite != nil {
		handler = handler.WithFavorites(b.services.favorite)
	}
	if rt.canonicalWorkspaces != nil && st.workspaces != nil && rt.reactionLimiter != nil {
		b.handlers.antiSpam = httpapi.NewAntiSpamGuard(rt.canonicalWorkspaces, st.workspaces, rt.reactionLimiter)
	}
	if st.workspaces != nil {
		handler = handler.WithEditing(st.workspaces, b.services.permission, rt.reactionLimiter).WithAntiSpam(b.handlers.antiSpam)
	}
	b.handlers.message = handler
}

// buildConversationHandlers assembles the DM, channel and category routes. The
// limiter is required, not optional: without it a create route would run
// unthrottled, so an unconfigured Valkey leaves the routes unregistered (404)
// rather than exposed. Readiness already fails in that configuration.
func (b *bootstrap) buildConversationHandlers() {
	st, limiter := b.stores, b.realtime.reactionLimiter
	if b.services.dm != nil {
		b.handlers.directMessages = httpapi.NewDMHandler(st.workspaces, b.services.dm, limiter)
	}
	if b.services.channel != nil && limiter != nil {
		b.handlers.channels = httpapi.NewChannelHandler(st.workspaces, b.services.channel, limiter)
	}
	if b.services.channelCategory != nil && limiter != nil {
		b.handlers.channelCategories = httpapi.NewChannelCategoryHandler(st.workspaces, b.services.channelCategory, limiter)
	}
}

// workerLifecycle is the context a worker group runs under. Its cancel is
// kept on the App and called by Shutdown, which is why it is not deferred here.
func workerLifecycle() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// startCallWorker starts ring expiry once the hub exists to announce it.
func (b *bootstrap) startCallWorker() {
	if b.services.call == nil {
		return
	}
	// hubBroadcaster, not hub directly: CallEventPublisher also needs
	// PublishConversationEvent (issue #835), whose string targetType the
	// adapter maps to the hub's own typed ws.TargetType.
	b.services.call.SetPublisher(b.broadcaster())
	ctx, cancel := workerLifecycle()
	b.workers.callCancel = cancel
	b.workers.callWG = &sync.WaitGroup{}
	b.workers.callWG.Add(1)
	go func() {
		defer b.workers.callWG.Done()
		runCallExpiryWorker(ctx, b.services.call, b.logger)
	}()
}

// startLinkWorkers starts the RF-21 / issue #807 workers once the hub exists:
// a message they promote or a link they settle has to be broadcast. Scan,
// preview and reconcile share one lifecycle rather than inventing three.
func (b *bootstrap) startLinkWorkers() {
	if b.services.message != nil {
		b.services.message.SetPublisher(b.broadcaster())
	}
	links := b.services.links
	if links.Scan == nil {
		return
	}
	links.Scan.SetPublisher(b.broadcaster())
	// The refusal channel is sender-scoped and therefore a different object: a
	// blocked message goes to its author alone, never to the conversation.
	links.Scan.SetBlockedPublisher(b.broadcaster())
	links.Announcer.SetPublishers(b.broadcaster(), b.broadcaster())
	ctx, cancel := workerLifecycle()
	b.workers.linkCancel = cancel
	b.workers.linkWG = &sync.WaitGroup{}
	b.runLinkWorker(func() { service.RunLinkScanWorker(ctx, links.Scan, service.LinkScanPollInterval, b.logger) })
	// The preview worker always runs: with the flag off it only drains.
	b.runLinkWorker(func() { service.RunLinkPreviewWorker(ctx, links.Preview, service.LinkPreviewPollInterval, b.logger) })
	// The recovery pass (issue #135) runs on its own, much slower ticker: it
	// corrects messages already delivered, so nobody is waiting on a pass.
	if links.Reconcile != nil {
		links.Reconcile.SetPublisher(b.broadcaster())
		b.runLinkWorker(func() {
			service.RunLinkReconcileWorker(ctx, links.Reconcile, service.LinkReconcileInterval, b.logger)
		})
	}
}

func (b *bootstrap) runLinkWorker(run func()) {
	b.workers.linkWG.Add(1)
	go func() {
		defer b.workers.linkWG.Done()
		run()
	}()
}

// attachMessageBroadcasters wires the message routes that announce over the
// hub, so they are attached after it exists.
func (b *bootstrap) attachMessageBroadcasters() {
	handler, links := b.handlers.message, b.services.links
	// The reader-driven half of the issue #135 recovery. The limiter is the
	// shared Valkey one (CQ-005); without it the route stays 503 — this is the
	// only user-triggered path that reaches a paid third party.
	if links.Reconcile != nil && b.realtime.reactionLimiter != nil {
		handler = handler.WithLinkReconcile(links.Reconcile, b.realtime.reactionLimiter)
	}
	handler = wireLinkPreviewImages(handler, b.stores.messages)
	// Pins broadcast over the same hub (RF-05).
	if b.services.pin != nil {
		handler = handler.WithPins(b.services.pin, b.broadcaster())
	}
	b.handlers.message = wireAcknowledgements(handler, b.services.acknowledgement, b.realtime.hub)
}

// attachConversationBroadcasters wires the channel and DM routes that report
// presence or announce membership changes, both of which need the hub.
func (b *bootstrap) attachConversationBroadcasters() {
	reporter := presenceReporter{tracker: b.realtime.presence}
	if channels := b.handlers.channels; channels != nil {
		// The channel-details panel (issue #435) reports member presence from
		// the same tracker the hub feeds; add members (issue #398) and rename
		// (issue #527) broadcast over the same hub.
		channels = channels.WithPresence(reporter).WithChannelUpdates(b.broadcaster())
		if b.services.member != nil {
			channels = channels.WithMembers(b.services.member, b.broadcaster())
		}
		b.handlers.channels = channels
	}
	if dms := b.handlers.directMessages; dms != nil {
		// The group-details panel (issue #441) annotates participants with the
		// same tracker, exactly like the channel panel.
		b.handlers.directMessages = dms.WithMembersBroadcast(b.broadcaster()).WithPresence(reporter)
	}
}

// app assembles the App from the finished stages. Database readiness is the
// pool's own bootstrap outcome, independent of JWT or service wiring; the
// remaining fields reflect each component's wiring.
func (b *bootstrap) app() *App {
	rt, h := &b.realtime, b.handlers
	readiness := httpapi.ReadinessState{
		Database:         b.stores.ready,
		TokenValidator:   b.validator != nil,
		SessionValidator: b.stores.sessions != nil,
		Sidebar:          b.services.sidebar != nil,
		Messages:         b.services.message != nil,
		WebSocket:        rt.workspaces != nil && b.validator != nil && b.stores.sessions != nil,
	}
	return &App{
		Config: b.cfg,
		Logger: b.logger,
		Handler: httpapi.NewRouter(b.cfg, b.logger, readiness, b.validator, b.stores.sessions, h.sidebar,
			h.message, rt.wsHandler, h.directMessages, h.channels, h.channelCategories, h.antiSpam, b.metrics),
		TracingShutdown:   b.shutdown,
		hub:               rt.hub,
		presence:          rt.presence,
		presenceDirectory: rt.presenceDirectory,
		mentionCache:      b.stores.mentionCache,
		reactionLimiter:   rt.reactionLimiter,
		typingLimiter:     rt.typingLimiter,
		typingStore:       rt.typingStore,
		callWorkerCancel:  b.workers.callCancel,
		callWorkerWG:      b.workers.callWG,
		linkScanCancel:    b.workers.linkCancel,
		linkScanWG:        b.workers.linkWG,
		closeDB:           b.stores.closeDB,
	}
}
