import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

const route = vi.hoisted(() => ({ pathname: null as string | null }));

vi.mock("next/navigation", () => ({
  usePathname: () => route.pathname,
}));

import { useUserLocaleSyncEnabled } from "./user-locale-sync";

describe("useUserLocaleSyncEnabled", () => {
  beforeEach(() => {
    route.pathname = null;
  });

  it("waits until the router has a pathname", () => {
    const { result, rerender } = renderHook(useUserLocaleSyncEnabled);
    expect(result.current).toBe(false);

    route.pathname = "/onboarding";
    rerender();
    expect(result.current).toBe(true);
  });

  it("syncs on a workspace path", () => {
    route.pathname = "/acme/issues";

    const { result } = renderHook(useUserLocaleSyncEnabled);

    expect(result.current).toBe(true);
  });
});
