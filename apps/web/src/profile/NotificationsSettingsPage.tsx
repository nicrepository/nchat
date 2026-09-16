import { useCallback, useId, useState } from "react";
import { useOutletContext } from "react-router";

import "./NotificationsSettingsPage.css";
import {
  enableWebPush,
  useWebPushHealth,
  type WebPushAvailableSnapshot,
  type WebPushBackendState,
  type WebPushErrorCategory,
  type WebPushSnapshot,
  type WebPushUnavailableReason,
  type WebPushWorkerState,
} from "../notifications/webPushReconciler";
import {
  getSoundNotificationMode,
  setSoundNotificationMode,
  type SoundNotificationMode,
} from "../chat/soundPreference";
import {
  getIncomingCallRingtoneEnabled,
  playIncomingCallRingtonePreview,
  setIncomingCallRingtoneEnabled,
} from "../calls/incomingCallRingtone";
import type { AppShellOutletContext } from "../chat/AppShell";
import {
  conversationNotificationMode,
  partitionDMs,
  type ConversationNotificationMode,
} from "../chat/chatTypes";

/**
 * One conversation as this page needs it: a name to show, the persisted mode to
 * reflect, and — for a conversation the server will not let anyone silence —
 * the reason in words, so a missing option is never conveyed by its absence
 * alone.
 */
interface ConversationRow {
  id: string;
  kind: "channel" | "dm";
  name: string;
  mode: ConversationNotificationMode;
  muteBlockedReason?: string;
}

/**
 * The three modes the first version of issue #136 offers, in the prototype's
 * order.
 *
 * The values are the canonical endpoint's own, so the select's value *is* the
 * request: nothing here maps a label onto a different vocabulary, and nothing
 * decides what a mode means — the server owns the translation into storage.
 */
const NOTIFICATION_MODES: ReadonlyArray<{ value: ConversationNotificationMode; label: string }> = [
  { value: "all", label: "Todas as mensagens" },
  { value: "mentions_replies", label: "Menções e respostas" },
  { value: "muted", label: "Silenciado" },
];

/**
 * The general channel is where everyone stays reachable by construction, so
 * chat-service refuses to silence it in SQL. A level is a different matter and
 * is allowed: being narrowed to mentions and replies still keeps somebody
 * reachable by name, which is what the invariant protects.
 */
const GENERAL_CHANNEL_REASON =
  "O canal geral não pode ser silenciado. Você ainda pode receber apenas menções e respostas.";

const SOUND_MODES: ReadonlyArray<{ value: SoundNotificationMode; id: string; label: string }> = [
  { value: "off", id: "sound-mode-off", label: "Desativado" },
  { value: "all", id: "sound-mode-all", label: "Todas as mensagens" },
  { value: "mentions", id: "sound-mode-mentions", label: "Somente menções" },
  {
    value: "mentions_and_dms",
    id: "sound-mode-mentions-and-dms",
    label: "Menções e mensagens diretas",
  },
];

const DIGEST_FREQUENCIES = ["Imediato", "Diário", "Semanal"] as const;

/**
 * What the browser-notifications row says, and the one action it offers.
 *
 * Every action that can change push state is `enableWebPush` (issue #748): it
 * prompts only while the permission is still `default` and otherwise repairs,
 * so "Ativar" and "Reconectar" are the same call under two honest labels.
 * `help` changes nothing — `denied` cannot be undone from a page.
 */
interface WebPushView {
  message: string;
  action: WebPushConnectAction | "help" | null;
}

/** The three labels of `enableWebPush`: ask, repair, or try the deployment again. */
type WebPushConnectAction = "enable" | "reconnect" | "retry";

const WEB_PUSH_UNAVAILABLE: Record<WebPushUnavailableReason, string> = {
  unsupported: "Seu navegador não tem suporte a notificações nativas.",
  insecure_context:
    "As notificações do navegador não estão disponíveis neste endereço. Acesse o NChat por HTTPS ou localhost.",
  not_configured:
    "As notificações do navegador ainda não estão disponíveis neste ambiente do NChat.",
};

const WEB_PUSH_ERROR: Record<WebPushErrorCategory, string> = {
  backend:
    "Não foi possível conectar ao serviço de notificações do NChat. Tente reconectar em instantes.",
  browser: "O navegador não conseguiu ativar as notificações. Tente reconectar.",
};

