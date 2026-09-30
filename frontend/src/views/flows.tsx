import { createEffect, createMemo, createSignal, createStore, For, Show, type StoreSetter } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Edge, Flow, PropertyWrite, Screen, Stage, StageTemplate } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { flows, guard, list, loadFlows, loadTemplates, templates, vocabulary } from "../state";
import FlowCanvas, { type CanvasApi, DRAG_KIND, type Selection, type StageWrite, condLabel, edgeIndexOf } from "./flowCanvas";
import {
  KIND_FALLBACK, NODE_COLORS, NODE_ICONS, NodeIcon, PICKER_FALLBACK, Tile, applyTemplate, newStage, ownsScreen, shapeOf, templateName, templateNote,
  templateOf,
} from "./templates";
import { errorText, label, propName, t } from "../i18n";

// The flow editor. The canvas is the flow itself and takes the whole window: a
// box is a stage — a place a card stands and the work done there — an arrow is
// what moves a card on, and the chips along the bottom of a box are what the
// stage leaves on the card.
//
// A box is made from a template — an agent, a terminal, a page, a diff — picked
// from the palette by what it is rather than assembled out of an action, a work
// mode and a list of screens. The inspector then asks only what that node needs:
// a command for a terminal, an address for a page, a brief for an agent. The
// templates themselves are a registry, edited from the same palette.
//
// A conditional arrow is pulled from the chip it asks about, which is the whole
// point of drawing the outputs at all: the edge that branches on «Verdict»
// starts at the thing that produces «Verdict».
//
// A flow is saved whole and checked whole: the refusal that comes back names the
// part that is wrong, and it is shown as it came.
//
// The cards standing on a flow are not shown here: a card is looked at in the
// inbox and worked on its ribbon, and a third place for it is a third place to
// look for it.

export default function FlowsView() {
  const [selectedID, setSelectedID] = createSignal<string>("");

  // Keyed on the flows arriving and on nothing else. Reading the selection as
  // a dependency too — which is what a Solid 1 effect did, because the body was
  // the dependency list — meant «+ New» cleared the selection and the same
  // effect put the first flow straight back.
  createEffect(flows, (all) => {
    if (!selectedID() && all.length > 0) setSelectedID(all[0].id);
  });

  const current = () => flows().find((f) => f.id === selectedID());

  return (
    <Editor flow={current()} selectedID={selectedID()} onPick={setSelectedID}
            onSaved={(f) => setSelectedID(f.id)} onRemoved={() => setSelectedID("")} />
  );
}

/** blankFlow is what «+ New» starts from: one stage, which is the smallest
 *  thing that validates, and it is already the entry. */
function blankFlow(): Flow {
  return {
    id: "", name: "", description: "", entryStage: "s1",
    stages: [{ id: "s1", name: t("flows.firstStage"), template: "wait", action: "none", x: 80, y: 120 } as Stage],
    edges: [],
  } as Flow;
}

/** freeName is the kind's own name, numbered past the ones already taken: two
 *  stages of one name are refused, and «Terminal 2» is a better start than an
 *  error on save. */
function freeName(stages: Stage[], base: string): string {
  const taken = new Set(stages.map((s) => s.name.trim().toLowerCase()));
  if (!taken.has(base.toLowerCase())) return base;
  for (let n = 2; ; n++) {
    const name = `${base} ${n}`;
    if (!taken.has(name.toLowerCase())) return name;
  }
}

