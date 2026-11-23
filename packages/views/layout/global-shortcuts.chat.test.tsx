import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { configureShortcutPlatform, RAW_KEYBOARD_ATTRIBUTE } from "@multica/core/shortcuts";
import { openCreateIssueWithPreference } from "@multica/core/issues/stores";
import { useRef } from "react";
import { useChatInputFocus } from "../chat/components/use-chat-input-focus";
import { GlobalShortcuts } from "./global-shortcuts";

// The floating chat overlay is reachable from the keyboard (MUL-5522). What
// matters is not just "the chord calls toggle", but that it stays a no-op —
// without swallowing the keypress — everywhere the overlay cannot exist.
const h = vi.hoisted(() => ({
  chat: {
    floatingChatEnabled: true,
    isOpen: false,
    activeSessionId: null as string | null,
    toggle: vi.fn(),
    setOpen: vi.fn(),
    startNewChat: vi.fn(),
  },
  navigation: { pathname: "/acme/issues", push: vi.fn() },
  toggleSidebar: vi.fn(),
  searchToggle: vi.fn(),
}));

vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (state: typeof h.chat) => unknown) => selector(h.chat),
    { getState: () => h.chat },
  ),
}));
vi.mock("@multica/core/issues/stores", () => ({
  openCreateIssueWithPreference: vi.fn(),
}));
vi.mock("@multica/core/modals", () => ({
  useModalStore: { getState: () => ({ modal: null }) },
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1" }),
  useWorkspacePaths: () => ({
    inbox: () => "/acme/inbox",
    chat: () => "/acme/chat",
    issues: () => "/acme/issues",
    projects: () => "/acme/projects",
    autopilots: () => "/acme/autopilots",
    agents: () => "/acme/agents",
    usage: () => "/acme/usage",
    runtimes: () => "/acme/runtimes",
    skills: () => "/acme/skills",
    settings: () => "/acme/settings",
  }),
}));
vi.mock("@multica/ui/components/ui/sidebar", () => ({
  useSidebar: () => ({ toggleSidebar: h.toggleSidebar }),
}));
vi.mock("../navigation", () => ({ useNavigation: () => h.navigation }));
vi.mock("@multica/core/command-bar", () => ({
  useSearchStore: { getState: () => ({ toggle: h.searchToggle }) },
}));

/** Mod+J on macOS, dispatched the way a real keypress reaches the document. */
function pressToggleChat(target: EventTarget = document): boolean {
  const event = new KeyboardEvent("keydown", {
    key: "j",
    metaKey: true,
    bubbles: true,
    cancelable: true,
  });
  target.dispatchEvent(event);
  return event.defaultPrevented;
}

/** Shift+Mod+J on macOS: Shift turns the logical key into "J". */
function pressNewChat(target: EventTarget = document): boolean {
  const event = new KeyboardEvent("keydown", {
    key: "J",
    metaKey: true,
    shiftKey: true,
    bubbles: true,
    cancelable: true,
  });
  target.dispatchEvent(event);
  return event.defaultPrevented;
}

/** Mod+K on macOS: the command palette. */
function pressSearch(target: EventTarget = document): boolean {
  const event = new KeyboardEvent("keydown", {
    key: "k",
    metaKey: true,
    bubbles: true,
    cancelable: true,
  });
  target.dispatchEvent(event);
  return event.defaultPrevented;
}

beforeEach(() => {
  // Pin the platform: jsdom's user agent reports the host OS, so Command vs
  // Control would otherwise depend on where the suite runs.
  configureShortcutPlatform("macos");
  h.chat.floatingChatEnabled = true;
  h.chat.isOpen = false;
  h.chat.activeSessionId = null;
  h.navigation.pathname = "/acme/issues";
});

afterEach(() => {
  configureShortcutPlatform(null);
  vi.clearAllMocks();
});