const WEB_PUSH_WORKER_LABEL: Record<WebPushWorkerState, string> = {
  registering: "Iniciando",
  active: "Ativo",
  failed: "Com falha",
};

const WEB_PUSH_BACKEND_LABEL: Record<WebPushBackendState, string> = {
  unknown: "Verificando",
  connected: "Conectada",
  disconnected: "Desconectada",
  invalid: "Precisa ser refeita",
  unavailable: "Indisponível",
};

/**
 * The snapshot in words. Connected means `health === "healthy"` and nothing
 * else: a granted permission, an active worker or a present subscription on
 * their own prove nothing about delivery (issue #862).
 */
function describeWebPush(snapshot: WebPushSnapshot): WebPushView {
  if (snapshot.status === "unavailable") {
    return { message: WEB_PUSH_UNAVAILABLE[snapshot.reason], action: null };
  }
  if (snapshot.permission === "denied") {
    return {
      message:
        "Notificações do navegador foram bloqueadas. Para ativá-las, altere a permissão deste site nas configurações do seu navegador.",
      action: "help",
    };
  }
  if (snapshot.worker === "failed") {
    return {
      message:
        "Não foi possível preparar as notificações neste navegador. Recarregue a página para tentar de novo.",
      action: null,
    };
  }
  // Nothing is offered until the pass has settled: whether this deployment can
  // use a permission is not known yet, and a prompt spent early is not given
  // back (issue #862). enableWebPush refuses to prompt then too; this only
  // keeps the page from offering a button that would.
  if (snapshot.health === "reconciling" || snapshot.worker === "registering") {
    return { message: "Verificando notificações do navegador…", action: null };
  }
  if (snapshot.health === "error") {
    return {
      message: WEB_PUSH_ERROR[snapshot.error ?? "browser"],
      action: snapshot.permission === "default" ? "retry" : "reconnect",
    };
  }
  if (snapshot.permission === "default") {
    return {
      message:
        "Ative notificações do navegador para ser avisado de novas mensagens mesmo com a aba em segundo plano.",
      action: "enable",
    };
  }
  return describeGrantedWebPush(snapshot);
}

function describeGrantedWebPush(snapshot: WebPushAvailableSnapshot): WebPushView {
  if (snapshot.health === "healthy") {
    return { message: "Notificações do navegador estão ativadas.", action: null };
  }
  return {
    message:
      "A permissão foi concedida, mas as notificações do navegador precisam ser reconectadas.",
    action: "reconnect",
  };
}

/**
 * The browser-notifications row of "Notificações gerais" (issues #729, #862).
 *
 * It reads the one health store #748 publishes and never a browser API: no
 * `Notification.permission`, no Service Worker, no PushManager, and no focus or
 * visibility listener of its own — the page-wide lifecycle in main.tsx already
 * re-reads all of it when the tab comes back, and `useWebPushHealth` refreshes
 * on mount. Never a switch, because no switch on this page can turn "denied"
 * back into "granted".
 */
function BrowserNotificationsRow() {
  const snapshot = useWebPushHealth();
  const view = describeWebPush(snapshot);
  const [showHelp, setShowHelp] = useState(false);
  const [connecting, setConnecting] = useState(false);

  // A gesture, and the only path to a permission prompt. enableWebPush never
  // rejects; `finally` is here so the button cannot stay disabled regardless.
  const onConnect = useCallback(() => {
    setConnecting(true);
    void enableWebPush().finally(() => setConnecting(false));
  }, []);

  return (
    <>
      <div className="notifications-settings__row notifications-settings__row--stacked">
        <span className="notifications-settings__row-text">
          <span className="notifications-settings__row-title">Notificações do navegador</span>
          <span className="notifications-settings__row-sub" aria-live="polite">
            {view.message}
          </span>
          {snapshot.status === "available" && snapshot.permission === "granted" && (
            <WebPushDiagnostics snapshot={snapshot} />
          )}
        </span>
        {view.action !== null && view.action !== "help" && (
          <button
            type="button"
            className="notifications-settings__button notifications-settings__button--primary"
            disabled={connecting}
            aria-busy={connecting}
            onClick={onConnect}
          >
            {connectLabel(view.action, connecting)}
          </button>
        )}
        {view.action === "help" && (
          <button
            type="button"
            className="notifications-settings__button"
            aria-expanded={showHelp}
            onClick={() => setShowHelp((shown) => !shown)}
          >
            Como ativar notificações
          </button>
        )}
      </div>
      {view.action === "help" && showHelp && (
        <ol className="notifications-settings__help">
          <li>Clique no ícone de cadeado ao lado do endereço do site.</li>
          <li>Localize a permissão de notificações.</li>
          <li>Remova o bloqueio ou selecione &quot;Permitir&quot;.</li>
          <li>Recarregue a página ou volte ao NChat.</li>
        </ol>
      )}
    </>
  );
}

