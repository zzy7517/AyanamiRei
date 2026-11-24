// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createAuthStore } from "./store";

describe("createAuthStore", () => {
  it("starts in recovery and resolves the fixed user", async () => {
    const user = { id: "u1", email: "dev@localhost", name: "dev" } as never;
    const store = createAuthStore({
      api: { getMe: async () => user } as never,
      storage: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
      },
    });

    expect(store.getState().isLoading).toBe(true);
    expect(store.getState().status).toBe("recovering");

    await store.getState().refreshMe();

    expect(store.getState().user).toBe(user);
    expect(store.getState().isLoading).toBe(false);
    expect(store.getState().status).toBe("authenticated");
  });
});
