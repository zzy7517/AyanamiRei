/**
 * The app's theme as xterm.js draws it.
 *
 * xterm.js takes concrete colors and cannot read a CSS variable, and it reads
 * a color it cannot parse as the default rather than failing. So every color
 * is resolved where the terminal sits — the browser computes the token, a
 * canvas turns the result into sRGB bytes — and handed over as `rgb()`.
 */
import type { ITheme } from "@xterm/xterm";

export interface Rgb {
  r: number;
  g: number;
  b: number;
}

/** The theme tokens a terminal is drawn with. */
export interface TerminalPalette {
  background: Rgb;
  foreground: Rgb;
  red: Rgb;
  green: Rgb;
  yellow: Rgb;
  blue: Rgb;
  selection: Rgb;
}

export interface TerminalFont {
  fontFamily: string;
  fontSize: number;
  /** Line height as a multiple of the font size, which is how xterm.js takes it. */
  lineHeight: number;
}

export interface TerminalAppearance {
  theme: ITheme;
  font: TerminalFont;
}

const WHITE: Rgb = { r: 255, g: 255, b: 255 };

/**
 * The token set has no magenta and no cyan. Those two are the info blue
 * turned to their hues, so they keep the theme's own intensity.
 */
const MAGENTA_HUE = 310;
const CYAN_HUE = 185;

// How far the grayscale ANSI colors sit along the way from the paper to the
// text. The one on the paper's own side stays just off it — black on a dark
// theme, white on a light one — and the other goes a little past the text.
const NEAR_PAPER = 0.12;
const PAST_TEXT = 1.06;
// A bright color is its color a quarter of the way to white, in a light theme
// as in a dark one: "bright" names the lighter half of the ANSI palette.
const BRIGHT_TOWARD_WHITE = 0.25;

const clampChannel = (value: number) => Math.min(255, Math.max(0, Math.round(value)));

/** `from` moved `t` of the way to `to`. A `t` outside 0..1 carries on past `to`. */
export function mix(from: Rgb, to: Rgb, t: number): Rgb {
  return {
    r: clampChannel(from.r + (to.r - from.r) * t),
    g: clampChannel(from.g + (to.g - from.g) * t),
    b: clampChannel(from.b + (to.b - from.b) * t),
  };
}

/** The one form xterm.js is guaranteed to parse: decimal sRGB. */
export function toCss(color: Rgb, alpha = 1): string {
  return alpha >= 1
    ? `rgb(${color.r}, ${color.g}, ${color.b})`
    : `rgba(${color.r}, ${color.g}, ${color.b}, ${alpha})`;
}

/** WCAG relative luminance, 0 for black and 1 for white. */
export function luminance(color: Rgb): number {
  const channel = (value: number) => {
    const c = value / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(color.r) + 0.7152 * channel(color.g) + 0.0722 * channel(color.b);
}

function toHsl({ r, g, b }: Rgb): { h: number; s: number; l: number } {
  const red = r / 255;
  const green = g / 255;
  const blue = b / 255;
  const max = Math.max(red, green, blue);
  const min = Math.min(red, green, blue);
  const l = (max + min) / 2;
  if (max === min) return { h: 0, s: 0, l };
  const span = max - min;
  const s = l > 0.5 ? span / (2 - max - min) : span / (max + min);
  let h: number;
  if (max === red) h = ((green - blue) / span + (green < blue ? 6 : 0)) * 60;
  else if (max === green) h = ((blue - red) / span + 2) * 60;
  else h = ((red - green) / span + 4) * 60;
  return { h, s, l };
}

function fromHsl(h: number, s: number, l: number): Rgb {
  const hueToChannel = (n: number) => {
    const k = (n + h / 30) % 12;
    const a = s * Math.min(l, 1 - l);
    return Math.round(255 * (l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1))));
  };
  return { r: hueToChannel(0), g: hueToChannel(8), b: hueToChannel(4) };
}

/** `color` turned around the color wheel to `hue` (0-360), keeping its saturation and lightness. */
export function withHue(color: Rgb, hue: number): Rgb {
  const { s, l } = toHsl(color);
  return fromHsl(((hue % 360) + 360) % 360, s, l);
}

/**
 * The xterm.js theme for a palette. Which side of the grayscale is "black" is
 * read off the palette itself, so one mapping serves a light theme and a dark
 * one, and a theme that is neither.
 */
export function buildTerminalTheme(palette: TerminalPalette): ITheme {
  const { background, foreground } = palette;
  const darkBackground = luminance(background) < luminance(foreground);
  const bright = (color: Rgb) => mix(color, WHITE, BRIGHT_TOWARD_WHITE);
  const nearPaper = mix(background, foreground, NEAR_PAPER);
  const pastText = mix(background, foreground, PAST_TEXT);
  const black = darkBackground ? nearPaper : pastText;
  const white = darkBackground ? pastText : nearPaper;
  const magenta = withHue(palette.blue, MAGENTA_HUE);
  const cyan = withHue(palette.blue, CYAN_HUE);

  return {
    background: toCss(background),
    foreground: toCss(foreground),
    cursor: toCss(foreground),
    cursorAccent: toCss(background),
    // Partly transparent, so the selection never hides the text it covers.
    selectionBackground: toCss(palette.selection, 0.45),
    selectionInactiveBackground: toCss(mix(palette.selection, background, 0.5), 0.35),
    scrollbarSliderBackground: toCss(foreground, 0.2),
    scrollbarSliderHoverBackground: toCss(foreground, 0.35),
    scrollbarSliderActiveBackground: toCss(foreground, 0.5),
    black: toCss(black),
    red: toCss(palette.red),
    green: toCss(palette.green),
    yellow: toCss(palette.yellow),
    blue: toCss(palette.blue),
    magenta: toCss(magenta),
    cyan: toCss(cyan),
    white: toCss(white),
    brightBlack: toCss(bright(black)),
    brightRed: toCss(bright(palette.red)),
    brightGreen: toCss(bright(palette.green)),
    brightYellow: toCss(bright(palette.yellow)),
    brightBlue: toCss(bright(palette.blue)),
    brightMagenta: toCss(bright(magenta)),
    brightCyan: toCss(bright(cyan)),
    brightWhite: toCss(bright(white)),
  };
}

