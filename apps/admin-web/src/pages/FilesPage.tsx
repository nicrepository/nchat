import { useCallback, useState } from "react";

import {
  listUploadPolicies,
  updateUploadPolicy,
  type PolicyBounds,
  type UploadPolicy,
} from "../api/managementApi";
import Pagination from "../components/Pagination";
import QueryStates from "../components/QueryStates";
import { uploadWarning, validateUploadMiB } from "../lib/policyForm";
import { bytesToMiB, formatBytes, isWholeMiB, mibToBytes } from "../lib/units";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useAdminSession } from "../session/useAdminSession";

const PAGE_SIZE = 25;

/**
 * Human labels for the controls the API reports as deployment-managed.
 *
 * An unknown id falls back to the id itself rather than being dropped: if the
 * server starts naming a control this build does not know, the operator should
 * see that it exists, not be shown a shorter list.
 */
const DEPLOYMENT_LABELS: Record<string, string> = {
  malware_scanning: "Verificação de malware (ClamAV) — definida no deployment do file-service",
  upload_concurrency: "Limite de uploads simultâneos — definido no deployment do file-service",
};

/**
 * Attachment size policy (RF-32), per workspace.
 *
 * This is the maximum size of *one* file, not a storage quota, and it is the
 * only upload-related value that is configurable at runtime. It narrows nothing
 * that protects the platform: it cannot exceed the gateway's static ceiling, it
 * cannot disable malware scanning, and there is no value meaning "unlimited" —
 * the minimum is 1 MiB precisely so a size control can never be used to switch
 * attachments off.
 */
export default function FilesPage() {
  const { can } = useAdminSession();
  const canManage = can("admin.infrastructure.manage");

  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const cursor = cursors[cursors.length - 1];

  const load = useCallback(
    (signal: AbortSignal) => listUploadPolicies(cursor, PAGE_SIZE, signal),
    [cursor],
  );
  const query = useAdminQuery(load);
  const page = query.data;

  return (
    <section aria-labelledby="admin-files-title">
      <h1 id="admin-files-title">Arquivos e armazenamento</h1>
      <p className="admin-lead">
        Tamanho máximo de um anexo, por workspace. É um limite por arquivo, não uma cota de
        armazenamento.
      </p>

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhum workspace encontrado."
        isEmpty={page !== null && page.items.length === 0}
        onRetry={query.reload}
      />

      {query.status === "ready" && page !== null && (
        <>
          <ul className="admin-policy-list">
            {page.items.map((policy) => (
              <UploadForm
                key={policy.workspace.id}
                policy={policy}
                bounds={page.bounds}
                editable={canManage}
              />
            ))}
          </ul>
          {page.items.length > 0 && (
            <Pagination
              count={page.items.length}
              hasMore={page.hasMore}
              canGoBack={cursors.length > 1}
              busy={false}
              onNext={() => setCursors((stack) => [...stack, page.nextCursor])}
              onPrevious={() => setCursors((stack) => stack.slice(0, -1))}
            />
          )}

          <h2>Controles que não são editáveis aqui</h2>
          <ul className="admin-fixed-list">
            <li>
              <strong>Teto do gateway</strong> — {formatBytes(page.gatewayHardCapBytes)} por
              requisição, estático na infraestrutura. Nenhum limite de workspace pode ultrapassá-lo.
            </li>
            {page.deploymentManaged.map((control) => (
              <li key={control}>{DEPLOYMENT_LABELS[control] ?? control}</li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

interface UploadFormProps {
  policy: UploadPolicy;
  bounds: PolicyBounds;
  editable: boolean;
}

/**
 * One workspace's attachment size limit.
 *
 * Like the anti-spam form, it does not reload the listing after a save: the
 * response carries the stored value, and reloading would unmount the form
 * mid-feedback.
 */
function UploadForm({ policy, bounds, editable }: UploadFormProps) {
  const editableValue = isWholeMiB(policy.maxUploadBytes);
  const [value, setValue] = useState(
    editableValue ? String(bytesToMiB(policy.maxUploadBytes)) : "",
  );
  // The limit as the *server* last confirmed it, which is what "atual" means.
  //
  // It is state rather than the prop because this form deliberately does not
  // reload the listing after a save — reloading would unmount it mid-feedback.
  // Seeded from the prop and advanced only by a successful response, so a
  // failed save leaves it showing the value that is really stored, and a typed
  // number never becomes "atual" before the backend agrees.
  const [confirmedBytes, setConfirmedBytes] = useState(policy.maxUploadBytes);
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: "ok" | "error"; text: string } | null>(null);

  const inputID = `upload-${policy.workspace.id}`;
  const validation = validateUploadMiB(value, bounds);
  const bytes = validation === null ? mibToBytes(Number(value)) : null;
  const warning = bytes === null ? null : uploadWarning(bytes, bounds);

  // A stored value that is not a whole MiB cannot be shown in this field
  // without being changed. Rather than round it — which would let an ordinary
  // save overwrite a limit the administrator never touched — the form refuses
  // to edit and says why. The correction is explicit, in the database.
  if (!editableValue) {
    return (
      <li className="admin-policy">
        <h3>{policy.workspace.name}</h3>
        <p role="alert" className="admin-alert">
          Limite armazenado em estado inválido: {policy.maxUploadBytes} bytes não é um número
          inteiro de MiB. Este formulário não altera o valor para evitar sobrescrever uma política
          que ninguém editou. A correção é feita no banco.
        </p>
      </li>
    );
  }

  const submit = () => {
    if (saving || validation !== null || bytes === null) return;
    setSaving(true);
    setFeedback(null);
    updateUploadPolicy(policy.workspace.id, bytes)
      .then((saved) => {
        // Everything on screen now comes from the response, not from what was
        // typed: the field, the confirmation and the "atual" label agree
        // because they are the same server-confirmed number.
        setValue(String(bytesToMiB(saved.maxUploadBytes)));
        setConfirmedBytes(saved.maxUploadBytes);
        setFeedback({ tone: "ok", text: `Limite salvo: ${formatBytes(saved.maxUploadBytes)}.` });
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
          <code>{policy.workspace.slug}</code> · atual: {formatBytes(confirmedBytes)}
        </p>

        <label className="admin-field" htmlFor={inputID}>
          <span>Tamanho máximo por arquivo</span>
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
          <span className="admin-field__unit">MiB</span>
          {editable && (
            <button type="submit" className="admin-button" disabled={saving || validation !== null}>
              {saving ? "Salvando…" : "Salvar"}
            </button>
          )}
        </div>
        <p id={`${inputID}-help`} className="admin-field__help">
          Entre {bytesToMiB(bounds.min)} e {bytesToMiB(bounds.max)} MiB, em números inteiros.
          Padrão: {bytesToMiB(bounds.default)} MiB. Valores fora dessa regra são recusados, nunca
          arredondados.
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
            Somente leitura: falta a permissão <code>admin.infrastructure.manage</code>.
          </p>
        )}
      </form>
    </li>
  );
}
