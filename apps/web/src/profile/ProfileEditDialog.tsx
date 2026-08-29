import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./ProfileEditDialog.css";
import {
  supportedTimezones,
  validateBio,
  validateDisplayName,
  validateShortProfileField,
  validateTimezone,
} from "./profileForm";
import { updateProfile, UpdateProfileError, type SelfProfile } from "./profileApi";
import { refreshSelfProfile } from "./selfProfile";

// ~419 timezone options; computed once at module scope to avoid rebuilding
// the entire list on every keystroke in any field.
const TIMEZONE_OPTION_ELEMENTS = supportedTimezones().map((tz) => (
  <option key={tz} value={tz}>
    {tz}
  </option>
));

interface ProfileEditDialogProps {
  profile: SelfProfile;
  onClose: () => void;
  onSaved: (profile: SelfProfile) => void;
}

const titleId = "profile-edit-title";
const errorId = "profile-edit-error";

export default function ProfileEditDialog({ profile, onClose, onSaved }: ProfileEditDialogProps) {
  const [displayName, setDisplayName] = useState(profile.displayName);
  const [jobTitle, setJobTitle] = useState(profile.jobTitle ?? "");
  const [bio, setBio] = useState(profile.bio ?? "");
  const [timezone, setTimezone] = useState(profile.timezone ?? "");
  const [customStatus, setCustomStatus] = useState(profile.customStatus ?? "");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  const dialogRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const jobTitleInputRef = useRef<HTMLInputElement>(null);
  const timezoneInputRef = useRef<HTMLSelectElement>(null);
  const customStatusInputRef = useRef<HTMLInputElement>(null);
  const bioInputRef = useRef<HTMLTextAreaElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    openerRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    nameInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
      if (openerRef.current?.isConnected) openerRef.current.focus();
    };
  }, []);

  function requestClose() {
    if (!submittingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled)",
    );
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

  const trimmedName = displayName.trim();
  const nameError = validateDisplayName(trimmedName);
  const jobTitleError = validateShortProfileField(jobTitle, "Cargo");
  const bioError = validateBio(bio);
  const timezoneError = validateTimezone(timezone);
  const customStatusError = validateShortProfileField(customStatus, "Status");
  const trimmedJobTitle = jobTitle.trim();
  const trimmedBio = bio.trim();
  const trimmedCustomStatus = customStatus.trim();
  const dirty =
    trimmedName !== profile.displayName ||
    trimmedJobTitle !== (profile.jobTitle ?? "") ||
    trimmedBio !== (profile.bio ?? "") ||
    timezone !== (profile.timezone ?? "") ||
    trimmedCustomStatus !== (profile.customStatus ?? "");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const message = validateDisplayName(trimmedName);
    if (message || jobTitleError || bioError || timezoneError || customStatusError) {
      setError(message ?? jobTitleError ?? timezoneError ?? customStatusError ?? bioError);
      if (message) nameInputRef.current?.focus();
      else if (jobTitleError) jobTitleInputRef.current?.focus();
      else if (timezoneError) timezoneInputRef.current?.focus();
      else if (customStatusError) customStatusInputRef.current?.focus();
      else bioInputRef.current?.focus();
      return;
    }
    if (submittingRef.current) return;
    submittingRef.current = true;
    setPending(true);
    setError(null);
    try {
      const saved = await updateProfile({
        displayName: trimmedName !== profile.displayName ? trimmedName : undefined,
        jobTitle: trimmedJobTitle !== (profile.jobTitle ?? "") ? trimmedJobTitle : undefined,
        bio: trimmedBio !== (profile.bio ?? "") ? trimmedBio : undefined,
        timezone: timezone !== (profile.timezone ?? "") ? timezone : undefined,
        customStatus:
          trimmedCustomStatus !== (profile.customStatus ?? "") ? trimmedCustomStatus : undefined,
      });
      if (!mountedRef.current) {
        refreshSelfProfile();
        return;
      }
      onSaved(saved);
      onClose();
    } catch (failure) {
      if (!mountedRef.current) return;
      const message =
        failure instanceof UpdateProfileError
          ? failure.message
          : "Não foi possível salvar o perfil.";
      setError(message);
      nameInputRef.current?.focus();
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    <div className="profile-edit__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="profile-edit"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="profile-edit__title">
          Editar perfil
        </h2>
        <form onSubmit={submit}>
          <label className="profile-edit__label" htmlFor="profile-edit-name">
            Nome de exibição
          </label>
          <input
            ref={nameInputRef}
            id="profile-edit-name"
            type="text"
            value={displayName}
            disabled={pending}
            aria-invalid={nameError !== null}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => {
              setDisplayName(e.target.value);
              setError(null);
            }}
          />

          {/*
           * job_title stays user-editable: PATCH /auth/me's request DTO
           * (services/auth-service/internal/http/profile_handler.go) accepts it
           * from the client today and there is no admin-authority field/source
           * for it anywhere in the backend — no department/role/email exists in
           * the self-profile contract at all. This is the documented exception
           * issue #672 §1.3 asks for: kept editable by deliberate reading of the
           * actual contract, not by defaulting to the prototype's read-only mock.
           */}
          <label className="profile-edit__label" htmlFor="profile-edit-job-title">
            Cargo
          </label>
          <input
            ref={jobTitleInputRef}
            id="profile-edit-job-title"
            type="text"
            value={jobTitle}
            disabled={pending}
            aria-invalid={jobTitleError !== null}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => setJobTitle(e.target.value)}
          />

          <label className="profile-edit__label" htmlFor="profile-edit-timezone">
            Fuso horário
          </label>
          <select
            ref={timezoneInputRef}
            id="profile-edit-timezone"
            value={timezone}
            disabled={pending}
            aria-invalid={timezoneError !== null}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => setTimezone(e.target.value)}
          >
            <option value="">Não definido</option>
            {TIMEZONE_OPTION_ELEMENTS}
          </select>

          <label className="profile-edit__label" htmlFor="profile-edit-custom-status">
            Status customizado
          </label>
          <input
            ref={customStatusInputRef}
            id="profile-edit-custom-status"
            type="text"
            value={customStatus}
            disabled={pending}
            aria-invalid={customStatusError !== null}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => setCustomStatus(e.target.value)}
          />

          <label className="profile-edit__label" htmlFor="profile-edit-bio">
            Biografia
          </label>
          <textarea
            ref={bioInputRef}
            id="profile-edit-bio"
            value={bio}
            disabled={pending}
            aria-invalid={bioError !== null}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => setBio(e.target.value)}
          />

          {error && (
            <p id={errorId} role="alert" className="profile-edit__error">
              {error}
            </p>
          )}

          <div className="profile-edit__actions">
            <button
              type="button"
              className="profile-edit__cancel"
              disabled={pending}
              onClick={requestClose}
            >
              Cancelar
            </button>
            <button
              type="submit"
              className="profile-edit__submit"
              disabled={!dirty || pending}
              aria-busy={pending}
            >
              {pending ? "Salvando…" : "Salvar alterações"}
            </button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}
