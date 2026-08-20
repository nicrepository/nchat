import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";

import "./ProfilePage.css";
import { validateDisplayName } from "./profileForm";
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
  AVATAR_ACCEPTED_TYPES,
  AVATAR_MAX_BYTES,
  AvatarUploadError,
  fetchMyProfile,
  removeAvatar,
  updateDisplayName,
  UpdateDisplayNameError,
  uploadAvatar,
} from "./profileApi";
import { refreshSelfProfile } from "./selfProfile";

/**
 * ProfilePage — lets the signed-in user edit their display name and set,
 * replace or remove their avatar.
 *
 * The persisted profile is loaded from GET /api/auth/me on mount, so it
 * survives a page reload. A newly chosen avatar file is previewed locally and
 * uploaded only on an explicit action; the preview object URL is always
 * revoked exactly once.
 *
 * Two distinct removal semantics are surfaced as two buttons so a single control
 * never carries an ambiguous meaning:
 *   - "Cancelar nova imagem" discards the local selection (no server call);
 *   - "Remover avatar" deletes the persisted avatar (server DELETE).
 *
 * Saving the display name (ID 7, cronograma 19/08) updates only this screen's
 * local state from the server's response. Propagating the new name to the
 * sidebar and elsewhere (refreshSelfProfile) is explicitly out of scope here —
 * that synchronization is ID 13 (20/08) — so the screen must reflect the
 * persisted value on its own rather than relying on that propagation.
 */
