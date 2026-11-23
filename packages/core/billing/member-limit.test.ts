// @vitest-environment node
import { describe, expect, it } from "vitest";

import { MemberLimitSchema } from "../api/schemas";
import type { MemberLimit } from "../types";
import { memberLimitKeys } from "./member-limit";
import { workspaceKeys } from "../workspace/queries";

const OFF: MemberLimit = { action: "off", limit: null, members: null, pendingInvitations: null };

describe("MemberLimitSchema", () => {
  it("reads an active limit with its counts", () => {
    expect(
      MemberLimitSchema.parse({ action: "enforce", limit: 5, members: 4, pending_invitations: 1 }),
    ).toEqual({ action: "enforce", limit: 5, members: 4, pendingInvitations: 1 });
  });

  it("reads off, an unknown action, or a limit without a value as no limit", () => {
    expect(MemberLimitSchema.parse({ action: "off" })).toEqual(OFF);
    expect(MemberLimitSchema.parse({ action: "block", limit: 5 })).toEqual(OFF);
    expect(MemberLimitSchema.parse({ action: "enforce", limit: "5" })).toEqual(OFF);
  });

  it("treats missing counts of an active limit as zero", () => {
    expect(MemberLimitSchema.parse({ action: "observe", limit: 5 })).toEqual({
      action: "observe",
      limit: 5,
      members: 0,
      pendingInvitations: 0,
    });
  });
});

describe("memberLimitKeys", () => {
  it("lives under the member list so a member change refetches it", () => {
    expect(memberLimitKeys.all("ws-1").slice(0, 3)).toEqual([...workspaceKeys.members("ws-1")]);
  });
});
