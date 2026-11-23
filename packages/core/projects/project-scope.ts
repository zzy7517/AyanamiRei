import { GIT_REF_MAX_LENGTH, validateGitRef } from "../github/repo-ref";

/** The most pairs one scope holds, as the server caps it. */
export const PROJECT_SCOPE_MAX_ITEMS = 50;

/** One "project + branch" pair as the scope editor holds it. A request
 *  carries the branch under its wire name, `base_branch`. */
export interface ProjectScopePair {
  project_id: string;
  branch: string;
}

export type ProjectScopeBranchError =
  | "branch_required"
  | "branch_invalid_characters"
  | "branch_invalid_format"
  | "branch_too_long";

export type ProjectScopePairError =
  | ProjectScopeBranchError
  | "project_required"
  | "duplicate";

/**
 * What keeps `branch` from being saved in a scope, or null. It is the rule
 * the server applies to a pair, so nothing is refused only after a
 * round-trip: required, at most `GIT_REF_MAX_LENGTH` bytes, and a git ref.
 *
 * The shared ref helper answers a different question: there an empty ref
 * means "the repository default", and it trims first. A scope branch names a
 * base to branch from, so it is checked as typed. Whitespace at either edge
 * is refused outright where the server would keep an exotic one such as a
 * no-break space: the helper would trim it, and saving a name other than the
 * one on screen is the worse half of that gap.
 */
export function validateProjectScopeBranch(
  branch: string,
): ProjectScopeBranchError | null {
  if (branch === "") return "branch_required";
  if (branch.trim() !== branch) return "branch_invalid_characters";
  // The server counts bytes and the helper UTF-16 units; bytes are never fewer.
  if (utf8Length(branch) > GIT_REF_MAX_LENGTH) return "branch_too_long";
  const validation = validateGitRef(branch);
  if (validation.ok) return null;
  switch (validation.reason) {
    case "invalid_characters":
      return "branch_invalid_characters";
    case "invalid_format":
      return "branch_invalid_format";
    case "too_long":
      return "branch_too_long";
  }
}

function utf8Length(value: string): number {
  let bytes = 0;
  for (const char of value) {
    const code = char.codePointAt(0)!;
    bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
  }
  return bytes;
}

/** What is wrong with the pair at `index`, or null when it can be saved. The
 *  same project may sit in several pairs, each with its own branch; the same
 *  pair twice cannot. Grammar comes before duplicates: of two pairs sharing a
 *  mistyped branch, fixing the branch answers both. */
export function projectScopePairError(
  pairs: readonly ProjectScopePair[],
  index: number,
): ProjectScopePairError | null {
  const pair = pairs[index];
  if (!pair) return null;
  if (pair.project_id === "") return "project_required";
  const branchError = validateProjectScopeBranch(pair.branch);
  if (branchError) return branchError;
  const duplicate = pairs.some(
    (other, otherIndex) =>
      otherIndex < index &&
      other.project_id === pair.project_id &&
      other.branch === pair.branch,
  );
  return duplicate ? "duplicate" : null;
}

/** True when a replace request holding exactly these pairs is accepted. */
export function isProjectScopeValid(pairs: readonly ProjectScopePair[]): boolean {
  return (
    pairs.length <= PROJECT_SCOPE_MAX_ITEMS &&
    pairs.every((_, index) => projectScopePairError(pairs, index) === null)
  );
}
