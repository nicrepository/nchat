import type { CallDeviceKind } from "../media/liveKitSession";

const STORAGE_KEY = "nchat.call.device-preference.v1";
const KINDS: CallDeviceKind[] = ["audioinput", "videoinput", "audiooutput"];

export type DevicePreference = Partial<Record<CallDeviceKind, string>>;

/**
 * Which mic/camera/output the user last picked, kept as a plain browser
 * preference (localStorage — same mechanism FloatingCallWindow already uses
 * for its corner) so a floating <-> dedicated handoff can restore the same
 * choice without threading it through callOwnership's lease/epoch/MediaIntent
 * machinery (issue #755): a device choice is never privacy-sensitive on its
 * own — see MediaIntent, which stays the sole authority for mic/camera
 * on/off — so it needs none of that causal ordering. Never sent to the
 * backend.
 */
export function readDevicePreference(): DevicePreference {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const record = parsed as Record<string, unknown>;
    const result: DevicePreference = {};
    for (const kind of KINDS) {
      const value = record[kind];
      if (typeof value === "string" && value.length > 0 && value.length <= 512) {
        result[kind] = value;
      }
    }
    return result;
  } catch {
    return {};
  }
}

export function writeDevicePreference(kind: CallDeviceKind, deviceId: string): void {
  try {
    const next = { ...readDevicePreference(), [kind]: deviceId };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Non-sensitive, optional preference — a storage failure is never fatal.
  }
}