function Editor(props: {
  flow?: Flow; selectedID: string; onPick: (id: string) => void;
  onSaved: (f: Flow) => void; onRemoved: () => void;
}) {
  const [draft, setDraft] = createStore<Flow>(blankFlow());
  const [selected, setSelected] = createSignal<Selection>(null);
  const [saveError, setSaveError] = createSignal("");
  const [dirty, setDirty] = createSignal(false);

  // Whether the flow has been asked about. Dropped whenever the panel stops
  // being about it, so a confirmation never follows the eye onto another flow.
  const [confirming, setConfirming] = createSignal(false);

  // The template open in the inspector, when it is a template being edited
  // rather than a stage: its id, or a fresh one being made. Never both at once
  // with a selected stage — the inspector is about one thing.
  const [editing, setEditing] = createSignal<StageTemplate | null>(null);

  // The palette's own state lives here, since the canvas has to know how much of
  // its left edge the palette covers.
  const [paletteOpen, setPaletteOpen] = createSignal(rememberedOpen());
  const togglePalette = () => {
    const next = !paletteOpen();
    setPaletteOpen(next);
    try { localStorage.setItem(PALETTE_KEY, next ? "1" : "0"); } catch { /* the choice lasts this run */ }
  };
  const select = (s: Selection) => { setSelected(s); if (s) setEditing(null); };
  const editTemplate = (tpl: StageTemplate) => { setSelected(null); setEditing(tpl); };

  // Loading a flow into the draft is a copy, not a reference: the canvas is
  // edited freely and nothing is written until «Save», because the engine
  // checks the whole picture before it takes it.
  createEffect(() => props.flow, (f) => {
    setDraft(() => (f ? (JSON.parse(JSON.stringify(f)) as Flow) : blankFlow()));
    setSelected(null);
    setSaveError("");
    setDirty(false);
    setConfirming(false);
  });

  const touch = () => setDirty(true);
  const stages = () => list(draft.stages);
  const edges = () => list(draft.edges);

  const save = async () => {
    setSaveError("");
    try {
      const saved = await API.SaveFlow(JSON.parse(JSON.stringify(draft)) as Flow);
      await loadFlows();
      setDirty(false);
      props.onSaved(saved);
    } catch (e) {
      // The backend's refusal says which part of the graph is wrong. Shown in
      // full, because replacing it with "could not save" throws away the only
      // part that helps.
      setSaveError(errorText(e));
    }
  };

  // Deleting a flow is asked about first, and then written through at once.
  // Everything else here is a draft somebody may still be arranging; a flow
  // somebody has just confirmed deleting is not, and leaving it on screen until
  // «Save» reads as the button not having worked.
  const remove = async () => {
    if (!draft.id) return;
    setConfirming(false);
    await guard(() => API.DeleteFlow(draft.id));
    await loadFlows();
    props.onRemoved();
  };

  // Where a new stage goes is the layout's: after the last column, since
  // nothing leads to it yet. The canvas then brings it into sight.
  let canvas: CanvasApi | undefined;

  const addStage = (tpl: StageTemplate) => {
    const stage = newStage(tpl, freeName(stages(), templateName(tpl)));
    setDraft((f) => {
      const list = f.stages || (f.stages = []);
      list.push(stage);
      if (!f.entryStage) f.entryStage = stage.id;
    });
    select({ kind: "stage", id: stage.id });
    touch();
    canvas?.reveal(stage.id, INSPECTOR_WIDTH);
  };

  // What a stage leaves on the card, for the chips on its box. Branchable is
  // whether an arrow can ask about it: an arrow asks "is it this value", and
  // there is nothing here that states a property's values, so every declared
  // output can be branched on. It stays a separate answer because the question
  // is the box's rather than the model's — see FlowCanvas.
  const writesOf = (stage: Stage): StageWrite[] =>
    list(stage.writes).map((w) => ({ name: w.property, branchable: true }));

  // The properties the application writes itself, mirroring model.IsAppProperty:
  // the outcome, the MR a publish opens or a review source brings, the verdict
  // sent to it. Reading one of them is not reading something nobody writes.
  const appProperty = (name: string) =>
    [vocabulary().outcomeProperty, "MR", "Review"].some((p) => sameFold(p, name));

  // The dataflow check: a conditional arrow reading a property no stage of this
  // flow declares it writes. Not an error — the value may be a person's own
  // answer — but a flow built on a property nothing produces is a flow that
  // quietly never moves, and it is worth a sentence here rather than a card
  // standing still later.
  const unwritten = createMemo(() => {
    const written = new Set(stages().flatMap((s) => list(s.writes).map((w) => w.property.trim().toLowerCase())));
    const out: string[] = [];
    for (const e of edges()) {
      const name = e.if?.property?.trim();
      if (!name || e.on === "card.changed") continue;
      if (appProperty(name)) continue;
      if (written.has(name.toLowerCase()) || out.some((n) => sameFold(n, name))) continue;
      out.push(name);
    }
    return out;
  });

  // The same check for screens: a window pointing at a property nobody writes
  // is a window that quietly stays blank, and a blank window with no reason is
  // worse than a sentence here.
  //
  // Said per stage: «a screen opens a property» left the reader to find which
  // screen, on which box, and what «opens» meant.
  const unresolved = createMemo(() => {
    const written = new Set(stages().flatMap((s) => list(s.writes).map((w) => w.property.trim().toLowerCase())));
    const out: Array<{ stage: string; property: string }> = [];
    for (const stage of stages()) {
      for (const sc of list(stage.screens)) {
        for (const name of refNames(sc.ref ?? "")) {
          if (appProperty(name)) continue;
          if (written.has(name.toLowerCase()) || out.some((o) => o.stage === stage.name && sameFold(o.property, name))) continue;
          out.push({ stage: stage.name, property: name });
        }
      }
    }
    return out;
  });

  const selectedStage = () => (selected()?.kind === "stage" ? selected()!.id : "");
  const selectedEdge = () => (selected()?.kind === "edge" ? edgeIndexOf(selected()!.id) : -1);

  return (
    <div class="flowpage">
      <header class="flowpage__head">
        <div class="flowpage__flows" role="tablist">
          <For each={flows()}>
            {(f) => (
              <button class={`flowtab ${f.id === props.selectedID ? "on" : ""}`} role="tab"
                      aria-selected={f.id === props.selectedID ? "true" : "false"} onClick={() => props.onPick(f.id)}>
                {f.name}
              </button>
            )}
          </For>
          <button class={`flowtab flowtab--new ${props.selectedID === "" ? "on" : ""}`}
                  onClick={() => props.onPick("")}>{t("flows.new")}</button>
        </div>

        <div class="flowpage__meta">
          <input type="text" class="flowpage__name" placeholder={t("flows.namePlaceholder")} value={draft.name}
                 aria-label={t("projects.name")}
                 onInput={(e) => { setDraft((d) => { d.name = e.currentTarget.value; }); touch(); }} />
          <input type="text" class="flowpage__desc" placeholder={t("flows.description")} value={draft.description ?? ""}
                 aria-label={t("flows.description")}
                 onInput={(e) => { setDraft((d) => { d.description = e.currentTarget.value; }); touch(); }} />
          <div class="spacer" />
          <Show when={dirty()}><span class="meta">{t("flows.unsaved")}</span></Show>
          <Show when={draft.id}>
            <Show when={confirming()} fallback={
              <button class="btn quiet" onClick={() => setConfirming(true)}>{t("flows.delete")}</button>
            }>
              <span class="meta">{t("flows.confirmDelete", { name: draft.name })}</span>
              <button class="btn quiet" onClick={() => setConfirming(false)}>{t("common.cancel")}</button>
              <button class="btn danger" onClick={remove}>{t("common.delete")}</button>
            </Show>
          </Show>
          <button class="btn primary" onClick={save}>{t("common.save")}</button>
        </div>
      </header>

      <div class="flowpage__body">
        <FlowCanvas
          flow={draft}
          triggers={list(vocabulary().triggers)}
          templates={templates()}
          outcomeProperty={vocabulary().outcomeProperty}
          outcomePassed={list(vocabulary().outcomeValues)[0]}
          writesOf={writesOf}
          selected={selected()}
          onSelect={select}
          onApi={(api) => { canvas = api; }}
          insetLeft={paletteOpen() ? PALETTE_WIDTH : 0}
          insetRight={selected() || editing() ? INSPECTOR_WIDTH : 0}
          onDropKind={(id) => {
            const tpl = templates().find((x) => x.id === id);
            if (tpl) addStage(tpl);
          }}
          onChange={(nextStages, nextEdges) => {
            setDraft((f) => { f.stages = nextStages; f.edges = nextEdges; });
            touch();
          }}
        />

        <Palette open={paletteOpen()} onToggle={togglePalette} onAdd={(tpl) => addStage(tpl)} onEdit={editTemplate}
                 onNew={() => editTemplate({ id: "", name: "", icon: "terminal", color: NODE_COLORS[0], action: "none" } as StageTemplate)} />

        <Show when={selected() || editing()}>
          <aside class="flowpage__inspector">
            <Show when={editing()} keyed>
              {(tpl) => <TemplatePanel tpl={tpl} onClose={() => setEditing(null)} onSaved={(saved) => setEditing(saved)} />}
            </Show>
            <Show when={selectedStage()}>
              <StagePanel draft={draft} setDraft={setDraft} stageID={selectedStage()} onChange={touch}
                          onClose={() => setSelected(null)} onSaveAsTemplate={editTemplate} />
            </Show>
            <Show when={selectedEdge() >= 0}>
              <EdgePanel draft={draft} setDraft={setDraft} index={selectedEdge()} onChange={touch}
                         onDeleted={() => setSelected(null)} />
            </Show>
          </aside>
        </Show>

        <Show when={saveError() || unwritten().length > 0 || unresolved().length > 0}>
          <div class="flowpage__notes">
            <Show when={saveError()}>
              <div class="error">
                <pre>{saveError()}</pre>
                <button class="btn quiet tiny" onClick={() => setSaveError("")}>×</button>
              </div>
            </Show>
            <Show when={unwritten().length > 0}>
              <div class="warn-note">
                {t("flows.unwritten", { properties: unwritten().map((n) => `«${propName(n)}»`).join(", ") })}
              </div>
            </Show>
            <Show when={unresolved().length > 0}>
              <div class="warn-note">
                <For each={unresolved()}>
                  {(u) => <div>{t("flows.unresolved", { stage: u.stage, property: propName(u.property) })}</div>}
                </For>
              </div>
            </Show>
          </div>
        </Show>
      </div>
    </div>
  );
}

