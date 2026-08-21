import { visibleNavItems } from "../shell/navigation";
import { useAdminSession } from "../session/useAdminSession";

/**
 * The landing page of the console.
 *
 * It states what this session actually is — who, where, what it may do, and
 * when it ends — and claims nothing else. The sections that do not exist yet
 * are listed as such rather than dressed up as features.
 */
export default function OverviewPage() {
  const { bootstrap, can } = useAdminSession();
  if (bootstrap === null) return null;

  const sections = visibleNavItems(can);
  const available = sections.filter((item) => item.path !== undefined);

  return (
    <section aria-labelledby="admin-overview-title">
      <h1 id="admin-overview-title">Visão geral</h1>
      <p className="admin-lead">
        Sessão administrativa ativa para {bootstrap.identity.display_name}.
      </p>

      <h2>Sessão</h2>
      <dl className="admin-definitions">
        <dt>Ambiente</dt>
        <dd>{bootstrap.environment}</dd>
        <dt>Expira por inatividade em</dt>
        <dd>{new Date(bootstrap.session.idle_expires_at).toLocaleString("pt-BR")}</dd>
        <dt>Expira definitivamente em</dt>
        <dd>{new Date(bootstrap.session.absolute_expires_at).toLocaleString("pt-BR")}</dd>
      </dl>

      <h2>Permissões efetivas</h2>
      {bootstrap.capabilities.length === 0 ? (
        <p>Nenhuma permissão administrativa atribuída.</p>
      ) : (
        <ul className="admin-capability-list">
          {bootstrap.capabilities.map((capability) => (
            <li key={capability}>
              <code>{capability}</code>
            </li>
          ))}
        </ul>
      )}

      <h2>Seções</h2>
      <p>
        {available.length} de {sections.length} seções visíveis já estão implementadas. As demais
        aparecem como indisponíveis até que sejam entregues.
      </p>
    </section>
  );
}
