package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
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
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)
	// One registry for the whole process, built here rather than inside the
	// router because RF-21's counter is registered during service wiring, which
	// happens first. The router serves this exact object.
	obsMetrics := observability.NewMetrics(obsCfg)

	// JWT token validator — nil when secret is not configured.
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		logger.Warn("sidebar auth disabled", "reason", "invalid_jwt_config")
	}

	var sidebarSvc *service.SidebarService
	var dmSvc *service.DMService
	var messageSvc *service.MessageService
	var mentionSvc *service.MentionService
	var workspaceStore *storage.PGXWorkspaceStore
	var userDisplayNameStore *storage.PGXUserDisplayNameStore
	var sessionValidator storage.SessionValidator
	var channelStore *storage.PGXChannelStore
	var memberStore *storage.PGXMemberStore
	var dmStore *storage.PGXDMStore
	var mentionCache *storage.ValkeyMentionLabelCache
	var reactionSvc *service.ReactionService
	var favoriteSvc *service.FavoriteService
	var pinSvc *service.PinService
	var sidebarPinStore *storage.PGXSidebarPinStore
	var conversationReadStateStore *storage.PGXConversationReadStateStore
	var permissionSvc *service.PermissionService
	var channelSvc *service.ChannelService
	var channelCategorySvc *service.ChannelCategoryService
	var memberSvc *service.MemberService
	var callSvc *service.CallService
	var linkScanSvc *service.LinkScanService
	var linkReconcileSvc *service.LinkReconcileService

	var closeDB func()
	databaseReady := false
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), dbBootstrapTimeout)
		pool, dbErr := openDBWithRetry(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, logger)
		cancel()
		if dbErr != nil {
			// Fail fast: a half-wired server must never start serving.
			// Kubernetes restarts the container and the retry window resets.
			logger.Error("database bootstrap failed; refusing degraded start", "reason", "open_db_failed")
			_ = shutdown(context.Background())
			return nil, dbErr
		}
		databaseReady = true
		if closer, ok := pool.(interface{ Close() }); ok {
			closeDB = closer.Close
		}
		if validator != nil {
			sessionValidator = storage.NewPGXSessionValidator(pool)
			workspaceStore = storage.NewPGXWorkspaceStore(pool)
			userDisplayNameStore = storage.NewPGXUserDisplayNameStore(pool)
			channelStore = storage.NewPGXChannelStore(pool)
			memberStore = storage.NewPGXMemberStore(pool)
			dmStore = storage.NewPGXDMStore(pool)
			dmSvc = service.NewDMService(dmStore, memberStore)
			messages := storage.NewPGXMessageStore(pool)
			reactionSvc = service.NewReactionService(storage.NewPGXReactionStore(pool))
			favoriteSvc = service.NewFavoriteService(storage.NewPGXFavoriteStore(pool))
			pinSvc = service.NewPinService(storage.NewPGXPinStore(pool))
			sidebarPinStore = storage.NewPGXSidebarPinStore(pool)
			conversationReadStateStore = storage.NewPGXConversationReadStateStore(pool)
			callSvc = service.NewCallService(storage.NewPGXCallStore(pool), time.Duration(cfg.CallRingTimeoutSeconds)*time.Second, nil, nil)
			permissionSvc = service.NewPermissionService(memberStore, channelStore)
			channelSvc = service.NewChannelService(workspaceStore, channelStore, memberStore)
			// channelStore is both the category store and the visible-channel read
			// side, so RF-17 groups channels through the same query the sidebar uses.
			channelCategorySvc = service.NewChannelCategoryService(workspaceStore, memberStore, channelStore, channelStore)
			sidebarSvc = service.NewSidebarService(workspaceStore, channelStore, memberStore, dmStore).
				WithPins(sidebarPinStore).
				WithReadState(conversationReadStateStore).
				WithNotificationPrefs(storage.NewPGXNotificationPrefStore(pool))
			messageSvc = service.NewMessageService(channelStore, dmStore, messages).
				WithMessageAttachmentLimits(cfg.MaxMessageAttachments, cfg.MaxMessageAttachmentBytes)
			// RF-21. Wired here, where the message service exists, and fatal:
			// starting with the flag on and no gate would accept links nobody
			// checked. A deployment without a database never reaches this block
			// and needs no gate — its message routes answer 503 already.
			//
			// The publisher is attached later (the hub does not exist yet), so
			// the worker is built here and given it below.
			linkSafety, wireErr := wireLinkSafety(cfg, messageSvc, messages, nil, obsMetrics, logger)
			if wireErr != nil {
				closeDB()
				_ = shutdown(context.Background())
				return nil, wireErr
			}
			linkScanSvc, linkReconcileSvc = linkSafety.Scan, linkSafety.Reconcile
			mentionCache = wireMentionLabelCache(cfg.ValkeyURL, cfg.MentionLabelCacheTTLSeconds, messageSvc, logger)
			// One MemberService instance for both consumers: mention autocomplete
			// reads channel members through it, and issue #398 writes them. Two
			// instances would only be two paths to the same stores.
			memberSvc = service.NewMemberService(memberStore, channelStore, workspaceStore)
			mentionSvc = service.NewMentionService(memberSvc, permissionSvc)
		}
	}

	sidebar := httpapi.NewSidebarHandler(sidebarSvc).
		WithMessageAttachmentLimits(cfg.MaxMessageAttachments, cfg.MaxMessageAttachmentBytes)
	messageHandler := httpapi.NewMessageHandler(workspaceStore, messageSvc, nil)
	if mentionSvc != nil {
		messageHandler = httpapi.NewMessageHandler(workspaceStore, messageSvc, mentionSvc)
	}
	if favoriteSvc != nil {
		messageHandler = messageHandler.WithFavorites(favoriteSvc)
	}

	// Hub and presence are always created so their lifecycle is always managed
	// by Shutdown. When DB is unavailable, NopAuthorizer denies all subscriptions
	// and wsWorkspaces is nil so ServeWS returns 503 before any client connects.
	presence := ws.NewPresenceTracker(defaultPresenceAwayTimeout)
	var authorizer ws.SubscriptionAuthorizer = ws.NopAuthorizer{}
	var wsWorkspaces ws.WorkspaceResolver
	var wsDisplayNames ws.UserDisplayNameResolver
	// Held concretely as well: the same adapter is the canonical workspace
	// resolver for the RF-19 guard, so WebSocket sessions and HTTP sends bind to
	// the same workspace by construction rather than by two similar lookups.
	var canonicalWorkspaces *appWSWorkspaceResolver
	if workspaceStore != nil {
		authorizer = ws.NewServiceAuthorizer(channelStore, dmStore)
		canonicalWorkspaces = &appWSWorkspaceResolver{store: workspaceStore}
		wsWorkspaces = canonicalWorkspaces
		wsDisplayNames = userDisplayNameStore
	}
	// Two identities, because they answer two different questions.
	//
	// instanceID is the logical one: configured through WS_INSTANCE_ID, meaningful
	// to operators, and used by the bus to suppress its own echo. Nothing
	// guarantees it is unique — a Deployment that sets a fixed value hands the
	// same string to every pod, and WS_INSTANCE_ID is not provisioned in any
	// manifest here at all, which is why it also needs a fallback.
	//
	// presenceInstanceID is the physical one: this execution of this process. The
	// presence directory names the field it owns by it, and everything about
	// single-writer ordering rests on no other process owning that field, so its
	// uniqueness cannot be left to configuration. It is generated here, never
	// read from the environment, never persisted, and changes on every restart —
	// two pods sharing WS_INSTANCE_ID still write two different fields.
	instanceID := cfg.WSInstanceID
	if instanceID == "" {
		instanceID = uuid.New().String()
	}
	presenceInstanceID := uuid.NewString()

	var bus ws.BroadcastBus = ws.NopBus{}
	if cfg.ValkeyWSBroadcastEnabled {
		if valkeyBus, busErr := ws.NewValkeyBus(cfg.ValkeyURL, instanceID, logger); busErr != nil {
			logger.Warn("distributed ws broadcast disabled", "reason", "invalid_valkey_config")
		} else {
			bus = valkeyBus
		}
	}
	options := []ws.HubOption{ws.WithPresence(presence), ws.WithPresenceInstanceID(presenceInstanceID)}
	// Shared presence state (RF-58). It only earns its keep when events already
	// cross instances: with no bus this process is the whole cluster and its own
	// connections are the complete answer, so a second source would be a cache
	// of what it already knows. With a bus, it is what lets a client joining a
	// conversation see the people connected to other replicas without waiting
	// for one of them to move.
	var presenceDirectory *ws.ValkeyPresenceDirectory
	if cfg.ValkeyWSBroadcastEnabled {
		if directory, dirErr := ws.NewValkeyPresenceDirectory(cfg.ValkeyURL, presenceInstanceID); dirErr != nil {
			logger.Warn("shared presence directory disabled", "reason", "invalid_valkey_config")
		} else {
			presenceDirectory = directory
			options = append(options, ws.WithPresenceDirectory(directory))
		}
	}
	var reactionLimiter *ws.ValkeyReactionLimiter
	if reactionSvc != nil {
		if limiter, limiterErr := ws.NewValkeyReactionLimiter(
			cfg.ValkeyURL, cfg.ReactionRateLimitMaxActions, cfg.ReactionRateLimitWindowSeconds,
		); limiterErr != nil {
			logger.Warn("message reactions disabled", "reason", "invalid_valkey_config")
		} else {
			reactionLimiter = limiter
			options = append(options, ws.WithReactionHandler(&reactionHandlerAdapter{service: reactionSvc}), ws.WithReactionLimiter(limiter))
			if callSvc != nil {
				options = append(options, ws.WithCallHandler(&callHandlerAdapter{service: callSvc}),
					ws.WithCallLimiter(limiter, cfg.CallStartRateLimitMaxActions, cfg.CallStartRateLimitWindowSeconds))
			}
		}
	}
	// Typing indicator: independent of the reaction feature (reactionSvc may be
	// nil while typing still works), so it gets its own Valkey-backed limiter
	// and TTL backstop, each dialed from the same VALKEY_URL — the established
	// pattern in this package, where every ws subsystem (bus, presence
	// directory, reaction limiter) owns its own client rather than sharing one.
	// Absent VALKEY_URL, typing.start is refused (ErrTypingFeatureDisabled,
	// fail-closed per SECURITY.md's WS rate-limit requirement) and the TTL
	// backstop is simply absent — delivery itself does not depend on Valkey.
	var typingLimiter *ws.ValkeyReactionLimiter
	if limiter, limiterErr := ws.NewValkeyReactionLimiter(
		cfg.ValkeyURL, cfg.TypingRateLimitMaxActions, cfg.TypingRateLimitWindowSeconds,
	); limiterErr != nil {
		logger.Warn("typing indicator rate limiting disabled", "reason", "invalid_valkey_config")
	} else {
		typingLimiter = limiter
		options = append(options, ws.WithTypingLimiter(limiter, cfg.TypingRateLimitMaxActions, cfg.TypingRateLimitWindowSeconds))
	}
	var typingStore *ws.ValkeyTypingStore
	if store, storeErr := ws.NewValkeyTypingStore(cfg.ValkeyURL); storeErr != nil {
		logger.Warn("typing ttl backstop disabled", "reason", "invalid_valkey_config")
	} else {
		typingStore = store
		options = append(options, ws.WithTypingStore(store))
	}
	// RF-19 (issue #419): the configurable per-workspace send limit. It reuses
	// the same Lua/Valkey limiter as reactions and edits — no second rate
	// limiting mechanism — so it exists only when that limiter does. When it is
	// nil the send routes answer 503 rather than degrading to a per-process
	// limiter, which would restore the cross-instance bypass RF-19 closes.
	//
	// The guard is given three distinct things on purpose: who decides the
	// workspace (canonicalWorkspaces), where the policy for a given workspace ID
	// is read (workspaceStore), and what counts (reactionLimiter). It has no
	// notion of a default workspace of its own.
	//
	// The nil checks are on the concrete pointers, not inside the constructor: a
	// nil *ws.ValkeyReactionLimiter assigned to an interface parameter is a
	// non-nil interface holding a nil pointer, so a check there would pass and
	// the guard would panic on its first send.
	var antiSpam *httpapi.AntiSpamGuard
	if canonicalWorkspaces != nil && workspaceStore != nil && reactionLimiter != nil {
		antiSpam = httpapi.NewAntiSpamGuard(canonicalWorkspaces, workspaceStore, reactionLimiter)
	}
	if workspaceStore != nil {
		messageHandler = messageHandler.WithEditing(workspaceStore, permissionSvc, reactionLimiter).WithAntiSpam(antiSpam)
	}
	var directMessages *httpapi.DMHandler
	if dmSvc != nil {
		directMessages = httpapi.NewDMHandler(workspaceStore, dmSvc, reactionLimiter)
	}
	// The limiter is required, not optional: without it the create route would run
	// unthrottled, so an unconfigured Valkey leaves the route unregistered (404)
	// rather than exposed. Readiness already fails in that configuration.
	var channels *httpapi.ChannelHandler
	if channelSvc != nil && reactionLimiter != nil {
		channels = httpapi.NewChannelHandler(workspaceStore, channelSvc, reactionLimiter)
	}
	// Same reasoning for the category routes (RF-17): the writes must be
	// throttled, so an unconfigured Valkey leaves them unregistered rather than
	// exposed. The read route shares the handler and so is gated with them.
	var channelCategories *httpapi.ChannelCategoryHandler
	if channelCategorySvc != nil && reactionLimiter != nil {
		channelCategories = httpapi.NewChannelCategoryHandler(workspaceStore, channelCategorySvc, reactionLimiter)
	}
	hub := ws.NewHub(authorizer, logger, bus, instanceID, options...)
	wsHandler := ws.ServeWSWithConfig(hub, logger, wsWorkspaces, httpapi.GetContextUserID, wsHandlerConfig(cfg, sessionValidator, wsDisplayNames))

	var callWorkerCancel context.CancelFunc
	var callWorkerWG *sync.WaitGroup
	if callSvc != nil {
		callSvc.SetPublisher(hub)
		workerCtx, cancel := context.WithCancel(context.Background())
		callWorkerCancel = cancel
		callWorkerWG = &sync.WaitGroup{}
		callWorkerWG.Add(1)
		go func() {
			defer callWorkerWG.Done()
			runCallExpiryWorker(workerCtx, callSvc, logger)
		}()
	}
	// Wire the hub as the broadcast publisher for message creation events.
	// SetPublisher is called after both messageSvc and hub are ready.
	if messageSvc != nil {
		messageSvc.SetPublisher(&hubBroadcaster{hub: hub})
	}

	// RF-21's worker starts here, for the same reason: a message it promotes has
	// to be broadcast, and the hub is what broadcasts. It shares the call
	// worker's lifecycle machinery rather than inventing a second one.
	var linkScanWorkerCancel context.CancelFunc
	var linkScanWorkerWG *sync.WaitGroup
	if linkScanSvc != nil {
		linkScanSvc.SetPublisher(&hubBroadcaster{hub: hub})
		// The refusal channel is sender-scoped and therefore a different object:
		// a blocked message goes to its author alone, never to the conversation
		// it was never shown in.
		linkScanSvc.SetBlockedPublisher(&hubBroadcaster{hub: hub})
		workerCtx, cancel := context.WithCancel(context.Background())
		linkScanWorkerCancel = cancel
		linkScanWorkerWG = &sync.WaitGroup{}
		linkScanWorkerWG.Add(1)
		go func() {
			defer linkScanWorkerWG.Done()
			service.RunLinkScanWorker(workerCtx, linkScanSvc, service.LinkScanPollInterval, logger)
		}()
		// The recovery pass (issue #135) shares that lifecycle rather than
		// inventing a third one, but runs on its own, much slower ticker: it
		// corrects messages that were already delivered, so nobody is waiting on a
		// pass. It is search-then-read at the provider and can never submit.
		if linkReconcileSvc != nil {
			linkReconcileSvc.SetPublisher(&hubBroadcaster{hub: hub})
			linkScanWorkerWG.Add(1)
			go func() {
				defer linkScanWorkerWG.Done()
				service.RunLinkReconcileWorker(
					workerCtx, linkReconcileSvc, service.LinkReconcileInterval, logger)
			}()
		}
	}
	// The reader-driven half of the same recovery. Wired after the hub, because a
	// verdict it obtains has to be announced to everyone holding the message.
	//
	// The limiter is the shared Valkey one every other user-action budget already
	// uses (issue #135, CQ-005). Without it the route stays 503: this is the only
	// user-triggered path that reaches a paid third party, and an unlimited one —
	// or one limited per replica — is not an acceptable degradation.
	if linkReconcileSvc != nil && reactionLimiter != nil {
		messageHandler = messageHandler.WithLinkReconcile(linkReconcileSvc, reactionLimiter)
	}

	// Pins broadcast over the same hub; wired after the hub exists (RF-05).
	if pinSvc != nil {
		messageHandler = messageHandler.WithPins(pinSvc, &hubBroadcaster{hub: hub})
	}

	// The channel-details panel (issue #435) reports member presence from the
	// same tracker the hub feeds, so the HTTP layer never has to invent one.
	if channels != nil {
		channels = channels.WithPresence(presenceReporter{tracker: presence})
	}
	// The group-details panel (issue #441) annotates participants with the same
	// tracker. Unlike the channel panel it does not filter by presence, so this
	// only decides what each row says about itself.
	// Add members (issue #398) broadcasts over the same hub, wired after it
	// exists. Both routes share one adapter so the channel and the group event
	// travel the identical path.
	if memberSvc != nil && channels != nil {
		channels = channels.WithMembers(memberSvc, &hubBroadcaster{hub: hub})
	}
	// Rename (issue #527) broadcasts over the same hub. Wired separately from
	// WithMembers because the rename route does not need the member service.
	if channels != nil {
		channels = channels.WithChannelUpdates(&hubBroadcaster{hub: hub})
	}
	if directMessages != nil {
		directMessages = directMessages.WithMembersBroadcast(&hubBroadcaster{hub: hub})
		// The details panel reports participant presence from the same tracker
		// the hub feeds, exactly like the channel panel.
		directMessages = directMessages.WithPresence(presenceReporter{tracker: presence})
	}

	// Database is the pool's own bootstrap outcome, independent of JWT or
	// service wiring; the remaining fields reflect each component's wiring.
	readiness := httpapi.ReadinessState{
		Database:         databaseReady,
		TokenValidator:   validator != nil,
		SessionValidator: sessionValidator != nil,
		Sidebar:          sidebarSvc != nil,
		Messages:         messageSvc != nil,
		WebSocket:        wsWorkspaces != nil && validator != nil && sessionValidator != nil,
	}

	return &App{
		Config:            cfg,
		Logger:            logger,
		Handler:           httpapi.NewRouter(cfg, logger, readiness, validator, sessionValidator, sidebar, messageHandler, wsHandler, directMessages, channels, channelCategories, antiSpam, obsMetrics),
		TracingShutdown:   shutdown,
		hub:               hub,
		presence:          presence,
		presenceDirectory: presenceDirectory,
		mentionCache:      mentionCache,
		reactionLimiter:   reactionLimiter,
		typingLimiter:     typingLimiter,
		typingStore:       typingStore,
		callWorkerCancel:  callWorkerCancel,
		callWorkerWG:      callWorkerWG,
		linkScanCancel:    linkScanWorkerCancel,
		linkScanWG:        linkScanWorkerWG,
		closeDB:           closeDB,
	}, nil
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
// linkSafetyStore is only the two halves of RF-21 the bootstrap touches — the
// verdicts the send path reads and the queue the worker drains — rather than the
// whole MessageStore. Narrow because it is also what a test has to provide.
type linkSafetyStore interface {
	service.URLSafetyChecker
	service.LinkScanQueue
	service.LinkReconcileQueue
}

