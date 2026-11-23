/**
 * Rewrite legacy mention shortcodes `[@ id="UUID" label="LABEL"]` to their
 * plain label. Older records stored a mention of a person this way; it has no
 * destination to open, so it shows as `@LABEL`.
 *
 * Pure regex transform shared by web/desktop and mobile. `packages/ui` keeps a
 * copy in `markdown/Markdown.tsx` because it cannot import core.
 */
export function preprocessMentionShortcodes(text: string): string {
  if (!text.includes("[@ ")) return text;
  return text.replace(/\[@\s+([^\]]*)\]/g, (match, attrs: string) => {
    const id = /\bid="([^"]*)"/.exec(attrs)?.[1];
    const label = /\blabel="([^"]*)"/.exec(attrs)?.[1];
    return id && label ? `@${label}` : match;
  });
}