// Tokens, as resolved where the terminal sits.
const BACKGROUND_TOKEN = "--surface";
const FOREGROUND_TOKEN = "--foreground";
const RED_TOKEN = "--destructive";
const GREEN_TOKEN = "--success";
const YELLOW_TOKEN = "--warning";
const BLUE_TOKEN = "--info";
const SELECTION_TOKEN = "--surface-selected";

/** A color no token is likely to resolve to, to tell an unparsed one apart from a black one. */
const SENTINEL = "rgb(1, 2, 3)";

/**
 * Reads tokens to sRGB bytes through one canvas: the browser computes each
 * token on a probe, the canvas turns the result into bytes. Null where there
 * is no canvas to do that.
 */
function createColorReader(host: HTMLElement): ((token: string) => Rgb | null) | null {
  const doc = host.ownerDocument;
  const view = doc.defaultView;
  const canvas = doc.createElement("canvas");
  if (!view || typeof canvas.getContext !== "function") return null;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  if (!context) return null;

  return (token) => {
    const probe = doc.createElement("span");
    probe.hidden = true;
    probe.style.color = `var(${token})`;
    host.appendChild(probe);
    try {
      context.fillStyle = SENTINEL;
      // A value the canvas cannot parse leaves the fill as it was.
      const resolved = view.getComputedStyle(probe).color;
      context.fillStyle = resolved;
      if (context.fillStyle === SENTINEL) return null;
      // Each token starts from an empty pixel, so a partly transparent one is
      // not blended over the token read before it.
      context.clearRect(0, 0, 1, 1);
      context.fillStyle = resolved;
      context.fillRect(0, 0, 1, 1);
      const [r = 0, g = 0, b = 0, alpha = 0] = context.getImageData(0, 0, 1, 1).data;
      return alpha === 0 ? null : { r, g, b };
    } catch {
      return null;
    } finally {
      probe.remove();
    }
  };
}

/**
 * The code font and size: the app's monospace at the body step of the text
 * scale, so the Appearance font and size settings carry over. Null when the
 * stylesheet does not answer.
 */
function readFont(host: HTMLElement): TerminalFont | null {
  const doc = host.ownerDocument;
  const view = doc.defaultView;
  if (!view) return null;
  const probe = doc.createElement("span");
  probe.hidden = true;
  probe.className = "font-mono text-body";
  host.appendChild(probe);
  try {
    const style = view.getComputedStyle(probe);
    const fontSize = Number.parseFloat(style.fontSize);
    const lineHeightPx = Number.parseFloat(style.lineHeight);
    const fontFamily = style.fontFamily.trim();
    if (!(fontSize > 0) || !fontFamily) return null;
    return {
      fontFamily,
      fontSize,
      lineHeight: lineHeightPx > 0 ? lineHeightPx / fontSize : 1,
    };
  } finally {
    probe.remove();
  }
}

function readPalette(host: HTMLElement): TerminalPalette | null {
  const read = createColorReader(host);
  const background = read?.(BACKGROUND_TOKEN);
  const foreground = read?.(FOREGROUND_TOKEN);
  // Without these two there is no telling a theme's text from its paper.
  if (!background || !foreground) return null;
  return {
    background,
    foreground,
    // A theme may not define a status color; the text color keeps such programs legible.
    red: read?.(RED_TOKEN) ?? foreground,
    green: read?.(GREEN_TOKEN) ?? foreground,
    yellow: read?.(YELLOW_TOKEN) ?? foreground,
    blue: read?.(BLUE_TOKEN) ?? foreground,
    selection: read?.(SELECTION_TOKEN) ?? mix(foreground, background, 0.6),
  };
}

// Plain values for a page that cannot resolve its tokens, which in practice
// is one without a canvas: only the scheme is picked up from the document.
const FALLBACK_FONT: TerminalFont = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
  fontSize: 13,
  lineHeight: 1.35,
};
const FALLBACK_PALETTES: Record<"light" | "dark", TerminalPalette> = {
  light: {
    background: { r: 255, g: 255, b: 255 },
    foreground: { r: 23, g: 23, b: 26 },
    red: { r: 200, g: 45, b: 45 },
    green: { r: 25, g: 130, b: 80 },
    yellow: { r: 160, g: 120, b: 10 },
    blue: { r: 40, g: 100, b: 210 },
    selection: { r: 180, g: 200, b: 245 },
  },
  dark: {
    background: { r: 30, g: 30, b: 33 },
    foreground: { r: 228, g: 228, b: 232 },
    red: { r: 240, g: 95, b: 95 },
    green: { r: 95, g: 200, b: 130 },
    yellow: { r: 230, g: 195, b: 95 },
    blue: { r: 110, g: 165, b: 250 },
    selection: { r: 70, g: 90, b: 140 },
  },
};

/** The app's theme for the terminal drawn inside `host`. */
export function readTerminalAppearance(host: HTMLElement): TerminalAppearance {
  const palette = readPalette(host) ?? FALLBACK_PALETTES[host.closest(".dark") ? "dark" : "light"];
  return { theme: buildTerminalTheme(palette), font: readFont(host) ?? FALLBACK_FONT };
}
