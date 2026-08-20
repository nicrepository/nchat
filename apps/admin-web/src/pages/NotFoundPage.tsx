import { Link } from "react-router";

/**
 * Answer for any path the console does not implement, including a deep link
 * into a section that does not exist yet.
 *
 * It states plainly that nothing is there. It does not offer an action, because
 * there is none to offer.
 */
export default function NotFoundPage() {
  return (
    <section aria-labelledby="admin-not-found-title">
      <h1 id="admin-not-found-title">Seção não disponível</h1>
      <p>Esta área do console administrativo ainda não foi implementada.</p>
      <Link to="/">Voltar para a visão geral</Link>
    </section>
  );
}
