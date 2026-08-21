import { useCallback, useState } from "react";

import {
  listAntiSpamPolicies,
  updateAntiSpamPolicy,
  type AntiSpamPolicy,
  type PolicyBounds,
} from "../api/managementApi";
import Pagination from "../components/Pagination";
import QueryStates from "../components/QueryStates";
import { rateLimitWarning, validateRateLimit } from "../lib/policyForm";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useAdminSession } from "../session/useAdminSession";

const PAGE_SIZE = 25;

/**
 * Anti-spam: the per-user message budget of each workspace (RF-19).
 *
 * This is the only rate limit in the platform that is configurable at runtime.
 * The others — reaction, edit, conversation-creation and link-scan budgets — are
 * read from the environment at boot by the service that enforces them, so
 * changing one is a rollout rather than a click, and this screen does not
 * pretend otherwise by offering a field for it.
 */
export default function SecurityPage() {
  const { can } = useAdminSession();
  const canManage = can("admin.security.manage");

  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const cursor = cursors[cursors.length - 1];

  const load = useCallback(
    (signal: AbortSignal) => listAntiSpamPolicies(cursor, PAGE_SIZE, signal),
    [cursor],
  );
  const query = useAdminQuery(load);
  const page = query.data;

  return (
    <section aria-labelledby="admin-security-title">
      <h1 id="admin-security-title">Segurança e políticas</h1>
      <p className="admin-lead">
        Limite anti-spam por usuário, por workspace. A janela é fixa em um minuto e não é
        configurável; não existe valor que desative a proteção.
      </p>

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhum workspace encontrado."
        isEmpty={page !== null && page.items.length === 0}
        onRetry={query.reload}
      />

      {query.status === "ready" && page !== null && page.items.length > 0 && (
        <>
          <ul className="admin-policy-list">
            {page.items.map((policy) => (
              <AntiSpamForm
                key={policy.workspace.id}
                policy={policy}
                bounds={page.bounds}
                editable={canManage}
              />
            ))}
          </ul>
          <Pagination
            count={page.items.length}
            hasMore={page.hasMore}
            canGoBack={cursors.length > 1}
            busy={false}
            onNext={() => setCursors((stack) => [...stack, page.nextCursor])}
            onPrevious={() => setCursors((stack) => stack.slice(0, -1))}
          />
        </>
      )}

      <h2>Controles que não são editáveis aqui</h2>
      <ul className="admin-fixed-list">
        <li>
          <strong>Janela do limite</strong> — fixa em 60 segundos pelo limitador compartilhado.
        </li>
        <li>
          <strong>Limites de reação, edição e criação de conversa</strong> — definidos por variável
          de ambiente do chat-service; alterá-los exige rollout.
        </li>
        <li>
          <strong>Orçamento de verificação de links</strong> — definido por variável de ambiente;
          alterá-lo exige rollout.
        </li>
      </ul>
    </section>
  );
}

interface AntiSpamFormProps {
  policy: AntiSpamPolicy;
  bounds: PolicyBounds;
  editable: boolean;
}

/**
 * One workspace's limit.
 *
 * It does not reload the listing after a save. The response carries the stored
 * value, so the form adopts that and stays mounted with its confirmation
 * visible — reloading would unmount the form mid-feedback and show a skeleton
 * where the operator expected to read what was saved.
 */
function AntiSpamForm({ policy, bounds, editable }: AntiSpamFormProps) {
  const [value, setValue] = useState(String(policy.messageRateLimitPerMinute));
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: "ok" | "error"; text: string } | null>(null);

  const validation = validateRateLimit(value, bounds);
  const warning = validation === null ? rateLimitWarning(Number(value), bounds) : null;
  const inputID = `anti-spam-${policy.workspace.id}`;

  const submit = () => {
    if (saving || validation !== null) return;
    setSaving(true);
    setFeedback(null);
    updateAntiSpamPolicy(policy.workspace.id, Number(value))
      .then((saved) => {
        setValue(String(saved.messageRateLimitPerMinute));
        setFeedback({
          tone: "ok",
          text: `Limite salvo: ${saved.messageRateLimitPerMinute} mensagens/minuto por usuário.`,
        });
      })
      .catch((error: unknown) => setFeedback({ tone: "error", text: classify(error).message }))
      .finally(() => setSaving(false));
  };

  return (
    <li className="admin-policy">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <h3>{policy.workspace.name}</h3>
        <p className="admin-table__muted">
          <code>{policy.workspace.slug}</code> · {policy.workspace.status}
        </p>

        <label className="admin-field" htmlFor={inputID}>
          <span>Mensagens por minuto, por usuário</span>
        </label>
        <div className="admin-field__row">
          <input
            id={inputID}
            type="text"
            inputMode="numeric"
            value={value}
            disabled={!editable || saving}
            aria-describedby={`${inputID}-help`}
            aria-invalid={validation !== null}
            onChange={(event) => setValue(event.target.value)}
          />
          <span className="admin-field__unit">msg/min</span>
          {editable && (
            <button type="submit" className="admin-button" disabled={saving || validation !== null}>
              {saving ? "Salvando…" : "Salvar"}
            </button>
          )}
        </div>
        <p id={`${inputID}-help`} className="admin-field__help">
          Entre {bounds.min} e {bounds.max}. Padrão da plataforma: {bounds.default}. O mínimo é{" "}
          {bounds.min} e não zero, para que o anti-spam nunca sirva de silenciamento.
        </p>

        {validation !== null && (
          <p role="alert" className="admin-alert">
            {validation}
          </p>
        )}
        {warning !== null && (
          <p className="admin-warning" role="status" data-testid="admin-policy-warning">
            {warning}
          </p>
        )}
        {feedback !== null && (
          <p
            className={feedback.tone === "ok" ? "admin-notice" : "admin-alert"}
            role={feedback.tone === "ok" ? "status" : "alert"}
            data-testid="admin-feedback"
          >
            {feedback.text}
          </p>
        )}
        {!editable && (
          <p className="admin-notice">
            Somente leitura: falta a permissão <code>admin.security.manage</code>.
          </p>
        )}
      </form>
    </li>
  );
}
