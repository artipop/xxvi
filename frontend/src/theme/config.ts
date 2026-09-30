// A theme is a config, and the config is Mattermost's — the format xciii
// themes are written in, so its themes come across as files. The keys are the
// ones documented at
// https://docs.mattermost.com/end-user-guide/preferences/customize-your-theme.html
//
// This application draws fewer things than a chat client, so the mapping is
// stated here once:
//
//   used      centerChannelBg → --bg, centerChannelColor → --text,
//             buttonBg → --accent, buttonColor → --on-accent,
//             onlineIndicator → --ok, awayIndicator → --warn,
//             errorTextColor (or dndIndicator) → --bad
//
//   kept,     every other Mattermost key: there is no sidebar of channels,
//   unused    no mention and no unread line here, but a file carrying them is
//             still a Mattermost theme.
//
// What Mattermost has no word for — the raised surfaces, the two borders, the
// two quieter inks, the shadow — is derived in `tokens.ts` unless the config
// states it with one of XXVI_KEYS.

import { parseColor, type RGB } from "./color";

export const MATTERMOST_KEYS = [
  "sidebarBg",
  "sidebarText",
  "sidebarUnreadText",
  "sidebarTextHoverBg",
  "sidebarTextActiveBorder",
  "sidebarTextActiveColor",
  "sidebarHeaderBg",
  "sidebarTeamBarBg",
  "sidebarHeaderTextColor",
  "onlineIndicator",
  "awayIndicator",
  "dndIndicator",
  "mentionBg",
  "mentionBj",
  "mentionColor",
  "centerChannelBg",
  "centerChannelColor",
  "newMessageSeparator",
  "linkColor",
  "buttonBg",
  "buttonColor",
  "errorTextColor",
  "mentionHighlightBg",
  "mentionHighlightLink",
] as const;

// `surfaceBg` and `shadowColor` mean what they mean in xciii, so a theme from
// there keeps its cards; the rest are this application's.
export const XXVI_KEYS = [
  "surfaceBg",
  "raisedBg",
  "borderColor",
  "borderStrongColor",
  "dimColor",
  "faintColor",
  "shadowColor",
] as const;

export type ColorKey = (typeof MATTERMOST_KEYS)[number] | (typeof XXVI_KEYS)[number];

export type ThemeConfig = {
  id?: string;
  name?: string;
  // `light` or `dark`; inferred from centerChannelBg when absent. A string
  // because the configs arrive as JSON, where TypeScript sees nothing
  // narrower — a value that is neither is refused at parse.
  type?: string;
  codeTheme?: string;
} & Partial<Record<ColorKey, string>>;

export type ParsedTheme = {
  id: string;
  dark: boolean;
  colors: Partial<Record<ColorKey, RGB>>;
};

// The ground, the ink on it and the accent. Everything else has somewhere to
// fall back to.
export const REQUIRED_KEYS: ColorKey[] = ["centerChannelBg", "centerChannelColor", "buttonBg"];

export class ThemeConfigError extends Error {}

export function parseTheme(config: ThemeConfig, fallbackId = "custom"): ParsedTheme {
  const colors: Partial<Record<ColorKey, RGB>> = {};
  const bad: string[] = [];
  for (const key of [...MATTERMOST_KEYS, ...XXVI_KEYS]) {
    const value = config[key];
    if (value === undefined || value === "") continue;
    const rgb = parseColor(value);
    if (rgb) colors[key] = rgb;
    else bad.push(key);
  }
  if (bad.length > 0) throw new ThemeConfigError(`color: ${bad.join(", ")}`);
  if (config.type !== undefined && config.type !== "light" && config.type !== "dark") {
    throw new ThemeConfigError("type");
  }
  const missing = REQUIRED_KEYS.filter((key) => !colors[key]);
  if (missing.length > 0) throw new ThemeConfigError(`missing: ${missing.join(", ")}`);

  return {
    id: config.id || fallbackId,
    dark: config.type ? config.type === "dark" : isDarkGround(colors.centerChannelBg!),
    colors,
  };
}

function isDarkGround([r, g, b]: RGB): boolean {
  return 0.299 * r + 0.587 * g + 0.114 * b < 128;
}
