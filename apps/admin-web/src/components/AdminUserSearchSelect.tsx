import { useCallback, useId, useRef, useState } from "react";

import QueryStates from "./QueryStates";
import { useAdminQuery } from "../lib/useAdminQuery";
import { useDebouncedValue } from "../lib/useDebouncedValue";

/**
 * A person, as a picker needs to show one.
 *
 * `id` is the only field the API consumers actually send; everything else is
 * what makes the row readable. Deliberately not the administrative user record:
 * this is a control for choosing a person, not a second user directory.
 */
export interface UserOption {
  id: string;
  displayName: string;
  secondary: string;
  avatarUrl?: string;
  /** A short qualifier — a workspace role, a status — shown beside the name. */
  hint?: string;
}

interface AdminUserSearchSelectProps {
  label: string;
  placeholder: string;
  /** Runs the server-side search. Must be stable; wrap it in useCallback. */
  search: (term: string, signal: AbortSignal) => Promise<UserOption[]>;
  selected: UserOption | null;
  onSelect: (user: UserOption | null) => void;
  disabled?: boolean;
  emptyLabel?: string;
  /** Extra guidance rendered under the field. */
  help?: string;
}

/**
 * The minimum characters before a search runs.
 *
 * One character matches most of a workspace and tells the operator nothing, so
 * the request is not worth making. It also keeps the field from firing on the
 * keystroke that clears it.
 */
const MIN_TERM_LENGTH = 2;

/**
 * Choose a person by searching for them.
 *
 * It exists because both places that needed a person — adding a channel member
 * and filtering channels by who administers them — were asking the operator to
 * type a UUID. An administrative console is operated by people who know
 * colleagues by name, not by identifier.
 *
 * What it owns: the search box, the debounce, the request, the loading/empty/
 * error states, staleness in both directions — a response that arrived for an
 * abandoned request, and results still on screen for a term the operator has
 * already changed — keyboard navigation, and how a person is rendered. What it deliberately does **not** own: which endpoint is
 * called, what happens on selection, or any capability decision — those stay
 * with the consumers, because they are different questions in the two places
 * this is used.
 *
 * The identifier never appears on screen. It travels in `UserOption.id`,
 * reaches the API, and stops there.
 *
 * Selection is a distinct state from typing. `onSelect` fires only when
 * somebody is actually chosen, so a consumer can require a real person before
 * enabling its action — typed text alone is a search, never a value.
 */
