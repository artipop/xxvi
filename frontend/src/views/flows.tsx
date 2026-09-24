import { createEffect, createMemo, createSignal, createStore, storePath, For, Show, type StoreSetter } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Edge, Flow, PropertyWrite, Screen, Stage } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import {
  agents, flows, guard, list, loadFlowCards, loadFlows, openCardByID, stageCards, vocabulary,
} from "../state";
import FlowCanvas, { type Selection, type StageWrite, condLabel, edgeIndexOf } from "./flowCanvas";
import { actionLabel, errorText, label, propName, t } from "../i18n";

// The flow editor. The canvas is the flow itself: a box is a stage — a place a
// card stands and the work done there — an arrow is what moves a card on, and
// the chips along the bottom of a box are what the stage leaves on the card.
//
// A conditional arrow is pulled from the chip it asks about, which is the whole
// point of drawing the outputs at all: the edge that branches on «Verdict»
// starts at the thing that produces «Verdict», instead of being assembled in a
// panel out of a list of every property anybody ever typed.
//
// A flow is saved whole and checked whole: the refusal that comes back names the
// part that is wrong, and it is shown as it came.

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
    <>
      <h1>{t("flows.title")}</h1>
      <p class="lede">
        {t("flows.lede")}
      </p>

      <div class="row wrap toolbar">
        <For each={flows()}>
          {(f) => (
            <button class={`btn ${f.id === selectedID() ? "primary" : ""}`} onClick={() => setSelectedID(f.id)}>
              {f.name}
            </button>
          )}
        </For>
        <button class="btn quiet" onClick={() => setSelectedID("")}>{t("flows.new")}</button>
      </div>

      <Editor flow={current()} onSaved={(f) => setSelectedID(f.id)} onRemoved={() => setSelectedID("")} />
    </>
  );
}

/** blankFlow is what «+ New» starts from: one stage, which is the smallest
 *  thing that validates, and it is already the entry. */
function blankFlow(): Flow {
  return {
    id: "", name: "", description: "", entryStage: "s1",
    stages: [{ id: "s1", name: t("flows.firstStage"), action: "none", x: 80, y: 120 } as Stage],
    edges: [],
  } as Flow;
}

