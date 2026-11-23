import { beforeEach, describe, expect, it } from "vitest";
import { defaultStorage } from "@multica/core/platform";
import { createSessionStorage, wasPreSharedTokenRejected } from "./session-storage";

beforeEach(() => {
  localStorage.clear();
});

describe("createSessionStorage", () => {
  it("is localStorage itself when desktop.json names no token", () => {
    localStorage.setItem("multica_token", "jwt-abc");

    const { storage, preSharedToken } = createSessionStorage(undefined);

    expect(storage).toBe(defaultStorage);
    expect(preSharedToken).toBeUndefined();
    expect(storage.getItem("multica_token")).toBe("jwt-abc");
  });

  it("presents the token as the credential and keeps it out of localStorage", () => {
    const { storage, preSharedToken } = createSessionStorage("open-sesame");

    expect(storage.getItem("multica_token")).toBe("open-sesame");
    expect(preSharedToken?.state()).toBe("presented");
    expect(localStorage.getItem("multica_token")).toBeNull();
  });
});

describe("wasPreSharedTokenRejected", () => {
  it("is true once the server ends the session that presented the token", () => {
    const { preSharedToken } = createSessionStorage("open-sesame");
    if (!preSharedToken) throw new Error("expected a pre-shared token");

    // Nothing was refused while the token is still what the app presents.
    expect(wasPreSharedTokenRejected(preSharedToken, true)).toBe(false);

    preSharedToken.removeItem("multica_token");
    expect(wasPreSharedTokenRejected(preSharedToken, true)).toBe(true);
    // The same end of session is a sign-out when the user asked for it.
    expect(wasPreSharedTokenRejected(preSharedToken, false)).toBe(false);
  });

  it("is never true without a token, or once a sign-in replaced it", () => {
    expect(wasPreSharedTokenRejected(undefined, true)).toBe(false);

    const { preSharedToken } = createSessionStorage("open-sesame");
    if (!preSharedToken) throw new Error("expected a pre-shared token");
    preSharedToken.setItem("multica_token", "a-session-jwt");
    preSharedToken.removeItem("multica_token");

    expect(wasPreSharedTokenRejected(preSharedToken, true)).toBe(false);
  });
});
