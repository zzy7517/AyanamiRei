import type { StorageAdapter } from "../types/storage";

const SESSION_TOKEN_KEY = "multica_token";

/**
 * Where a pre-shared token stands in this process.
 *
 * - `presented`: a read of the session credential returns the token.
 * - `dropped`: the session that presented it ended — the server rejected the
 *   token, or the user signed out — and it is not presented again.
 * - `replaced`: the app stored a credential of its own (a sign-in). From here
 *   on the session credential is that one.
 */
export type PreSharedTokenState = "presented" | "dropped" | "replaced";

export interface PreSharedTokenStorage extends StorageAdapter {
  state(): PreSharedTokenState;
}

/**
 * Makes `token` this client's session credential without storing it.
 *
 * The client reads its credential in one place: the session token key of the
 * storage it was wired with. The API client's bearer header, the WebSocket
 * auth frame and the identity probe at boot all go through it, so a token
 * presented there travels with every request and is what the boot probe
 * verifies — the path a restored session takes, which is what lets the app open
 * past the login page.
 *
 * That key reads as `token` in memory only; `storage` is never written for it.
 * The app ends the presentation itself, through the two things it already does
 * to the session credential: dropping it, which is how a session the server
 * rejected or the user left is torn down, or storing a credential of its own,
 * which is a sign-in. Either way the token is not presented again in this
 * process, so a refused token is not replayed and a later sign-in is not
 * overridden.
 */
export function withPreSharedToken(
  storage: StorageAdapter,
  token: string,
): PreSharedTokenStorage {
  let state: PreSharedTokenState = "presented";
  const listKeys = storage.keys;
  return {
    getItem: (key) =>
      state === "presented" && key === SESSION_TOKEN_KEY
        ? token
        : storage.getItem(key),
    setItem: (key, value) => {
      if (key === SESSION_TOKEN_KEY && state !== "replaced") state = "replaced";
      storage.setItem(key, value);
    },
    removeItem: (key) => {
      if (key === SESSION_TOKEN_KEY && state === "presented") state = "dropped";
      storage.removeItem(key);
    },
    ...(listKeys && {
      keys: () => {
        const keys = listKeys.call(storage);
        if (state !== "presented" || keys.includes(SESSION_TOKEN_KEY)) return keys;
        return [...keys, SESSION_TOKEN_KEY];
      },
    }),
    state: () => state,
  };
}
