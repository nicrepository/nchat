import { useRef, useState } from "react";

import {
  applyConfiguration,
  previewConfigRollback,
  previewConfiguration,
  rollbackConfigVersion,
  type ConfigApplyResult,
  type ConfigPlan,
  type ConfigValue,
} from "../api/configApi";
import { AUTH_POLICY_DOCUMENT } from "./configFields";
import { classify } from "./useAdminQuery";

/** An edit under review, or a version about to be reverted. */
export type ConfigReviewKind = "apply" | "rollback";

/**
 * Exactly what was sent for review, frozen at the moment the review opened.
 *
 * This is the whole of the confirmation guarantee. The form stays live while
 * the dialog is open — an operator can keep typing behind it, and a reload can
 * move the document underneath — so a confirm that rebuilt its payload from
 * current state could apply something the operator never saw. The snapshot is
 * what gets applied; the form does not participate.
 *
 * The two kinds carry different things because they *are* different
 * operations. An edit is a set of values the operator chose. A rollback is the
 * identity of a version: which values it restores, and whether it can still be
 * performed, are derived by the server from the recorded version — never
 * rebuilt here from the history this console happens to be rendering.
 */
export type ConfigReviewSnapshot =
  | { kind: "apply"; revision: number; changes: Record<string, ConfigValue> }
  | { kind: "rollback"; revision: number; versionId: string };

/** A snapshot and the plan the server computed for it, kept together. */
export interface ConfigReview {
  request: ConfigReviewSnapshot;
  plan: ConfigPlan;
}

export interface ConfigReviewFlow {
  review: ConfigReview | null;
  /** True while a preview is in flight, so the form can be held still. */
  previewing: boolean;
  applying: boolean;
  /**
   * True whenever a review is being prepared, shown or written.
   *
   * The form is held still for all three. It is a comfort and not the
   * guarantee — what gets applied is the snapshot the review froze, whatever
   * the form holds by then — but an operator should not be invited to edit
   * behind a dialog that will ignore what they type.
   */
  busy: boolean;
  failure: string | null;
  feedback: string | null;
  /** Reviews the edits the form is holding. */
  open: (values: Record<string, ConfigValue>) => void;
  /** Reviews undoing one recorded version, by identity alone. */
  openRollback: (versionId: string) => void;
  confirm: (reason: string) => void;
  cancel: () => void;
}

interface ConfigReviewOptions {
  /** The revision the form was loaded at, frozen into every snapshot. */
  revision: number;
  /** Called after a successful write, so the screen re-reads what now exists. */
  onApplied: () => void;
}

/**
 * The write interaction of the configuration screen: review, then confirm.
 *
 * One flow with one invariant — nothing is written that was not previewed
 * first, and what is written is exactly what was previewed. Two things enforce
 * it:
 *
 *   - `confirm` sends the snapshot, never the live form;
 *   - a preview generation counter drops answers that arrive out of order, so
 *     a slow first request cannot replace the review a later one produced.
 *
 * The plan always comes from the server. Nothing here computes a diff.
 */
export function useConfigReview({ revision, onApplied }: ConfigReviewOptions): ConfigReviewFlow {
  const [review, setReview] = useState<ConfigReview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [applying, setApplying] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [feedback, setFeedback] = useState<string | null>(null);

  // Which preview the screen is waiting for. Incremented when one starts and
  // when a review is cancelled or completed, so any answer from an earlier
  // generation — including one for a review the operator has since closed — is
  // discarded instead of reopening a dialog nobody asked for.
  const generation = useRef(0);

  const startReview = (snapshot: ConfigReviewSnapshot) => {
    const current = ++generation.current;
    setFailure(null);
    setFeedback(null);
    setPreviewing(true);
    previewFor(snapshot)
      .then((plan) => {
        if (current !== generation.current) return;
        setReview({ request: snapshot, plan });
      })
      .catch((error: unknown) => {
        if (current !== generation.current) return;
        setFailure(classify(error).message);
      })
      .finally(() => {
        if (current !== generation.current) return;
        setPreviewing(false);
      });
  };

  const open: ConfigReviewFlow["open"] = (values) =>
    // Copied, not referenced: the caller's object keeps changing as the form
    // does, and this one must not.
    startReview({ kind: "apply", revision, changes: { ...values } });

  const openRollback: ConfigReviewFlow["openRollback"] = (versionId) =>
    startReview({ kind: "rollback", revision, versionId });

  const confirm = (reason: string) => {
    // No review, or one already being written: a second click must not produce
    // a second mutation.
    if (review === null || applying) return;
    setApplying(true);
    setFailure(null);
    submitReview(review.request, reason)
      .then((result) => {
        generation.current += 1;
        setReview(null);
        setFeedback(appliedMessage(result));
        onApplied();
      })
      .catch((error: unknown) => setFailure(classify(error).message))
      .finally(() => setApplying(false));
  };

  return {
    review,
    previewing,
    applying,
    busy: previewing || applying || review !== null,
    failure,
    feedback,
    open,
    openRollback,
    confirm,
    cancel: () => {
      // Retiring the generation is what stops an answer still in flight from
      // reopening the dialog after the operator closed it.
      generation.current += 1;
      setReview(null);
      setPreviewing(false);
      setFailure(null);
    },
  };
}

function appliedMessage(result: ConfigApplyResult): string {
  if (!result.applied) {
    return "Nada foi alterado: os valores enviados já eram os atuais.";
  }
  return `Configuração aplicada. Revisão ${result.revision}.`;
}

/**
 * Which preview a snapshot asks for.
 *
 * A rollback has its own endpoint rather than being sent through the generic
 * one carrying historical values, so the plan an operator reviews is computed
 * by the same server-side derivation the confirmed rollback uses.
 */
function previewFor(snapshot: ConfigReviewSnapshot) {
  if (snapshot.kind === "rollback") {
    return previewConfigRollback(snapshot.versionId, snapshot.revision);
  }
  return previewConfiguration({
    document: AUTH_POLICY_DOCUMENT,
    expectedRevision: snapshot.revision,
    changes: snapshot.changes,
  });
}

/**
 * Which write a confirmed review performs, from the snapshot alone.
 *
 * A rollback names the version and lets the server recompute — and re-verify —
 * the values it restores; an apply sends the reviewed changes. Both echo the
 * revision the snapshot froze, so both are refused the same way when the
 * document has moved since. The preview is informative; this is the request the
 * server revalidates atomically.
 */
function submitReview(snapshot: ConfigReviewSnapshot, reason: string) {
  if (snapshot.kind === "rollback") {
    return rollbackConfigVersion(snapshot.versionId, snapshot.revision, reason);
  }
  return applyConfiguration({
    document: AUTH_POLICY_DOCUMENT,
    expectedRevision: snapshot.revision,
    reason,
    changes: snapshot.changes,
  });
}
