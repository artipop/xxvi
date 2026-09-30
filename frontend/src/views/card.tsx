import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { agents, applyCard, attention, closeCard, folderName, guard, list, openCard, projectFolders, projects, showRibbon, taskFolder, vocabulary, workModes } from "../state";
import { label, propName, propValue, t, waitText } from "../i18n";
import { QuestionForm, WorktreeForm } from "./attention";
import { JournalList } from "./journal";
import { Marks } from "./marks";

// One card, in full: where it stands, what it is waiting for, and who works it.
// What happened to it along the way is the ribbon's to show; the journal is the
// audit behind both.

export default function CardPanel() {
  const view = () => openCard()!;
  const card = () => view().card;
  const flow = () => view().flow;

  const act = async (fn: () => Promise<any>) => applyCard(await guard(fn));

  return (
    <div>
      <div class="panel">
        <div class="row">
          <h3>{card().source || t("card.own")}</h3>
          <div class="spacer" />
          {/* Any card that has been anywhere has a strip, finished or not —
              and a finished one is where its results are. */}
          <Show when={list(view().events).length > 0}>
            <button class="btn quiet" onClick={() => showRibbon(card().id)}>{t("common.toRibbon")}</button>
          </Show>
          <button class="btn quiet" onClick={closeCard}>{t("common.close")}</button>
        </div>

        <div class="title card-title">{card().title}</div>
        <Show when={card().body}><div class="body">{card().body}</div></Show>
        <Show when={card().url}>
          <div class="meta mono note">{card().url}</div>
        </Show>

        <Show when={flow()}>
          <div class="strip">
            <For each={list(flow()!.stages)}>
              {(s) => (
                <button
                  class={`stage ${s.current ? "current" : ""} ${s.done ? "done" : ""} ${s.final ? "final" : ""}`}
                  title={t("card.moveHere")}
                  onClick={() => act(() => API.MoveTo(card().id, s.id))}
                >
                  {s.name}
                </button>
              )}
            </For>
          </div>

          <div class="row wrap">
            <span class="tag">{flow()!.flowName}</span>
            <Show when={flow()!.running}><span class="tag accent"><span class="dot" />{t("common.agentWorking")}</span></Show>
            <Show when={flow()!.queued}><span class="tag"><span class="dot" />{t("card.queued")}</span></Show>
          </div>

          <Show when={(flow()!.waitingFor?.length ?? 0) > 0}>
            <div class="meta note">
              {t("card.stageWaits", { what: list(flow()!.waitingFor).map(waitText).join("; ") })}
            </div>
          </Show>

          {/* A stage where nobody works and nothing runs waits for a person to
              say how it went, and saying so used to mean finding the field in
              «Properties» and typing a value into it. There are two answers and
              the flow already knows where each of them leads, so they are two
              buttons naming the stage they lead to. */}
          <Show when={list(flow()!.marks).length > 0}>
            <Marks cardId={card().id} marks={list(flow()!.marks)} answer={(fn) => void act(fn)} />
          </Show>

          <div class="row wrap actions">
            <Show when={flow()!.running}>
              <button class="btn quiet" onClick={() => act(async () => {
                await API.CancelCard(card().id);
                return API.Card(card().id);
              })}>{t("card.stopAgent")}</button>
            </Show>
            <button class="btn quiet" onClick={() => act(() => API.RemoveFromFlow(card().id))}>
              {t("card.removeFromFlow")}
            </button>
          </div>
        </Show>
      </div>

      {/* The question, on the card it belongs to. It is the same question the
          attention panel shows — answering either lets the agent go on. */}
      <Show when={view().question}>
        <div class="panel">
          <h3>{t("question.asks")}</h3>
          <QuestionForm
            questionId={view().question!.id}
            kind={view().question!.kind}
            tool={view().question!.tool}
            text={view().question!.text}
            options={view().question!.options ?? []}
            freeText={view().question!.freeText}
          />
        </div>
      </Show>

      <Props />
      <Place />
      <Assignee />
      <Journal />
    </div>
  );
}

/** Props are how a person answers a waiting stage: setting one is the event a
 *  card.changed edge is listening for. */
function Props() {
  const view = () => openCard()!;
  const [name, setName] = createSignal("");
  const [value, setValue] = createSignal("");

  const set = async (n: string, v: string) => applyCard(await guard(() => API.SetProp(view().card.id, n, v)));

  const add = async () => {
    if (!name().trim()) return;
    await set(name(), value());
    setName(""); setValue("");
  };

  // The values the current stage is waiting for, offered as buttons: a person
  // answering a stage should not have to retype what the flow already names.
  const awaited = () => {
    const flow = view().flow;
    if (!flow) return [] as { property: string; value: string }[];
    const out: { property: string; value: string }[] = [];
    for (const wait of flow.waitingFor ?? []) {
      if (wait.if?.property && wait.if.value) out.push({ property: wait.if.property, value: wait.if.value });
    }
    return out;
  };

  return (
    <div class="panel">
      <h3>{t("card.props")}</h3>

      <Show when={awaited().length > 0}>
        <div class="row wrap toolbar">
          <For each={awaited()}>
            {(a) => (
              <button class="btn primary" onClick={() => set(a.property, a.value)}>
                {propName(a.property)}: {propValue(a.property, a.value)}
              </button>
            )}
          </For>
        </div>
      </Show>

      <div class="props">
        <For each={Object.entries(view().card.props ?? {})}>
          {([n, v]) => (
            <>
              <span class="name">{propName(n)}</span>
              {/* The outcome is the application's own closed set, stored as an
                  identifier: offered as its two values, in the person's words,
                  rather than as an identifier to retype. */}
              <Show when={sameName(n, vocabulary().outcomeProperty)} fallback={
                <input type="text" value={v} onChange={(e) => set(n, e.currentTarget.value)} />
              }>
                <select value={v} onChange={(e) => set(n, e.currentTarget.value)}>
                  <For each={list(vocabulary().outcomeValues)}>
                    {(o) => <option value={o}>{label("outcome", o)}</option>}
                  </For>
                </select>
              </Show>
              <button class="btn quiet" title={t("common.remove")} onClick={() => set(n, "")}>×</button>
            </>
          )}
        </For>
      </div>

      <div class="row actions">
        <input type="text" placeholder={t("card.propName")} value={name()} onInput={(e) => setName(e.currentTarget.value)} />
        <input type="text" placeholder={t("card.propValue")} value={value()} onInput={(e) => setValue(e.currentTarget.value)} />
        <button class="btn" onClick={add}>{t("card.setProp")}</button>
      </div>
    </div>
  );
}

