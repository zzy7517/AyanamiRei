"use client";

import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  useTerminalSessionState,
  type TerminalSession,
  type TerminalSessionState,
} from "@multica/core/terminal";
import { RAW_KEYBOARD_ATTRIBUTE } from "@multica/core/shortcuts";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useThemeVersion } from "../editor/hooks/use-theme-version";
import { readTerminalAppearance } from "./terminal-theme";

/**
 * Waits for the container to stop moving before measuring it. A window drag
 * fires hundreds of size changes a second, and every fit would reach the
 * shell as a resize for it to redraw.
 */
const RESIZE_DEBOUNCE_MS = 120;
const SCROLLBACK_LINES = 10_000;

export interface TerminalHandle {
  /** Put text on screen, as the terminal itself would have printed it. */
  write(data: string | Uint8Array): void;
  focus(): void;
}

export interface TerminalProps {
  /** The terminal on screen. Another session is another, blank, terminal. */
  session: TerminalSession;
  /**
   * The terminal answered and takes input, with the id it can be attached by
   * again. Fires after every connection, reconnects included.
   */
  onReady?: (terminalId: string) => void;
  /** The user asked for the terminal to end from a place this component owns. */
  onRequestClose?: () => void;
  className?: string;
}

/**
 * One terminal, drawn by xterm.js: the session's output on screen, the
 * keyboard and the clipboard on their way to the session, and a line about
 * every state that is not a terminal waiting for input.
 *
 * The whole keyboard belongs to the program on screen. The root carries the
 * attribute that keeps the app's own shortcuts out of it.
 */
export const Terminal = forwardRef<TerminalHandle, TerminalProps>(function Terminal(
  { session, onReady, onRequestClose, className },
  ref,
) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  /** The theme version the terminal was last dressed in; -1 before that. */
  const themedWith = useRef(-1);
  const state = useTerminalSessionState(session);
  const themeVersion = useThemeVersion();

  // The effects below run on the session's and the theme's schedule. These
  // hold what they need to read at that moment.
  const latest = useRef({ themeVersion, onReady });
  useEffect(() => {
    latest.current = { themeVersion, onReady };
  });

  useImperativeHandle(
    ref,
    () => ({
      write: (data) => termRef.current?.write(data),
      focus: () => termRef.current?.focus(),
    }),
    [],
  );

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const { theme, font } = readTerminalAppearance(host);
    const term = new XTerm({
      cursorBlink: true,
      scrollback: SCROLLBACK_LINES,
      theme,
      fontFamily: font.fontFamily,
      fontSize: font.fontSize,
      lineHeight: font.lineHeight,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    // Listening before the first fit: that resize is the size the shell opens at.
    const stop = [
      term.onData((data) => session.sendInput(data)),
      // Bytes that are not text (a mouse report, a file protocol).
      term.onBinary((data) => session.sendInput(Uint8Array.from(data, (char) => char.charCodeAt(0)))),
      term.onResize(({ cols, rows }) => session.resize(cols, rows)),
    ].map((subscription) => () => subscription.dispose());
    stop.push(session.onOutput((data) => term.write(data)));
    term.open(host);
    fit.fit();
    term.focus();
    termRef.current = term;
    fitRef.current = fit;
    themedWith.current = latest.current.themeVersion;
    host.style.backgroundColor = theme.background ?? "";

    let resizeTimer: ReturnType<typeof setTimeout> | null = null;
    const observer = new ResizeObserver(() => {
      if (resizeTimer !== null) clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        resizeTimer = null;
        fit.fit();
      }, RESIZE_DEBOUNCE_MS);
    });
    observer.observe(host);

    return () => {
      observer.disconnect();
      if (resizeTimer !== null) clearTimeout(resizeTimer);
      for (const stopListening of stop) stopListening();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [session]);

  // A theme or text-size change is applied in place, so what is on screen stays on it.
  useEffect(() => {
    const host = hostRef.current;
    const term = termRef.current;
    if (!host || !term || themedWith.current === themeVersion) return;
    themedWith.current = themeVersion;
    const { theme, font } = readTerminalAppearance(host);
    term.options = {
      theme,
      fontFamily: font.fontFamily,
      fontSize: font.fontSize,
      lineHeight: font.lineHeight,
    };
    host.style.backgroundColor = theme.background ?? "";
    fitRef.current?.fit();
  }, [themeVersion]);

  useEffect(() => {
    if (state.status === "ready" && state.terminalId) latest.current.onReady?.(state.terminalId);
  }, [state.status, state.terminalId]);

  return (
    <div
      ref={hostRef}
      {...{ [RAW_KEYBOARD_ATTRIBUTE]: true }}
      className={cn("relative h-full min-h-0 w-full min-w-0 overflow-hidden", className)}
      // Clicks that land beside the text — on the scrollbar, in the padding — focus it too.
      onPointerDown={() => termRef.current?.focus()}
    >
      <TerminalStatus state={state} onRequestClose={onRequestClose} />
    </div>
  );
});

const STATUS_PILL =
  "rounded-md border border-surface-border bg-background/95 px-3 py-1.5 text-caption text-muted-foreground shadow-xs";

/** What to say while the terminal is not there to be used. */
function TerminalStatus({
  state,
  onRequestClose,
}: {
  state: TerminalSessionState;
  onRequestClose?: () => void;
}) {
  const { t } = useT("terminal");

  if (state.status === "ready") return null;

  if (state.status === "connecting") {
    // Along the top: what is on screen, if anything, stays readable under it.
    const reconnecting = state.reconnectAttempt > 0;
    const canEnd = reconnecting && !state.closing && onRequestClose !== undefined;
    return (
      <div className="pointer-events-none absolute inset-x-0 top-0 z-10 flex justify-center p-3">
        <div
          role="status"
          className={cn(STATUS_PILL, "pointer-events-auto flex items-center gap-2")}
        >
          <Spinner aria-hidden className="size-3.5" />
          <span>
            {reconnecting ? t(($) => $.status.reconnecting) : t(($) => $.status.connecting)}
          </span>
          {canEnd && (
            <Button
              type="button"
              size="xs"
              variant="ghost"
              className="-me-1.5 text-muted-foreground"
              onClick={onRequestClose}
            >
              {t(($) => $.end_session)}
            </Button>
          )}
        </div>
      </div>
    );
  }

  if (state.status === "exited") {
    const { exit } = state;
    const text = exit?.signal
      ? t(($) => $.status.ended_by_signal, { signal: exit.signal })
      : typeof exit?.exitCode === "number"
        ? t(($) => $.status.ended_with_code, { code: exit.exitCode })
        : t(($) => $.status.ended);
    return (
      <div className="pointer-events-none absolute inset-x-0 top-0 z-10 flex justify-center p-3">
        <div role="status" className={STATUS_PILL}>
          {text}
        </div>
      </div>
    );
  }

  // An error means the session gave up before there was anything to show.
  const { error } = state;
  return (
    <div className="absolute inset-0 z-10 flex items-center justify-center p-6">
      <div
        role="alert"
        className="max-w-md rounded-lg border border-surface-border bg-background px-4 py-3 text-body text-foreground shadow-md"
      >
        <p>{error ? t(($) => $.errors[error.code]) : t(($) => $.errors.internal)}</p>
        {error?.message && (
          <p className="mt-1.5 break-all font-mono text-caption text-muted-foreground">{error.message}</p>
        )}
      </div>
    </div>
  );
}
