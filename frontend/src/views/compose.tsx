import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { agents, applyCard, flows, guard, list, projects, showRibbon } from "../state";

// A task typed where the work is watched, not filed first and fetched back from
// the inbox: the inbox is for what arrived and waits for a decision, and a task
// a person has just thought of is already decided.
//
// The choices are remembered between tasks: the next task is usually in the
// same place and for the same agent, and asking again every time is asking a
// question with a known answer.

const KEY = "xxvi.compose";

function remembered(): { project?: string; agent?: string; flow?: string } {
  try { return JSON.parse(localStorage.getItem(KEY) ?? "{}"); } catch { return {}; }
}

export function Compose(props: { onDone?: () => void }): JSX.Element {
  const was = remembered();
  const [text, setText] = createSignal("");
  const [project, setProject] = createSignal(was.project ?? "");
  const [agent, setAgent] = createSignal(was.agent ?? "");
  const [flow, setFlow] = createSignal(was.flow ?? "");
  const [busy, setBusy] = createSignal(false);
  let box: HTMLTextAreaElement | undefined;

  // A remembered choice that is no longer in the registry is not a choice.
  const projectID = () => (projects().some((p) => p.id === project()) ? project() : "");
  const agentName = () =>
    list(agents().agents).find((a) => a.name === agent())?.name ?? list(agents().agents)[0]?.name ?? "";
  const flowID = () => flows().find((f) => f.id === flow())?.id ?? flows()[0]?.id ?? "";

  onSettled(() => { box?.focus(); });

  const start = async () => {
    if (busy() || !text().trim() || !flowID()) return;
    setBusy(true);
    const view = await guard(() => API.StartTask(text(), projectID(), agentName(), flowID()));
    setBusy(false);
    if (!view) return;
    try {
      localStorage.setItem(KEY, JSON.stringify({ project: projectID(), agent: agentName(), flow: flowID() }));
    } catch { /* a convenience, not a record */ }
    setText("");
    applyCard(view);
    props.onDone?.();
    showRibbon(view.card.id);
  };

  return (
    <div class="compose">
      <textarea
        ref={box}
        class="compose-text"
        placeholder="Что сделать? Первая строка — заголовок, весь текст уйдёт агенту."
        value={text()}
        onInput={(e) => setText(e.currentTarget.value)}
        onKeyDown={(e) => {
          // Enter sends, as in a chat; a new line is Shift+Enter. The ribbon
          // listens on the window, and a key meant for this box stays here.
          if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); void start(); }
          if (e.key === "Escape" && props.onDone) { e.stopPropagation(); props.onDone(); }
        }}
      />
      <div class="row wrap">
        <select value={projectID()} onChange={(e) => setProject(e.currentTarget.value)} title="Где делать">
          <option value="">своя папка</option>
          <For each={projects()}>{(p) => <option value={p.id}>{p.name}</option>}</For>
        </select>
        <select value={agentName()} onChange={(e) => setAgent(e.currentTarget.value)} title="Кто делает">
          <For each={list(agents().agents)}>{(a) => <option value={a.name}>{a.name}</option>}</For>
        </select>
        <select value={flowID()} onChange={(e) => setFlow(e.currentTarget.value)} title="По какому флоу">
          <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
        </select>
        <div class="spacer" />
        <Show when={props.onDone}>
          <button class="btn quiet" onClick={() => props.onDone!()}>Отмена</button>
        </Show>
        <button class="btn primary" onClick={start} disabled={busy() || !text().trim() || !flowID()}>
          Начать ↵
        </button>
      </div>
    </div>
  );
}
