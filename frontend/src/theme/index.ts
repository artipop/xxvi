import { createSignal } from "solid-js";
import { parseTheme } from "./config";
import { DEFAULT_FAMILY, themeFamily, THEME_FAMILIES, type ThemeFamily } from "./themes";
import { themeTokens } from "./tokens";

// Which theme applies, and the applying of it: the tokens a config produces
// are written onto the document element, over the ones styles.css declares
// for the first paint.
//
// Two settings, not one — which design, and which polarity — so that following
// the system does not mean giving up every design but one.
//
// Kept in this webview's storage, like the sidebar's width and the terminal
// engine: it is this machine's look, and nothing on the backend reads it.
// Read synchronously before the first render, so a light theme never opens
// on a dark frame.

export type ThemeMode = "light" | "dark" | "system";
export const THEME_MODES: ThemeMode[] = ["system", "light", "dark"];
export { THEME_FAMILIES, type ThemeFamily };

const STORAGE_KEY = "xxvi.theme";

type Kept = { family: string; mode: ThemeMode };

function remembered(): Kept {
  try {
    const kept = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null") as Partial<Kept> | null;
    return {
      family: kept?.family && themeFamily(kept.family) ? kept.family : DEFAULT_FAMILY,
      mode: THEME_MODES.includes(kept?.mode as ThemeMode) ? (kept!.mode as ThemeMode) : "system",
    };
  } catch {
    return { family: DEFAULT_FAMILY, mode: "system" };
  }
}

const start = remembered();
const [family, setFamily] = createSignal(start.family);
const [mode, setMode] = createSignal<ThemeMode>(start.mode);
// Bumped on every apply, for the few things painted from JavaScript — an
// arrow's SVG marker cannot take a CSS variable — to read the colours again.
const [painted, setPainted] = createSignal(0);

export const themeFamilyId = family;
export const themeMode = mode;

const systemDark = window.matchMedia("(prefers-color-scheme: dark)");

// The values are passed rather than read back: Solid holds a write until the
// next flush, and a read straight after it answers with the old value.
function apply({ family: id, mode: m }: Kept) {
  const f = themeFamily(id) ?? themeFamily(DEFAULT_FAMILY)!;
  const dark = m === "dark" || (m === "system" && systemDark.matches);
  const config = dark ? f.dark : f.light;
  const theme = parseTheme(config, config.id ?? f.id);
  const root = document.documentElement;
  root.dataset.theme = theme.dark ? "dark" : "light";
  // Told, or the webview draws its own widgetry — a scrollbar, a select's
  // list — for the wrong side.
  root.style.colorScheme = theme.dark ? "dark" : "light";
  for (const [token, value] of Object.entries(themeTokens(theme))) root.style.setProperty(token, value);
  setPainted((n) => n + 1);
}

function keep(kept: Kept) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(kept));
  } catch {
    // The choice lasts this run.
  }
}

export function chooseThemeFamily(id: string) {
  const kept = { family: id, mode: mode() };
  setFamily(id);
  keep(kept);
  apply(kept);
}

export function chooseThemeMode(m: ThemeMode) {
  const kept = { family: family(), mode: m };
  setMode(m);
  keep(kept);
  apply(kept);
}

/** themeColor is a token's value now, tracked, for what cannot read CSS. */
export function themeColor(token: string): string {
  painted();
  return getComputedStyle(document.documentElement).getPropertyValue(token).trim();
}

systemDark.addEventListener("change", () => {
  if (mode() === "system") apply({ family: family(), mode: mode() });
});
apply(start);
