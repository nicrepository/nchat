import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";

import "./ProfilePage.css";
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
  uploadAvatar,
} from "./profileApi";
import { refreshSelfProfile } from "./selfProfile";

/**
 * ProfilePage — lets the signed-in user set, replace or remove their avatar.
 *
 * The persisted avatar is loaded from GET /api/auth/me on mount, so it survives a
 * page reload and can be removed later (never derived from the last local upload
 * alone). A newly chosen file is previewed locally and uploaded only on an
 * explicit action; the preview object URL is always revoked exactly once.
 *
 * Two distinct removal semantics are surfaced as two buttons so a single control
 * never carries an ambiguous meaning:
 *   - "Cancelar nova imagem" discards the local selection (no server call);
 *   - "Remover avatar" deletes the persisted avatar (server DELETE).
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

  // loadProfile performs the fetch and settles state asynchronously (in the
  // promise callbacks), so it never calls setState synchronously — safe to run
  // from the mount effect without triggering a cascading render.
  const loadProfile = useCallback((signal?: AbortSignal) => {
    fetchMyProfile(signal)
      .then((profile) => {
        setPersistedAvatarUrl(profile.avatarUrl ?? "");
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

  const busy = uploading || removing;
  const hasPersistedAvatar = persistedAvatarUrl !== undefined && persistedAvatarUrl !== "";
  const shownImage = previewUrl ?? (hasPersistedAvatar ? persistedAvatarUrl : null);

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
      </section>
    </main>
  );
}
