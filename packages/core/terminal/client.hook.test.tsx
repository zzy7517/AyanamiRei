// @vitest-environment jsdom
import { StrictMode, useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import {
  TerminalSession,
  useTerminalSession,
  useTerminalSessionState,
  type TerminalSessionIntent,
} from "./client";

const READY = JSON.stringify({ type: "ready", terminalId: "t-1", pid: 1, cols: 80, rows: 24 });

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  binaryType = "blob";
  sent: string[] = [];
  closeCalls = 0;
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.closeCalls += 1;
  }
  open() {
    this.onopen?.();
  }
  static get last(): FakeWebSocket {
    return FakeWebSocket.instances.at(-1)!;
  }
}

/** Let the ticket request settle, as a real event loop would between renders. */
const flush = async () => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
};

const options = {
  createTicket: async () => ({ ticket: "tk", expiresInSeconds: 120 }),
  socketUrl: (ticket: string) => `wss://srv.test/terminal/ws?ticket=${ticket}`,
};

function Probe({
  intent,
  onSession,
}: {
  intent: TerminalSessionIntent;
  onSession?: (session: TerminalSession) => void;
}) {
  const session = useTerminalSession(intent, options);
  const state = useTerminalSessionState(session);
  useEffect(() => onSession?.(session), [session, onSession]);
  return <span data-testid="status">{`${state.status}:${state.terminalId ?? "-"}`}</span>;
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useTerminalSession", () => {
  it("opens a shell in the directory on mount, and detaches on unmount without ending it", async () => {
    const { unmount } = render(<Probe intent={{ cwd: "/srv/app", daemonId: "d-1" }} />);
    expect(screen.getByTestId("status").textContent).toBe("connecting:-");

    await flush();
    expect(FakeWebSocket.instances).toHaveLength(1);
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual([
      '{"type":"open","cwd":"/srv/app","cols":80,"rows":24,"daemonId":"d-1"}',
    ]);

    unmount();
    expect(FakeWebSocket.last.sent).toHaveLength(1);
    expect(FakeWebSocket.last.closeCalls).toBe(1);
    expect(FakeWebSocket.last.onclose).toBeNull();
  });

  it("attaches to the terminal an id names instead of opening one", async () => {
    render(<Probe intent={{ cwd: "/srv/app", terminalId: "t-1" }} />);
    await flush();
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"attach","terminalId":"t-1"}']);
  });

  it("re-renders when the terminal becomes ready", async () => {
    render(<Probe intent={{ cwd: "/srv/app" }} />);
    await flush();
    act(() => {
      FakeWebSocket.last.open();
      FakeWebSocket.last.onmessage?.({ data: READY });
    });
    expect(screen.getByTestId("status").textContent).toBe("ready:t-1");
  });

  it("leaves the first shell running when the directory changes", async () => {
    const { rerender } = render(<Probe intent={{ cwd: "/first" }} />);
    await flush();
    act(() => FakeWebSocket.last.open());
    const first = FakeWebSocket.last;

    rerender(<Probe intent={{ cwd: "/second" }} />);
    expect(first.closeCalls).toBe(1);
    expect(first.sent).toHaveLength(1);

    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"open","cwd":"/second","cols":80,"rows":24}']);
  });

  it("keeps the terminal running when the id of the one it just opened arrives", async () => {
    const { rerender } = render(<Probe intent={{ cwd: "/srv/app" }} />);
    await flush();
    act(() => {
      FakeWebSocket.last.open();
      FakeWebSocket.last.onmessage?.({ data: READY });
    });
    const socket = FakeWebSocket.last;

    // The page records the id once `ready` names it; that is not a new intent.
    rerender(<Probe intent={{ cwd: "/srv/app", terminalId: "t-1" }} />);
    expect(screen.getByTestId("status").textContent).toBe("ready:t-1");
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(socket.closeCalls).toBe(0);
    expect(socket.sent).toEqual(['{"type":"open","cwd":"/srv/app","cols":80,"rows":24}']);
  });

  it("attaches when the directory changes together with an id", async () => {
    const { rerender } = render(<Probe intent={{ cwd: "/first" }} />);
    await flush();
    act(() => FakeWebSocket.last.open());

    rerender(<Probe intent={{ cwd: "/second", terminalId: "t-2" }} />);
    await flush();
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"attach","terminalId":"t-2"}']);
  });

  it("opens an agent's terminal in the tree on mount, and keeps it when the id of the one it opened arrives", async () => {
    const agent = { issue_id: "issue-1", tree_id: "tree-1" };
    const { rerender } = render(<Probe intent={{ agent }} />);
    await flush();
    act(() => {
      FakeWebSocket.last.open();
      FakeWebSocket.last.onmessage?.({ data: READY });
    });
    const socket = FakeWebSocket.last;
    expect(socket.sent).toEqual([`{"type":"open","agent":{"issue_id":"issue-1","tree_id":"tree-1"},"cols":80,"rows":24}`]);

    rerender(<Probe intent={{ agent, terminalId: "t-1" }} />);
    expect(screen.getByTestId("status").textContent).toBe("ready:t-1");
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(socket.closeCalls).toBe(0);
  });

  it("attaches to the terminal an id names for an agent's session", async () => {
    render(<Probe intent={{ agent: { issue_id: "issue-1", tree_id: "tree-1" }, terminalId: "t-9" }} />);
    await flush();
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual(['{"type":"attach","terminalId":"t-9"}']);
  });

  it("leaves the first session running when the tree changes", async () => {
    const { rerender } = render(<Probe intent={{ agent: { issue_id: "issue-1", tree_id: "tree-1" } }} />);
    await flush();
    act(() => FakeWebSocket.last.open());
    const first = FakeWebSocket.last;

    rerender(<Probe intent={{ agent: { issue_id: "issue-1", tree_id: "tree-2" } }} />);
    expect(first.closeCalls).toBe(1);
    expect(first.sent).toHaveLength(1);

    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    act(() => FakeWebSocket.last.open());
    expect(FakeWebSocket.last.sent).toEqual([`{"type":"open","agent":{"issue_id":"issue-1","tree_id":"tree-2"},"cols":80,"rows":24}`]);
  });

  it("under StrictMode ends up with one shell and no socket left open", async () => {
    const { unmount } = render(
      <StrictMode>
        <Probe intent={{ cwd: "/srv/app" }} />
      </StrictMode>,
    );
    await flush();
    // The ticket of the effect StrictMode immediately cleaned up resolved after
    // that run was disposed, so only the surviving run ever connected.
    expect(FakeWebSocket.instances).toHaveLength(1);

    unmount();
    expect(FakeWebSocket.last.closeCalls).toBe(1);
  });
});
