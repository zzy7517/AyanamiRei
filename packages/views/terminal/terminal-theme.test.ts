import { afterEach, describe, expect, it, vi } from "vitest";
import {
  buildTerminalTheme,
  luminance,
  mix,
  readTerminalAppearance,
  toCss,
  withHue,
  type Rgb,
  type TerminalPalette,
} from "./terminal-theme";

const DARK: TerminalPalette = {
  background: { r: 30, g: 30, b: 33 },
  foreground: { r: 228, g: 228, b: 232 },
  red: { r: 240, g: 95, b: 95 },
  green: { r: 95, g: 200, b: 130 },
  yellow: { r: 230, g: 195, b: 95 },
  blue: { r: 110, g: 165, b: 250 },
  selection: { r: 70, g: 90, b: 140 },
};

const LIGHT: TerminalPalette = {
  background: { r: 255, g: 255, b: 255 },
  foreground: { r: 23, g: 23, b: 26 },
  red: { r: 200, g: 45, b: 45 },
  green: { r: 25, g: 130, b: 80 },
  yellow: { r: 160, g: 120, b: 10 },
  blue: { r: 40, g: 100, b: 210 },
  selection: { r: 180, g: 200, b: 245 },
};

/** The two forms xterm.js parses; every channel a whole number in range. */
const CSS_COLOR = /^rgba\((\d+), (\d+), (\d+), (?:0|1|0?\.\d+)\)$|^rgb\((\d+), (\d+), (\d+)\)$/;

function channels(color: string): Rgb {
  const match = CSS_COLOR.exec(color);
  if (!match) throw new Error(`not a color xterm.js takes: ${color}`);
  const [r, g, b] = match[0].startsWith("rgba") ? [match[1], match[2], match[3]] : [match[4], match[5], match[6]];
  return { r: Number(r), g: Number(g), b: Number(b) };
}

function allColors(theme: Record<string, unknown>): string[] {
  return Object.values(theme).filter((value): value is string => typeof value === "string");
}

/** HSL of a color, by the textbook definitions, independent of the module's own helpers. */
function hsl({ r, g, b }: Rgb): { h: number; s: number; l: number } {
  const [red, green, blue] = [r / 255, g / 255, b / 255];
  const max = Math.max(red, green, blue);
  const min = Math.min(red, green, blue);
  const l = (max + min) / 2;
  const d = max - min;
  if (d === 0) return { h: 0, s: 0, l };
  const s = d / (1 - Math.abs(2 * l - 1));
  let h: number;
  if (max === red) h = ((green - blue) / d) % 6;
  else if (max === green) h = (blue - red) / d + 2;
  else h = (red - green) / d + 4;
  return { h: (h * 60 + 360) % 360, s, l };
}

/** The grays of a theme as numbers, named by the place each has between the paper and the text. */
function grays(theme: ReturnType<typeof buildTerminalTheme>) {
  return {
    paper: channels(theme.background!),
    black: channels(theme.black!),
    brightBlack: channels(theme.brightBlack!),
    text: channels(theme.foreground!),
    white: channels(theme.white!),
    brightWhite: channels(theme.brightWhite!),
  };
}

/**
 * A canvas 2D context that only knows what the color reader uses. Like a real
 * one, it keeps its fill when handed a string it cannot parse, and a fill only
 * lands on the pixels that are drawn afterwards.
 */
function installCanvas() {
  let fill = "#000000";
  let pending = { r: 0, g: 0, b: 0, a: 255 };
  let pixel = { r: 0, g: 0, b: 0, a: 0 };
  const context = {
    get fillStyle() {
      return fill;
    },
    set fillStyle(value: string) {
      const match = /^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(value);
      if (!match) return;
      const alpha = match[4] === undefined ? 1 : Number(match[4]);
      pending = { r: Number(match[1]), g: Number(match[2]), b: Number(match[3]), a: Math.round(255 * alpha) };
      fill = `rgb(${match[1]}, ${match[2]}, ${match[3]})`;
    },
    clearRect() {
      pixel = { r: 0, g: 0, b: 0, a: 0 };
    },
    fillRect() {
      pixel = pending;
    },
    getImageData() {
      return { data: [pixel.r, pixel.g, pixel.b, pixel.a] };
    },
  };
  return vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    context as unknown as CanvasRenderingContext2D,
  );
}

