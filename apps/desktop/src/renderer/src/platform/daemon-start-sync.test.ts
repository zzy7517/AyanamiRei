// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";

import { startDaemonOnLaunch, type DaemonStartSyncAPI } from "./daemon-start-sync";

const calls: string[] = [];

function makeApi(overrides: Partial<DaemonStartSyncAPI> = {}): DaemonStartSyncAPI {
  return {
    setTargetApiUrl: vi.fn(async () => {
      calls.push("setTargetApiUrl");
    }),
    autoStart: vi.fn(async () => {
      calls.push("autoStart");
    }),
    ...overrides,
  };
}

beforeEach(() => {
  calls.length = 0;
  vi.clearAllMocks();
});

describe("startDaemonOnLaunch", () => {
  it("pushes the target URL before starting the daemon", async () => {
    const api = makeApi();
    await startDaemonOnLaunch(api, "https://api.example.com");

    expect(calls).toEqual(["setTargetApiUrl", "autoStart"]);
    expect(api.setTargetApiUrl).toHaveBeenCalledWith("https://api.example.com");
  });

  // Regression: the start used to race a separate effect's setTargetApiUrl.
  // Arriving first meant main had no resolved profile for the start.
  it("awaits the target URL rather than firing it off", async () => {
    let released: (() => void) | undefined;
    const api = makeApi({
      setTargetApiUrl: vi.fn(
        () =>
          new Promise<void>((resolve) => {
            released = () => {
              calls.push("setTargetApiUrl");
              resolve();
            };
          }),
      ),
    });

    const pending = startDaemonOnLaunch(api, "https://api.example.com");
    await Promise.resolve();
    expect(api.autoStart).not.toHaveBeenCalled();

    released?.();
    await pending;
    expect(calls).toEqual(["setTargetApiUrl", "autoStart"]);
  });
});
