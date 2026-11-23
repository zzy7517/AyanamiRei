import { beforeEach, describe, expect, it } from "vitest";
import { useQuickCreateStore } from "./quick-create-store";

const RESET_STATE = {
  lastAgentId: null,
  keepOpen: false,
};

describe("quick create store", () => {
  beforeEach(() => {
    useQuickCreateStore.setState(RESET_STATE);
  });

  // The store remembers the agent and nothing else about the last create.
  // Project is a property of the issue being filed, not a standing
  // preference, so carrying it forward files the next issue somewhere the
  // user never picked (MUL-5862).
  it("does not carry any project memory", () => {
    expect(useQuickCreateStore.getState()).not.toHaveProperty("lastProjectId");
    expect(useQuickCreateStore.getState()).not.toHaveProperty("setLastProjectId");
  });

  it("remembers the last agent and clears it with null", () => {
    const { setLastAgent } = useQuickCreateStore.getState();

    setLastAgent("agent-1");
    expect(useQuickCreateStore.getState().lastAgentId).toBe("agent-1");

    setLastAgent(null);
    expect(useQuickCreateStore.getState().lastAgentId).toBeNull();
  });
});