/** Computed styles straight from a table: a probe's `color` names a token, its class names the font. */
function installStyles(tokens: Record<string, string>, font: { size: number; line: number; family: string }) {
  return vi.spyOn(window, "getComputedStyle").mockImplementation((element) => {
    const probe = element as HTMLElement;
    const token = /^var\((--[\w-]+)\)$/.exec(probe.style.color)?.[1];
    const isFont = probe.className.includes("font-mono");
    return {
      color: token ? (tokens[token] ?? "") : "",
      fontSize: isFont ? `${font.size}px` : "",
      lineHeight: isFont ? `${font.line}px` : "",
      fontFamily: isFont ? font.family : "",
    } as CSSStyleDeclaration;
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  document.body.replaceChildren();
});

describe("mix", () => {
  const from = { r: 0, g: 100, b: 255 };
  const to = { r: 200, g: 100, b: 55 };

  it("lands at each end for 0 and 1, halfway in between", () => {
    expect(mix(from, to, 0)).toEqual(from);
    expect(mix(from, to, 1)).toEqual(to);
    expect(mix(from, to, 0.5)).toEqual({ r: 100, g: 100, b: 155 });
  });

  it("carries on past the far end, stopping at the channel limits", () => {
    expect(mix(from, to, -0.5)).toEqual({ r: 0, g: 100, b: 255 });
    expect(mix(from, to, 1.5)).toEqual({ r: 255, g: 100, b: 0 });
  });
});

describe("toCss", () => {
  it("writes decimal sRGB, with an alpha only when there is one", () => {
    expect(toCss({ r: 1, g: 2, b: 3 })).toBe("rgb(1, 2, 3)");
    expect(toCss({ r: 1, g: 2, b: 3 }, 0.45)).toBe("rgba(1, 2, 3, 0.45)");
  });
});

describe("luminance", () => {
  it("runs from black to white", () => {
    expect(luminance({ r: 0, g: 0, b: 0 })).toBe(0);
    expect(luminance({ r: 255, g: 255, b: 255 })).toBeCloseTo(1, 10);
    expect(luminance(DARK.background)).toBeLessThan(luminance(DARK.foreground));
    expect(luminance(LIGHT.background)).toBeGreaterThan(luminance(LIGHT.foreground));
  });
});

describe("withHue", () => {
  const blue = { r: 40, g: 100, b: 210 };

  it("turns a color to the hue asked, keeping how saturated and how light it is", () => {
    for (const hue of [310, 185]) {
      const turned = withHue(blue, hue);
      const before = hsl(blue);
      const after = hsl(turned);
      const delta = Math.abs(after.h - hue);
      expect(Math.min(delta, 360 - delta)).toBeLessThan(3);
      expect(after.s).toBeCloseTo(before.s, 1);
      expect(after.l).toBeCloseTo(before.l, 2);
    }
  });

  it("leaves a gray gray", () => {
    expect(withHue({ r: 80, g: 80, b: 80 }, 310)).toEqual({ r: 80, g: 80, b: 80 });
  });
});

describe("buildTerminalTheme", () => {
  it("hands xterm.js only colors it can parse", () => {
    const theme = buildTerminalTheme(DARK) as unknown as Record<string, unknown>;
    expect(allColors(theme)).toHaveLength(Object.keys(theme).length);
    for (const value of allColors(theme)) channels(value);
  });

  it("draws the cursor in the text color over the background", () => {
    const theme = buildTerminalTheme(DARK);
    expect(theme.background).toBe("rgb(30, 30, 33)");
    expect(theme.foreground).toBe("rgb(228, 228, 232)");
    expect(theme.cursor).toBe(theme.foreground);
    expect(theme.cursorAccent).toBe(theme.background);
  });

  it("passes the theme's status colors through, and fills magenta and cyan from the blue", () => {
    const theme = buildTerminalTheme(DARK);
    expect(theme.red).toBe("rgb(240, 95, 95)");
    expect(theme.green).toBe("rgb(95, 200, 130)");
    expect(theme.yellow).toBe("rgb(230, 195, 95)");
    expect(theme.blue).toBe("rgb(110, 165, 250)");
    const magenta = channels(theme.magenta!);
    const cyan = channels(theme.cyan!);
    expect(magenta.r).toBeGreaterThan(magenta.g);
    expect(magenta.b).toBeGreaterThan(magenta.g);
    expect(cyan.g).toBeGreaterThan(cyan.r);
    expect(cyan.b).toBeGreaterThan(cyan.r);
  });

  it("reads a dark theme off a dark background: black near the paper, white near the text", () => {
    const theme = buildTerminalTheme(DARK);
    const { paper, black, brightBlack, text, white, brightWhite } = grays(theme);
    expect(luminance(paper)).toBeLessThan(luminance(black));
    expect(luminance(black)).toBeLessThan(luminance(brightBlack));
    expect(luminance(brightBlack)).toBeLessThan(luminance(text));
    expect(luminance(text)).toBeLessThan(luminance(white));
    expect(luminance(white)).toBeLessThan(luminance(brightWhite));
    expect(channels(theme.black!)).toEqual({ r: 54, g: 54, b: 57 });
    expect(luminance(channels(theme.brightRed!))).toBeGreaterThan(luminance(channels(theme.red!)));
  });

  it("reads a light theme off a light background, with the sides swapped", () => {
    // black below the text, brightBlack above it, white below the paper
    const theme = buildTerminalTheme(LIGHT);
    const { paper, black, brightBlack, text, white, brightWhite } = grays(theme);
    expect(luminance(paper)).toBeGreaterThan(luminance(white));
    expect(luminance(white)).toBeLessThan(luminance(brightWhite));
    expect(luminance(brightWhite)).toBeLessThan(luminance(paper));
    expect(luminance(white)).toBeGreaterThan(luminance(brightBlack));
    expect(luminance(brightBlack)).toBeGreaterThan(luminance(text));
    expect(luminance(text)).toBeGreaterThan(luminance(black));
    expect(channels(theme.black!)).toEqual({ r: 9, g: 9, b: 12 });
    expect(luminance(channels(theme.brightRed!))).toBeGreaterThan(luminance(channels(theme.red!)));
  });

  it("keeps the selection and the scrollbar see-through", () => {
    const theme = buildTerminalTheme(DARK);
    expect(theme.selectionBackground).toBe("rgba(70, 90, 140, 0.45)");
    expect(theme.scrollbarSliderBackground).toBe("rgba(228, 228, 232, 0.2)");
  });
});

describe("readTerminalAppearance", () => {
  it("reads the theme's tokens and its code font where the terminal sits", () => {
    installCanvas();
    installStyles(
      {
        "--surface": "rgb(10, 20, 30)",
        "--foreground": "rgb(200, 210, 220)",
        "--destructive": "rgb(255, 60, 60)",
        "--success": "rgb(60, 255, 60)",
        "--warning": "rgb(255, 220, 60)",
        "--info": "rgb(80, 160, 255)",
        "--surface-selected": "rgba(120, 140, 200, 0.5)",
      },
      { size: 15.4, line: 22, family: '"JetBrains Mono", monospace' },
    );
    const host = document.createElement("div");
    document.body.append(host);

    const { theme, font } = readTerminalAppearance(host);

    expect(theme.background).toBe("rgb(10, 20, 30)");
    expect(theme.foreground).toBe("rgb(200, 210, 220)");
    expect(theme.red).toBe("rgb(255, 60, 60)");
    expect(theme.green).toBe("rgb(60, 255, 60)");
    expect(theme.yellow).toBe("rgb(255, 220, 60)");
    expect(theme.blue).toBe("rgb(80, 160, 255)");
    // A token with alpha is read by its color; the selection's own comes from the theme.
    expect(channels(theme.selectionBackground!)).toEqual({ r: 120, g: 140, b: 200 });
    expect(font).toEqual({ fontFamily: '"JetBrains Mono", monospace', fontSize: 15.4, lineHeight: 22 / 15.4 });
    expect(host.childElementCount).toBe(0);
  });

  it("falls back to the text color for a status color the theme does not define", () => {
    installCanvas();
    installStyles(
      { "--surface": "rgb(10, 20, 30)", "--foreground": "rgb(200, 210, 220)" },
      { size: 13, line: 18, family: "monospace" },
    );

    const theme = readTerminalAppearance(document.createElement("div")).theme;

    expect(theme.red).toBe(theme.foreground);
    expect(theme.blue).toBe(theme.foreground);
    expect(theme.magenta).not.toBe(theme.foreground);
  });

  it("leaves nothing behind when the canvas answers with nothing", () => {
    installCanvas();
    installStyles({ "--surface": "rgba(0, 0, 0, 0)" }, { size: 13, line: 18, family: "monospace" });

    const theme = readTerminalAppearance(document.createElement("div")).theme;

    expect(theme.background).not.toBe("rgb(0, 0, 0)");
  });

  it("without a canvas, uses plain values for the scheme of the document", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
    const light = readTerminalAppearance(document.createElement("div"));
    const darkHost = document.createElement("div");
    darkHost.className = "dark";
    const dark = readTerminalAppearance(darkHost);

    expect(luminance(channels(light.theme.background!))).toBeGreaterThan(
      luminance(channels(light.theme.foreground!)),
    );
    expect(luminance(channels(dark.theme.background!))).toBeLessThan(
      luminance(channels(dark.theme.foreground!)),
    );
    expect(light.font).toEqual(dark.font);
    expect(light.font.fontSize).toBeGreaterThan(0);
  });
});
