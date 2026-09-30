import { createEffect, createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { PastSession } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
import { agents, applyCard, flows, folderName, guard, list, projectFolders, projects, showRibbon, taskFolder, workModes, workspace } from "../state";
import { errorText, t, when } from "../i18n";

// A task typed where the work is watched, not filed first and fetched back from
// the inbox: the inbox is for what arrived and waits for a decision, and a task
// a person has just thought of is already decided.
//
// Two ways to begin: a new conversation, or one the agent already had. A new
// one needs no text — the stage's brief is enough to open the agent, and the
// rest can be said to it in the terminal. A continued one needs no text either:
// the conversation is the task.
//
// The choices are remembered between tasks: the next task is usually in the
// same place and for the same agent, and asking again every time is asking a
// question with a known answer.

const KEY = "xxvi.compose";

function remembered(): { workMode?: string; agent?: string; flow?: string; folders?: Record<string, string> } {
  try { return JSON.parse(localStorage.getItem(KEY) ?? "{}"); } catch { return {}; }
}

export function Compose(props: { onStarted?: () => void; onCancel?: () => void }): JSX.Element {
  const was = remembered();
  const [text, setText] = createSignal("");
  const [workMode, setWorkMode] = createSignal(was.workMode ?? "");
  const [agent, setAgent] = createSignal(was.agent ?? "");
  const [flow, setFlow] = createSignal(was.flow ?? "");
  // The folder is remembered per project: a front and a back are chosen
  // between again only when the work moves from one to the other.
  const [folders, setFolders] = createSignal<Record<string, string>>(was.folders ?? {});
  const [busy, setBusy] = createSignal(false);
  const [fromSession, setFromSession] = createSignal(false);
  const [sessions, setSessions] = createSignal<PastSession[] | undefined>();
  const [sessionsError, setSessionsError] = createSignal("");
  const [session, setSession] = createSignal<PastSession | undefined>();
  let box: HTMLTextAreaElement | undefined;

  // The project is where the task is typed, not a question: the workspace
  // standing open is the answer.
  const projectID = () => (projects().some((p) => p.id === workspace()) ? workspace() : "");
  // Which of the project's folders: a question only when it has more than one.
  const folder = () => taskFolder(projectID(), folders()[projectID()]) ?? taskFolder(projectID(), "");
  const folderID = () => folder()?.id ?? "";
  // A branch of its own is a question about a repository; anywhere else the
  // answer is the folder as it stands, whatever was remembered. A continued
  // conversation's unfinished work is in the folder itself, so it never gets
  // a branch or tree: that would continue it somewhere the work is not.
  const isRepo = () => folder()?.repo ?? false;
  const mode = () =>
    !fromSession() && isRepo() && workModes().some((m) => m.value === workMode()) ? workMode() : "";
  const agentName = () =>
    list(agents().agents).find((a) => a.name === agent())?.name ?? list(agents().agents)[0]?.name ?? "";
  const flowID = () => flows().find((f) => f.id === flow())?.id ?? flows()[0]?.id ?? "";

  // Asked of the agent itself, whenever whose conversations or where changes.
  // The answer to an older question is dropped: a slow adapter must not fill
  // the list for an agent that is no longer chosen.
  let asked = 0;
  createEffect(() => [fromSession(), agentName(), projectID(), folderID()] as const, ([on, who, where, dir]) => {
    setSession(undefined);
    setSessions(undefined);
    setSessionsError("");
    if (!on || !who || !where) return;
    const n = ++asked;
    API.PastSessions(who, where, dir).then(
      (got) => { if (n === asked) setSessions(got ?? []); },
      (err) => { if (n === asked) setSessionsError(errorText(err)); },
    );
  });

  const ready = () => !busy() && !!flowID() && (!fromSession() || !!session());

  // Without scrolling: the stack moves by whole ribbons and only when asked,
  // and a focus that dragged it would land it between two of them.
  onSettled(() => { box?.focus({ preventScroll: true }); });

  const start = async () => {
    if (!ready()) return;
    setBusy(true);
    const from = fromSession() ? session() : undefined;
    // A card needs a title, and a task begun without a word gets one that
    // says so rather than one that pretends to describe it. Only the title:
    // the agent is handed what was typed, and here that is nothing.
    const view = await guard(() =>
      from
        ? API.ContinueSession(from.id, from.title ?? "", "", projectID(), folderID(), "", agentName(), flowID())
        : API.StartTask(text(), t("compose.untitled"), projectID(), folderID(), mode(), agentName(), flowID()));
    setBusy(false);
    if (!view) return;
    try {
      localStorage.setItem(KEY, JSON.stringify({ workMode: workMode(), agent: agentName(), flow: flowID(), folders: folders() }));
    } catch { /* a convenience, not a record */ }
    setText("");
    setFromSession(false);
    applyCard(view);
    props.onStarted?.();
    showRibbon(view.card.id);
  };

  return (
    <div
      class="compose"
      onKeyDown={(e) => {
        // The ribbon listens on the window, and a key meant for this form stays here.
        if (e.key === "Escape" && props.onCancel) { e.stopPropagation(); props.onCancel(); }
      }}
    >
      <Show
        when={fromSession()}
        fallback={
          <textarea
            ref={box}
            class="compose-text"
            placeholder={t("compose.placeholder")}
            value={text()}
            onInput={(e) => setText(e.currentTarget.value)}
            onKeyDown={(e) => {
              // Enter sends, as in a chat; a new line is Shift+Enter.
              if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); void start(); }
            }}
          />
        }
      >
        <div class="compose-sessions">
          <Show when={!projectID()}>
            <p class="empty">{t("compose.sessionsNeedProject")}</p>
          </Show>
          <Show when={projectID() && sessionsError()}>
            <p class="empty">{sessionsError()}</p>
          </Show>
          <Show when={projectID() && !sessionsError() && !sessions()}>
            <p class="empty">{t("compose.sessionsLoading")}</p>
          </Show>
          <Show when={sessions()?.length === 0}>
            <p class="empty">{t("compose.sessionsNone")}</p>
          </Show>
          <For each={sessions() ?? []}>
            {(s) => (
              <button
                class={`compose-session ${session()?.id === s.id ? "on" : ""}`}
                onClick={() => setSession(s)}
                onDblClick={() => { setSession(s); void start(); }}
                title={s.cwd}
              >
                <span class="meta">{when(s.updatedAt, false)}</span>
                <span class="compose-session-title">{s.title || s.id}</span>
              </button>
            )}
          </For>
        </div>
      </Show>
      <div class="row wrap">
        <div class="compose-mode" role="group">
          <button class={!fromSession() ? "on" : ""} onClick={() => setFromSession(false)}>
            {t("compose.modeNew")}
          </button>
          <button class={fromSession() ? "on" : ""} onClick={() => setFromSession(true)} title={t("compose.fromSessionWhy")}>
            {t("compose.fromSession")}
          </button>
        </div>
        <Show when={projectFolders(projectID()).length > 1}>
          <select value={folderID()} title={t("compose.folder")}
                  onChange={(e) => { const id = e.currentTarget.value; setFolders((f) => ({ ...f, [projectID()]: id })); }}>
            <For each={projectFolders(projectID())}>{(f) => <option value={f.id}>{folderName(f)}</option>}</For>
          </select>
        </Show>
        <Show when={isRepo() && !fromSession()}>
          <select value={mode()} onChange={(e) => setWorkMode(e.currentTarget.value)}
                  title={workModes().find((m) => m.value === mode())?.why}>
            <For each={workModes()}>{(m) => <option value={m.value}>{m.label}</option>}</For>
          </select>
        </Show>
        <select value={agentName()} onChange={(e) => setAgent(e.currentTarget.value)} title={t("compose.who")}>
          <For each={list(agents().agents)}>{(a) => <option value={a.name}>{a.name}</option>}</For>
        </select>
        <select value={flowID()} onChange={(e) => setFlow(e.currentTarget.value)} title={t("compose.flow")}>
          <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
        </select>
        <div class="spacer" />
        <button class="btn primary" onClick={start} disabled={!ready()}>
          {t("compose.start")}
        </button>
      </div>
    </div>
  );
}