const PALETTE_KEY = "xxvi.flows.palette";

// How much of the canvas the open palette covers: its width in styles.css and
// the gap it floats at.
const PALETTE_WIDTH = 292 + 14;
const INSPECTOR_WIDTH = 380 + 14;

function rememberedOpen(): boolean {
  try { return localStorage.getItem(PALETTE_KEY) !== "0"; } catch { return true; }
}

/**
 * Palette is what a flow is built from: one big card per template, with its
 * logo, clicked to add or dragged to where it should stand. It floats over the
 * canvas rather than beside it, so the canvas keeps the whole width, and folds
 * away to a button once the flow is built. The templates are edited from here
 * too: a pencil on a card, and a card of its own for a new one.
 */
function Palette(props: {
  open: boolean; onToggle: () => void;
  onAdd: (tpl: StageTemplate) => void; onEdit: (tpl: StageTemplate) => void; onNew: () => void;
}) {
  const toggle = () => props.onToggle();

  return (
    <Show when={props.open} fallback={
      <button class="btn flowpage__paletteOpen" onClick={toggle}>{t("flows.addNode")}</button>
    }>
      <div class="palette">
        <div class="palette__head">
          <span class="caption">{t("flows.palette")}</span>
          <div class="spacer" />
          <button class="btn quiet palette__fold" onClick={toggle} aria-label={t("flows.paletteFold")}
                  title={t("flows.paletteFold")}>‹</button>
        </div>
        <div class="palette__grid">
          <For each={templates()}>
            {(tpl) => (
              <div class="kind-card" role="button" tabindex="0" draggable="true"
                   title={templateNote(tpl)}
                   style={{ "--kind": tpl.color || KIND_FALLBACK }}
                   onClick={() => props.onAdd(tpl)}
                   onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); props.onAdd(tpl); } }}
                   onDragStart={(e) => {
                     e.dataTransfer?.setData(DRAG_KIND, tpl.id);
                     if (e.dataTransfer) e.dataTransfer.effectAllowed = "copy";
                   }}>
                <Tile tpl={tpl} />
                <span class="kind-card__name">{templateName(tpl)}</span>
                <span class="kind-card__note">{templateNote(tpl)}</span>
                <button class="kind-card__edit" title={t("flows.editTemplate")} aria-label={t("flows.editTemplate")}
                        onClick={(e) => { e.stopPropagation(); props.onEdit(tpl); }}>✎</button>
              </div>
            )}
          </For>
          <button class="kind-card kind-card--new" onClick={props.onNew}>
            <span class="kind-card__plus">+</span>
            <span class="kind-card__name">{t("flows.newTemplate")}</span>
          </button>
        </div>
        <details class="palette__help">
          <summary>{t("flows.howToConnect")}</summary>
          <p>{t("flows.canvasHint")}</p>
        </details>
      </div>
    </Show>
  );
}

function sameFold(a: string | undefined | null, b: string | undefined | null): boolean {
  return (a || "").trim().toLowerCase() === (b || "").trim().toLowerCase();
}