const CONNECT_LABEL: Record<WebPushConnectAction, string> = {
  enable: "Ativar notificações do navegador",
  reconnect: "Reconectar notificações",
  retry: "Tentar novamente",
};

function connectLabel(action: WebPushConnectAction, connecting: boolean): string {
  return connecting ? "Conectando…" : CONNECT_LABEL[action];
}

/**
 * The axes behind the summary, in words, once permission is granted — the
 * point at which "allowed" and "connected" can disagree and a person needs to
 * see which one it is. Categories only; nothing here can name an endpoint or a
 * key, because the snapshot has no field that holds one.
 */
function WebPushDiagnostics({ snapshot }: Readonly<{ snapshot: WebPushAvailableSnapshot }>) {
  return (
    <dl className="notifications-settings__diagnostics">
      <div>
        <dt>Permissão do navegador</dt>
        <dd>Concedida</dd>
      </div>
      <div>
        <dt>Serviço em segundo plano</dt>
        <dd>{WEB_PUSH_WORKER_LABEL[snapshot.worker]}</dd>
      </div>
      <div>
        <dt>Registro deste navegador</dt>
        <dd>{snapshot.subscription === "present" ? "Registrado" : "Não registrado"}</dd>
      </div>
      <div>
        <dt>Conexão com o NChat</dt>
        <dd>{WEB_PUSH_BACKEND_LABEL[snapshot.backend]}</dd>
      </div>
    </dl>
  );
}

/**
 * Card holding the per-conversation notification preference
 * (issues #527/#729/#136).
 *
 * Rendered twice — once for channels, once for groups — because the two are
 * separate blocks visually and one abstraction underneath. Every write goes
 * through the sidebar's own `setNotificationMode`, which is optimistic, holds at
 * most one write per conversation, and rolls itself back when the server
 * refuses; this only has to say so when it happens, so the row and the server
 * never disagree silently.
 *
 * The control is a select because the capability is now three real modes
 * (issue #136) rather than the boolean #729 had to render as a switch. The
 * modes are the server's own: nothing here decides what one means, and nothing
 * here decides whether a message counts as a mention.
 */
