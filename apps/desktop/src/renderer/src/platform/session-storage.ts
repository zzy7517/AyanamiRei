import {
  defaultStorage,
  withPreSharedToken,
  type PreSharedTokenStorage,
} from "@multica/core/platform";
import type { StorageAdapter } from "@multica/core/types";

export interface SessionStorage {
  /** The storage this window's session credential is read through. */
  storage: StorageAdapter;
  /** Set when desktop.json names a pre-shared token. */
  preSharedToken?: PreSharedTokenStorage;
}

/**
 * The storage a window reads its session credential through.
 *
 * With a pre-shared token from desktop.json that credential is the token,
 * presented from memory and never stored: nothing is left behind once the
 * server refuses it or the user signs out. Without one every read goes straight
 * to localStorage, as before.
 */
export function createSessionStorage(authToken: string | undefined): SessionStorage {
  const preSharedToken = authToken
    ? withPreSharedToken(defaultStorage, authToken)
    : undefined;
  return { storage: preSharedToken ?? defaultStorage, preSharedToken };
}

/**
 * The server refused the pre-shared token from desktop.json. `expired` says the
 * server ended the session rather than the user signing out; "dropped" says the
 * credential it ended it over was the token, not a later sign-in.
 */
export function wasPreSharedTokenRejected(
  preSharedToken: PreSharedTokenStorage | undefined,
  expired: boolean,
): boolean {
  return expired && preSharedToken?.state() === "dropped";
}
