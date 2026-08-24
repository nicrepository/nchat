import type { HealthState } from "../api/observabilityApi";
import { presentState } from "../lib/healthStatus";

interface HealthStateBadgeProps {
  state: HealthState;
}

/**
 * One health state, rendered so that colour is never the information.
 *
 * Three carriers, and each covers a case the others do not: the word is what a
 * screen reader announces and what survives a screenshot; the shape
 * distinguishes the states on a monochrome display or for an operator with a
 * colour vision deficiency; the colour is reinforcement for everyone else. The
 * shape is `aria-hidden` because announcing "black square" after "Indisponível"
 * is noise, not information.
 */
export default function HealthStateBadge({ state }: HealthStateBadgeProps) {
  const presentation = presentState(state);
  return (
    <span className={`admin-health-badge admin-health-badge--${presentation.tone}`}>
      <span className="admin-health-badge__mark" aria-hidden="true">
        {presentation.mark}
      </span>
      {presentation.label}
    </span>
  );
}
