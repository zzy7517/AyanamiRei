// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { hasLocalMatch } from "next/dist/shared/lib/match-local-pattern";

vi.mock("dotenv", () => ({ config: vi.fn() }));

import nextConfig from "./next.config";

describe("image optimization paths", () => {
  // No route optimizes local images any more, and the optimizer must never
  // serve workspace-private sources. The allowlist stays empty so a future
  // pattern has to be added deliberately, with these re-checked.
  it.each([
    "/uploads/workspaces/workspace/attachment.png",
    "/api/attachments/attachment/download",
    "/api/attachments/attachment/signed-download?exp=1&sig=signature",
    "/images/hero.png?url=/uploads/workspaces/workspace/attachment.png",
  ])("rejects private or unexpected sources: %s", (url) => {
    expect(hasLocalMatch(nextConfig.images?.localPatterns, url)).toBe(false);
  });
});