/** Assignee is who the card is for. An agent's name means "let this agent work
 *  it"; anything else means a person took it, and then no agent starts. */
// Beside the assignee on purpose: one says by whom, the other says where, and
// both are a person's answer rather than the graph's.
function Place() {
  const view = () => openCard()!;
  const set = async (id: string, folder = "") =>
    applyCard(await guard(() => API.SetCardProject(view().card.id, id, folder)));
  const current = () => projects().find((p) => p.id === view().card.project);
  const folder = () => taskFolder(view().card.project, view().card.folder);
  const where = () => view().card.worktree || folder()?.path || current()!.path;

  return (
    <div class="panel">
      <h3>{t("card.project")}</h3>
      <Show when={projects().length > 0} fallback={
        <div class="meta">{t("card.noProjects")}</div>
      }>
        <select value={view().card.project ?? ""} onChange={(e) => set(e.currentTarget.value)} class="fit">
          <option value="">{t("common.ownFolder")}</option>
          <For each={projects()}>{(p) => <option value={p.id}>{p.name}</option>}</For>
        </select>
        <Show when={projectFolders(view().card.project).length > 1}>
          <select value={folder()?.id ?? ""} onChange={(e) => set(view().card.project!, e.currentTarget.value)}
                  class="fit" title={t("compose.folder")}>
            <For each={projectFolders(view().card.project)}>{(f) => <option value={f.id}>{folderName(f)}</option>}</For>
          </select>
        </Show>
        <div class="meta note">
          {current() ? t("card.opensIn", { path: where() }) : t("card.ownFolderNote")}
        </div>
        <Show when={folder()?.repo}>
          <WorkMode />
        </Show>
      </Show>
    </div>
  );
}

/** How the card works in a repository: the folder as it stands, a separate
 *  working tree, or its own branch in the folder itself. Answered before the
 *  work starts — once the branch exists, the work is on it and the answer is
 *  the fact, so it is shown rather than offered. */
function WorkMode() {
  const view = () => openCard()!;
  const set = async (mode: string) =>
    applyCard(await guard(() => API.SetCardWorkMode(view().card.id, mode)));
  // A closed card's tree is asked about here too: it is the same question as
  // the one in the attention list, answered by the same form.
  const asked = () => attention().find((a) => a.cardId === view().card.id && a.worktree);

  return (
    <div class="subsection">
      <Show when={!view().card.branch} fallback={
        <div class="meta">
          {view().card.worktree ? t("card.onWorktree") : t("card.onBranch")}:{" "}
          <code>{view().card.branch}</code>
          {view().card.base ? <> {t("card.from")} <code>{view().card.base}</code></> : null}
          <Show when={asked()}>
            <WorktreeForm a={asked()!} onAnswered={applyCard} />
          </Show>
        </div>
      }>
        <select value={view().card.workMode ?? ""} onChange={(e) => set(e.currentTarget.value)}
                class="fit" title={t("card.workModeTitle")}>
          <For each={workModes()}>{(m) => <option value={m.value}>{m.label}</option>}</For>
        </select>
        <div class="meta note">
          {workModes().find((m) => m.value === (view().card.workMode ?? ""))?.why}
        </div>
      </Show>
    </div>
  );
}

function Assignee() {
  const view = () => openCard()!;
  const set = async (who: string) =>
    applyCard(await guard(() => API.SetAssignee(view().card.id, who)));

  return (
    <div class="panel">
      <h3>{t("card.assignee")}</h3>
      <div class="row wrap">
        <select value={view().card.assignee ?? ""} onChange={(e) => set(e.currentTarget.value)} class="fit">
          <option value="">{t("card.unassigned")}</option>
          <For each={agents().agents}>{(a) => <option value={a.name}>{a.name}</option>}</For>
        </select>
        <input type="text" placeholder={t("card.orPerson")}
               value={isAgent(view().card.assignee ?? "") ? "" : (view().card.assignee ?? "")}
               onChange={(e) => set(e.currentTarget.value)} />
      </div>
      <div class="meta note">
        {t("card.assigneeNote")}
      </div>
    </div>
  );
}

function isAgent(name: string): boolean {
  return list(agents().agents).some((a) => a.name.toLowerCase() === name.toLowerCase());
}

// Folded, and last: it is found when somebody goes looking, and nothing on the
// card depends on reading it.
function Journal() {
  const view = () => openCard()!;
  const [open, setOpen] = createSignal(false);
  return (
    <div class="panel">
      <button class="fold" onClick={() => setOpen(!open())}>
        {t("card.journal")} <span class={`caret ${open() ? "open" : ""}`}>›</span>
      </button>
      <Show when={open()}>
        <JournalList entries={list(view().journal)} />
      </Show>
    </div>
  );
}

function sameName(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}
