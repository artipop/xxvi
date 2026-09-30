import type { JSX } from "@solidjs/web";
import type { Screen, Stage, StageTemplate } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { list } from "../state";
import { label, t } from "../i18n";

// Stage templates on this side: the palette's cards, and what picking one does
// to a stage (model.StageTemplate). A template is a preset — its action, brief
// and screens are copied onto the stage and the stage owns them from then on —
// so nothing here decides how a card moves; it decides what a box is called,
// what it looks like and which of its fields the inspector asks for first.

// NODE_ICONS is every icon in public/node-icons, in the order the template
// editor offers them. The standard templates' own come first.
export const NODE_ICONS = [
  "agent", "terminal", "docker", "web", "diff", "notes", "wait", "publish", "verdict", "final",
  "bot", "flask", "rocket", "database", "server", "bug", "package", "cloud", "mobile", "shield",
  "search", "mail", "branch", "book",
];

// A few accents to pick from, which read on the dark panel. A template may hold
// any #rrggbb; these are the ones offered.
export const NODE_COLORS = [
  "#4fb8ad", "#7bc47f", "#e0a458", "#e06c75", "#c38ee0", "#6aa2e8", "#2496ed", "#d9c36a", "#c9cdd6", "#949aab",
];

const FALLBACK_COLOR = "#949aab";

/** NodeIcon draws an icon file in the colour of the text around it: the file is
 *  a mask, so one set of pictures takes every template's accent. */
export function NodeIcon(props: { icon?: string }): JSX.Element {
  const icon = () => (NODE_ICONS.includes(props.icon ?? "") ? props.icon : "wait");
  return <span class="node-icon" aria-hidden="true" style={{ "--icon": `url("/node-icons/${icon()}.svg")` }} />;
}

/** Tile is the icon on its coloured square — the same on a palette card, on a
 *  box and at the head of the inspector. */
export function Tile(props: { tpl?: StageTemplate; big?: boolean }): JSX.Element {
  return (
    <span class={`kind-tile${props.big ? " kind-tile--big" : ""}`}
          style={{ "--kind": props.tpl?.color || FALLBACK_COLOR }}>
      <NodeIcon icon={props.tpl?.icon} />
    </span>
  );
}

/** templateName is what the palette calls a template: its own name, or, for a
 *  builtin nobody renamed, the application's word in the current language. */
export function templateName(tpl: StageTemplate | undefined): string {
  if (!tpl) return t("flows.noTemplate");
  if (tpl.name) return tpl.name;
  return tpl.builtin ? label("stageKind", tpl.id) : tpl.id;
}

export function templateNote(tpl: StageTemplate | undefined): string {
  if (!tpl) return "";
  if (tpl.description) return tpl.description;
  return tpl.builtin ? t(`stageKindNote.${tpl.id}`) : "";
}

/** builtinOf mirrors model.BuiltinTemplateOf: a stage that never named its
 *  template is read by what it does and what it opens first. */
export function builtinOf(stage: Stage): string {
  if (stage.final) return "final";
  if (stage.action === "agent") return "agent";
  if (stage.action === "publish") return "publish";
  if (stage.action === "verdict") return "verdict";
  const sc = list(stage.screens)[0];
  if (!sc) return "wait";
  if (sc.kind === "diff") return "diff";
  if (sc.kind === "browser" || sc.kind === "run") return "web";
  if (sc.kind === "notes") return "notes";
  if (sc.kind === "terminal") return (sc.ref ?? "").trim().startsWith("docker") ? "docker" : "terminal";
  return "wait";
}

export function templateOf(stage: Stage, all: StageTemplate[]): StageTemplate | undefined {
  const id = stage.template || builtinOf(stage);
  return all.find((tpl) => tpl.id === id);
}

/** Shape is what a stage is, read off the stage itself rather than off its
 *  template — the template may since have been edited or deleted, and the
 *  inspector has to ask for what the stage actually does. */
export type Shape = "agent" | "publish" | "verdict" | "final" | "screen" | "wait";

export function shapeOf(stage: Stage, owns: boolean): Shape {
  if (stage.final) return "final";
  if (stage.action === "agent") return "agent";
  if (stage.action === "publish") return "publish";
  if (stage.action === "verdict") return "verdict";
  return owns && list(stage.screens).length > 0 ? "screen" : "wait";
}

/** ownsScreen says a node made from this template *is* its first screen: a
 *  terminal, a page, a diff. The inspector then edits that screen as the node's
 *  own fields, and lists the rest as screens beside it. */
export function ownsScreen(tpl: StageTemplate | undefined): boolean {
  return Boolean(tpl && !tpl.final && (tpl.action || "none") === "none" && list(tpl.screens).length > 0);
}

/** applyTemplate turns a stage into another template's node. What belonged to
 *  the old one goes — an agent's brief means nothing on a terminal, and the
 *  backend refuses outputs where nothing runs — and screens a person added
 *  beside the node's own stay. */
export function applyTemplate(stage: Stage, tpl: StageTemplate, from: StageTemplate | undefined): Partial<Stage> {
  const kept = ownsScreen(from) ? list(stage.screens).slice(1) : list(stage.screens);
  const action = tpl.final ? "none" : tpl.action || "none";
  const patch: Partial<Stage> = {
    template: tpl.id,
    final: Boolean(tpl.final),
    action,
    screens: tpl.final ? [] : [...list(tpl.screens).map((sc) => ({ ...sc }) as Screen), ...kept],
  };
  if (action === "agent") {
    Object.assign(patch, {
      work: tpl.work || "terminal",
      prompt: tpl.prompt || stage.prompt || "",
      crew: list(tpl.crew).length > 0 ? [...list(tpl.crew)] : list(stage.crew),
    });
  } else {
    Object.assign(patch, { work: "", prompt: "", crew: [], writes: [], reads: [], maxRunning: 0 });
  }
  return patch;
}

/** newStage is a node as the palette drops it: the template's preset and its
 *  name, which is the first thing anybody renames. */
export function newStage(tpl: StageTemplate, name: string, at: { x: number; y: number }): Stage {
  const id = `stage-${Math.random().toString(36).slice(2, 9)}`;
  const base = { id, name, action: "none", x: at.x, y: at.y } as Stage;
  return { ...base, ...applyTemplate(base, tpl, undefined) } as Stage;
}

/** nodeDetail is the one line under a box's name: what exactly it runs or
 *  opens, so a flow reads off the canvas without opening every box. */
export function nodeDetail(stage: Stage, owns: boolean): string {
  const sc = list(stage.screens)[0];
  const ref = (sc?.ref ?? "").trim();
  switch (shapeOf(stage, owns)) {
    case "agent": {
      const who = list(stage.crew).join(", ") || t("flows.anyAgent");
      return stage.work === "session" ? `${who} · ${label("work", "session")}` : who;
    }
    case "screen":
      if (sc.kind === "terminal") return ref || t("flows.shell");
      if (sc.kind === "run") return ref ? label("launch", ref) : t("flows.byProject");
      if (sc.kind === "diff") return ref || t("flows.uncommitted");
      return ref;
    case "wait":
      return t("flows.waitsPerson");
  }
  return "";
}