function ConversationNotificationsCard({
  title,
  emptyMessage,
  status,
  rows,
  granular,
  onRetry,
  setMuted,
  setNotificationMode,
}: Readonly<{
  title: string;
  emptyMessage: string;
  status: "loading" | "error" | "ready";
  rows: ConversationRow[];
  /**
   * Whether this deployment has opened the issue #136 rollout gate, as the
   * server reported it.
   *
   * True renders the three-mode select; false renders the binary switch #729
   * shipped, writing through the mute endpoints that every build of this
   * product has always had. The server refuses the granular mode while the
   * gate is shut, so offering the select then would offer an option that 503s.
   */
  granular: boolean;
  onRetry: () => void;
  setMuted: AppShellOutletContext["setMuted"];
  setNotificationMode: AppShellOutletContext["setNotificationMode"];
}>) {
  const headingId = useId();
  const errorId = useId();
  // The row whose write failed, so the message can be tied to the control that
  // produced it instead of floating at the bottom of the card.
  const [error, setError] = useState<{ rowId: string; message: string } | null>(null);
  // The conversations whose write is still in flight, by id. A set rather than
  // a single "saving" flag because changing #infra must not freeze #avisos: the
  // writes are independent and so are the rows.
  const [saving, setSaving] = useState<ReadonlySet<string>>(() => new Set());

  /**
   * Starts one write and reports its outcome on the row that asked for it.
   *
   * Shared by both controls, so the pending set, the rollback message and the
   * per-row error association behave identically whichever one this deployment
   * renders. The write itself differs — the select states the whole
   * preference, the switch states only the mute — and both go through the one
   * coordination primitive in useChatSidebar.
   */
  function startWrite(row: ConversationRow, write: () => Promise<void>) {
    setError(null);
    setSaving((current) => new Set(current).add(row.id));
    void write()
      .catch(() =>
        setError({
          rowId: row.id,
          message: `Não foi possível atualizar as notificações de ${row.name}. Tente novamente.`,
        }),
      )
      .finally(() =>
        setSaving((current) => {
          const next = new Set(current);
          next.delete(row.id);
          return next;
        }),
      );
  }

  function onSelect(row: ConversationRow, mode: ConversationNotificationMode) {
    startWrite(row, () => setNotificationMode({ kind: row.kind, targetId: row.id }, mode));
  }

  // The phase-one control: notifications on or off, which is the mute the
  // sidebar's own shortcut writes.
  function onToggle(row: ConversationRow, notify: boolean) {
    startWrite(row, () => setMuted({ kind: row.kind, targetId: row.id }, !notify));
  }

  return (
    <section className="notifications-settings__card" aria-labelledby={headingId}>
      <div className="notifications-settings__card-head">
        <h3 id={headingId} className="notifications-settings__card-title">
          {title}
        </h3>
      </div>
      <div className="notifications-settings__card-body">
        {status === "loading" && (
          <p className="notifications-settings__hint" role="status">
            Carregando suas conversas…
          </p>
        )}
        {status === "error" && (
          <div className="notifications-settings__retry">
            <p className="notifications-settings__hint">
              Não foi possível carregar suas conversas.
            </p>
            <button type="button" className="notifications-settings__button" onClick={onRetry}>
              Tentar novamente
            </button>
          </div>
        )}
        {status === "ready" && rows.length === 0 && (
          <p className="notifications-settings__hint">{emptyMessage}</p>
        )}
        {status === "ready" &&
          rows.map((row) => {
            const isSaving = saving.has(row.id);
            // Two independent notes, and both are words rather than a colour or
            // a dimmed control: why an option is missing, and that a write is in
            // flight. They can coexist — #geral is still switchable between the
            // two levels it does allow.
            const noteId = `notifications-note-${row.id}`;
            const notes = [row.muteBlockedReason, isSaving ? "Salvando…" : undefined].filter(
              (note): note is string => Boolean(note),
            );
            const failed = error?.rowId === row.id;
            // The row's own select points at whichever of the two it has, so a
            // screen reader announcing the control hears the restriction and the
            // failure rather than only the name.
            const describedBy =
              [notes.length > 0 ? noteId : undefined, failed ? errorId : undefined]
                .filter(Boolean)
                .join(" ") || undefined;
            // A control only ever offers what the server would accept. The
            // refusal itself stays server-side, in SQL — this is the
            // affordance, never the control.
            const options = row.muteBlockedReason
              ? NOTIFICATION_MODES.filter((mode) => mode.value !== "muted")
              : NOTIFICATION_MODES;
            // The phase-one control cannot express three states, so the row it
            // renders is the one #729 shipped: a switch, unavailable for a
            // conversation the server will not let anyone silence.
            if (!granular) {
              const switchNotes = [
                row.muteBlockedReason,
                isSaving ? "Salvando…" : undefined,
              ].filter((note): note is string => Boolean(note));
              const switchDescribedBy =
                [switchNotes.length > 0 ? noteId : undefined, failed ? errorId : undefined]
                  .filter(Boolean)
                  .join(" ") || undefined;
              return (
                <label key={row.id} className="notifications-settings__row">
                  <span className="notifications-settings__row-text">
                    <span className="notifications-settings__row-title">{row.name}</span>
                    {switchNotes.length > 0 && (
                      <span id={noteId} className="notifications-settings__row-sub">
                        {switchNotes.join(" ")}
                      </span>
                    )}
                  </span>
                  <span className="notifications-settings__switch">
                    <input
                      type="checkbox"
                      checked={row.mode !== "muted"}
                      // Unavailable while its own write is in flight, and for a
                      // conversation that cannot be silenced at all. Nothing
                      // here disables any other row.
                      disabled={isSaving || Boolean(row.muteBlockedReason)}
                      aria-busy={isSaving}
                      aria-label={`Notificações de ${row.name}`}
                      aria-describedby={switchDescribedBy}
                      onChange={(event) => onToggle(row, event.currentTarget.checked)}
                    />
                    <span className="notifications-settings__switch-track" aria-hidden="true" />
                  </span>
                </label>
              );
            }
            return (
              <div key={row.id} className="notifications-settings__row">
                <span className="notifications-settings__row-text">
                  <label
                    className="notifications-settings__row-title"
                    htmlFor={`notifications-mode-${row.id}`}
                  >
                    {row.name}
                  </label>
                  {notes.length > 0 && (
                    <span id={noteId} className="notifications-settings__row-sub">
                      {notes.join(" ")}
                    </span>
                  )}
                </span>
                <select
                  id={`notifications-mode-${row.id}`}
                  className="notifications-settings__select"
                  value={row.mode}
                  // Unavailable while its own write is in flight, so a second
                  // change cannot start a second request for this conversation.
                  // Nothing here disables any other row.
                  disabled={isSaving}
                  aria-busy={isSaving}
                  // Contains the visible label, and says what it governs: the
                  // conversation's name on its own would not.
                  aria-label={`Notificações de ${row.name}`}
                  aria-describedby={describedBy}
                  onChange={(event) =>
                    onSelect(row, event.currentTarget.value as ConversationNotificationMode)
                  }
                >
                  {options.map((mode) => (
                    <option key={mode.value} value={mode.value}>
                      {mode.label}
                    </option>
                  ))}
                </select>
              </div>
            );
          })}
        {error && (
          <p id={errorId} className="notifications-settings__error" role="alert">
            {error.message}
          </p>
        )}
      </div>
    </section>
  );
}

