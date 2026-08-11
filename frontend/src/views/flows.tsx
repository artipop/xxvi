import { createEffect, createSignal, For, Show } from "solid-js";
import { createStore, produce } from "solid-js/store";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Edge, Flow, Stage } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import {
  agents, flows, guard, loadFlowCards, loadFlows, openCardByID, stageCards, vocabulary,
} from "../state";

// The flow editor. The canvas is the flow itself: a box is a stage — a place a
// card stands and the work done there — and an arrow is what moves a card on.
//
// A flow is saved whole and checked whole: the refusal that comes back names
// the part that is wrong, and it is shown as it came.

export default function FlowsView() {
  const [selectedID, setSelectedID] = createSignal<string>("");

  createEffect(() => {
    if (!selectedID() && flows().length > 0) setSelectedID(flows()[0].id);
  });

  const current = () => flows().find((f) => f.id === selectedID());

  return (
    <>
      <h1>Флоу</h1>
      <p class="lede">
        Стадия — место, где карточка стоит, и работа, которая на ней делается.
        Переход — событие, которое двигает карточку дальше.
      </p>

      <div class="row wrap" style={{ "margin-bottom": "14px" }}>
        <For each={flows()}>
          {(f) => (
            <button class={`btn ${f.id === selectedID() ? "primary" : ""}`} onClick={() => setSelectedID(f.id)}>
              {f.name}
            </button>
          )}
        </For>
        <button class="btn quiet" onClick={() => setSelectedID("")}>+ Новый</button>
      </div>

      <Editor flow={current()} onSaved={(f) => setSelectedID(f.id)} />
    </>
  );
}

/** blankFlow is what «+ Новый» starts from: one stage, which is the smallest
 *  thing that validates, and it is already the entry. */
function blankFlow(): Flow {
  return {
    id: "", name: "", description: "", entryStage: "s1",
    stages: [{ id: "s1", name: "Первая стадия", action: "none", x: 80, y: 120 } as Stage],
    edges: [],
  } as Flow;
}

