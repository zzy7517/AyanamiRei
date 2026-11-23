// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent } from "@multica/core/types";
import { commentMentionCandidates } from "./comment-mention-candidates";

const agents = [
  { id: "a-2", name: "Zeta" },
  { id: "a-1", name: "Lambda" },
] as Agent[];

describe("commentMentionCandidates", () => {
  it("offers agents sorted by name", () => {
    const result = commentMentionCandidates({ query: "", agents });

    expect(result.map((a) => a.id)).toEqual(["a-1", "a-2"]);
  });

  it("filters agents by the query", () => {
    const result = commentMentionCandidates({ query: " lam ", agents });

    expect(result).toEqual([{ id: "a-1", name: "Lambda" }]);
  });
});
