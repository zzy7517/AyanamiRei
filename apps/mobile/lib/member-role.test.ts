// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { MemberWithUser } from "@multica/core/types";
import { memberRoleOf } from "./member-role";

function member(userId: string, role: MemberWithUser["role"]): MemberWithUser {
  return {
    id: `m-${userId}`,
    workspace_id: "ws-1",
    user_id: userId,
    role,
    created_at: "2026-01-01T00:00:00Z",
    name: userId,
    email: `${userId}@example.com`,
    avatar_url: null,
  };
}

describe("memberRoleOf", () => {
  const members = [member("owner-1", "owner"), member("member-1", "member")];

  it("returns the current user's role", () => {
    expect(memberRoleOf(members, "member-1")).toBe("member");
    expect(memberRoleOf(members, "owner-1")).toBe("owner");
  });

  it("is null while the list loads, signed out, or for a non-member", () => {
    expect(memberRoleOf(undefined, "member-1")).toBeNull();
    expect(memberRoleOf(members, null)).toBeNull();
    expect(memberRoleOf(members, "stranger")).toBeNull();
  });
});
