import { useState } from "react";
import type { Candidate } from "./meetingTypes";

/** Inspect large selections a page at a time, without discarding any identity. */
export default function MeetingSelection({
  participants,
  locked,
  toggle,
}: Readonly<{
  participants: Candidate[];
  locked: boolean;
  toggle: (person: Candidate) => void;
}>) {
  const [requestedPage, setPage] = useState(0);
  const lastPage = Math.max(0, Math.ceil(participants.length / 50) - 1);
  const page = Math.min(requestedPage, lastPage);
  return (
    <div className="meeting-selected">
      <p>
        {participants.length} selecionado(s) · Página {page + 1} de {lastPage + 1}
      </p>
      <ul aria-label="Participantes selecionados">
        {participants.slice(page * 50, (page + 1) * 50).map((person) => (
          <li key={person.user_id}>
            <span>{person.display_name}</span>
            <button
              type="button"
              disabled={locked}
              aria-label={`Remover ${person.display_name}`}
              onClick={() => toggle(person)}
            >
              Remover
            </button>
          </li>
        ))}
      </ul>
      {lastPage > 0 && (
        <div className="meeting-actions">
          <button type="button" disabled={page === 0} onClick={() => setPage(page - 1)}>
            Selecionados anteriores
          </button>
          <button type="button" disabled={page === lastPage} onClick={() => setPage(page + 1)}>
            Próximos selecionados
          </button>
        </div>
      )}
    </div>
  );
}
