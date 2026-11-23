// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { RAW_KEYBOARD_ATTRIBUTE, isRawKeyboardShortcutTarget } from "./definitions";

afterEach(() => {
  document.body.replaceChildren();
});

function mount(): { owner: HTMLDivElement; reader: HTMLTextAreaElement; outside: HTMLButtonElement } {
  document.body.innerHTML = `
    <button id="outside">Outside</button>
    <div id="owner" ${RAW_KEYBOARD_ATTRIBUTE}>
      <div>
        <textarea id="reader"></textarea>
      </div>
    </div>
  `;
  return {
    owner: document.getElementById("owner") as HTMLDivElement,
    reader: document.getElementById("reader") as HTMLTextAreaElement,
    outside: document.getElementById("outside") as HTMLButtonElement,
  };
}

describe("isRawKeyboardShortcutTarget", () => {
  it("is true for the marked element and anything inside it, however deep", () => {
    const { owner, reader } = mount();
    expect(isRawKeyboardShortcutTarget(owner)).toBe(true);
    expect(isRawKeyboardShortcutTarget(owner.firstElementChild)).toBe(true);
    expect(isRawKeyboardShortcutTarget(reader)).toBe(true);
  });

  it("is false outside the marked element", () => {
    const { owner, outside } = mount();
    expect(isRawKeyboardShortcutTarget(outside)).toBe(false);
    expect(isRawKeyboardShortcutTarget(document.body)).toBe(false);
    owner.removeAttribute(RAW_KEYBOARD_ATTRIBUTE);
    expect(isRawKeyboardShortcutTarget(owner)).toBe(false);
  });

  it("needs an element: null, the window and text nodes are not targets", () => {
    mount();
    expect(isRawKeyboardShortcutTarget(null)).toBe(false);
    expect(isRawKeyboardShortcutTarget(window)).toBe(false);
    expect(isRawKeyboardShortcutTarget(document.createTextNode("x"))).toBe(false);
  });

  it("matches the attribute by itself, without a value", () => {
    expect(RAW_KEYBOARD_ATTRIBUTE).toBe("data-raw-keyboard");
    const div = document.createElement("div");
    div.setAttribute(RAW_KEYBOARD_ATTRIBUTE, "");
    expect(isRawKeyboardShortcutTarget(div)).toBe(true);
  });
});
