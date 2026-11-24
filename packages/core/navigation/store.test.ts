import { describe, it, expect } from "vitest";

// EXCLUDED_PREFIXES is private to store.ts but checked here via behavior.
// Pre-workspace routes must not become lastPath, or the next launch would
// restore the user onto a flow that is not a workspace page.
describe("useNavigationStore.lastPath excludes global paths", () => {
  it("does not persist /workspaces/new or /onboarding", async () => {
    const { useNavigationStore } = await import("./store");
    const globalPrefixes = [
      "/workspaces/new",
      "/onboarding",
    ];

    for (const path of globalPrefixes) {
      // Reset to a known sentinel so we can detect any write.
      useNavigationStore.setState({ lastPath: "/sentinel" });
      useNavigationStore.getState().onPathChange(path);
      expect(
        useNavigationStore.getState().lastPath,
        `${path} should not be persisted as lastPath (would restore user to a global route)`,
      ).toBe("/sentinel");
    }
  });

  it("does persist workspace-scoped paths", async () => {
    const { useNavigationStore } = await import("./store");
    useNavigationStore.setState({ lastPath: null });
    useNavigationStore.getState().onPathChange("/acme/issues");
    expect(useNavigationStore.getState().lastPath).toBe("/acme/issues");
  });
});
