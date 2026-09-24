import { createSignal } from "solid-js";
import type { Msg } from "../bindings/github.com/artipop/xxvi/internal/msg/models";
import { en } from "./locales/en";
import { ru } from "./locales/ru";

// Every word on the screen is said here, and only here. The backend never sends
// a sentence: it sends codes and the values that go into them (internal/msg),
// and names every closed set by its members. What they are called — in which
// language, with which plural — is this file's to say, because this is the only
// side that knows who is reading.

export type Lang = "en" | "ru";
export const LANGS: Lang[] = ["en", "ru"];

/** Args are the values a phrase is worded with. */
export type Args = Record<string, string | number | null | undefined>;
/** A phrase is a template with «{name}» holes, or a function when the grammar
 *  needs more than holes — a plural, a clause that is there or not. */
export type Phrase = string | ((a: Args) => string);
export type Dict = Record<string, Phrase>;

const DICTS: Record<Lang, Dict> = { en, ru };

const STORAGE_KEY = "xxvi.lang";

function initialLang(): Lang {
  try {
    const kept = localStorage.getItem(STORAGE_KEY);
    if (kept === "en" || kept === "ru") return kept;
  } catch {
    // Storage can be unavailable; the system language is the next best guess.
  }
  return navigator.language.toLowerCase().startsWith("ru") ? "ru" : "en";
}

export const [lang, setLangSignal] = createSignal<Lang>(initialLang());

export function setLang(l: Lang) {
  try { localStorage.setItem(STORAGE_KEY, l); } catch { /* remembered for this window only */ }
  document.documentElement.lang = l;
  setLangSignal(l);
}

document.documentElement.lang = lang();

function fill(template: string, a: Args): string {
  return template.replace(/\{(\w+)\}/g, (_, k: string) => {
    const v = a[k];
    return v === undefined || v === null ? "" : String(v);
  });
}

function phrase(key: string): Phrase | undefined {
  return DICTS[lang()][key] ?? DICTS.en[key];
}

/** t words one key. A key missing from both languages shows as itself, which is
 *  a bug to see rather than a blank to miss. */
export function t(key: string, a: Args = {}): string {
  const p = phrase(key);
  if (p === undefined) return key;
  return typeof p === "function" ? p(a) : fill(p, a);
}

/** has says whether a key is worded — for sets the backend may grow. */
export function has(key: string): boolean {
  return phrase(key) !== undefined;
}

/** plural words a count: the key «base.<rule>» for the current language's
 *  plural rule — one, few, many, other — falling back to «base.other». */
export function plural(base: string, n: number, a: Args = {}): string {
  const rule = new Intl.PluralRules(lang()).select(n);
  const key = has(`${base}.${rule}`) ? `${base}.${rule}` : `${base}.other`;
  return t(key, { ...a, n });
}

// ---- the closed sets the backend names by their members ----

/** label words a member of a closed set: a trigger, a screen kind, a work mode.
 *  A member this build does not know yet is shown as it is. */
export function label(set: string, member: string): string {
  const key = `${set}.${member || "_"}`;
  return has(key) ? t(key) : member;
}

/** propName words a card property. Two of them are the application's own and
 *  stored as identifiers — how a stage ended, and the flow a source suggests —
 *  and the rest are the flow's words, shown as they were written. */
export function propName(name: string): string {
  const key = `prop.${name}`;
  return has(key) ? t(key) : name;
}

/** propValue words a value of the application's own outcome field; any other
 *  value is somebody's and is shown as it is. */
export function propValue(name: string, value: string): string {
  if (name.toLowerCase() === "outcome") return label("outcome", value);
  return value;
}

// ---- messages ----

/** say words a message from the backend: a refusal, a journal entry, why a card
 *  moved. The cause, when there is one, goes where the phrase puts «{cause}»,
 *  or after a colon when it puts it nowhere. */