/** upstreamWrites is every property the stages ahead of this one declare, and
 *  which stage declares it. Kept in step with Go's Flow.UpstreamWrites: walked
 *  backwards over the incoming edges with a visited set, because a flow has
 *  cycles on purpose — a failed check sends the card back to the agent. A stage
 *  never reads its own output back. */
// refNames mirrors model.ScreenRefs on the Go side: «{Preview}», and the braces
// are the whole syntax. Anything richer would be a script.
function refNames(ref: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const m of ref.matchAll(/\{([^}]+)\}/g)) {
    const name = m[1].trim();
    const key = name.toLowerCase();
    if (name && !seen.has(key)) { seen.add(key); out.push(name); }
  }
  return out;
}

function upstreamWrites(flow: Flow, from: string): Array<{ property: string; from: string }> {
  const stages = new Map(list(flow.stages).map((s) => [s.id, s]));
  const seen = new Set([from]);
  const taken = new Set<string>();
  const out: Array<{ property: string; from: string }> = [];
  const queue = [from];
  while (queue.length > 0) {
    const at = queue.shift() as string;
    for (const edge of list(flow.edges)) {
      if (edge.to !== at || seen.has(edge.from)) continue;
      seen.add(edge.from);
      queue.push(edge.from);
      const stage = stages.get(edge.from);
      if (!stage) continue;
      for (const w of list(stage.writes)) {
        const key = w.property.trim().toLowerCase();
        if (!key || taken.has(key)) continue;
        taken.add(key);
        out.push({ property: w.property, from: stage.name });
      }
    }
  }
  return out;
}


