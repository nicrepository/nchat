import { useCallback, useEffect, useId, useState } from "react";
import { useOutletContext } from "react-router";

import "./NotificationsSettingsPage.css";
import {
  type BrowserNotificationPermission,
  getBrowserNotificationPermission,
  isBrowserNotificationSecureContext,
  requestBrowserNotificationPermission,
} from "../chat/browserNotification";
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
import { partitionDMs } from "../chat/chatTypes";

/**
 * One conversation as this page needs it: a name to show, the persisted
 * preference to reflect, and — when the server would refuse the change — the
 * reason in words, so an unavailable control is never conveyed by colour alone.
 */
interface ConversationRow {
  id: string;
  kind: "channel" | "dm";
  name: string;
  muted: boolean;
  lockedReason?: string;
}

/** The general channel is where everyone is reachable by construction; chat-service refuses to mute it in SQL. */
const GENERAL_CHANNEL_REASON = "O canal geral não pode ser silenciado.";

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
 * Card holding the per-conversation notification preference (issues #527/#729).
 *
 * Rendered twice — once for channels, once for groups — because the two are
 * separate blocks visually and one abstraction underneath. Every write goes
 * through the sidebar's own `setMuted`, which is optimistic and rolls itself
 * back when the server refuses; this only has to say so when it happens, so the
 * row and the server never disagree silently.
 *
 * The capability chat-service actually has is mute/unmute, a real boolean, so
 * the control is a switch. The prototype's three delivery modes are not
 * invented here: there is nothing to persist them in.
 */
function ConversationNotificationsCard({
  title,
  emptyMessage,
  status,
  rows,
  onRetry,
  setMuted,
}: Readonly<{
  title: string;
  emptyMessage: string;
  status: "loading" | "error" | "ready";
  rows: ConversationRow[];
  onRetry: () => void;
  setMuted: AppShellOutletContext["setMuted"];
}>) {
  const headingId = useId();
  const [error, setError] = useState("");
  // The conversations whose write is still in flight, by id. A set rather than
  // a single "saving" flag because muting #infra must not freeze #avisos: the
  // writes are independent and so are the rows.
  const [saving, setSaving] = useState<ReadonlySet<string>>(() => new Set());

  function onToggle(row: ConversationRow, notify: boolean) {
    setError("");
    setSaving((current) => new Set(current).add(row.id));
    void setMuted({ kind: row.kind, targetId: row.id }, !notify)
      .catch(() =>
        setError(`Não foi possível atualizar as notificações de ${row.name}. Tente novamente.`),
      )
      .finally(() =>
        setSaving((current) => {
          const next = new Set(current);
          next.delete(row.id);
          return next;
        }),
      );
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
            // One note slot per row, and the two states cannot coexist: a
            // conversation that cannot be silenced never has a write in flight.
            // Both are words, not just a dimmed control.
            const note = row.lockedReason ?? (saving.has(row.id) ? "Salvando…" : undefined);
            return (
              <label key={row.id} className="notifications-settings__row">
                <span className="notifications-settings__row-text">
                  <span className="notifications-settings__row-title">{row.name}</span>
                  {note && (
                    <span
                      id={`notifications-note-${row.id}`}
                      className="notifications-settings__row-sub"
                    >
                      {note}
                    </span>
                  )}
                </span>
                <span className="notifications-settings__switch">
                  <input
                    type="checkbox"
                    checked={!row.muted}
                    // Unavailable while its own write is in flight, so a second
                    // click cannot start a second request for this conversation.
                    // Nothing here disables any other row.
                    disabled={Boolean(note)}
                    aria-busy={saving.has(row.id)}
                    aria-label={`Notificações de ${row.name}`}
                    aria-describedby={note ? `notifications-note-${row.id}` : undefined}
                    onChange={(event) => onToggle(row, event.currentTarget.checked)}
                  />
                  <span className="notifications-settings__switch-track" aria-hidden="true" />
                </span>
              </label>
            );
          })}
        {error && (
          <p className="notifications-settings__error" role="alert">
            {error}
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
  const { state, retry, setMuted } = useOutletContext<AppShellOutletContext>();

  const [soundMode, setSoundModeState] = useState<SoundNotificationMode>(() =>
    getSoundNotificationMode(),
  );
  const [incomingCallRingtoneEnabled, setIncomingCallRingtoneEnabledState] = useState(() =>
    getIncomingCallRingtoneEnabled(),
  );
  const [browserPermission, setBrowserPermission] = useState<BrowserNotificationPermission>(() =>
    getBrowserNotificationPermission(),
  );
  const [showBrowserNotificationHelp, setShowBrowserNotificationHelp] = useState(false);
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

  const onEnableBrowserNotifications = useCallback(async () => {
    const result = await requestBrowserNotificationPermission();
    setBrowserPermission(result);
  }, []);

  useEffect(() => {
    const refreshBrowserPermission = () => setBrowserPermission(getBrowserNotificationPermission());
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") refreshBrowserPermission();
    };
    window.addEventListener("focus", refreshBrowserPermission);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      window.removeEventListener("focus", refreshBrowserPermission);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
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
          muted: Boolean(channel.muted),
          lockedReason: channel.isGeneral ? GENERAL_CHANNEL_REASON : undefined,
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
          muted: Boolean(group.muted),
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
          {/* The browser owns this one outright. It is a status plus, at most,
              one explicit action — never a switch, because no switch on this
              page can turn "denied" back into "granted". */}
          <div className="notifications-settings__row notifications-settings__row--stacked">
            <span className="notifications-settings__row-text">
              <span className="notifications-settings__row-title">Notificações do navegador</span>
              {browserPermission === "granted" && (
                <span className="notifications-settings__row-sub">
                  Notificações do navegador estão ativadas.
                </span>
              )}
              {browserPermission === "denied" && (
                <span className="notifications-settings__row-sub">
                  Notificações do navegador foram bloqueadas. Para ativá-las, altere a permissão
                  deste site nas configurações do seu navegador.
                </span>
              )}
              {browserPermission === "unsupported" && (
                <span className="notifications-settings__row-sub">
                  {isBrowserNotificationSecureContext()
                    ? "Seu navegador não tem suporte a notificações nativas."
                    : "As notificações do navegador não estão disponíveis neste endereço. Acesse o NChat por HTTPS ou localhost."}
                </span>
              )}
              {browserPermission === "default" && (
                <span className="notifications-settings__row-sub">
                  Ative notificações do navegador para ser avisado de novas mensagens mesmo com a
                  aba em segundo plano.
                </span>
              )}
            </span>
            {browserPermission === "default" && (
              <button
                type="button"
                className="notifications-settings__button notifications-settings__button--primary"
                onClick={onEnableBrowserNotifications}
              >
                Ativar notificações do navegador
              </button>
            )}
            {browserPermission === "denied" && (
              <button
                type="button"
                className="notifications-settings__button"
                aria-expanded={showBrowserNotificationHelp}
                onClick={() => setShowBrowserNotificationHelp((shown) => !shown)}
              >
                Como ativar notificações
              </button>
            )}
          </div>
          {browserPermission === "denied" && showBrowserNotificationHelp && (
            <ol className="notifications-settings__help">
              <li>Clique no ícone de cadeado ao lado do endereço do site.</li>
              <li>Localize a permissão de notificações.</li>
              <li>Remova o bloqueio ou selecione &quot;Permitir&quot;.</li>
              <li>Recarregue a página ou volte ao NChat.</li>
            </ol>
          )}

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
        onRetry={retry}
        setMuted={setMuted}
      />

      <ConversationNotificationsCard
        title="Notificações por grupos"
        emptyMessage="Você ainda não participa de nenhum grupo."
        status={state.status}
        rows={groupRows}
        onRetry={retry}
        setMuted={setMuted}
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
