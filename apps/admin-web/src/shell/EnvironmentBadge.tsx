import type { AdminEnvironment } from "../api/adminApi";

const LABELS: Record<AdminEnvironment, string> = {
  DEVELOPMENT: "DEVELOPMENT",
  STAGING: "STAGING",
  PRODUCTION: "PRODUCTION",
};

interface EnvironmentBadgeProps {
  environment: AdminEnvironment;
}

/**
 * The persistent environment indicator.
 *
 * The value comes from the bootstrap payload, which the deployment's own
 * configuration produced. Nothing here reads `window.location`, a query string
 * or stored state — an operator must not be able to make the console claim it
 * is somewhere safer than it is, and neither must an attacker.
 *
 * The environment is stated in text, not only in colour, so it survives a
 * colour-blind reader, a monochrome display and a screenshot.
 */
export default function EnvironmentBadge({ environment }: EnvironmentBadgeProps) {
  return (
    <span
      className={`admin-env admin-env--${environment.toLowerCase()}`}
      data-testid="admin-environment"
      data-environment={environment}
    >
      <span className="admin-visually-hidden">Ambiente: </span>
      {LABELS[environment] ?? environment}
    </span>
  );
}
