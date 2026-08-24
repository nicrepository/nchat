import type { DiagnosticReport } from "../api/integrationsApi";
import { categoryLabel, formatLatency } from "../lib/healthStatus";
import { presentDiagnostic, stageLabel } from "../lib/integrationView";

interface DiagnosticStepsProps {
  report: DiagnosticReport;
  /** Ties the result to the button that produced it, for screen readers. */
  labelledBy: string;
}

/**
 * One diagnostic run, stage by stage.
 *
 * The shape is the point of the whole feature: "Erro 500" tells an operator
 * nothing, while "DNS: OK · TCP: OK · TLS: falha — certificado não confiável ·
 * autenticação: não executada" tells them what to fix and what was never
 * reached. So every declared stage has a row, including the ones that did not
 * run, and a stage with no measured duration shows an em dash rather than 0 ms.
 *
 * Nothing here is remote text. The detail is a sentence the server chose from
 * its own vocabulary and the category is from a closed set; no response body,
 * header, hostname or stack trace can reach this component.
 */
export default function DiagnosticSteps({ report, labelledBy }: DiagnosticStepsProps) {
  const overall = presentDiagnostic(report.status);
  return (
    <div className="admin-diagnostic" aria-labelledby={labelledBy} data-testid="diagnostic-report">
      <p className={`admin-diagnostic__summary admin-diagnostic__summary--${overall.tone}`}>
        <span aria-hidden="true">{overall.mark}</span> <strong>{overall.label}</strong> ·{" "}
        {report.summary}
      </p>
      {report.version !== "" && (
        <p className="admin-table__muted">Versão informada: {report.version}</p>
      )}
      <ol className="admin-diagnostic__steps">
        {report.steps.map((step) => (
          <li key={step.stage} data-testid={`diagnostic-step-${step.stage}`}>
            <DiagnosticStepRow
              stage={step.stage}
              status={step.status}
              category={step.category}
              detail={step.detail}
              latencyMS={step.latencyMS}
            />
          </li>
        ))}
      </ol>
    </div>
  );
}

interface DiagnosticStepRowProps {
  stage: string;
  status: DiagnosticReport["steps"][number]["status"];
  category: string;
  detail: string;
  latencyMS: number | null;
}

function DiagnosticStepRow({ stage, status, category, detail, latencyMS }: DiagnosticStepRowProps) {
  const presentation = presentDiagnostic(status);
  return (
    <>
      <span className={`admin-diagnostic__mark admin-diagnostic__mark--${presentation.tone}`}>
        <span aria-hidden="true">{presentation.mark}</span>
        <span className="admin-visually-hidden">{presentation.label}:</span>
      </span>
      <span className="admin-diagnostic__stage">{stageLabel(stage)}</span>
      <span className="admin-diagnostic__verdict">{presentation.label}</span>
      <span className="admin-diagnostic__latency">{formatLatency(latencyMS)}</span>
      {detail !== "" && <span className="admin-diagnostic__detail">{detail}</span>}
      {category !== "" && (
        <span className="admin-chip admin-diagnostic__category">{categoryLabel(category)}</span>
      )}
    </>
  );
}
