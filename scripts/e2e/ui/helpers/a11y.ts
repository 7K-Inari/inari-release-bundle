// axe-core accessibility scanning for the @a11y spec layer (taxonomy §1.3
// execution-profile label; docs/e2e/test-taxonomy.md). One scanner config for
// the whole layer: WCAG 2.0/2.1 A+AA, zero unexpected violations.
//
// Accepted exclusions are rule-level and each MUST carry a linked tracking
// issue (quarantine-discipline mirror: a violation without an exclusion fails
// the spec; an exclusion without an issue is a config error; an exclusion
// whose rule no longer violates is stale and must be removed — fix-forward in
// inari-ui is the default). Selector-level exclusions are deliberately not
// supported: masking a subtree would hide future regressions inside it.

import { AxeBuilder } from "@axe-core/playwright";
import type { Page } from "@playwright/test";

/** Standard ruleset for every scan in the a11y layer. */
export const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] as const;

/** A rule-level violation accepted pending an inari-ui fix. */
export interface AcceptedViolation {
  /** axe rule id (e.g. "color-contrast"). */
  rule: string;
  /** Tracking issue (e.g. "7K-Inari/inari-ui#123"). Required, non-empty. */
  issue: string;
  /** Why the violation is accepted. */
  reason: string;
}

/** The subset of an axe violation the specs assert/format on. */
export interface ScanViolation {
  id: string;
  impact: string | null;
  description: string;
  nodes: Array<{ target: unknown }>;
}

/**
 * Split scan results by the accepted-exclusion list. Throws when an accepted
 * entry is malformed (empty rule, issue, or reason) — an exclusion without a
 * tracking issue and rationale is a config error, not a test outcome.
 */
export function partitionViolations(
  violations: ScanViolation[],
  accepted: AcceptedViolation[],
): { expected: ScanViolation[]; unexpected: ScanViolation[] } {
  for (const a of accepted) {
    if (!a.rule || !a.issue) {
      throw new Error(
        `a11y accepted violation for rule "${a.rule || "?"}" must carry a tracking issue`,
      );
    }
    if (!a.reason) {
      throw new Error(
        `a11y accepted violation for rule "${a.rule}" must carry a reason`,
      );
    }
  }
  const acceptedRules = new Set(accepted.map((a) => a.rule));
  const expected: ScanViolation[] = [];
  const unexpected: ScanViolation[] = [];
  for (const v of violations) {
    (acceptedRules.has(v.id) ? expected : unexpected).push(v);
  }
  return { expected, unexpected };
}

/**
 * Accepted rules that produced NO violation in this scan — the underlying
 * issue was fixed (or the DOM changed) and the exclusion must be removed.
 */
export function staleExclusions(
  violations: ScanViolation[],
  accepted: AcceptedViolation[],
): AcceptedViolation[] {
  const present = new Set(violations.map((v) => v.id));
  return accepted.filter((a) => !present.has(a.rule));
}

/** Render violations for an expect() message; "" when there are none. */
export function formatViolations(violations: ScanViolation[]): string {
  if (violations.length === 0) return "";
  return violations
    .map((v) => {
      const targets = v.nodes.map((n) => String(n.target));
      const shown = targets.slice(0, 3);
      const more = targets.length > shown.length ? ` (and ${targets.length - shown.length} more)` : "";
      return `- [${v.impact ?? "?"}] ${v.id}: ${v.description}\n  targets: ${shown.join(", ")}${more}`;
    })
    .join("\n");
}

/**
 * Run the standard WCAG scan against the currently rendered page. Callers
 * partition/format the result; this helper never asserts (README rule 2 —
 * specs own every expect).
 */
export async function scanA11y(page: Page): Promise<ScanViolation[]> {
  const result = await new AxeBuilder({ page }).withTags([...WCAG_TAGS]).analyze();
  return result.violations.map((v) => ({
    id: v.id,
    impact: v.impact ?? null,
    description: v.description,
    nodes: v.nodes.map((n) => ({ target: n.target })),
  }));
}