export default function ProfilePage() {
  // persistedAvatarUrl: undefined = still loading / unknown, "" = confirmed none.
  const [persistedAvatarUrl, setPersistedAvatarUrl] = useState<string | undefined>(undefined);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [loadingProfile, setLoadingProfile] = useState(true);
  const [profileLoadError, setProfileLoadError] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [networkError, setNetworkError] = useState<string | null>(null);
  const [notice, setNotice] = useState<"saved" | "removed" | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  // Local-only preference (no backend endpoint for it yet), read once at
  // mount — every write goes through setSoundNotificationMode immediately
  // below, so this state never drifts from what's persisted.
  const [soundMode, setSoundModeState] = useState<SoundNotificationMode>(() =>
    getSoundNotificationMode(),
  );
  // Notification.permission is the canonical state (never mirrored to
  // localStorage) — read fresh at mount and re-read on visibilitychange so a
  // permission the user changes via the browser's own UI (e.g. the address
  // bar lock icon) shows up here without requiring a reload.
  const [browserPermission, setBrowserPermission] = useState<BrowserNotificationPermission>(() =>
    getBrowserNotificationPermission(),
  );
  // Whether the "how to unblock" steps are expanded, in the 'denied' state —
  // there's no functional retry there (see onEnableBrowserNotifications).
  const [showBrowserNotificationHelp, setShowBrowserNotificationHelp] = useState(false);

  // persistedDisplayName: undefined = still loading / unknown.
  const [persistedDisplayName, setPersistedDisplayName] = useState<string | undefined>(undefined);
  const [nameDraft, setNameDraft] = useState("");
  const [savingName, setSavingName] = useState(false);
  const [nameNetworkError, setNameNetworkError] = useState<string | null>(null);
  const [nameNotice, setNameNotice] = useState(false);
  const nameInputRef = useRef<HTMLInputElement>(null);
  // State updates are asynchronous, so `savingName` cannot stop a second submit
  // fired in the same tick; this ref is what actually makes the save single.
  const savingNameRef = useRef(false);

  // loadProfile performs the fetch and settles state asynchronously (in the
  // promise callbacks), so it never calls setState synchronously — safe to run
  // from the mount effect without triggering a cascading render.
  const loadProfile = useCallback((signal?: AbortSignal) => {
    fetchMyProfile(signal)
      .then((profile) => {
        setPersistedAvatarUrl(profile.avatarUrl ?? "");
        setPersistedDisplayName(profile.displayName);
        setNameDraft(profile.displayName);
        setLoadingProfile(false);
      })
      .catch((error: unknown) => {
        if (signal?.aborted || (error as { name?: string })?.name === "AbortError") return;
        setProfileLoadError(true);
        setLoadingProfile(false);
      });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    loadProfile(controller.signal);
    return () => controller.abort();
  }, [loadProfile]);

  // Retry is a user action (not an effect), so setting loading here is fine.
  const retryLoadProfile = useCallback(() => {
    setLoadingProfile(true);
    setProfileLoadError(false);
    loadProfile();
  }, [loadProfile]);

  // Revoke the current preview object URL whenever it changes or on unmount —
  // exactly once, since state holds a single URL at a time.
  useEffect(() => {
    return () => {
      if (previewUrl) URL.revokeObjectURL(previewUrl);
    };
  }, [previewUrl]);

  // clearSelectedAvatarState drops the staged file and its preview. Revocation
  // of the object URL is owned solely by the previewUrl effect below (which runs
  // its cleanup whenever previewUrl changes or the component unmounts), so each
  // URL is revoked exactly once — never here as well. It does NOT touch the
  // <input>, so it is safe to call after a new file has been captured from it.
  const clearSelectedAvatarState = useCallback(() => {
    setPreviewUrl(null);
    setSelectedFile(null);
  }, []);

  // clearFileInput zeroes the native input value. On a real <input type="file">
  // this also empties its FileList, which is why onSelect must capture the File
  // BEFORE calling this. Zeroing it also lets the same file be picked again
  // (a change event only fires when the value actually changes).
  const clearFileInput = useCallback(() => {
    if (fileInputRef.current) fileInputRef.current.value = "";
  }, []);

  // discardSelection is the combined reset used by the non-onSelect callers
  // (cancel button, post-upload, post-remove), which have no file to preserve.
  const discardSelection = useCallback(() => {
    clearSelectedAvatarState();
    clearFileInput();
  }, [clearSelectedAvatarState, clearFileInput]);

  const onSelect = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>) => {
      // Capture the File and the element FIRST: clearing input.value below also
      // clears the native FileList, so reading it afterwards would lose the file.
      // Index access works on both a real FileList and the array test harnesses
      // inject; a real FileList also supports .item(0) equivalently.
      const input = event.currentTarget;
      const file = input.files?.[0] ?? null;

      setNotice(null);
      setNetworkError(null);
      setSelectionError(null);
      // Reset the prior selection before validating, so an invalid choice can
      // never leave a previously valid file hidden in state.
      clearSelectedAvatarState();
      clearFileInput();

      if (!file) return; // cancelled selector or empty selection: nothing staged.

      if (!AVATAR_ACCEPTED_TYPES.includes(file.type as (typeof AVATAR_ACCEPTED_TYPES)[number])) {
        setSelectionError("Escolha uma imagem JPEG ou PNG.");
        return;
      }
      if (file.size > AVATAR_MAX_BYTES) {
        setSelectionError("A imagem é muito grande (máx. 5 MB).");
        return;
      }
      // Only after successful validation do we stage the file and its preview.
      setSelectedFile(file);
      setPreviewUrl(URL.createObjectURL(file));
    },
    [clearSelectedAvatarState, clearFileInput],
  );

  const onUpload = useCallback(async () => {
    if (!selectedFile) return;
    setUploading(true);
    setNetworkError(null);
    setNotice(null);
    try {
      const url = await uploadAvatar(selectedFile);
      setPersistedAvatarUrl(url); // server-decided, same-origin URL — not the local blob.
      // Only now, with the change persisted, does every other screen showing the
      // profile (the sidebar) get told to re-read it. On failure nothing below
      // runs, so nothing the server did not confirm is ever published.
      refreshSelfProfile();
      discardSelection();
      setNotice("saved");
    } catch (error) {
      const message =
        error instanceof AvatarUploadError ? error.message : "Não foi possível enviar o avatar.";
      setNetworkError(message); // selection is preserved so the user can retry.
    } finally {
      setUploading(false);
    }
  }, [selectedFile, discardSelection]);

  const onRemove = useCallback(async () => {
    setRemoving(true);
    setNetworkError(null);
    setNotice(null);
    try {
      await removeAvatar();
      setPersistedAvatarUrl(""); // only clear the visible avatar AFTER the server confirms.
      refreshSelfProfile();
      discardSelection();
      setNotice("removed");
    } catch (error) {
      const message =
        error instanceof AvatarUploadError ? error.message : "Não foi possível remover o avatar.";
      setNetworkError(message); // persisted avatar stays visible on failure.
    } finally {
      setRemoving(false);
    }
  }, [discardSelection]);

  const onChangeSoundMode = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    const next = event.currentTarget.value as SoundNotificationMode;
    setSoundNotificationMode(next);
    setSoundModeState(next);
  }, []);

  // Only ever invoked by the 'default'-state button's onClick below —
  // requestPermission() must never run on mount, on login, or when a message
  // arrives. Once denied, browsers never reopen the native prompt from a
  // second call, which is why 'denied' offers instructions instead of a
  // retry (see the JSX below).
  const onEnableBrowserNotifications = useCallback(async () => {
    const result = await requestBrowserNotificationPermission();
    setBrowserPermission(result);
  }, []);

  // Notification.permission can change outside this page's own control (the
  // user unblocks it via the browser's own lock-icon UI, in this tab or
  // another) — re-read it whenever the user could plausibly have done that:
  // this tab regaining focus, or the whole document becoming visible again.
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

  const busy = uploading || removing;
  const hasPersistedAvatar = persistedAvatarUrl !== undefined && persistedAvatarUrl !== "";
  const shownImage = previewUrl ?? (hasPersistedAvatar ? persistedAvatarUrl : null);

  const trimmedNameDraft = nameDraft.trim();
  // Reported while the user types rather than only on submit, so shortening a
  // too-long name shows the fix immediately. The empty case is guarded here
  // (an untouched or just-cleared field should not shout "required" before
  // any attempt to save) and is instead caught by validateDisplayName itself
  // inside onSaveName.
  const nameError = trimmedNameDraft === "" ? null : validateDisplayName(trimmedNameDraft);
  const nameDirty = persistedDisplayName !== undefined && nameDraft !== persistedDisplayName;

  const onNameChange = useCallback((event: React.ChangeEvent<HTMLInputElement>) => {
    setNameDraft(event.target.value);
    setNameNetworkError(null);
    setNameNotice(false);
  }, []);

  const onCancelName = useCallback(() => {
    setNameDraft(persistedDisplayName ?? "");
    setNameNetworkError(null);
    setNameNotice(false);
  }, [persistedDisplayName]);

  /**
   * Saves the display name, at most once per submission (savingNameRef makes
   * the guard synchronous; savingName only drives the disabled UI). On
   * success, the field is set to exactly what the server persisted rather
   * than what was typed — the two coincide in the normal case, but the
   * response is what is actually true. Only this screen's own state is
   * updated: no sidebar/session propagation here, that is ID 13's job.
   */
  const onSaveName = useCallback(async () => {
    if (savingNameRef.current) return;
    const trimmed = nameDraft.trim();
    const message = validateDisplayName(trimmed);
    if (message) {
      nameInputRef.current?.focus();
      return; // nameError is already shown live; nothing else to do.
    }
    savingNameRef.current = true;
    setSavingName(true);
    setNameNetworkError(null);
    setNameNotice(false);
    try {
      const profile = await updateDisplayName(trimmed);
      setPersistedDisplayName(profile.displayName);
      setNameDraft(profile.displayName);
      setNameNotice(true);
    } catch (error) {
      const errMessage =
        error instanceof UpdateDisplayNameError
          ? error.message
          : "Não foi possível atualizar o nome.";
      setNameNetworkError(errMessage); // draft is preserved so the user can retry.
    } finally {
      savingNameRef.current = false;
      setSavingName(false);
    }
  }, [nameDraft]);

  /**
   * The form's only entry point, for both Enter and the Save button — Enter
   * submits from the input via the native <form>, and the guards in
   * onSaveName apply the same way either path is taken.
   */
  const onNameFormSubmit = useCallback(
    (event: FormEvent<HTMLFormElement>) => {
      event.preventDefault();
      void onSaveName();
    },
    [onSaveName],
  );

  return (
    <main className="profile-page" aria-labelledby="profile-title">
      <header className="profile-page__header">
        <h1 id="profile-title" className="profile-page__title">
          Meu perfil
        </h1>
        <Link to="/chat" className="profile-page__back">
          Voltar ao chat
        </Link>
      </header>

      <section className="profile-page__name-card" aria-label="Nome de exibição">
        <form className="profile-page__name-form" onSubmit={onNameFormSubmit}>
          <label className="profile-page__field-label" htmlFor="profile-display-name">
            Nome de exibição
          </label>
          <input
            ref={nameInputRef}
            id="profile-display-name"
            type="text"
            className="profile-page__input"
            autoComplete="name"
            value={nameDraft}
            disabled={savingName || loadingProfile}
            aria-invalid={nameError !== null}
            aria-describedby={nameError ? "profile-display-name-error" : undefined}
            onChange={onNameChange}
          />
          {nameError && (
            <p id="profile-display-name-error" className="profile-page__error" role="alert">
              {nameError}
            </p>
          )}

          <div className="profile-page__buttons">
            <button
              type="submit"
              className="profile-page__btn profile-page__btn--primary"
              disabled={!nameDirty || nameError !== null || savingName || loadingProfile}
              aria-busy={savingName}
            >
              {savingName ? "Salvando…" : "Salvar nome"}
            </button>
            {nameDirty && (
              <button
                type="button"
                className="profile-page__btn"
                onClick={onCancelName}
                disabled={savingName}
              >
                Cancelar
              </button>
            )}
          </div>

          <div className="profile-page__status" role="status" aria-live="polite">
            {nameNetworkError && <span className="profile-page__error">{nameNetworkError}</span>}
            {nameNotice && <span className="profile-page__ok">Nome atualizado.</span>}
          </div>
        </form>
      </section>

      <section className="profile-page__avatar-card" aria-label="Avatar">
        <div className="profile-page__avatar-preview">
          {loadingProfile ? (
            <span className="profile-page__avatar-loading" role="status" aria-label="Carregando" />
          ) : shownImage ? (
            <img
              className="profile-page__avatar-img"
              src={shownImage}
              alt="Pré-visualização do avatar"
              referrerPolicy="no-referrer"
            />
          ) : (
            <span className="profile-page__avatar-placeholder" aria-hidden="true">
              ?
            </span>
          )}
        </div>

        <div className="profile-page__avatar-actions">
          <p className="profile-page__hint">JPEG ou PNG, até 5 MB.</p>
          <input
            ref={fileInputRef}
            type="file"
            accept="image/jpeg,image/png"
            className="profile-page__file"
            aria-label="Escolher imagem de avatar"
            onChange={onSelect}
            disabled={busy || loadingProfile}
          />
          <div className="profile-page__buttons">
            <button
              type="button"
              className="profile-page__btn profile-page__btn--primary"
              onClick={onUpload}
              disabled={!selectedFile || busy}
            >
              {uploading ? "Enviando…" : "Enviar avatar"}
            </button>
            {selectedFile && (
              <button
                type="button"
                className="profile-page__btn"
                onClick={discardSelection}
                disabled={busy}
              >
                Cancelar nova imagem
              </button>
            )}
            <button
              type="button"
              className="profile-page__btn"
              onClick={onRemove}
              disabled={busy || loadingProfile || !hasPersistedAvatar}
            >
              {removing ? "Removendo…" : "Remover avatar"}
            </button>
          </div>

          <div className="profile-page__status" role="status" aria-live="polite">
            {selectionError && <span className="profile-page__error">{selectionError}</span>}
            {networkError && <span className="profile-page__error">{networkError}</span>}
            {profileLoadError && (
              <span className="profile-page__error">
                Não foi possível carregar o perfil.{" "}
                <button type="button" className="profile-page__retry" onClick={retryLoadProfile}>
                  Tentar novamente
                </button>
              </span>
            )}
            {notice === "saved" && <span className="profile-page__ok">Avatar atualizado.</span>}
            {notice === "removed" && <span className="profile-page__ok">Avatar removido.</span>}
          </div>
        </div>
      </section>

      <section className="profile-page__notifications-card" aria-label="Notificações">
        <fieldset className="profile-page__sound-modes">
          <legend className="profile-page__sound-modes-legend">Som de notificações</legend>
          <label className="profile-page__checkbox-row" htmlFor="sound-mode-off">
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
          <label className="profile-page__checkbox-row" htmlFor="sound-mode-all">
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
          <label className="profile-page__checkbox-row" htmlFor="sound-mode-mentions">
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
          <label className="profile-page__checkbox-row" htmlFor="sound-mode-mentions-and-dms">
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

        <div className="profile-page__browser-notifications">
          {browserPermission === "granted" && (
            <p className="profile-page__browser-notifications-status">
              Notificações do navegador estão ativadas.
            </p>
          )}
          {browserPermission === "denied" && (
            <>
              <p className="profile-page__browser-notifications-status">
                Notificações do navegador foram bloqueadas. Para ativá-las, altere a permissão deste
                site nas configurações do seu navegador.
              </p>
              <button
                type="button"
                className="profile-page__browser-notifications-button"
                aria-expanded={showBrowserNotificationHelp}
                onClick={() => setShowBrowserNotificationHelp((shown) => !shown)}
              >
                Como ativar notificações
              </button>
              {showBrowserNotificationHelp && (
                <ol className="profile-page__browser-notifications-help">
                  <li>Clique no ícone de cadeado ao lado do endereço do site.</li>
                  <li>Localize a permissão de notificações.</li>
                  <li>Remova o bloqueio ou selecione &quot;Permitir&quot;.</li>
                  <li>Recarregue a página ou volte ao NChat.</li>
                </ol>
              )}
            </>
          )}
          {browserPermission === "unsupported" && (
            <p className="profile-page__browser-notifications-status">
              {isBrowserNotificationSecureContext()
                ? "Seu navegador não tem suporte a notificações nativas."
                : "As notificações do navegador não estão disponíveis neste endereço. Acesse o NChat por HTTPS ou localhost."}
            </p>
          )}
          {browserPermission === "default" && (
            <>
              <p className="profile-page__browser-notifications-status">
                Ative notificações do navegador para ser avisado de novas mensagens mesmo com a aba
                em segundo plano.
              </p>
              <button
                type="button"
                className="profile-page__browser-notifications-button"
                onClick={onEnableBrowserNotifications}
              >
                Ativar notificações do navegador
              </button>
            </>
          )}
        </div>
      </section>
    </main>
  );
}
