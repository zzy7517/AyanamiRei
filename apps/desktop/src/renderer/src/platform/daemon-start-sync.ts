/**
 * Bring the local daemon up once the app is open.
 *
 * The order matters and is the reason this is a function rather than two
 * inline awaits. Desktop's daemon profile is derived from the target API URL,
 * so until the main process has that URL there is no Desktop-owned profile and
 * a start request cannot pick a profile (#6399).
 *
 * That has no retry of its own, so the URL must be pushed and awaited here
 * instead of racing a separate effect's IPC message. Re-sending it is cheap —
 * the main-process handler ignores an unchanged value.
 */
export interface DaemonStartSyncAPI {
  setTargetApiUrl: (url: string) => Promise<void>;
  autoStart: () => Promise<unknown>;
}

export async function startDaemonOnLaunch(api: DaemonStartSyncAPI, apiUrl: string): Promise<void> {
  await api.setTargetApiUrl(apiUrl);
  await api.autoStart();
}
