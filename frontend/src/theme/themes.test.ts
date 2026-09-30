import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { AA, AA_MARK, contrast, parseColor, type RGB } from "./color";
import { parseTheme } from "./config";
import { THEME_FAMILIES } from "./themes";
import { themeTokens } from "./tokens";

// styles.css paints the first frame and the config every frame after it, so
// the two disagreeing is a palette that changes as the page finishes loading.
// And every half of every theme has to read — including the ones written for
// another product.

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const rootBlock = /:root\s*\{([\s\S]*?)\n\}/.exec(css)![1];
const declared = (token: string) => new RegExp(`${token}:\\s*([^;]+);`).exec(rootBlock)?.[1].trim();

const same = (value: string) => value.replace(/\s+/g, "").replace(/(^|[,(])\./g, "$10.");
const rgb = (value: string): RGB => parseColor(value)!;

describe("theme", () => {
  it("gives every theme a light half and a dark half", () => {
    for (const f of THEME_FAMILIES) {
      expect([f.id, parseTheme(f.light).dark]).toEqual([f.id, false]);
      expect([f.id, parseTheme(f.dark).dark]).toEqual([f.id, true]);
    }
  });

  it("says in the XXVI dark half exactly what styles.css paints first", () => {
    const tokens = themeTokens(parseTheme(THEME_FAMILIES[0].dark));
    for (const [token, value] of Object.entries(tokens)) {
      expect([token, same(declared(token) ?? "")]).toEqual([token, same(value)]);
    }
  });

  it("declares nothing in styles.css' palette that a theme does not set", () => {
    const tokens = themeTokens(parseTheme(THEME_FAMILIES[0].dark));
    const palette = [...rootBlock.matchAll(/^\s*(--[\w-]+):\s*#/gm)]
      .map((m) => m[1])
      .filter((t) => !t.startsWith("--ansi-") && !t.startsWith("--term-"));
    expect(palette.filter((t) => !(t in tokens))).toEqual([]);
  });

  for (const f of THEME_FAMILIES) {
    for (const half of ["light", "dark"] as const) {
      it(`reads in ${f.id} ${half}`, () => {
        const t = themeTokens(parseTheme(f[half]));
        const low: string[] = [];
        const check = (fg: string, bg: string, ratio: number) => {
          const r = contrast(rgb(t[fg]), rgb(t[bg]));
          if (r < ratio) low.push(`${fg} on ${bg}: ${r.toFixed(2)}`);
        };
        for (const bg of ["--bg", "--panel"]) {
          for (const fg of ["--text", "--dim", "--accent", "--ok", "--warn", "--bad"]) check(fg, bg, AA);
          check("--faint", bg, AA_MARK);
        }
        check("--text", "--panel-2", AA);
        check("--on-accent", "--accent", AA);
        expect(low).toEqual([]);
      });
    }
  }
});
