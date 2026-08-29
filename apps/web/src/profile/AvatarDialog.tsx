/**
 * AvatarDialog — "Trocar foto" (issue #672 §1.5).
 *
 * Ports ProfilePage.tsx's avatar-card logic (persistedAvatarUrl/selectedFile/
 * previewUrl/onSelect/onUpload/onRemove/discardSelection) into a dedicated
 * portal dialog, mirroring RenameChannelDialog's shell (portal, backdrop,
 * role="dialog", Escape, focus trap, a *Ref guard that makes each mutation
 * single). Two differences from the page version:
 *   - "what's currently persisted" is a `currentAvatarUrl` prop the caller
 *     supplies (via useSelfProfile()), not page-owned state — this dialog
 *     does not own that truth;
 *   - the native <input type="file"> is hidden and triggered only by the
 *     visible "Selecionar arquivo" button, never itself the primary control.
 *
 * Behavior preserved exactly: client-side MIME/size validation before staging
 * (backend remains authoritative), the preview object URL revoked exactly
 * once (unmount or replacement), refreshSelfProfile() called only after the
 * server confirms a mutation (never optimistically), and the selection/
 * current state preserved on failure so the user can retry.
 */

import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";

import "./AvatarDialog.css";
import {
  AVATAR_ACCEPTED_TYPES,
  AVATAR_MAX_BYTES,
  AvatarUploadError,
  removeAvatar,
  uploadAvatar,
} from "./profileApi";
import { refreshSelfProfile } from "./selfProfile";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";

interface AvatarDialogProps {
  currentAvatarUrl?: string;
  onClose: () => void;
}

const titleId = "avatar-dialog-title";

export default function AvatarDialog({ currentAvatarUrl, onClose }: AvatarDialogProps) {
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [networkError, setNetworkError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  // One guard for both mutually exclusive mutations. Separate guards allow an
  // upload and a removal fired in the same React batch to race each other.
  const mutationRef = useRef(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const mountedRef = useRef(true);
  const selectButtonRef = useRef<HTMLButtonElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    openerRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    selectButtonRef.current?.focus();
    return () => {
      mountedRef.current = false;
      if (openerRef.current?.isConnected) openerRef.current.focus();
    };
  }, []);

  // Revoke the current preview object URL whenever it changes or on unmount —
  // exactly once, since state holds a single URL at a time.
  useEffect(() => {
    return () => {
      if (previewUrl) URL.revokeObjectURL(previewUrl);
    };
  }, [previewUrl]);

  const clearSelectedAvatarState = useCallback(() => {
    setPreviewUrl(null);
    setSelectedFile(null);
  }, []);

  const clearFileInput = useCallback(() => {
    if (fileInputRef.current) fileInputRef.current.value = "";
  }, []);

  const discardSelection = useCallback(() => {
    clearSelectedAvatarState();
    clearFileInput();
  }, [clearSelectedAvatarState, clearFileInput]);

  function requestClose() {
    // Closing mid-mutation would leave the user unable to see whether it landed.
    if (!mutationRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled)");
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const onSelect = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>) => {
      // Capture the File BEFORE clearing input.value below, which also
      // clears the native FileList.
      const input = event.currentTarget;
      const file = input.files?.[0] ?? null;
      setSelectionError(null);
      setNetworkError(null);
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
      setSelectedFile(file);
      setPreviewUrl(URL.createObjectURL(file));
    },
    [clearSelectedAvatarState, clearFileInput],
  );

  const onUpload = useCallback(async () => {
    if (!selectedFile || mutationRef.current) return;
    mutationRef.current = true;
    setUploading(true);
    setNetworkError(null);
    try {
      await uploadAvatar(selectedFile);
      // Only now, with the change persisted, does every other screen showing
      // the profile (the sidebar) get told to re-read it.
      refreshSelfProfile();
      if (mountedRef.current) onClose();
    } catch (error) {
      if (!mountedRef.current) return;
      setNetworkError(
        error instanceof AvatarUploadError ? error.message : "Não foi possível enviar o avatar.",
      );
    } finally {
      mutationRef.current = false;
      if (mountedRef.current) setUploading(false);
    }
  }, [selectedFile, onClose]);

  const onRemove = useCallback(async () => {
    if (mutationRef.current) return;
    mutationRef.current = true;
    setRemoving(true);
    setNetworkError(null);
    try {
      await removeAvatar();
      refreshSelfProfile();
      if (mountedRef.current) {
        discardSelection();
        onClose();
      }
    } catch (error) {
      if (!mountedRef.current) return;
      setNetworkError(
        error instanceof AvatarUploadError ? error.message : "Não foi possível remover o avatar.",
      );
    } finally {
      mutationRef.current = false;
      if (mountedRef.current) setRemoving(false);
    }
  }, [discardSelection, onClose]);

  const busy = uploading || removing;
  const hasPersistedAvatar = Boolean(currentAvatarUrl);
  const shownSrc = previewUrl ?? currentAvatarUrl;

  return createPortal(
    <div className="avatar-dialog__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="avatar-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="avatar-dialog__title">
          Trocar foto
        </h2>
        <div className="avatar-dialog__preview">
          {previewUrl ? (
            <img
              src={previewUrl}
              alt="Pré-visualização do avatar"
              className="avatar-dialog__preview-img"
            />
          ) : (
            <PersonAvatarImage
              src={shownSrc}
              initials=""
              imgClassName="avatar-dialog__preview-img"
            />
          )}
        </div>
        <p className="avatar-dialog__hint">JPEG ou PNG, até 5 MB.</p>
        <input
          ref={fileInputRef}
          type="file"
          accept="image/jpeg,image/png"
          className="avatar-dialog__file-input"
          hidden
          aria-hidden="true"
          tabIndex={-1}
          onChange={onSelect}
          disabled={busy}
        />
        <div className="avatar-dialog__actions">
          <button
            ref={selectButtonRef}
            type="button"
            className="avatar-dialog__btn"
            onClick={() => fileInputRef.current?.click()}
            disabled={busy}
          >
            Selecionar arquivo
          </button>
          {selectedFile && (
            <button
              type="button"
              className="avatar-dialog__btn"
              onClick={discardSelection}
              disabled={busy}
            >
              Cancelar seleção
            </button>
          )}
          <button
            type="button"
            className="avatar-dialog__btn avatar-dialog__btn--primary"
            onClick={() => void onUpload()}
            disabled={!selectedFile || busy}
            aria-busy={uploading}
          >
            {uploading ? "Enviando…" : "Enviar avatar"}
          </button>
          <button
            type="button"
            className="avatar-dialog__btn avatar-dialog__btn--destructive"
            onClick={() => void onRemove()}
            disabled={busy || !hasPersistedAvatar}
            aria-busy={removing}
          >
            {removing ? "Removendo…" : "Remover avatar"}
          </button>
          <button
            type="button"
            className="avatar-dialog__btn"
            onClick={requestClose}
            disabled={busy}
          >
            Fechar
          </button>
        </div>
        <div role="status" aria-live="polite" className="avatar-dialog__status">
          {selectionError && <span className="avatar-dialog__error">{selectionError}</span>}
          {networkError && <span className="avatar-dialog__error">{networkError}</span>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
