import type { SessionOwner } from "./useSessionOwner";

/** What a screen shows instead of its data while there is no settled owner. */
export default function SessionPending({
  session,
  className,
}: {
  session: SessionOwner;
  className: string;
}) {
  if (session.failed && session.retry)
    return (
      <div className={className} role="alert">
        Não foi possível confirmar sua sessão.{" "}
        <button type="button" data-reading-recovery="true" onClick={session.retry}>
          Tentar novamente
        </button>
      </div>
    );
  return (
    <p className={className} role="status">
      Confirmando sua sessão…
    </p>
  );
}
