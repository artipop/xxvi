import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { agents, applyCard, flows, guard, list, projects, showRibbon, WORK_MODES } from "../state";

// A task typed where the work is watched, not filed first and fetched back from
// the inbox: the inbox is for what arrived and waits for a decision, and a task
// a person has just thought of is already decided.
//
// The choices are remembered between tasks: the next task is usually in the
// same place and for the same agent, and asking again every time is asking a
// question with a known answer.

const KEY = "xxvi.compose";

function remembered(): { project?: string; workMode?: string; agent?: string; flow?: string } {
  try { return JSON.parse(localStorage.getItem(KEY) ?? "{}"); } catch { return {}; }
}

export function Compose(props: { onStarted?: () => void; onCancel?: () => void }): JSX.Element {
  const was = remembered();
  const [text, setText] = createSignal("");
  const [project, setProject] = createSignal(was.project ?? "");
  const [workMode, setWorkMode] = createSignal(was.workMode ?? "");
  const [agent, setAgent] = createSignal(was.agent ?? "");
  const [flow, setFlow] = createSignal(was.flow ?? "");
  const [busy, setBusy] = createSignal(false);
  let box: HTMLTextAreaElement | undefined;

  // A remembered choice that is no longer in the registry is not a choice.
  const projectID = () => (projects().some((p) => p.id === project()) ? project() : "");
  // A branch of its own is a question about a repository; anywhere else the
  // answer is the folder as it stands, whatever was remembered.
  const isRepo = () => projects().find((p) => p.id === projectID())?.repo ?? false;
  const mode = () => (isRepo() && WORK_MODES.some((m) => m.value === workMode()) ? workMode() : "");
  const agentName = () =>
    list(agents().agents).find((a) => a.name === agent())?.name ?? list(agents().agents)[0]?.name ?? "";
  const flowID = () => flows().find((f) => f.id === flow())?.id ?? flows()[0]?.id ?? "";

  // Without scrolling: the stack moves by whole ribbons and only when asked,
  // and a focus that dragged it would land it between two of them.
  onSettled(() => { box?.focus({ preventScroll: true }); });

  const start = async () => {
    if (busy() || !text().trim() || !flowID()) return;
    setBusy(true);
    const view = await guard(() => API.StartTask(text(), projectID(), mode(), agentName(), flowID()));
    setBusy(false);
    if (!view) return;
    try {
      localStorage.setItem(KEY, JSON.stringify({ project: projectID(), workMode: workMode(), agent: agentName(), flow: flowID() }));
    } catch { /* a convenience, not a record */ }
    setText("");
    applyCard(view);
    props.onStarted?.();
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
          if (e.key === "Escape" && props.onCancel) { e.stopPropagation(); props.onCancel(); }
        }}
      />
      <div class="row wrap">
        <select value={projectID()} onChange={(e) => setProject(e.currentTarget.value)} title="Где делать">
          <option value="">своя папка</option>
          <For each={projects()}>{(p) => <option value={p.id}>{p.name}</option>}</For>
        </select>
        <Show when={isRepo()}>
          <select value={mode()} onChange={(e) => setWorkMode(e.currentTarget.value)}
                  title={WORK_MODES.find((m) => m.value === mode())?.why}>
            <For each={WORK_MODES}>{(m) => <option value={m.value}>{m.label}</option>}</For>
          </select>
        </Show>
        <select value={agentName()} onChange={(e) => setAgent(e.currentTarget.value)} title="Кто делает">
          <For each={list(agents().agents)}>{(a) => <option value={a.name}>{a.name}</option>}</For>
        </select>
        <select value={flowID()} onChange={(e) => setFlow(e.currentTarget.value)} title="По какому флоу">
          <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
        </select>
        <div class="spacer" />
        <button class="btn primary" onClick={start} disabled={busy() || !text().trim() || !flowID()}>
          Начать ↵
        </button>
      </div>
    </div>
  );
}