// linkSafetyWiring is the pair of workers RF-21 runs.
//
// Two, not one, and they are deliberately separate objects with separate provider
// interfaces. The scan worker may submit; the reconcile worker may not, and its
// dependency (service.LinkVerdictReconciler) has no method that could. Returning
// them together keeps the bootstrap to one call while leaving that separation
// visible in the types.
type linkSafetyWiring struct {
	// Scan drains the queue of URLs awaiting a first verdict.
	Scan *service.LinkScanService
	// Reconcile re-reads scans that finished without one. Nil when the feature is
	// off, which is a working deployment: an inconclusive link stays inconclusive
	// and the server still never fetches it.
	Reconcile *service.LinkReconcileService
}

func wireLinkSafety(
	cfg config.Config, messageSvc *service.MessageService,
	store linkSafetyStore, publisher service.MessageEventPublisher,
	metrics *observability.Metrics, logger *slog.Logger,
) (linkSafetyWiring, error) {
	if !cfg.LinkSafetyEnabled {
		return linkSafetyWiring{}, nil
	}
	if messageSvc == nil || store == nil {
		return linkSafetyWiring{}, errLinkSafetyUnwired
	}
	_ = publisher // attached after the hub exists; see SetPublisher below.
	scanner, err := urlsafety.NewCloudflareScanner(
		cfg.LinkSafetyCloudflareAccount, cfg.LinkSafetyCloudflareToken,
	)
	if err != nil {
		// The constructor's message names no value, but it is not repeated
		// either: this returns a fixed error so nothing about the credentials
		// can reach a log through it.
		return linkSafetyWiring{}, errLinkSafetyUnwired
	}
	// The shared counter, registered on this service's own registry so
	// chat-service reports verdict outcomes exactly as file-service does. Its
	// labels are the closed set the shared package defines; no URL, host, user or
	// message id is ever one.
	safety := urlsafety.NewService(scanner, urlsafety.NewMetrics(metrics))
	messageSvc.SetLinkSafety(store)
	// What a workspace, and this deployment, may spend on new provider work.
	// Applied at admission, before any message is created and before any job is
	// queued, so a refusal costs the provider nothing.
	messageSvc.SetLinkScanCapacity(storage.LinkScanCapacity{
		WorkspaceNewURLBudget: cfg.LinkSafetyWorkspaceBudget,
		BudgetWindow:          time.Duration(cfg.LinkSafetyBudgetWindowSeconds) * time.Second,
		MaxPendingJobs:        cfg.LinkSafetyMaxPendingJobs,
	})
	// The pipeline gauges and counters. Without them a Cloudflare outage is
	// indistinguishable from a quiet system: messages simply stop appearing.
	// One reporter, shared by the request path and the worker, so the admission
	// counter and the pipeline counters land on the same registry — registering
	// twice would produce nothing at all.
	pipeline := urlsafety.NewPipelineMetrics(metrics, cfg.ServiceName)
	messageSvc.SetAdmissionMetrics(pipeline)

	worker := service.NewLinkScanService(store, safety, publisher, logger)
	worker.SetMetrics(pipeline)
	worker.SetCapacity(service.LinkScanWorkerCapacity{
		ProviderSubmitLimit:  cfg.LinkSafetyProviderSubmitLimit,
		ProviderSubmitWindow: time.Duration(cfg.LinkSafetyProviderSubmitWindowSeconds) * time.Second,
		UncertainTimeout:     time.Duration(cfg.LinkSafetySubmitUncertainTimeoutSeconds) * time.Second,
	})

	// The recovery half (issue #135). It shares the provider client, and therefore
	// the same strict verdict rules and the same in-process verdict cache, but it
	// is handed to a narrower interface: LinkVerdictReconciler has exactly one
	// method and no way to submit. That is the structural guarantee that no
	// inconclusive scan can ever turn into a second billed Cloudflare scan.
	//
	// It shares the pipeline metrics reporter for the same reason the worker does:
	// one registry, one set of series, and no risk of a duplicate registration
	// silently disabling both.
	reconcile := service.NewLinkReconcileService(store, safety, logger)
	reconcile.SetMetrics(pipeline)

	return linkSafetyWiring{Scan: worker, Reconcile: reconcile}, nil
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
		reactions[i] = ws.ReactionPayload{Emoji: reaction.Emoji, Count: reaction.Count}
	}
	return ws.ReactionUpdate{
		MessageID: result.MessageID, TargetType: targetType, TargetID: targetID,
		Added: result.Added, Reactions: reactions,
	}, nil
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
		Body: body, BodyFormat: string(msg.BodyFormat), LinkSafetyState: string(msg.LinkSafety), EditedAt: msg.EditedAt,
		EditCount: msg.EditCount, IsEdited: msg.EditCount > 0,
		Status: string(msg.Status), IsRemoved: removed, DeletedAt: deletedAt, UpdatedAt: msg.UpdatedAt,
	}
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
		LinkSafetyState:   string(msg.LinkSafety),
		IsRemoved:         removed,
		CreatedAt:         msg.CreatedAt,
		UpdatedAt:         msg.UpdatedAt,
		EditedAt:          editedAt,
		DeletedAt:         deletedAt,
		Quoted:            quoted,
		Attachments:       attachments,
		IsForwarded:       msg.ForwardedFromMessageID != "",
		HasReference:      msg.ReferencedMessageID != "",
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