export default function NotificationsSettingsPage() {
  // The one sidebar instance lives in AppShell and reaches here through
  // ProfileSettingsShell (issue #729). Mounting useChatSidebar() again here
  // would duplicate the fetch and the realtime subscriptions for a screen that
  // only reads names and one preference per row.
  const { state, retry, setMuted, setNotificationMode } = useOutletContext<AppShellOutletContext>();
  // The capability the server published in the payload that hydrated this
  // state. Strict equality: absent — an older server, or a state assembled
  // without it — is the compatible binary control (issue #136).
  const granular = state.status === "ready" && state.notificationLevelsEnabled === true;

  const [soundMode, setSoundModeState] = useState<SoundNotificationMode>(() =>
    getSoundNotificationMode(),
  );
  const [incomingCallRingtoneEnabled, setIncomingCallRingtoneEnabledState] = useState(() =>
    getIncomingCallRingtoneEnabled(),
  );
  const digestNoteId = useId();

  const onChangeSoundMode = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const next = event.currentTarget.value as SoundNotificationMode;
    setSoundNotificationMode(next);
    setSoundModeState(next);
  }, []);

  const onChangeIncomingCallRingtone = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const enabled = event.currentTarget.checked;
    setIncomingCallRingtoneEnabled(enabled);
    setIncomingCallRingtoneEnabledState(enabled);
  }, []);

  // Both lists are exactly what the server returned for this session — no
  // per-conversation request, and therefore nothing here that could enumerate a
  // channel or a group outside the viewer's membership.
  const channelRows: ConversationRow[] =
    state.status === "ready"
      ? state.channels.map((channel) => ({
          id: channel.id,
          kind: "channel",
          name: channel.name,
          // The one shared derivation, so this page and the sidebar cannot
          // disagree about what a stored preference means (issue #136).
          mode: conversationNotificationMode(channel),
          // The server's own structural column, never a comparison against the
          // visible name.
          muteBlockedReason: channel.isGeneral ? GENERAL_CHANNEL_REASON : undefined,
        }))
      : [];
  // `type` is the server's own discriminator, so a 1:1 can never land in the
  // groups block — partitionDMs drops it, and everything it does not recognise.
  const groupRows: ConversationRow[] =
    state.status === "ready"
      ? partitionDMs(state.dms).groups.map((group) => ({
          id: group.id,
          kind: "dm",
          name: group.name,
          mode: conversationNotificationMode(group),
        }))
      : [];

  return (
    <div className="notifications-settings">
      <header className="notifications-settings__header">
        <h2 className="notifications-settings__title">Notificações</h2>
        <p className="notifications-settings__description">
          Gerencie como você é avisado sobre mensagens, menções, chamadas e atividades.
        </p>
      </header>

      <section className="notifications-settings__card" aria-labelledby="notifications-general">
        <div className="notifications-settings__card-head">
          <h3 id="notifications-general" className="notifications-settings__card-title">
            Notificações gerais
          </h3>
        </div>
        <div className="notifications-settings__card-body">
          <BrowserNotificationsRow />

          {/* Four states, not two: a switch would have to drop two of them, so
              this stays a radio group and only its skin changed. */}
          <fieldset className="notifications-settings__fieldset">
            <legend className="notifications-settings__legend">Som de notificações</legend>
            <div className="notifications-settings__choices">
              {SOUND_MODES.map((mode) => (
                <label
                  key={mode.value}
                  className="notifications-settings__choice"
                  htmlFor={mode.id}
                >
                  <input
                    id={mode.id}
                    type="radio"
                    name="sound-mode"
                    value={mode.value}
                    checked={soundMode === mode.value}
                    onChange={onChangeSoundMode}
                  />
                  {mode.label}
                </label>
              ))}
            </div>
          </fieldset>

          <label className="notifications-settings__row" htmlFor="incoming-call-ringtone-enabled">
            <span className="notifications-settings__row-text">
              <span className="notifications-settings__row-title">
                Tocar som para chamadas recebidas
              </span>
              <span id="incoming-call-ringtone-sub" className="notifications-settings__row-sub">
                Independente do som de mensagens.
              </span>
            </span>
            <span className="notifications-settings__switch">
              <input
                id="incoming-call-ringtone-enabled"
                type="checkbox"
                checked={incomingCallRingtoneEnabled}
                aria-label="Tocar som para chamadas recebidas"
                aria-describedby="incoming-call-ringtone-sub"
                onChange={onChangeIncomingCallRingtone}
              />
              <span className="notifications-settings__switch-track" aria-hidden="true" />
            </span>
          </label>
          <div className="notifications-settings__actions">
            <button
              type="button"
              className="notifications-settings__button"
              onClick={playIncomingCallRingtonePreview}
            >
              Testar som de chamada
            </button>
          </div>
        </div>
      </section>

      <ConversationNotificationsCard
        title="Notificações por canal"
        emptyMessage="Você ainda não participa de nenhum canal."
        status={state.status}
        rows={channelRows}
        granular={granular}
        onRetry={retry}
        setMuted={setMuted}
        setNotificationMode={setNotificationMode}
      />

      <ConversationNotificationsCard
        title="Notificações por grupos"
        emptyMessage="Você ainda não participa de nenhum grupo."
        status={state.status}
        rows={groupRows}
        granular={granular}
        onRetry={retry}
        setMuted={setMuted}
        setNotificationMode={setNotificationMode}
      />

      {/* Structurally ready, deliberately inert: there is no digest backend to
          write to, and a control that pretended otherwise would be exactly the
          false persistence issue #729 forbids. No localStorage, no "salvo", no
          scheduling in the browser. */}
      <section className="notifications-settings__card" aria-labelledby="notifications-digest">
        <div className="notifications-settings__card-head">
          <h3 id="notifications-digest" className="notifications-settings__card-title">
            E-mail digest
          </h3>
        </div>
        <div className="notifications-settings__card-body">
          <p id={digestNoteId} className="notifications-settings__hint">
            O resumo por e-mail ainda não está disponível nesta instalação. Os controles abaixo
            ficam indisponíveis até o serviço existir, e nada preenchido aqui é salvo.
          </p>
          <fieldset className="notifications-settings__fieldset" disabled>
            <legend className="notifications-settings__legend">Frequência</legend>
            <div className="notifications-settings__choices notifications-settings__choices--inline">
              {DIGEST_FREQUENCIES.map((frequency) => (
                <label
                  key={frequency}
                  className="notifications-settings__choice"
                  htmlFor={`digest-frequency-${frequency}`}
                >
                  <input
                    id={`digest-frequency-${frequency}`}
                    type="radio"
                    name="digest-frequency"
                    value={frequency}
                    defaultChecked={false}
                    aria-describedby={digestNoteId}
                  />
                  {frequency}
                </label>
              ))}
            </div>
          </fieldset>
          <div className="notifications-settings__field">
            <label className="notifications-settings__legend" htmlFor="digest-time">
              Horário preferido
            </label>
            <input
              id="digest-time"
              type="time"
              className="notifications-settings__time"
              disabled
              aria-describedby={digestNoteId}
            />
          </div>
          <div className="notifications-settings__actions">
            <button
              type="button"
              className="notifications-settings__button notifications-settings__button--primary"
              disabled
              aria-describedby={digestNoteId}
            >
              Salvar preferências
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