function StagePanel(props: {
  draft: Flow; setDraft: StoreSetter<Flow>; stageID: string; onChange: () => void; onClose: () => void;
  onSaveAsTemplate: (tpl: StageTemplate) => void;
}) {
  const index = () => list(props.draft.stages).findIndex((s) => s.id === props.stageID);
  const stage = () => list(props.draft.stages)[index()];
  const tpl = () => templateOf(stage(), templates());
  const owns = () => ownsScreen(tpl());
  const shape = () => shapeOf(stage(), owns());

  const set = (patch: Partial<Stage>) => { props.setDraft((f) => { Object.assign(list(f.stages)[index()], patch); }); props.onChange(); };

  const setTemplate = (next: StageTemplate) => {
    if (next.id !== tpl()?.id) set(applyTemplate(stage(), next, tpl()));
  };

  // A stage somebody has shaped by hand is the best start for a template of
  // their own: its action, brief and screens, under a name still to be given.
  const saveAsTemplate = () => {
    const st = stage();
    props.onSaveAsTemplate({
      id: "", name: "", icon: tpl()?.icon || "terminal", color: tpl()?.color || NODE_COLORS[0],
      action: st.action || "none", work: st.work, prompt: st.prompt,
      final: st.final, screens: list(st.screens).map((sc) => ({ ...sc })),
    } as StageTemplate);
  };

  const removeStage = () => {
    const id = props.stageID;
    props.setDraft((f) => {
      f.stages = list(f.stages).filter((s) => s.id !== id);
      f.edges = list(f.edges).filter((e) => e.from !== id && e.to !== id);
      if (f.entryStage === id) f.entryStage = list(f.stages)[0]?.id ?? "";
    });
    props.onChange();
    props.onClose();
  };

  // ---- what the stage leaves on the card ----

  const writes = () => list(stage().writes);

  const setWrites = (next: PropertyWrite[]) => set({ writes: next });

  const addWrite = () => setWrites([...writes(), { property: "", required: false } as PropertyWrite]);

  const editWrite = (i: number, patch: Partial<PropertyWrite>) =>
    setWrites(writes().map((w, at) => (at === i ? { ...w, ...patch } : w)));

  const removeWrite = (i: number) => setWrites(writes().filter((_, at) => at !== i));

  // ---- what it is handed on the way in ----

  const available = () => upstreamWrites(props.draft, props.stageID);
  const reads = () => list(stage().reads);

  const toggleRead = (name: string) => {
    const chosen = new Set(reads());
    chosen.has(name) ? chosen.delete(name) : chosen.add(name);
    set({ reads: [...chosen] });
  };

  // ---- the window the node is, and the ones beside it ----
  //
  // A terminal, a page, a diff or notes *is* its first screen, so that screen is
  // edited as the node's own fields and the list below starts after it.

  const screens = () => list(stage().screens);
  const own = () => (shape() === "screen" ? 1 : 0);
  const primary = () => screens()[0];
  const extra = () => screens().slice(own());

  const setScreens = (next: Screen[]) => set({ screens: next });

  const editScreen = (i: number, patch: Partial<Screen>) =>
    setScreens(screens().map((sc, at) => (at === i ? { ...sc, ...patch } : sc)));

  const addScreen = () =>
    setScreens([...screens(), { kind: vocabulary().screenKinds?.[0] ?? "notes", ref: "" } as Screen]);

  const removeScreen = (i: number) => setScreens(screens().filter((_, at) => at !== i));

  const refHint = (k: string) =>
    k === "notes" ? t("flows.refNotes")
      : k === "terminal" ? t("flows.refTerminal")
      : k === "diff" ? t("flows.refDiff")
      : k === "run" ? t("flows.refRun")
      : t("flows.refBrowser");

  const note = () => t(`shapeHelp.${shape()}`);

  return (
    <Show when={stage()}>
      <div class="inspector">
        <div class="inspector__head">
          <Tile tpl={tpl()} big />
          <div class="inspector__title">
            <input type="text" class="inspector__name" value={stage().name} aria-label={t("projects.name")}
                   onInput={(e) => set({ name: e.currentTarget.value })} />
            <span class="meta">
              {templateName(tpl())}
              <Show when={props.draft.entryStage === props.stageID}>{` · ${t("flows.isEntry")}`}</Show>
            </span>
          </div>
          <button class="btn quiet inspector__close" onClick={props.onClose} aria-label={t("common.close")}>×</button>
        </div>

        {/* The template can be changed in place: a wait that turns out to need
            a diff is the same stage with the same arrows, not a new one. */}
        <div class="kind-strip" role="radiogroup" aria-label={t("flows.kind")}>
          <For each={templates()}>
            {(x) => (
              <button class={`kind-strip__item ${x.id === tpl()?.id ? "on" : ""}`}
                      style={{ "--kind": x.color || KIND_FALLBACK }}
                      role="radio" aria-checked={x.id === tpl()?.id ? "true" : "false"}
                      title={templateName(x)} aria-label={templateName(x)}
                      onClick={() => setTemplate(x)}>
                <NodeIcon icon={x.icon} />
              </button>
            )}
          </For>
        </div>

        <p class="inspector__note">{note()}</p>

        <Show when={shape() === "screen" && primary()?.kind === "terminal"}>
          <label class="field">
            <span>{t("flows.command")}</span>
            <input type="text" class="mono" value={primary()?.ref ?? ""}
                   placeholder={t("flows.refTerminal")}
                   onInput={(e) => editScreen(0, { ref: e.currentTarget.value })} />
          </label>
        </Show>

        <Show when={shape() === "screen" && (primary()?.kind === "run" || primary()?.kind === "browser")}>
          <div class="field">
            <span class="field-label">{t("flows.webWhat")}</span>
            <div class="segmented">
              <button class={primary()?.kind === "run" ? "on" : ""}
                      onClick={() => editScreen(0, { kind: "run", ref: "web" })}>{t("flows.localFrontOption")}</button>
              <button class={primary()?.kind === "browser" ? "on" : ""}
                      onClick={() => editScreen(0, { kind: "browser", ref: primary()?.kind === "browser" ? primary()!.ref : "" })}>
                {t("flows.addressOption")}
              </button>
            </div>
          </div>
          <Show when={primary()?.kind === "browser"} fallback={
            <label class="field">
              <span>{t("flows.launchAs")}</span>
              <select value={primary()?.ref ?? ""} onChange={(e) => editScreen(0, { ref: e.currentTarget.value })}>
                <For each={LAUNCHES}>
                  {(l) => <option value={l}>{l ? label("launch", l) : t("flows.byProject")}</option>}
                </For>
              </select>
              <span class="meta">{t("flows.localFrontNote")}</span>
            </label>
          }>
            <label class="field">
              <span>{t("flows.address")}</span>
              <input type="text" class="mono" value={primary()?.ref ?? ""} placeholder={t("flows.refBrowser")}
                     onInput={(e) => editScreen(0, { ref: e.currentTarget.value })} />
              <span class="meta">{t("flows.refNote")}</span>
            </label>
          </Show>
        </Show>

        <Show when={shape() === "screen" && primary()?.kind === "diff"}>
          <label class="field">
            <span>{t("flows.revisions")}</span>
            <input type="text" class="mono" value={primary()?.ref ?? ""} placeholder={t("flows.refDiff")}
                   onInput={(e) => editScreen(0, { ref: e.currentTarget.value })} />
          </label>
        </Show>

        <Show when={shape() === "screen" && primary()?.kind === "notes"}>
          <label class="field">
            <span>{t("flows.notesFile")}</span>
            <input type="text" class="mono" value={primary()?.ref ?? ""} placeholder={t("flows.refNotes")}
                   onInput={(e) => editScreen(0, { ref: e.currentTarget.value })} />
          </label>
        </Show>

        <Show when={shape() === "agent"}>
          {/* Where the work happens: a terminal is where a person sits beside the
              agent and answers it in its own interface, a session is a step
              nobody watches (docs/system.md §4.1.1). */}
          <div class="field">
            <span class="field-label">{t("flows.whereWork")}</span>
            <div class="segmented">
              <For each={list(vocabulary().works)}>
                {(w) => (
                  <button class={(stage().work || "terminal") === w ? "on" : ""}
                          onClick={() => set({ work: w })}>{label("work", w)}</button>
                )}
              </For>
            </div>
            <span class="meta">
              {stage().work === "session" ? t("flows.sessionNote") : t("flows.terminalNote")}
            </span>
          </div>

          <label class="field">
            <span>{t("flows.stagePrompt")}</span>
            <textarea value={stage().prompt ?? ""} onInput={(e) => set({ prompt: e.currentTarget.value })} />
          </label>
          <label class="field">
            <span>{t("flows.maxRunning")}</span>
            <input type="text" value={String(stage().maxRunning ?? 0)}
                   onInput={(e) => set({ maxRunning: Number(e.currentTarget.value) || 0 })} />
          </label>

          {/* The stage's outputs. Declaring one is what makes a transition on a
              property deterministic rather than hopeful: the arrow that asks
              about «Verdict» is pulled from the stage that must produce it. */}
          <div class="field">
            <div class="row">
              <span class="caption">{t("flows.writes")}</span>
              <div class="spacer" />
              <button class="btn quiet" onClick={addWrite}>{t("flows.addWrite")}</button>
            </div>
            <Show when={writes().length > 0} fallback={
              <div class="meta">{t("flows.noWrites")}</div>
            }>
              <For each={writes()}>
                {(w, i) => (
                  <div class="row list-row">
                    <input type="text" placeholder={t("flows.writePlaceholder")} value={w.property} class="grow"
                           onInput={(e) => editWrite(i(), { property: e.currentTarget.value })} />
                    <label class="meta" title={t("flows.requiredTitle")}>
                      <input type="checkbox" checked={Boolean(w.required)}
                             onChange={(e) => editWrite(i(), { required: e.currentTarget.checked })} />
                      {" "}{t("flows.required")}
                    </label>
                    <button class="btn quiet" onClick={() => removeWrite(i())}>✕</button>
                  </div>
                )}
              </For>
              <div class="meta note">
                {t("flows.writesNote")}
              </div>
            </Show>
          </div>

          {/* The stage's inputs. Empty is not "nothing": it falls back to
              whatever the stages ahead of this one declare they write, so a
              value already on the card does not have to be ticked again on
              every later stage. */}
          <div class="field">
            <span class="field-label">
              {t("flows.reads")}
            </span>
            <Show when={available().length > 0} fallback={
              <div class="meta">{t("flows.noUpstream")}</div>
            }>
              <div class="row wrap">
                <For each={available()}>
                  {(w) => (
                    <button class={`btn ${reads().includes(w.property) ? "primary" : "quiet"}`}
                            title={t("flows.writtenBy", { stage: w.from })}
                            onClick={() => toggleRead(w.property)}>
                      {w.property}
                    </button>
                  )}
                </For>
              </div>
              <div class="meta note">
                {reads().length === 0
                  ? t("flows.readsAll")
                  : t("flows.readsSome")}
              </div>
            </Show>
          </div>
        </Show>

        {/* Screens beside the node's own, on every kind but the final one: a
            stage where nothing runs is exactly where somebody is looking. */}
        <Show when={shape() !== "final"}>
          <div class="field">
            <div class="row">
              <span class="caption">{own() ? t("flows.moreScreens") : t("flows.screens")}</span>
              <div class="spacer" />
              <button class="btn quiet" onClick={addScreen}>{t("flows.addScreen")}</button>
            </div>
            <Show when={extra().length > 0} fallback={
              <div class="meta">{own() ? t("flows.noMoreScreens") : t("flows.noScreens")}</div>
            }>
              <For each={extra()}>
                {(sc, i) => (
                  <div class="row list-row">
                    <select value={sc.kind} class="fit"
                            onChange={(e) => editScreen(own() + i(), { kind: e.currentTarget.value })}>
                      <For each={list(vocabulary().screenKinds)}>
                        {(k) => <option value={k}>{label("screen", k)}</option>}
                      </For>
                    </select>
                    <input type="text" placeholder={refHint(sc.kind)} value={sc.ref ?? ""} class="grow"
                           onInput={(e) => editScreen(own() + i(), { ref: e.currentTarget.value })} />
                    <button class="btn quiet" onClick={() => removeScreen(own() + i())}>✕</button>
                  </div>
                )}
              </For>
              <div class="meta note">
                {t("flows.refNote")}
              </div>
            </Show>
          </div>
        </Show>

        <div class="row inspector__foot">
          <button class="btn quiet"
                  disabled={props.draft.entryStage === props.stageID}
                  onClick={() => { props.setDraft((d) => { d.entryStage = props.stageID; }); props.onChange(); }}>
            {props.draft.entryStage === props.stageID ? t("flows.isEntry") : t("flows.makeEntry")}
          </button>
          <button class="btn quiet" onClick={saveAsTemplate} title={t("flows.saveAsTemplateTitle")}>
            {t("flows.saveAsTemplate")}
          </button>
          <div class="spacer" />
          <button class="btn quiet" onClick={removeStage}>{t("common.delete")}</button>
        </div>
      </div>
    </Show>
  );
}