export function say(m: Msg | null | undefined): string {
  if (!m) return "";
  const args: Args = { ...(m.args ?? {}) };
  const cause = m.cause ? say(m.cause) : "";
  args.cause = cause;
  const p = phrase(`msg.${m.code}`);
  let text: string;
  if (p === undefined) {
    // A code this build has no words for: better its code and values than
    // nothing, so the person at least has something to report.
    const values = Object.entries(m.args ?? {}).map(([k, v]) => `${k}=${v}`).join(", ");
    text = values ? `${m.code} (${values})` : m.code;
  } else {
    text = typeof p === "function" ? p(args) : fill(p, args);
  }
  if (cause && !(typeof p === "string" && p.includes("{cause}")) && typeof p !== "function") {
    text = `${text}: ${cause}`;
  }
  // Why a card moved carries the condition that chose the edge, whatever the
  // reason was; it is worded the same after every one of them.
  if (m.args?.ifComment) text += t("cond.suffixComment", { text: m.args.ifComment });
  else if (m.args?.ifProperty) {
    text += t("cond.suffixProp", {
      property: propName(m.args.ifProperty), value: propValue(m.args.ifProperty, m.args.ifValue ?? ""),
    });
  }
  return text;
}

/** errorText is a failure as the person reads it. A refusal from the backend
 *  arrives as a message in the error's cause (main.go); anything else — a
 *  network hiccup, a bug on this side — is shown as it came. */
export function errorText(e: unknown): string {
  const cause = (e as { cause?: unknown } | null)?.cause;
  if (cause && typeof cause === "object" && "code" in cause) return say(cause as Msg);
  if (e instanceof Error) return e.message;
  return String(e);
}

/** entryText is a journal entry as the person reads it: the application's own
 *  message, worded here, or words somebody else wrote. */
export function entryText(e: { msg?: Msg | null; text?: string }): string {
  return e.msg ? say(e.msg) : e.text ?? "";
}

/** quoted puts a name in the quotes this application uses in both languages. */
export const q = (s: string | null | undefined) => `«${s ?? ""}»`;

// ---- what a flow says about itself ----

type CondLike = { property?: string; value?: string; commentContains?: string } | null | undefined;

/** condText is a transition's condition as a person reads it. */
export function condText(c: CondLike): string {
  if (!c) return "";
  if (c.commentContains) return t("cond.comment", { text: c.commentContains });
  if (!c.property && !c.value) return "";
  return t("cond.prop", { property: propName(c.property ?? ""), value: propValue(c.property ?? "", c.value ?? "") });
}

/** waitText is one thing a parked card waits for: the event and, when there is
 *  one, the condition that makes it the one. */
export function waitText(w: { on: string; if?: CondLike }): string {
  const cond = condText(w.if);
  return cond ? `${label("trigger", w.on)} ${cond}` : label("trigger", w.on);
}

/** when is a moment as this language writes it. */
export function when(value: unknown, withSeconds = true): string {
  const d = value instanceof Date ? value : new Date(value as string);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(lang(), {
    day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
    ...(withSeconds ? { second: "2-digit" } : {}),
  });
}

/** markTitle says what one of a waiting stage's two answers does. */
export function markTitle(mark: { value: string; stage: string }): string {
  return t("mark.title", { value: label("outcome", mark.value), stage: mark.stage });
}

/** questionText is what an agent asks, as a person reads it: a form is the
 *  agent's own message; a permission is worded around the tool and the agent's
 *  own title for the call, whichever of the two it gave. */
export function questionText(kind: string | undefined, tool: string | undefined, text: string | undefined): string {
  if (kind !== "permission") return text ?? "";
  const tl = (tool ?? "").trim();
  const tx = (text ?? "").trim();
  if (tl && tx) return t("question.allowBoth", { tool: tl, text: tx });
  if (tx) return t("question.allowText", { text: tx });
  if (tl) return t("question.allowTool", { tool: tl });
  return t("question.allowBare");
}
