import { useCallback, useEffect, useState } from "react";

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

export default function NotificationsSettingsPage() {
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

  return (
    <div className="notifications-settings">
      <header className="notifications-settings__header">
        <h2 className="notifications-settings__title">Notificações</h2>
        <p className="notifications-settings__description">
          Gerencie como você é avisado sobre mensagens, menções e chamadas.
        </p>
      </header>

      <fieldset className="notifications-settings__sound-modes">
        <legend className="notifications-settings__sound-modes-legend">Som de notificações</legend>
        <label className="notifications-settings__checkbox-row" htmlFor="sound-mode-off">
          <input
            id="sound-mode-off"
            type="radio"
            name="sound-mode"
            value="off"
            checked={soundMode === "off"}
            onChange={onChangeSoundMode}
          />
          Desativado
        </label>
        <label className="notifications-settings__checkbox-row" htmlFor="sound-mode-all">
          <input
            id="sound-mode-all"
            type="radio"
            name="sound-mode"
            value="all"
            checked={soundMode === "all"}
            onChange={onChangeSoundMode}
          />
          Todas as mensagens
        </label>
        <label className="notifications-settings__checkbox-row" htmlFor="sound-mode-mentions">
          <input
            id="sound-mode-mentions"
            type="radio"
            name="sound-mode"
            value="mentions"
            checked={soundMode === "mentions"}
            onChange={onChangeSoundMode}
          />
          Somente menções
        </label>
        <label
          className="notifications-settings__checkbox-row"
          htmlFor="sound-mode-mentions-and-dms"
        >
          <input
            id="sound-mode-mentions-and-dms"
            type="radio"
            name="sound-mode"
            value="mentions_and_dms"
            checked={soundMode === "mentions_and_dms"}
            onChange={onChangeSoundMode}
          />
          Menções e mensagens diretas
        </label>
      </fieldset>

      <div className="notifications-settings__browser-notifications">
        <label
          className="notifications-settings__checkbox-row"
          htmlFor="incoming-call-ringtone-enabled"
        >
          <input
            id="incoming-call-ringtone-enabled"
            type="checkbox"
            checked={incomingCallRingtoneEnabled}
            onChange={onChangeIncomingCallRingtone}
          />
          Tocar som para chamadas recebidas
        </label>
        <button
          type="button"
          className="notifications-settings__browser-notifications-button"
          onClick={playIncomingCallRingtonePreview}
        >
          Testar som de chamada
        </button>
      </div>

      <div className="notifications-settings__browser-notifications">
        {browserPermission === "granted" && (
          <p className="notifications-settings__browser-notifications-status">
            Notificações do navegador estão ativadas.
          </p>
        )}
        {browserPermission === "denied" && (
          <>
            <p className="notifications-settings__browser-notifications-status">
              Notificações do navegador foram bloqueadas. Para ativá-las, altere a permissão deste
              site nas configurações do seu navegador.
            </p>
            <button
              type="button"
              className="notifications-settings__browser-notifications-button"
              aria-expanded={showBrowserNotificationHelp}
              onClick={() => setShowBrowserNotificationHelp((shown) => !shown)}
            >
              Como ativar notificações
            </button>
            {showBrowserNotificationHelp && (
              <ol className="notifications-settings__browser-notifications-help">
                <li>Clique no ícone de cadeado ao lado do endereço do site.</li>
                <li>Localize a permissão de notificações.</li>
                <li>Remova o bloqueio ou selecione &quot;Permitir&quot;.</li>
                <li>Recarregue a página ou volte ao NChat.</li>
              </ol>
            )}
          </>
        )}
        {browserPermission === "unsupported" && (
          <p className="notifications-settings__browser-notifications-status">
            {isBrowserNotificationSecureContext()
              ? "Seu navegador não tem suporte a notificações nativas."
              : "As notificações do navegador não estão disponíveis neste endereço. Acesse o NChat por HTTPS ou localhost."}
          </p>
        )}
        {browserPermission === "default" && (
          <>
            <p className="notifications-settings__browser-notifications-status">
              Ative notificações do navegador para ser avisado de novas mensagens mesmo com a aba em
              segundo plano.
            </p>
            <button
              type="button"
              className="notifications-settings__browser-notifications-button"
              onClick={onEnableBrowserNotifications}
            >
              Ativar notificações do navegador
            </button>
          </>
        )}
      </div>
    </div>
  );
}
