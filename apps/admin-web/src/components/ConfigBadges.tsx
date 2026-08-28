import type { ConfigSetting } from "../api/configApi";
import { applyLabel, classLabel, sourceLabel } from "../lib/configFields";

interface ConfigBadgesProps {
  setting: ConfigSetting;
}

/**
 * What an operator needs to know about a setting before touching it: its impact
 * class, where the authoritative value lives, and how a change would reach the
 * platform.
 *
 * All three come from the server. The console does not derive them, because the
 * class is a statement about how the platform behaves and guessing it from the
 * shape of a field is how a screen ends up promising that a change is live when
 * it is waiting for a rollout.
 */
export default function ConfigBadges({ setting }: ConfigBadgesProps) {
  return (
    <p className="admin-config__badges">
      <span className="admin-badge admin-badge--strong">{classLabel(setting.configClass)}</span>
      <span className="admin-badge">{sourceLabel(setting.source)}</span>
      <span className="admin-badge">{applyLabel(setting.apply)}</span>
      {setting.sensitive && (
        <span className="admin-badge admin-badge--strong" data-testid="config-badge-secret">
          Credencial
        </span>
      )}
      {setting.ownerService !== "" && <span className="admin-chip">{setting.ownerService}</span>}
    </p>
  );
}