// The ways the run screen can start a project (model.LaunchKinds), and empty
// for «tell by the project's files».
const LAUNCHES = ["web", "backend", "desktop", "mobile", "command", ""];

const BEHAVIOURS = ["wait", "agent", "publish", "verdict", "final"] as const;
type Behaviour = (typeof BEHAVIOURS)[number];

function behaviourOf(tpl: StageTemplate): Behaviour {
  if (tpl.final) return "final";
  const a = tpl.action || "none";
  return a === "none" ? "wait" : (a as Behaviour);
}

/**
 * TemplatePanel edits one template of the palette: how its card and its boxes
 * look, and the preset a node made from it starts with. Saved on its own, apart
 * from the flow: a template is the palette's, not this flow's, and the stages
 * already made from it keep what they were given.
 */
function TemplatePanel(props: {
  tpl: StageTemplate; onClose: () => void; onSaved: (tpl: StageTemplate) => void;
}) {
  const [draft, setDraft] = createStore<StageTemplate>(JSON.parse(JSON.stringify(props.tpl)) as StageTemplate);
  const [error, setError] = createSignal("");
  const [confirming, setConfirming] = createSignal(false);

  const set = (patch: Partial<StageTemplate>) => setDraft((d) => { Object.assign(d, patch); });

  const setBehaviour = (b: Behaviour) =>
    set(b === "final"
      ? { final: true, action: "none", work: "", screens: [] }
      : { final: false, action: b === "wait" ? "none" : b, work: b === "agent" ? (draft.work || "terminal") : "" });

  const screens = () => list(draft.screens);
  const editScreen = (i: number, patch: Partial<Screen>) =>
    set({ screens: screens().map((sc, at) => (at === i ? { ...sc, ...patch } : sc)) });

  const save = async () => {
    setError("");
    try {
      const saved = await API.SaveStageTemplate(JSON.parse(JSON.stringify(draft)) as StageTemplate);
      await loadTemplates();
      props.onSaved(saved);
    } catch (e) {
      setError(errorText(e));
    }
  };

  const remove = async () => {
    setConfirming(false);
    await guard(() => API.DeleteStageTemplate(draft.id));
    await loadTemplates();
    props.onClose();
  };

  return (
    <div class="inspector">
      <div class="inspector__head">
        <Tile tpl={draft} big />
        <div class="inspector__title">
          <input type="text" class="inspector__name" value={draft.name ?? ""}
                 placeholder={draft.builtin ? templateName({ ...draft, name: "" } as StageTemplate) : t("flows.templateNamePlaceholder")}
                 aria-label={t("projects.name")}
                 onInput={(e) => set({ name: e.currentTarget.value })} />
          <span class="meta">{draft.id ? t("flows.template") : t("flows.newTemplate")}</span>
        </div>
        <button class="btn quiet inspector__close" onClick={props.onClose} aria-label={t("common.close")}>×</button>
      </div>

      <label class="field">
        <span>{t("flows.description")}</span>
        <input type="text" value={draft.description ?? ""}
               placeholder={draft.builtin ? templateNote({ ...draft, description: "" } as StageTemplate) : ""}
               onInput={(e) => set({ description: e.currentTarget.value })} />
      </label>

      <div class="field">
        <span class="field-label">{t("flows.icon")}</span>
        <div class="icon-grid" style={{ "--kind": draft.color || KIND_FALLBACK }}>
          <For each={NODE_ICONS}>
            {(icon) => (
              <button class={`icon-grid__item ${draft.icon === icon ? "on" : ""}`} title={icon} aria-label={icon}
                      onClick={() => set({ icon })}>
                <NodeIcon icon={icon} />
              </button>
            )}
          </For>
        </div>
      </div>

      <div class="field">
        <span class="field-label">{t("flows.color")}</span>
        <div class="swatches">
          <For each={NODE_COLORS}>
            {(c) => (
              <button class={`swatch ${draft.color === c ? "on" : ""}`} style={{ background: c }}
                      aria-label={c} title={c} onClick={() => set({ color: c })} />
            )}
          </For>
          <input type="color" class="swatch swatch--pick" value={draft.color || PICKER_FALLBACK}
                 aria-label={t("flows.color")} onInput={(e) => set({ color: e.currentTarget.value })} />
        </div>
      </div>

      <div class="field">
        <span class="field-label">{t("flows.behaviour")}</span>
        <div class="segmented segmented--wrap">
          <For each={BEHAVIOURS}>
            {(b) => (
              <button class={behaviourOf(draft) === b ? "on" : ""} onClick={() => setBehaviour(b)}>
                {t(`behaviour.${b}`)}
              </button>
            )}
          </For>
        </div>
        <span class="meta">{t(`shapeHelp.${behaviourOf(draft) === "wait" && screens().length > 0 ? "screen" : behaviourOf(draft)}`)}</span>
      </div>

      <Show when={behaviourOf(draft) === "agent"}>
        <div class="field">
          <span class="field-label">{t("flows.whereWork")}</span>
          <div class="segmented">
            <For each={list(vocabulary().works)}>
              {(w) => (
                <button class={(draft.work || "terminal") === w ? "on" : ""}
                        onClick={() => set({ work: w })}>{label("work", w)}</button>
              )}
            </For>
          </div>
        </div>
        <label class="field">
          <span>{t("flows.stagePrompt")}</span>
          <textarea value={draft.prompt ?? ""} onInput={(e) => set({ prompt: e.currentTarget.value })} />
        </label>
      </Show>

      <Show when={behaviourOf(draft) !== "final"}>
        <div class="field">
          <div class="row">
            <span class="caption">{t("flows.screens")}</span>
            <div class="spacer" />
            <button class="btn quiet"
                    onClick={() => set({ screens: [...screens(), { kind: "terminal", ref: "" } as Screen] })}>
              {t("flows.addScreen")}
            </button>
          </div>
          <Show when={screens().length > 0} fallback={<div class="meta">{t("flows.noTemplateScreens")}</div>}>
            <For each={screens()}>
              {(sc, i) => (
                <div class="row list-row">
                  <select value={sc.kind} class="fit" onChange={(e) => editScreen(i(), { kind: e.currentTarget.value })}>
                    <For each={list(vocabulary().screenKinds)}>
                      {(k) => <option value={k}>{label("screen", k)}</option>}
                    </For>
                  </select>
                  <input type="text" class="grow" value={sc.ref ?? ""}
                         onInput={(e) => editScreen(i(), { ref: e.currentTarget.value })} />
                  <button class="btn quiet"
                          onClick={() => set({ screens: screens().filter((_, at) => at !== i()) })}>✕</button>
                </div>
              )}
            </For>
            <Show when={behaviourOf(draft) === "wait"}>
              <div class="meta note">{t("flows.firstScreenNote")}</div>
            </Show>
          </Show>
        </div>
      </Show>

      <Show when={error()}>
        <div class="error"><pre>{error()}</pre></div>
      </Show>

      <div class="row inspector__foot">
        <Show when={draft.id}>
          <Show when={confirming()} fallback={
            <button class="btn quiet" onClick={() => setConfirming(true)}>{t("common.delete")}</button>
          }>
            <button class="btn quiet" onClick={() => setConfirming(false)}>{t("common.cancel")}</button>
            <button class="btn danger" onClick={remove}>{t("flows.deleteTemplate")}</button>
          </Show>
        </Show>
        <div class="spacer" />
        <button class="btn primary" onClick={save}>{t("common.save")}</button>
      </div>
      <p class="meta note">{t("flows.templateNote")}</p>
    </div>
  );
}

