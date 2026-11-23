// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  PROJECT_SCOPE_MAX_ITEMS,
  isProjectScopeValid,
  projectScopePairError,
  validateProjectScopeBranch,
} from "./project-scope";

const pair = (project_id = "p1", branch = "main") => ({ project_id, branch });

describe("validateProjectScopeBranch", () => {
  it("accepts what the server accepts", () => {
    for (const branch of [
      "main",
      "release/2026-09",
      "team/alice/feat/new-thing",
      "v1.2.3",
      "5e0b1cfa0a7d6a1a0f4b3f2e1d0c9b8a7f6e5d4c",
      "功能/登录",
    ]) {
      expect(validateProjectScopeBranch(branch)).toBeNull();
    }
  });

  it("requires a branch: empty is not the repository default here", () => {
    expect(validateProjectScopeBranch("")).toBe("branch_required");
  });

  it("refuses whitespace at either edge as typed, where the ref helper would trim it", () => {
    for (const branch of [" main", "main ", "\tmain", "main\n"]) {
      expect(validateProjectScopeBranch(branch)).toBe("branch_invalid_characters");
    }
  });

  it.each([
    ["a space inside", "my branch", "branch_invalid_characters"],
    ["a tilde", "main~1", "branch_invalid_characters"],
    ["a colon", "origin:main", "branch_invalid_characters"],
    ["a backslash", "feat\\thing", "branch_invalid_characters"],
    ["a control character", "ma\nin", "branch_invalid_characters"],
    ["a range", "main..dev", "branch_invalid_format"],
    ["reflog syntax", "main@{1}", "branch_invalid_format"],
    ["a lone at-sign", "@", "branch_invalid_format"],
    ["a leading slash", "/main", "branch_invalid_format"],
    ["a trailing slash", "main/", "branch_invalid_format"],
    ["a leading dot", ".hidden", "branch_invalid_format"],
    ["a trailing dot", "main.", "branch_invalid_format"],
    ["a lock suffix", "feat/thing.lock", "branch_invalid_format"],
    ["a dot-prefixed segment", "feat/.hidden", "branch_invalid_format"],
  ])("names the server's reason for %s", (_label, branch, error) => {
    expect(validateProjectScopeBranch(branch)).toBe(error);
  });

  it("counts bytes for the length cap, as the server does", () => {
    expect(validateProjectScopeBranch("a".repeat(255))).toBeNull();
    expect(validateProjectScopeBranch("a".repeat(256))).toBe("branch_too_long");
    // 255 UTF-16 units either way; only the byte count crosses the cap.
    expect(validateProjectScopeBranch("功".repeat(85))).toBeNull();
    expect(validateProjectScopeBranch("功".repeat(86))).toBe("branch_too_long");
  });
});

describe("projectScopePairError", () => {
  it("accepts one project on several branches and refuses the same pair twice", () => {
    const pairs = [pair(), pair("p1", "develop"), pair("p2", "main")];
    expect(pairs.map((_, index) => projectScopePairError(pairs, index))).toEqual([
      null,
      null,
      null,
    ]);
    pairs.push(pair("p1", "develop"));
    expect(projectScopePairError(pairs, 3)).toBe("duplicate");
    // The first of the two is the one that stays.
    expect(projectScopePairError(pairs, 1)).toBeNull();
  });

  it("asks for the project, then the branch, of a pair that lacks them", () => {
    expect(projectScopePairError([pair("", "")], 0)).toBe("project_required");
    expect(projectScopePairError([pair("p1", "")], 0)).toBe("branch_required");
  });

  it("reports a mistyped branch before the duplicate it makes", () => {
    const pairs = [pair("p1", "my branch"), pair("p1", "my branch")];
    expect(projectScopePairError(pairs, 1)).toBe("branch_invalid_characters");
  });

  it("has nothing to report for an index past the list", () => {
    expect(projectScopePairError([pair()], 1)).toBeNull();
  });
});

describe("isProjectScopeValid", () => {
  it("accepts an empty scope and one at the limit, and refuses one past it", () => {
    expect(isProjectScopeValid([])).toBe(true);
    const atLimit = Array.from({ length: PROJECT_SCOPE_MAX_ITEMS }, (_, i) =>
      pair(`p${i}`),
    );
    expect(isProjectScopeValid(atLimit)).toBe(true);
    expect(isProjectScopeValid([...atLimit, pair("extra")])).toBe(false);
  });

  it("is false while any pair has an error", () => {
    expect(isProjectScopeValid([pair("p1", "main"), pair("", "main")])).toBe(false);
    expect(isProjectScopeValid([pair("p1", "main"), pair("p1", "main..x")])).toBe(false);
    expect(isProjectScopeValid([pair("p1", "main"), pair("p1", "main")])).toBe(false);
    // A row just added and not filled in yet blocks the save too.
    expect(isProjectScopeValid([pair("p1", "main"), pair("", "")])).toBe(false);
  });
});