export default function AdminUserSearchSelect({
  label,
  placeholder,
  search,
  selected,
  onSelect,
  disabled = false,
  emptyLabel = "Nenhuma pessoa encontrada.",
  help,
}: AdminUserSearchSelectProps) {
  const inputID = useId();
  const listID = `${inputID}-list`;
  const [term, setTerm] = useState("");
  const [open, setOpen] = useState(false);
  // The highlighted row is stored together with the term it belongs to, and
  // resolved during render. Keeping them in one value is what makes "Enter can
  // never pick somebody from the previous search" true without an effect that
  // resets an index after the results have already been drawn.
  const [highlight, setHighlight] = useState({ term: "", index: 0 });
  const inputRef = useRef<HTMLInputElement>(null);

  const debounced = useDebouncedValue(term);
  const searching = debounced.trim().length >= MIN_TERM_LENGTH;
  // What the operator typed, versus what has been searched for. Between a
  // keystroke and the end of the debounce these differ, and everything on
  // screen still describes the previous term.
  const typed = term.trim();
  const stale = typed !== debounced.trim();
  // The panel follows the live term, not the settled one: shortening a term
  // below the minimum has to close the list on the keystroke, not 300ms later.
  const offering = typed.length >= MIN_TERM_LENGTH;

  // A term too short resolves to an empty list without a request. Returning
  // rather than skipping the query keeps one code path for the component and
  // avoids a "loading" state that would never resolve.
  const load = useCallback(
    (signal: AbortSignal) => (searching ? search(debounced.trim(), signal) : Promise.resolve([])),
    [search, debounced, searching],
  );
  const query = useAdminQuery(load);
  // Results the operator can act on. A result set that answers a term nobody is
  // typing any more is not a smaller problem than a wrong one: clicking it, or
  // pressing Enter on it, would select a person the operator never saw offered.
  // So the stale window has no results at all — not results that are merely
  // dimmed — which is what makes click, Enter and ArrowDown+Enter safe by
  // construction rather than by three separate guards.
  const results = stale ? [] : (query.data ?? []);
  // And it reads as loading, because that is what it is. Falling through to the
  // "ready" branch would flash "nenhuma pessoa encontrada" over a search that
  // has not run yet.
  const status = stale ? "loading" : query.status;
  const active =
    highlight.term === debounced && highlight.index < results.length ? highlight.index : 0;
  const moveHighlight = (index: number) => setHighlight({ term: debounced, index });

  const choose = (user: UserOption) => {
    onSelect(user);
    setTerm("");
    setOpen(false);
  };

  const clear = () => {
    onSelect(null);
    setTerm("");
    setOpen(false);
    inputRef.current?.focus();
  };

  if (selected !== null) {
    return (
      <div className="admin-field">
        <span id={`${inputID}-label`}>{label}</span>
        <div className="admin-picker__selected">
          <UserRow user={selected} />
          <button
            type="button"
            className="admin-button admin-button--ghost"
            onClick={clear}
            disabled={disabled}
            aria-describedby={`${inputID}-label`}
          >
            Trocar
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="admin-field admin-picker">
      <label htmlFor={inputID}>{label}</label>
      <input
        id={inputID}
        ref={inputRef}
        type="text"
        role="combobox"
        aria-expanded={open && offering}
        aria-controls={listID}
        aria-autocomplete="list"
        aria-activedescendant={
          open && offering && results.length > 0 ? optionID(inputID, results[active].id) : undefined
        }
        autoComplete="off"
        placeholder={placeholder}
        value={term}
        disabled={disabled}
        aria-describedby={help === undefined ? undefined : `${inputID}-help`}
        onChange={(event) => {
          setTerm(event.target.value);
          setOpen(true);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            setOpen(false);
            return;
          }
          if (results.length === 0) return;
          if (event.key === "ArrowDown") {
            event.preventDefault();
            moveHighlight((active + 1) % results.length);
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            moveHighlight((active - 1 + results.length) % results.length);
          } else if (event.key === "Enter") {
            // Enter picks the highlighted person and never submits the form
            // around it: a search box that submits on Enter is how a half-typed
            // name becomes a request.
            event.preventDefault();
            choose(results[active]);
          }
        }}
      />
      {help !== undefined && (
        <p id={`${inputID}-help`} className="admin-field__help">
          {help}
        </p>
      )}

      {open && offering && (
        <div className="admin-picker__results">
          <QueryStates
            status={status}
            message={query.message}
            empty={emptyLabel}
            isEmpty={results.length === 0}
            onRetry={query.reload}
            skeletonRows={2}
          />
          {status === "ready" && results.length > 0 && (
            <ul className="admin-picker__list" role="listbox" id={listID} aria-label={label}>
              {results.map((user, index) => (
                // The option itself is the control. An interactive element
                // nested inside role="option" is invalid ARIA, and it is also
                // why the combobox pattern keeps focus on the input and points
                // at the highlighted row with aria-activedescendant instead of
                // moving focus into the list.
                <li
                  key={user.id}
                  id={optionID(inputID, user.id)}
                  role="option"
                  aria-selected={index === active}
                  className={`admin-picker__option${index === active ? " admin-picker__option--active" : ""}`}
                  onMouseEnter={() => moveHighlight(index)}
                  onClick={() => choose(user)}
                >
                  <UserRow user={user} />
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * The DOM id of one option.
 *
 * Derived from the person rather than from their position, so
 * aria-activedescendant keeps pointing at the same person when the list is
 * re-ordered by a newer search.
 */
function optionID(inputID: string, userID: string): string {
  return `${inputID}-option-${userID}`;
}

/** How a person is shown. Never the identifier. */
function UserRow({ user }: { user: UserOption }) {
  return (
    <span className="admin-picker__person">
      {user.avatarUrl ? (
        <img className="admin-picker__avatar" src={user.avatarUrl} alt="" />
      ) : (
        <span className="admin-picker__avatar admin-picker__avatar--empty" aria-hidden="true">
          {user.displayName.slice(0, 1).toUpperCase()}
        </span>
      )}
      <span className="admin-picker__names">
        <span className="admin-table__name">{user.displayName}</span>
        <span className="admin-table__muted">{user.secondary}</span>
      </span>
      {user.hint !== undefined && user.hint !== "" && (
        <span className="admin-chip">{user.hint}</span>
      )}
    </span>
  );
}
