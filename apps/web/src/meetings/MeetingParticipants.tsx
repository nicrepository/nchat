import MeetingSelection from "./MeetingSelection";
import type { MeetingSession } from "./meetingSession";
import type { Candidate, MeetingDraft } from "./meetingTypes";
import { useMeetingCandidates } from "./useMeetingCandidates";

export default function MeetingParticipants({
  session,
  organizerId,
  draft,
  locked,
}: Readonly<{
  session: MeetingSession;
  organizerId: string;
  draft: MeetingDraft;
  locked: boolean;
}>) {
  const picker = useMeetingCandidates(session, organizerId, !locked);
  function toggle(person: Candidate) {
    const current = session.getSnapshot();
    const selected = current.draft.participants;
    const participants = selected.some((p) => p.user_id === person.user_id)
      ? selected.filter((p) => p.user_id !== person.user_id)
      : [...selected, person];
    session.update({ seeded: true, draft: { ...current.draft, participants } });
  }
  return (
    <section
      id="meeting-participants"
      tabIndex={-1}
      aria-describedby="meeting-participants-error"
      aria-labelledby="meeting-participants-heading"
      className="meeting-participants"
    >
      <h3 id="meeting-participants-heading">Participantes</h3>
      {!session.target.direct && (
        <label className="meeting-check">
          <input
            type="checkbox"
            checked={draft.inviteAll}
            disabled={locked}
            onChange={(e) =>
              session.update({
                draft: { ...session.getSnapshot().draft, inviteAll: e.target.checked },
              })
            }
          />
          Convidar todos os membros elegíveis
        </label>
      )}
      {draft.inviteAll && (
        <p className="meeting-hint">
          Após criar a reunião, o servidor convidará os elegíveis naquele momento (até 500). A
          operação é separada da criação; novas tentativas podem incluir novos membros elegíveis.
        </p>
      )}
      <label htmlFor="meeting-search">Buscar participantes pelo início do nome</label>
      <input
        id="meeting-search"
        type="search"
        maxLength={100}
        value={picker.query}
        disabled={locked}
        onChange={(e) => picker.search(e.target.value)}
      />
      {draft.participants.length > 0 && (
        <MeetingSelection participants={draft.participants} locked={locked} toggle={toggle} />
      )}
      <CandidateResults
        picker={picker}
        participants={draft.participants}
        toggle={toggle}
        locked={locked}
      />
    </section>
  );
}

function CandidateResults({
  picker,
  participants,
  toggle,
  locked,
}: Readonly<{
  picker: ReturnType<typeof useMeetingCandidates>;
  participants: Candidate[];
  toggle: (person: Candidate) => void;
  locked: boolean;
}>) {
  return (
    <div aria-live="polite">
      {picker.page.status === "loading" && !locked && <p role="status">Buscando participantes…</p>}
      {picker.page.status === "error" && (
        <p role="alert">
          {picker.page.error}{" "}
          <button type="button" disabled={locked} onClick={picker.retry}>
            Tentar busca novamente
          </button>
        </p>
      )}
      {picker.page.status === "ready" && (
        <>
          {picker.page.items.length === 0 && (
            <p>
              Nenhum participante elegível encontrado. Você pode agendar sem convidados adicionais.
            </p>
          )}
          <ul className="meeting-candidates" aria-label="Participantes elegíveis">
            {picker.page.items.map((person) => (
              <li key={person.user_id}>
                <label className="meeting-check">
                  <input
                    type="checkbox"
                    disabled={locked}
                    checked={participants.some((p) => p.user_id === person.user_id)}
                    onChange={() => toggle(person)}
                  />
                  {person.display_name}
                </label>
              </li>
            ))}
          </ul>
          {picker.page.next_cursor && (
            <button type="button" disabled={locked} onClick={picker.next}>
              Próxima página de participantes
            </button>
          )}
          {picker.query && (
            <button type="button" disabled={locked} onClick={() => picker.search("")}>
              Limpar busca
            </button>
          )}
        </>
      )}
    </div>
  );
}