/**
 * EdgePanel is about the one arrow that is selected, and not about every arrow
 * leaving a stage. The canvas is where an arrow is found; the panel is where the
 * one already in hand is answered.
 */
function EdgePanel(props: {
  draft: Flow; setDraft: StoreSetter<Flow>; index: number; onChange: () => void; onDeleted: () => void;
}) {
  const edge = () => list(props.draft.edges)[props.index];
  const stages = () => list(props.draft.stages);
  const nameOf = (id: string) => stages().find((s) => s.id === id)?.name ?? id;

  const set = (patch: Partial<Edge>) => { props.setDraft((f) => { Object.assign(list(f.edges)[props.index], patch); }); props.onChange(); };

  const removeEdge = () => {
    const at = props.index;
    props.setDraft((f) => { f.edges = list(f.edges).filter((_, i) => i !== at); });
    props.onChange();
    props.onDeleted();
  };

  // The properties a condition may usefully ask about, ordered by the graph:
  // what the stage this arrow leaves produces comes first, then what the stages
  // before it produce. A condition is a question about a value, and the useful
  // ones are the values this flow actually puts on the card.
  const choices = createMemo(() => {
    const e = edge();
    if (!e) return [] as Array<{ property: string; from: string }>;
    const own = list(stages().find((s) => s.id === e.from)?.writes)
      .map((w) => ({ property: w.property, from: nameOf(e.from) }));
    const taken = new Set(own.map((w) => w.property.toLowerCase()));
    const rest = upstreamWrites(props.draft, e.from).filter((w) => !taken.has(w.property.toLowerCase()));
    return [...own, ...rest];
  });

  return (
    <Show when={edge()}>
      <div class="panel">
        <h3>{t("flows.edge")}</h3>
        <div class="meta lead">
          «{nameOf(edge().from)}» → «{nameOf(edge().to)}»{condLabel(edge()) ? ` · ${condLabel(edge())}` : ""}
        </div>

        <div class="grid2">
          <label class="field">
            <span>{t("flows.when")}</span>
            <select value={edge().on} onChange={(e) => set({ on: e.currentTarget.value })}>
              <For each={list(vocabulary().triggers)}>
                {(tr) => <option value={tr.kind}>{label("trigger", tr.kind)}</option>}
              </For>
            </select>
          </label>
          <label class="field">
            <span>{t("flows.to")}</span>
            <select value={edge().to} onChange={(e) => set({ to: e.currentTarget.value })}>
              <For each={stages().filter((s) => s.id !== edge().from)}>
                {(s) => <option value={s.id}>{s.name}</option>}
              </For>
            </select>
          </label>
        </div>

        <Condition edge={edge()} choices={choices()} onChange={(cond) => set({ if: cond })} />

        <div class="row">
          <div class="spacer" />
          <button class="btn quiet" onClick={removeEdge}>{t("flows.removeEdge")}</button>
        </div>
      </div>
    </Show>
  );
}

