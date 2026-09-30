// Colour arithmetic for the theme configs: parsing a config's values, and the
// mixing the derivation in `tokens.ts` does with them. Taken from xciii.
//
// A module of its own so the contrast test uses exactly the arithmetic the
// product does: a test that computes contrast its own way is a second
// implementation, and the second one goes wrong.

export type RGB = [number, number, number];

const HEX = /^#?([\da-f]{3}|[\da-f]{4}|[\da-f]{6}|[\da-f]{8})$/i;

// Mattermost writes `#rrggbb`, and a hand-edited theme may be short form or
// carry an alpha there is nowhere to put. Alpha is dropped rather than refused.
export function parseColor(value: unknown): RGB | null {
  if (typeof value !== "string") return null;
  const match = HEX.exec(value.trim());
  if (!match) return null;
  let digits = match[1];
  if (digits.length <= 4) digits = digits.split("").map((c) => c + c).join("");
  return [0, 2, 4].map((i) => parseInt(digits.slice(i, i + 2), 16)) as RGB;
}

export function toHex([r, g, b]: RGB): string {
  return `#${[r, g, b].map((n) => Math.round(clamp(n)).toString(16).padStart(2, "0")).join("")}`;
}

function clamp(n: number): number {
  return Math.min(255, Math.max(0, n));
}

// `amount` is the share of `a`: mix(hue, ground, 0.2) is a fifth of the hue
// over the ground. Rounded because every result ends up as a hex colour, and a
// search that measures the unrounded value settles one level short of the
// ratio it was looking for.
export function mix(a: RGB, b: RGB, amount: number): RGB {
  return [0, 1, 2].map((i) => Math.round(clamp(a[i] * amount + b[i] * (1 - amount)))) as RGB;
}

export function luminance([r, g, b]: RGB): number {
  const channel = (v: number) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

export function contrast(a: RGB, b: RGB): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

export const BLACK: RGB = [0, 0, 0];
export const WHITE: RGB = [255, 255, 255];

// Which end to walk toward to get away from a colour — asked as a contrast
// question, not a luminance threshold: `#abb2bf`, a light grey to any eye,
// sits at luminance 0.44, and a threshold at 0.5 sends it toward white.
export function awayFrom(color: RGB): RGB {
  return contrast(WHITE, color) >= contrast(BLACK, color) ? WHITE : BLACK;
}

// WCAG AA for anything that is a word, and the non-text figure for a mark.
export const AA = 4.5;
export const AA_MARK = 3;

// Ink that can be read on `bg`. The preferred value wins whenever it reaches
// AA, so a config's own answer is honoured; otherwise the candidates are tried
// in turn, and the best contrast is the last resort.
export function readableInk(bg: RGB, preferred: RGB | null, candidates: RGB[] = []): RGB {
  const tried = [preferred, ...candidates, WHITE, BLACK].filter((c): c is RGB => c !== null);
  for (const ink of tried) if (contrast(ink, bg) >= AA) return ink;
  return tried.reduce((best, ink) => (contrast(ink, bg) > contrast(best, bg) ? ink : best), tried[0]);
}

// A colour that can be seen against `bg`, moved as little as possible and
// never off its own hue. A theme written for another product says a colour at
// the size it draws it there: Mattermost's `awayIndicator` is a dot beside a
// name, and here it is the word on a warning.
export function ensureReadable(color: RGB, bg: RGB, ratio = AA): RGB {
  if (contrast(color, bg) >= ratio) return color;
  const target = awayFrom(bg);
  if (contrast(target, bg) < ratio) return target;

  // Contrast rises monotonically toward the far end, so twelve halvings put it
  // within a level of the least adjustment that works.
  let lo = 0;
  let hi = 1;
  for (let i = 0; i < 12; i++) {
    const mid = (lo + hi) / 2;
    if (contrast(mix(target, color, mid), bg) >= ratio) hi = mid;
    else lo = mid;
  }
  return mix(target, color, hi);
}
