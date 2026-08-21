import "./CallPresentation.css";

export default function GlobalCallIndicator({
  title,
  participantCount,
  onReturn,
}: {
  title: string;
  participantCount: number;
  onReturn: () => unknown;
}) {
  return (
    <aside className="global-call-indicator" aria-label={`Chamada com ${title}`}>
      <span className="global-call-indicator__pulse" aria-hidden="true" />
      <div>
        <strong>Chamada aberta em outra aba</strong>
        <span>
          {title} · {participantCount} participante{participantCount === 1 ? "" : "s"}
        </span>
      </div>
      <button type="button" aria-label="Trazer chamada para esta aba" onClick={onReturn}>
        Trazer para cá
      </button>
    </aside>
  );
}
