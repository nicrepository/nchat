import { useEffect, useState } from "react";

import type { ConfigCatalog, ConfigSetting } from "../api/configApi";
import { draftsFrom, hasInvalidDraft, isDirty } from "./configFields";

/**
 * The in-progress edits of the configuration form, and the warning that goes
 * with having some.
 *
 * They are one responsibility because they answer one question: what has the
 * operator typed that the server does not yet have. The unsaved-changes warning
 * is part of that question and not a separate feature — it exists precisely
 * while the answer is non-empty.
 */
export interface ConfigDrafts {
  drafts: Record<string, string>;
  /** At least one field differs from what is stored. */
  dirty: boolean;
  /** At least one changed field would be refused before it was even sent. */
  invalid: boolean;
  edit: (key: string, value: string) => void;
  discard: () => void;
}

/**
 * Edits, tagged with the catalog load they were made against.
 *
 * The tag is what makes a reload authoritative without a second render pass: if
 * it does not match the current data, there are no edits.
 */
interface TaggedEdits {
  source: ConfigCatalog | null;
  drafts: Record<string, string>;
}

export function useConfigDrafts(
  catalog: ConfigCatalog | null,
  settings: ConfigSetting[],
): ConfigDrafts {
  const [edits, setEdits] = useState<TaggedEdits>({ source: null, drafts: {} });

  // Derived during render from the catalog the edits belong to, the same way
  // useAdminQuery derives its status. After a successful write the catalog is a
  // new object, the edits no longer match it, and the form is showing the
  // stored values again — so "unsaved" stops being true because nothing is,
  // without an effect having to write state a render later.
  const drafts = edits.source === catalog ? edits.drafts : draftsFrom(settings);
  const dirty = isDirty(settings, drafts);

  useEffect(() => {
    if (!dirty) return undefined;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  return {
    drafts,
    dirty,
    invalid: hasInvalidDraft(settings, drafts),
    edit: (key, value) => setEdits({ source: catalog, drafts: { ...drafts, [key]: value } }),
    discard: () => setEdits({ source: null, drafts: {} }),
  };
}