/**
 * Condition is the question an edge asks about the card. There are exactly two
 * forms and they are mutually exclusive, so this offers a choice of three rather
 * than two independent inputs somebody could fill in at once.
 */
function Condition(props: {
  edge: Edge;
  choices: Array<{ property: string; from: string }>;
  onChange: (cond: any) => void;
}) {
  const kind = () => {
    if (!props.edge.if) return "none";
    return props.edge.if.commentContains ? "words" : "prop";
  };
  const isHuman = () => props.edge.on === "card.changed";
  const outcome = () => vocabulary().outcomeProperty;

  return (
    <div>
      <label class="field">
        <span>{t("flows.condition")}</span>
        <select value={kind()} onChange={(e) => {
          const v = e.currentTarget.value;
          if (v === "none") props.onChange(null);
          // On a person's answer the field defaults to the outcome one: that is
          // what a waiting stage is answered with, and it is the arrow the card
          // then offers as a button.
          else if (v === "prop") props.onChange({ property: isHuman() ? outcome() : "", value: "" });
          else props.onChange({ commentContains: "" });
        }}>
          {/* card.changed has no unconditional form: the condition is not a
              guard there but the event itself — which value fires it. */}
          <Show when={!isHuman()}><option value="none">{t("flows.condNone")}</option></Show>
          <option value="prop">{t("flows.condProp")}</option>
          <Show when={!isHuman()}><option value="words">{t("flows.condWords")}</option></Show>
        </select>
      </label>

      <Show when={kind() === "prop"}>
        <div class="grid2">
          <input type="text" list="cond-props" placeholder={t("card.propName")} value={props.edge.if?.property ?? ""}
                 onInput={(e) => props.onChange({ property: e.currentTarget.value, value: props.edge.if?.value ?? "" })} />
          <input type="text" list="cond-values" placeholder={t("card.propValue")} value={props.edge.if?.value ?? ""}
                 onInput={(e) => props.onChange({ property: props.edge.if?.property ?? "", value: e.currentTarget.value })} />
        </div>
        {/* The flow's own vocabulary, offered rather than imposed: a value a
            person types by hand is still a value. */}
        <datalist id="cond-props">
          <option value={outcome()} label={propName(outcome())} />
          <For each={props.choices}>{(w) => <option value={w.property} label={t("flows.writtenBy", { stage: w.from })} />}</For>
        </datalist>
        <datalist id="cond-values">
          <Show when={sameFold(props.edge.if?.property, outcome())}>
            <For each={list(vocabulary().outcomeValues)}>{(v) => <option value={v} label={label("outcome", v)} />}</For>
          </Show>
        </datalist>
        <Show when={props.choices.length > 0}>
          <div class="meta note">
            {t("flows.writtenBefore", { list: props.choices.map((w) => `«${w.property}» (${w.from})`).join(", ") })}
          </div>
        </Show>
        <Show when={isHuman()}>
          <div class="meta note">
            {t("flows.outcomeBranch", { property: propName(outcome()) })}
          </div>
        </Show>
      </Show>

      <Show when={kind() === "words"}>
        <input type="text" placeholder={t("flows.wordsPlaceholder")} value={props.edge.if?.commentContains ?? ""}
               onInput={(e) => props.onChange({ commentContains: e.currentTarget.value })} />
        <div class="meta note">
          {t("flows.wordsNote")}
        </div>
      </Show>
    </div>
  );
}