function Editor(props: { flow?: Flow; onSaved: (f: Flow) => void }) {
  const [draft, setDraft] = createStore<Flow>(blankFlow());
  const [selected, setSelected] = createSignal<string>("");
  const [saveError, setSaveError] = createSignal("");
  const [dirty, setDirty] = createSignal(false);

  // Loading a flow into the draft is a copy, not a reference: the canvas is
  // edited freely and nothing is written until «Сохранить», because the engine
  // checks the whole picture before it takes it.
  createEffect(() => {
    const f = props.flow;
    setDraft(f ? (JSON.parse(JSON.stringify(f)) as Flow) : blankFlow());
    setSelected("");
    setSaveError("");
    setDirty(false);
    if (f?.id) void loadFlowCards(f.id);
  });

  const touch = () => setDirty(true);

  const save = async () => {
    setSaveError("");
    try {
      const saved = await API.SaveFlow(JSON.parse(JSON.stringify(draft)) as Flow);
      await loadFlows();
      setDirty(false);
      props.onSaved(saved);
    } catch (e) {
      // The backend's refusal is a sentence written for a person. Shown as it
      // came, because replacing it with "ошибка сохранения" throws away the
      // only part that helps.
      setSaveError(e instanceof Error ? e.message : String(e));
    }
  };

  const remove = async () => {
    if (!draft.id) return;
    await guard(() => API.DeleteFlow(draft.id));
    await loadFlows();
  };

  const addStage = () => {
    const id = `stage-${Math.random().toString(36).slice(2, 9)}`;
    setDraft(produce((f) => {
      f.stages.push({ id, name: "Новая стадия", action: "none", x: 60 + f.stages.length * 30, y: 60 + f.stages.length * 24 } as Stage);
      if (!f.entryStage) f.entryStage = id;
    }));
    setSelected(id);
    touch();
  };

  const cardsOn = (stageID: string) => stageCards().filter((c) => c.stageId === stageID);

  return (
    <>
      <div class="row wrap" style={{ "margin-bottom": "10px" }}>
        <label class="field" style={{ flex: "1", margin: 0 }}>
          <span>Название</span>
          <input type="text" value={draft.name}
                 onInput={(e) => { setDraft("name", e.currentTarget.value); touch(); }} />
        </label>
        <label class="field" style={{ flex: "2", margin: 0 }}>
          <span>Описание</span>
          <input type="text" value={draft.description ?? ""}
                 onInput={(e) => { setDraft("description", e.currentTarget.value); touch(); }} />
        </label>
      </div>

      <div class="row" style={{ "margin-bottom": "10px" }}>
        <button class="btn" onClick={addStage}>+ Стадия</button>
        <div class="spacer" />
        <Show when={dirty()}><span class="meta">есть несохранённые изменения</span></Show>
        <Show when={draft.id}><button class="btn quiet" onClick={remove}>Удалить флоу</button></Show>
        <button class="btn primary" onClick={save}>Сохранить</button>
      </div>

      <Show when={saveError()}>
        <div class="error"><pre>{saveError()}</pre></div>
      </Show>

      <div class="row" style={{ "align-items": "flex-start", gap: "14px" }}>
        <Canvas
          draft={draft}
          setDraft={setDraft}
          selected={selected()}
          onSelect={setSelected}
          onMoved={touch}
          cardsOn={cardsOn}
        />
        <div style={{ width: "340px", flex: "none" }}>
          <Show when={selected()} fallback={
            <div class="panel"><div class="empty">Выберите стадию, чтобы настроить её.</div></div>
          }>
            <StagePanel draft={draft} setDraft={setDraft} stageID={selected()} onChange={touch}
                        onDeleted={() => setSelected("")} />
            <EdgePanel draft={draft} setDraft={setDraft} stageID={selected()} onChange={touch} />
          </Show>
          <Show when={cardsOn(selected()).length > 0}>
            <div class="panel">
              <h3>Карточки на стадии</h3>
              <For each={cardsOn(selected())}>
                {(c) => (
                  <div class="row" style={{ padding: "4px 0", cursor: "pointer" }}
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

const NODE_W = 172;
const NODE_H = 54;

function Canvas(props: {
  draft: Flow;
  setDraft: any;
  selected: string;
  onSelect: (id: string) => void;
  onMoved: () => void;
  cardsOn: (id: string) => unknown[];
}) {
  let root: HTMLDivElement | undefined;

  // Dragging edits x/y in the draft, which is where the editor left the stage
  // on its canvas — the layout is part of the flow, so it survives a reload.
  const startDrag = (e: MouseEvent, index: number) => {
    e.preventDefault();
    props.onSelect(props.draft.stages[index].id);
    const rect = root!.getBoundingClientRect();
    const stage = props.draft.stages[index];
    const dx = e.clientX - rect.left + root!.scrollLeft - (stage.x ?? 0);
    const dy = e.clientY - rect.top + root!.scrollTop - (stage.y ?? 0);

    const move = (ev: MouseEvent) => {
      const x = Math.max(0, ev.clientX - rect.left + root!.scrollLeft - dx);
      const y = Math.max(0, ev.clientY - rect.top + root!.scrollTop - dy);
      props.setDraft("stages", index, { x, y });
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      props.onMoved();
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  const centre = (id: string) => {
    const s = props.draft.stages.find((st) => st.id === id);
    return { x: (s?.x ?? 0) + NODE_W / 2, y: (s?.y ?? 0) + NODE_H / 2 };
  };

  return (
    <div class="canvas" ref={root} style={{ flex: "1" }}>
      <svg>
        <defs>
          <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5"
                  markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="#4a5170" />
          </marker>
        </defs>
        <For each={props.draft.edges}>
          {(edge) => {
            const a = () => centre(edge.from);
            const b = () => centre(edge.to);
            const mid = () => ({ x: (a().x + b().x) / 2, y: (a().y + b().y) / 2 });
            return (
              <>
                <line x1={a().x} y1={a().y} x2={b().x} y2={b().y}
                      stroke="#4a5170" stroke-width="1.5" marker-end="url(#arrow)" />
                <text class="edge-label" x={mid().x} y={mid().y - 4} text-anchor="middle">
                  {edgeCaption(edge)}
                </text>
              </>
            );
          }}
        </For>
      </svg>

      <For each={props.draft.stages}>
        {(stage, i) => (
          <div
            class={`node ${props.selected === stage.id ? "selected" : ""} ${props.draft.entryStage === stage.id ? "entry" : ""}`}
            style={{ left: `${stage.x ?? 0}px`, top: `${stage.y ?? 0}px` }}
            onMouseDown={(e) => startDrag(e, i())}
          >
            <div class="name">{stage.name}</div>
            <div class="sub">
              {stage.final ? "финал" : stage.action === "agent" ? `агент: ${(stage.crew ?? []).join(", ") || "любой"}` : "ждёт события"}
              <Show when={props.cardsOn(stage.id).length > 0}>
                {" · "}{props.cardsOn(stage.id).length} карт.
              </Show>
            </div>
          </div>
        )}
      </For>
    </div>
  );
}

function edgeCaption(edge: Edge): string {
  const label = vocabulary().triggers.find((t) => t.kind === edge.on)?.label ?? edge.on;
  if (!edge.if) return label;
  if (edge.if.commentContains) return `${label}: «${edge.if.commentContains}»`;
  return `${label}: ${edge.if.property}=${edge.if.value}`;
}

function StagePanel(props: {
  draft: Flow; setDraft: any; stageID: string; onChange: () => void; onDeleted: () => void;
}) {
  const index = () => props.draft.stages.findIndex((s) => s.id === props.stageID);
  const stage = () => props.draft.stages[index()];

  const set = (patch: Partial<Stage>) => { props.setDraft("stages", index(), patch); props.onChange(); };

  const toggleCrew = (name: string) => {
    const crew = new Set(stage().crew ?? []);
    crew.has(name) ? crew.delete(name) : crew.add(name);
    set({ crew: [...crew] });
  };

  const removeStage = () => {
    const id = props.stageID;
    props.setDraft(produce((f: Flow) => {
      f.stages = f.stages.filter((s) => s.id !== id);
      f.edges = f.edges.filter((e) => e.from !== id && e.to !== id);
      if (f.entryStage === id) f.entryStage = f.stages[0]?.id ?? "";
    }));
    props.onChange();
    props.onDeleted();
  };

  return (
    <Show when={stage()}>
      <div class="panel">
        <h3>Стадия</h3>
        <label class="field">
          <span>Название</span>
          <input type="text" value={stage().name} onInput={(e) => set({ name: e.currentTarget.value })} />
        </label>

        <label class="field">
          <span>Что происходит</span>
          <select value={stage().final ? "final" : stage().action}
                  onChange={(e) => {
                    const v = e.currentTarget.value;
                    // A final stage is where the card stops: it runs nothing,
                    // so choosing it and choosing an action are one control.
                    v === "final" ? set({ final: true, action: "none" }) : set({ final: false, action: v });
                  }}>
            <For each={vocabulary().actions}>
              {(a) => <option value={a}>{a === "agent" ? "работает агент" : "ждёт события"}</option>}
            </For>
            <option value="final">финал — карточка закрывается</option>
          </select>
        </label>

        <Show when={stage().action === "agent" && !stage().final}>
          <label class="field">
            <span>Что сказать агенту на этом шаге</span>
            <textarea value={stage().prompt ?? ""} onInput={(e) => set({ prompt: e.currentTarget.value })} />
          </label>
          <div class="field">
            <span style={{ display: "block", color: "var(--dim)", "font-size": "12px", "margin-bottom": "3px" }}>
              Состав — кому разрешено работать эту стадию
            </span>
            <div class="row wrap">
              <For each={agents().agents}>
                {(a) => (
                  <button class={`btn ${(stage().crew ?? []).includes(a.name) ? "primary" : "quiet"}`}
                          onClick={() => toggleCrew(a.name)}>
                    {a.name}
                  </button>
                )}
              </For>
            </div>
          </div>
          <label class="field">
            <span>Сколько карточек одновременно (0 — без ограничения стадии)</span>
            <input type="text" value={String(stage().maxRunning ?? 0)}
                   onInput={(e) => set({ maxRunning: Number(e.currentTarget.value) || 0 })} />
          </label>
        </Show>

        <div class="row">
          <button class="btn quiet"
                  disabled={props.draft.entryStage === props.stageID}
                  onClick={() => { props.setDraft("entryStage", props.stageID); props.onChange(); }}>
            {props.draft.entryStage === props.stageID ? "Это входная стадия" : "Сделать входной"}
          </button>
          <div class="spacer" />
          <button class="btn quiet" onClick={removeStage}>Удалить</button>
        </div>
      </div>
    </Show>
  );
}

function EdgePanel(props: { draft: Flow; setDraft: any; stageID: string; onChange: () => void }) {
  const outgoing = () =>
    props.draft.edges
      .map((e, i) => ({ edge: e, index: i }))
      .filter((x) => x.edge.from === props.stageID);

  const others = () => props.draft.stages.filter((s) => s.id !== props.stageID);

  const add = () => {
    const to = others()[0]?.id;
    if (!to) return;
    props.setDraft(produce((f: Flow) => {
      f.edges.push({ id: "", from: props.stageID, to, on: vocabulary().triggers[0]?.kind ?? "success" } as Edge);
    }));
    props.onChange();
  };

  const set = (index: number, patch: Partial<Edge>) => {
    props.setDraft("edges", index, patch);
    props.onChange();
  };

  const removeEdge = (index: number) => {
    props.setDraft(produce((f: Flow) => { f.edges.splice(index, 1); }));
    props.onChange();
  };

  return (
    <div class="panel">
      <div class="row">
        <h3 style={{ margin: 0 }}>Переходы отсюда</h3>
        <div class="spacer" />
        <button class="btn quiet" onClick={add} disabled={others().length === 0}>+ Переход</button>
      </div>

      <Show when={outgoing().length > 0} fallback={<div class="empty">Отсюда карточка сама никуда не поедет.</div>}>
        <For each={outgoing()}>
          {(x) => (
            <div style={{ "border-top": "1px solid var(--line)", "padding-top": "10px", "margin-top": "10px" }}>
              <div class="grid2">
                <label class="field">
                  <span>Когда</span>
                  <select value={x.edge.on} onChange={(e) => set(x.index, { on: e.currentTarget.value })}>
                    <For each={vocabulary().triggers}>
                      {(t) => <option value={t.kind}>{t.label}</option>}
                    </For>
                  </select>
                </label>
                <label class="field">
                  <span>Куда</span>
                  <select value={x.edge.to} onChange={(e) => set(x.index, { to: e.currentTarget.value })}>
                    <For each={others()}>{(s) => <option value={s.id}>{s.name}</option>}</For>
                  </select>
                </label>
              </div>

              <Condition edge={x.edge} onChange={(cond) => set(x.index, { if: cond })} />

              <div class="row">
                <div class="spacer" />
                <button class="btn quiet" onClick={() => removeEdge(x.index)}>Убрать переход</button>
              </div>
            </div>
          )}
        </For>
      </Show>
    </div>
  );
}

/**
 * Condition is the question an edge asks about the card. There are exactly two
 * forms and they are mutually exclusive, so this offers a choice of three
 * rather than two independent inputs somebody could fill in at once.
 */
function Condition(props: { edge: Edge; onChange: (cond: any) => void }) {
  const kind = () => {
    if (!props.edge.if) return "none";
    return props.edge.if.commentContains ? "words" : "prop";
  };
  const isHuman = () => props.edge.on === "card.changed";

  return (
    <div>
      <label class="field">
        <span>Условие</span>
        <select value={kind()} onChange={(e) => {
          const v = e.currentTarget.value;
          if (v === "none") props.onChange(null);
          else if (v === "prop") props.onChange({ property: "", value: "" });
          else props.onChange({ commentContains: "" });
        }}>
          {/* card.changed has no unconditional form: the condition is not a
              guard there but the event itself — which value fires it. */}
          <Show when={!isHuman()}><option value="none">без условия — запасной выход</option></Show>
          <option value="prop">на карточке выбрано свойство</option>
          <Show when={!isHuman()}><option value="words">в ответе агента есть текст</option></Show>
        </select>
      </label>

      <Show when={kind() === "prop"}>
        <div class="grid2">
          <input type="text" placeholder="свойство" value={props.edge.if?.property ?? ""}
                 onInput={(e) => props.onChange({ property: e.currentTarget.value, value: props.edge.if?.value ?? "" })} />
          <input type="text" placeholder="значение" value={props.edge.if?.value ?? ""}
                 onInput={(e) => props.onChange({ property: props.edge.if?.property ?? "", value: e.currentTarget.value })} />
        </div>
      </Show>

      <Show when={kind() === "words"}>
        <input type="text" placeholder="ГОТОВО К ДЕПЛОЮ" value={props.edge.if?.commentContains ?? ""}
               onInput={(e) => props.onChange({ commentContains: e.currentTarget.value })} />
        <div class="meta" style={{ "margin-top": "4px" }}>
          Эти слова допишутся в промпт стадии, чтобы агент знал, чем закончить.
        </div>
      </Show>
    </div>
  );
}
