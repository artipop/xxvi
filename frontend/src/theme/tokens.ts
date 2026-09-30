// A parsed config becomes the tokens the top of styles.css declares. Every
// other colour in the stylesheet is mixed from these, so setting them is the
// whole of applying a theme.
//
// Two things happen here that a rename would not. What a config does not say
// is derived — a Mattermost theme has no word for a raised surface or a
// border. And what it says unreadably is moved until it reads, never off its
// hue: a value drawn as a dot in a chat client is a word here. A config whose
// own answers already work comes out untouched, which is why the XXVI dark
// half comes out of this exactly as styles.css declares it.

import { AA, AA_MARK, BLACK, ensureReadable, mix, readableInk, toHex, WHITE, contrast, type RGB } from "./color";
import type { ParsedTheme } from "./config";

export type ThemeTokens = Record<string, string>;

export function themeTokens(theme: ParsedTheme): ThemeTokens {
  const { colors: c, dark } = theme;
  const bg = c.centerChannelBg!;
  const text = c.centerChannelColor!;

  // A panel is lighter than the ground under it in both polarities — a sheet
  // on a desk, or the only depth a screen has. Half the way to white barely
  // moves an off-white and would flood a black, hence two lifts.
  const panel = c.surfaceBg ?? lifted(bg, text, dark);
  const panel2 = c.raisedBg ?? mix(text, panel, 0.05);

  // Words sit on the ground and on panels alike, so a colour meant as a word
  // has to read on both.
  const word = (color: RGB, ratio = AA) => ensureReadable(ensureReadable(color, bg, ratio), panel, ratio);

  const accent = word(c.buttonBg!);
  const shadow = c.shadowColor ?? (dark ? BLACK : text);

  return {
    "--bg": toHex(bg),
    "--panel": toHex(panel),
    "--panel-2": toHex(panel2),
    "--line": toHex(c.borderColor ?? mix(text, bg, dark ? 0.12 : 0.14)),
    "--line-strong": toHex(c.borderStrongColor ?? mix(text, bg, 0.22)),
    "--text": toHex(text),
    "--dim": toHex(word(c.dimColor ?? mix(text, bg, 0.6))),
    // Quieter than a word on purpose — a timestamp, a count — but still a
    // mark that has to be seen.
    "--faint": toHex(word(c.faintColor ?? mix(text, bg, 0.4), AA_MARK)),
    "--accent": toHex(accent),
    // `buttonColor` is a chat client's pill; here the same pair is a checked
    // box and a count on the rail.
    "--on-accent": toHex(readableInk(accent, c.buttonColor ?? null, [bg, text])),
    "--ok": toHex(word(c.onlineIndicator ?? [61, 184, 135])),
    "--warn": toHex(word(c.awayIndicator ?? [224, 164, 88])),
    "--bad": toHex(word(c.errorTextColor ?? c.dndIndicator ?? [224, 108, 117])),
    "--shadow": `rgba(${shadow.join(", ")}, ${dark ? ".45" : ".16"})`,
  };
}

// Backs off from the lift rather than fixing it, since lifting a dark ground
// walks the surface toward its own ink.
function lifted(bg: RGB, text: RGB, dark: boolean): RGB {
  for (let lift = dark ? 0.07 : 0.5; lift > 0; lift -= 0.01) {
    const surface = mix(WHITE, bg, lift);
    if (contrast(text, surface) >= AA) return surface;
  }
  return bg;
}