function Editor(props: { flow?: Flow; onSaved: (f: Flow) => void; onRemoved: () => void }) {
  const [draft, setDraft] = createStore<Flow>(blankFlow());
  const [selected, setSelected] = createSignal<Selection>(null);
  const [saveError, setSaveError] = createSignal("");
  const [dirty, setDirty] = createSignal(false);

  // Whether the flow has been asked about. Dropped whenever the panel stops
  // being about it, so a confirmation never follows the eye onto another flow.
  const [confirming, setConfirming] = createSignal(false);

  // Loading a flow into the draft is a copy, not a reference: the canvas is
  // edited freely and nothing is written until «Save», because the engine
  // checks the whole picture before it takes it.
  createEffect(() => props.flow, (f) => {
    setDraft(() => (f ? (JSON.parse(JSON.stringify(f)) as Flow) : blankFlow()));
    setSelected(null);
    setSaveError("");
    setDirty(false);
    setConfirming(false);
    if (f?.id) void loadFlowCards(f.id);
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

  const addStage = (stage?: Partial<Stage>) => {
    const id = `stage-${Math.random().toString(36).slice(2, 9)}`;
    setDraft((f) => {
      const list = f.stages || (f.stages = []);
      list.push({
        id, name: t("flows.newStage"), action: "none",
        x: 60 + list.length * 220, y: 140, ...stage,
      } as Stage);
      if (!f.entryStage) f.entryStage = id;
    });
    setSelected({ kind: "stage", id });
    touch();
  };

  // A review is a stage that runs nothing and shows what is being judged: the
  // waiting point of §4.1 with the diff screen open on it. It is a button
  // rather than a kind of stage of its own, because that is all it is — and a
  // fourth action would have to be explained where two are enough.
  //
  // The edges are not drawn here. Where «passed» and «failed» lead is the
  // one arrow a person draws (docs/system.md §4.4), and guessing it would make
  // the button a flow of its own.
  const addReview = () =>
    addStage({ name: t("flows.review"), action: "none", screens: [{ kind: "diff", ref: "" } as Screen] });

  // Push and MR are the application's own step (docs/system.md §15.2): there
  // is nothing to configure on it, so the button is the whole of it.
  const addPublish = () => addStage({ name: t("flows.mrStage"), action: "publish" });

  const cardsOn = (stageID: string) => stageCards().filter((c) => c.stageId === stageID);

  const counts = createMemo(() =>
    stages().map((s) => {
      const on = cardsOn(s.id);
      return { stageId: s.id, cards: on.length, running: 0, queued: 0 };
    }));

  // What a stage leaves on the card, for the chips on its box. Branchable is
  // whether an arrow can ask about it: an arrow asks "is it this value", and
  // there is nothing here that states a property's values, so every declared
  // output can be branched on. It stays a separate answer because the question
  // is the box's rather than the model's — see FlowCanvas.
  const writesOf = (stage: Stage): StageWrite[] =>
    list(stage.writes).map((w) => ({ name: w.property, branchable: true }));

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
      if (sameFold(name, vocabulary().outcomeProperty)) continue;
      if (written.has(name.toLowerCase()) || out.some((n) => sameFold(n, name))) continue;
      out.push(name);
    }
    return out;
  });

  // The same check for screens: a window pointing at a property nobody writes
  // is a window that quietly stays blank, and a blank window with no reason is
  // worse than a sentence here.
  const unresolved = createMemo(() => {
    const written = new Set(stages().flatMap((s) => list(s.writes).map((w) => w.property.trim().toLowerCase())));
    const out: string[] = [];
    for (const stage of stages()) {
      for (const sc of list(stage.screens)) {
        for (const name of refNames(sc.ref ?? "")) {
          if (sameFold(name, vocabulary().outcomeProperty)) continue;
          if (written.has(name.toLowerCase()) || out.some((n) => sameFold(n, name))) continue;
          out.push(name);
        }
      }
    }
    return out;
  });

  const selectedStage = () => (selected()?.kind === "stage" ? selected()!.id : "");
  const selectedEdge = () => (selected()?.kind === "edge" ? edgeIndexOf(selected()!.id) : -1);

  return (
    <>
      <div class="row wrap toolbar">
        <label class="field grow">
          <span>{t("projects.name")}</span>
          <input type="text" value={draft.name}
                 onInput={(e) => { setDraft(storePath("name", e.currentTarget.value)); touch(); }} />
        </label>
        <label class="field grow-2">
          <span>{t("flows.description")}</span>
          <input type="text" value={draft.description ?? ""}
                 onInput={(e) => { setDraft(storePath("description", e.currentTarget.value)); touch(); }} />
        </label>
      </div>

      <div class="row toolbar">
        <button class="btn" onClick={() => addStage()}>{t("flows.addStage")}</button>
        <button class="btn" onClick={addReview} title={t("flows.addReviewTitle")}>{t("flows.addReview")}</button>
        <button class="btn" onClick={addPublish} title={t("flows.addMRTitle")}>{t("flows.addMR")}</button>
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

      <Show when={saveError()}>
        <div class="error"><pre>{saveError()}</pre></div>
      </Show>

      <Show when={unwritten().length > 0}>
        <div class="warn-note">
          {t("flows.unwritten", { properties: unwritten().map((n) => `«${propName(n)}»`).join(", ") })}
        </div>
      </Show>

      <Show when={unresolved().length > 0}>
        <div class="warn-note">
          {t("flows.unresolved", { properties: unresolved().map((n) => `«${propName(n)}»`).join(", ") })}
        </div>
      </Show>

      <div class="flow-editor">
        <div class="flow-editor__canvas">
          <FlowCanvas
            flow={draft}
            triggers={list(vocabulary().triggers)}
            counts={counts()}
            outcomeProperty={vocabulary().outcomeProperty}
            outcomePassed={list(vocabulary().outcomeValues)[0]}
            writesOf={writesOf}
            selected={selected()}
            onSelect={setSelected}
            onChange={(nextStages, nextEdges) => {
              setDraft((f) => { f.stages = nextStages; f.edges = nextEdges; });
              touch();
            }}
          />
          <div class="meta note">
            {t("flows.canvasHint")}
          </div>
        </div>

        <div class="flow-editor__side">
          <Show when={selectedStage()}>
            <StagePanel draft={draft} setDraft={setDraft} stageID={selectedStage()} onChange={touch}
                        onDeleted={() => setSelected(null)} />
          </Show>
          <Show when={selectedEdge() >= 0}>
            <EdgePanel draft={draft} setDraft={setDraft} index={selectedEdge()} onChange={touch}
                       onDeleted={() => setSelected(null)} />
          </Show>
          <Show when={!selected()}>
            <div class="panel"><div class="empty">{t("flows.selectHint")}</div></div>
          </Show>

          <Show when={cardsOn(selectedStage()).length > 0}>
            <div class="panel">
              <h3>{t("flows.cardsOnStage")}</h3>
              <For each={cardsOn(selectedStage())}>
                {(c) => (
                  <div class="row list-item"
                       onClick={() => openCardByID(c.card.id)}>
                    <span>{c.card.title}</span>
                    <div class="spacer" />
                    <Show when={c.asking}><span class="tag warn"><span class="dot" /></span></Show>
                  </div>
                )}
              </For>
            </div>
          </Show>
        </div>
      </div>
    </>
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
  draft: Flow; setDraft: StoreSetter<Flow>; stageID: string; onChange: () => void; onDeleted: () => void;
}) {
  const index = () => list(props.draft.stages).findIndex((s) => s.id === props.stageID);
  const stage = () => list(props.draft.stages)[index()];

  const set = (patch: Partial<Stage>) => { props.setDraft(storePath("stages", index(), patch)); props.onChange(); };

  const toggleCrew = (name: string) => {
    const crew = new Set(list(stage().crew));
    crew.has(name) ? crew.delete(name) : crew.add(name);
    set({ crew: [...crew] });
  };

  const removeStage = () => {
    const id = props.stageID;
    props.setDraft((f) => {
      f.stages = list(f.stages).filter((s) => s.id !== id);
      f.edges = list(f.edges).filter((e) => e.from !== id && e.to !== id);
      if (f.entryStage === id) f.entryStage = list(f.stages)[0]?.id ?? "";
    });
    props.onChange();
    props.onDeleted();
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

  // ---- what a person standing at this stage sees ----
  //
  // Unlike writes and reads, screens are offered on every stage: an output is
  // about the card and there is nobody to produce one where nothing runs, while
  // a screen is about the person, and the review stage is exactly where the
  // preview has to be open (docs/system.md §12.4).

  const screens = () => list(stage().screens);

  const setScreens = (next: Screen[]) => set({ screens: next });

  const addScreen = () =>
    setScreens([...screens(), { kind: vocabulary().screenKinds?.[0] ?? "notes", ref: "" } as Screen]);

  const editScreen = (i: number, patch: Partial<Screen>) =>
    setScreens(screens().map((sc, at) => (at === i ? { ...sc, ...patch } : sc)));

  const removeScreen = (i: number) => setScreens(screens().filter((_, at) => at !== i));

  const refHint = (kind: string) =>
    kind === "notes" ? t("flows.refNotes")
      : kind === "terminal" ? t("flows.refTerminal")
      : kind === "diff" ? t("flows.refDiff")
      : t("flows.refBrowser");

  return (
    <Show when={stage()}>
      <div class="panel">
        <h3>{t("flows.stage")}</h3>
        <label class="field">
          <span>{t("projects.name")}</span>
          <input type="text" value={stage().name} onInput={(e) => set({ name: e.currentTarget.value })} />
        </label>

        <label class="field">
          <span>{t("flows.whatHappens")}</span>
          <select value={stage().final ? "final" : stage().action}
                  onChange={(e) => {
                    const v = e.currentTarget.value;
                    // A final stage is where the card stops: it runs nothing, so
                    // choosing it and choosing an action are one control.
                    v === "final"
                      ? set({ final: true, action: "none", work: "" })
                      : set({ final: false, action: v, work: v === "agent" ? (stage().work || "terminal") : "" });
                  }}>
            <For each={list(vocabulary().actions)}>
              {(a) => <option value={a}>{actionLabel(a)}</option>}
            </For>
            <option value="final">{t("flows.finalOption")}</option>
          </select>
        </label>

        <Show when={stage().action === "agent" && !stage().final}>
          {/* Where the work happens, and it is a question only an agent stage
              has: a terminal is where a person sits beside the agent and answers
              it in its own interface, a session is a step nobody watches
              (docs/system.md §4.1.1). */}
          <label class="field">
            <span>{t("flows.whereWork")}</span>
            <select value={stage().work || "terminal"}
                    onChange={(e) => set({ work: e.currentTarget.value })}>
              <For each={list(vocabulary().works)}>
                {(w) => <option value={w}>{label("work", w)}</option>}
              </For>
            </select>
            <span class="meta">
              {stage().work === "session"
                ? t("flows.sessionNote")
                : t("flows.terminalNote")}
            </span>
          </label>
          <label class="field">
            <span>{t("flows.stagePrompt")}</span>
            <textarea value={stage().prompt ?? ""} onInput={(e) => set({ prompt: e.currentTarget.value })} />
          </label>
          <div class="field">
            <span class="field-label">
              {t("flows.crew")}
            </span>
            <div class="row wrap">
              <For each={list(agents().agents)}>
                {(a) => (
                  <button class={`btn ${list(stage().crew).includes(a.name) ? "primary" : "quiet"}`}
                          onClick={() => toggleCrew(a.name)}>
                    {a.name}
                  </button>
                )}
              </For>
            </div>
          </div>
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

        {/* Screens are outside the agent-only block on purpose: a stage where
            nothing runs is exactly where somebody is looking. */}
        <div class="field">
          <div class="row">
            <span class="caption">{t("flows.screens")}</span>
            <div class="spacer" />
            <button class="btn quiet" onClick={addScreen}>{t("flows.addScreen")}</button>
          </div>
          <Show when={screens().length > 0} fallback={
            <div class="meta">{t("flows.noScreens")}</div>
          }>
            <For each={screens()}>
              {(sc, i) => (
                <div class="row list-row">
                  <select value={sc.kind} class="fit"
                          onChange={(e) => editScreen(i(), { kind: e.currentTarget.value })}>
                    <For each={list(vocabulary().screenKinds)}>
                      {(k) => <option value={k}>{label("screen", k)}</option>}
                    </For>
                  </select>
                  <input type="text" placeholder={refHint(sc.kind)} value={sc.ref ?? ""} class="grow"
                         onInput={(e) => editScreen(i(), { ref: e.currentTarget.value })} />
                  <button class="btn quiet" onClick={() => removeScreen(i())}>✕</button>
                </div>
              )}
            </For>
            <div class="meta note">
              {t("flows.refNote")}
            </div>
          </Show>
        </div>

        <div class="row">
          <button class="btn quiet"
                  disabled={props.draft.entryStage === props.stageID}
                  onClick={() => { props.setDraft(storePath("entryStage", props.stageID)); props.onChange(); }}>
            {props.draft.entryStage === props.stageID ? t("flows.isEntry") : t("flows.makeEntry")}
          </button>
          <div class="spacer" />
          <button class="btn quiet" onClick={removeStage}>{t("common.delete")}</button>
        </div>
      </div>
    </Show>
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

  const set = (patch: Partial<Edge>) => { props.setDraft(storePath("edges", props.index, patch)); props.onChange(); };

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