describe("chat toggle shortcut", () => {
  it("lets C create an issue after the focused floating composer closes", () => {
    function Composer({ isOpen }: { isOpen: boolean }) {
      const windowRef = useRef<HTMLDivElement>(null);
      useChatInputFocus(isOpen, windowRef);
      return <div ref={windowRef} inert={!isOpen}><div data-testid="composer" contentEditable tabIndex={0} /></div>;
    }
    const { getByTestId, rerender } = render(<><GlobalShortcuts /><Composer isOpen /></>);
    const editor = getByTestId("composer");
    editor.focus();
    expect(document.activeElement).toBe(editor);

    const pressCreate = () => document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", {
      key: "c", bubbles: true, cancelable: true,
    }));
    pressCreate();
    expect(openCreateIssueWithPreference).not.toHaveBeenCalled();

    rerender(<><GlobalShortcuts /><Composer isOpen={false} /></>);
    expect(document.activeElement).toBe(document.body);
    expect(getByTestId("composer").parentElement).toHaveAttribute("inert");
    pressCreate();
    expect(openCreateIssueWithPreference).toHaveBeenCalledTimes(1);
  });

  it("toggles the floating window and consumes the chord", () => {
    render(<GlobalShortcuts />);

    expect(pressToggleChat()).toBe(true);
    expect(h.chat.toggle).toHaveBeenCalledTimes(1);
  });

  it("still fires while the caret is inside a text input", () => {
    render(<GlobalShortcuts />);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();

    expect(pressToggleChat(input)).toBe(true);
    expect(h.chat.toggle).toHaveBeenCalledTimes(1);

    input.remove();
  });

  it("leaves the chord alone on the Chat tab, where the overlay is suppressed", () => {
    h.navigation.pathname = "/acme/chat";
    render(<GlobalShortcuts />);

    // Not prevented: an action that cannot run must not swallow the keypress.
    expect(pressToggleChat()).toBe(false);
    expect(h.chat.toggle).not.toHaveBeenCalled();
  });

  it("leaves the chord alone when the floating window is turned off", () => {
    h.chat.floatingChatEnabled = false;
    render(<GlobalShortcuts />);

    expect(pressToggleChat()).toBe(false);
    expect(h.chat.toggle).not.toHaveBeenCalled();
  });

  it("reads the preference at press time, not at mount", () => {
    render(<GlobalShortcuts />);

    h.chat.floatingChatEnabled = false;
    expect(pressToggleChat()).toBe(false);

    h.chat.floatingChatEnabled = true;
    expect(pressToggleChat()).toBe(true);
    expect(h.chat.toggle).toHaveBeenCalledTimes(1);
  });

  it("does not confuse the chat chord with the other global bindings", () => {
    render(<GlobalShortcuts />);

    document.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "b",
        metaKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(h.toggleSidebar).toHaveBeenCalledTimes(1);
    expect(h.chat.toggle).not.toHaveBeenCalled();
  });
});

describe("new chat shortcut", () => {
  it("starts a new chat instead of reopening the last one", () => {
    h.chat.activeSessionId = "session-1";
    render(<GlobalShortcuts />);

    expect(pressNewChat()).toBe(true);
    expect(h.chat.startNewChat).toHaveBeenCalledTimes(1);
    expect(h.chat.toggle).not.toHaveBeenCalled();
  });

  it("switches an open window from another chat to a new one", () => {
    h.chat.isOpen = true;
    h.chat.activeSessionId = "session-1";
    render(<GlobalShortcuts />);

    expect(pressNewChat()).toBe(true);
    expect(h.chat.startNewChat).toHaveBeenCalledTimes(1);
    expect(h.chat.setOpen).not.toHaveBeenCalled();
  });

  it("closes the window while it shows the new chat, like Ask Multica", () => {
    h.chat.isOpen = true;
    h.chat.activeSessionId = null;
    render(<GlobalShortcuts />);

    expect(pressNewChat()).toBe(true);
    expect(h.chat.setOpen).toHaveBeenCalledWith(false);
    expect(h.chat.startNewChat).not.toHaveBeenCalled();
  });

  it("still fires while the caret is inside a text input", () => {
    render(<GlobalShortcuts />);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();

    expect(pressNewChat(input)).toBe(true);
    expect(h.chat.startNewChat).toHaveBeenCalledTimes(1);

    input.remove();
  });

  it("leaves the chord alone where the floating window is unavailable", () => {
    h.navigation.pathname = "/acme/chat";
    const { unmount } = render(<GlobalShortcuts />);
    expect(pressNewChat()).toBe(false);
    unmount();

    h.navigation.pathname = "/acme/issues";
    h.chat.floatingChatEnabled = false;
    render(<GlobalShortcuts />);
    expect(pressNewChat()).toBe(false);
    expect(h.chat.startNewChat).not.toHaveBeenCalled();
  });
});

describe("raw keyboard targets", () => {
  // A terminal is focused through a textarea, so the text-field rules alone
  // would let these chords through to the app.
  it("leaves every chord to an element that reads the whole keyboard", () => {
    render(<GlobalShortcuts />);
    const owner = document.createElement("div");
    owner.setAttribute(RAW_KEYBOARD_ATTRIBUTE, "");
    const reader = document.createElement("textarea");
    owner.appendChild(reader);
    document.body.appendChild(owner);
    reader.focus();

    expect(pressToggleChat(reader)).toBe(false);
    expect(pressNewChat(reader)).toBe(false);
    expect(pressSearch(reader)).toBe(false);
    reader.dispatchEvent(new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true }));
    expect(h.chat.toggle).not.toHaveBeenCalled();
    expect(h.chat.startNewChat).not.toHaveBeenCalled();
    expect(h.searchToggle).not.toHaveBeenCalled();
    expect(h.toggleSidebar).not.toHaveBeenCalled();

    // A bare key is left alone too: it may be an app-wide binding by default.
    reader.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true, cancelable: true }));
    expect(openCreateIssueWithPreference).not.toHaveBeenCalled();
    owner.remove();
  });

  it("fires again once focus has left that element", () => {
    render(<GlobalShortcuts />);
    const owner = document.createElement("div");
    owner.setAttribute(RAW_KEYBOARD_ATTRIBUTE, "");
    const reader = document.createElement("textarea");
    owner.appendChild(reader);
    document.body.appendChild(owner);
    reader.focus();
    expect(pressToggleChat(reader)).toBe(false);

    reader.blur();
    expect(pressToggleChat()).toBe(true);
    expect(h.chat.toggle).toHaveBeenCalledTimes(1);
    owner.remove();
  });
});
