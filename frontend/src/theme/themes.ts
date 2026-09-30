// Every theme this build ships, in the order the list draws them. A theme is a
// pair — one design, light and dark — and the polarity is a second setting,
// so picking a design never means giving up following the system.
//
// The halves after XXVI's own are xciii's, which ported them from Mattermost's
// community list and wrote the missing half of each from the theme's real
// counterpart (../xciii/docs/themes.md says which half came from where). Only
// xciii's label tints were dropped: there are no labels here.

import discordDark from "./themes/discord-dark.json";
import discordLight from "./themes/discord-light.json";
import githubDark from "./themes/github-dark.json";
import githubLight from "./themes/github-light.json";
import gruvboxDark from "./themes/gruvbox-dark.json";
import gruvboxLight from "./themes/gruvbox-light.json";
import monokaiDark from "./themes/monokai-dark.json";
import monokaiLight from "./themes/monokai-light.json";
import nightOwlDark from "./themes/night-owl-dark.json";
import nightOwlLight from "./themes/night-owl-light.json";
import oneDark from "./themes/one-dark.json";
import oneLight from "./themes/one-light.json";
import solarizedDark from "./themes/solarized-dark.json";
import solarizedLight from "./themes/solarized-light.json";
import windowsDark from "./themes/windows-dark.json";
import windowsLight from "./themes/windows-light.json";
import xxviDark from "./themes/xxvi-dark.json";
import xxviLight from "./themes/xxvi-light.json";
import type { ThemeConfig } from "./config";

export type ThemeFamily = { id: string; name: string; light: ThemeConfig; dark: ThemeConfig };

export const DEFAULT_FAMILY = "xxvi";

export const THEME_FAMILIES: ThemeFamily[] = [
  { id: "xxvi", name: "XXVI", light: xxviLight, dark: xxviDark },
  { id: "windows", name: "Windows", light: windowsLight, dark: windowsDark },
  { id: "one", name: "One", light: oneLight, dark: oneDark },
  { id: "gruvbox", name: "Gruvbox", light: gruvboxLight, dark: gruvboxDark },
  { id: "night-owl", name: "Night Owl", light: nightOwlLight, dark: nightOwlDark },
  { id: "discord", name: "Discord", light: discordLight, dark: discordDark },
  { id: "solarized", name: "Solarized", light: solarizedLight, dark: solarizedDark },
  { id: "github", name: "GitHub", light: githubLight, dark: githubDark },
  { id: "monokai", name: "Monokai", light: monokaiLight, dark: monokaiDark },
];

export function themeFamily(id: string): ThemeFamily | undefined {
  return THEME_FAMILIES.find((family) => family.id === id);
}
