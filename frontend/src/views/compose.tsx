import { createEffect, createSignal, For, onCleanup, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/gandvik/internal/app/api";
import type { PastSession } from "../../bindings/github.com/artipop/gandvik/internal/acp/models";
import { agents, applyCard, flows, folderName, guard, list, projectFolders, projects, showRibbon, taskFolder, workModes, workspace } from "../state";
import { errorText, t, when } from "../i18n";
import { InstallButton } from "./adapter-install";
import { SessionPreview } from "./session-preview";

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

const KEY = "gandvik.compose";
type SessionChoice = PastSession & { folderID: string };

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
  const [step, setStep] = createSignal<"new" | "existing" | null>(null);
  const fromSession = () => step() === "existing";
  const [sessions, setSessions] = createSignal<SessionChoice[] | undefined>();
  const [sessionsError, setSessionsError] = createSignal("");
  const [missingAdapter, setMissingAdapter] = createSignal(false);
  const [session, setSession] = createSignal<SessionChoice | undefined>();
  const [confirming, setConfirming] = createSignal("");
  const [deleting, setDeleting] = createSignal("");
  const [preview, setPreview] = createSignal<PastSession | undefined>();
  const [editing, setEditing] = createSignal("");
  const [sessionTitle, setSessionTitle] = createSignal("");
  const [savingTitle, setSavingTitle] = createSignal("");
  createEffect(workspace, () => { setStep(null); });

  // The project is where the task is typed, not a question: the workspace
  // standing open is the answer.
  const projectID = () => (projects().some((p) => p.id === workspace()) ? workspace() : "");
  const projectName = () => projects().find((p) => p.id === projectID())?.name ?? t("ribbon.noProject");
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
  const installableAdapter = () => {
    if (!missingAdapter()) return undefined;
    const kind = list(agents().agents).find((a) => a.name === agentName())?.kind;
    const adapter = list(agents().adapters).find((a) => a.kind === kind);
    return adapter?.installable ? adapter : undefined;
  };
  const canManage = () => ["claude", "codex"].includes(list(agents().agents).find((a) => a.name === agentName())?.kind ?? "");
  const flowID = () => flows().find((f) => f.id === flow())?.id ?? flows()[0]?.id ?? "";

  // Asked of the agent itself, whenever whose conversations or where changes.
  // The answer to an older question is dropped: a slow adapter must not fill
  // the list for an agent that is no longer chosen.
  let asked = 0;
  onCleanup(() => { ++asked; });
  createEffect(() => [fromSession(), agentName(), projectID(), folderID()] as const, ([on, who, where, dir]) => {
    const n = ++asked;
    setSession(undefined);
    setSessions(undefined);
    setSessionsError("");
    setMissingAdapter(false);
    setConfirming("");
    setDeleting("");
    setPreview(undefined);
    setEditing("");
    setSessionTitle("");
    setSavingTitle("");
    if (!on || !who) return;
    API.PastSessions(who, where, dir).then(
      (got) => { if (n === asked) setSessions((got ?? []).map((past) => ({ ...past, folderID: dir }))); },
      (err) => {
        if (n !== asked) return;
        setSessionsError(errorText(err));
        setMissingAdapter((err as { cause?: { code?: string } } | null)?.cause?.code === "adapter.install");
      },
    );
  });

  const sessionBusy = () => busy() || !!deleting() || !!savingTitle();
  const ready = () => !!step() && !!agentName() && !sessionBusy() && !editing() && !preview() && !!flowID() && (!fromSession() || !!session());

  const renameSession = async (s: SessionChoice) => {
    if (sessionBusy() || editing() !== s.id || !sessionTitle().trim()) return;
    const n = asked;
    setSavingTitle(s.id);
    setSessionsError("");
    try {
      const title = await API.RenamePastSession(agentName(), projectID(), s.folderID, s.id, sessionTitle());
      if (n !== asked) return;
      setSessions((got) => got?.map((past) => past.id === s.id && past.folderID === s.folderID ? { ...past, title } : past));
      if (session()?.id === s.id && session()?.folderID === s.folderID) setSession({ ...s, title });
      setEditing("");
    } catch (err) {
      if (n === asked) setSessionsError(errorText(err));
    } finally {
      if (n === asked) setSavingTitle("");
    }
  };

  const removeSession = async (s: SessionChoice) => {
    if (sessionBusy() || confirming() !== s.id) return;
    const n = asked;
    setDeleting(s.id);
    setSessionsError("");
    try {
      await API.DeletePastSession(agentName(), projectID(), s.folderID, s.id);
      if (n !== asked) return;
      setSessions((got) => got?.filter((past) => past.id !== s.id || past.folderID !== s.folderID));
      if (session()?.id === s.id && session()?.folderID === s.folderID) setSession(undefined);
      setConfirming("");
    } catch (err) {
      if (n === asked) setSessionsError(errorText(err));
    } finally {
      if (n === asked) setDeleting("");
    }
  };

  const start = async () => {
    if (!ready()) return;
    setBusy(true);
    const from = fromSession() ? session() : undefined;
    // A card needs a title, and a task begun without a word gets one that
    // says so rather than one that pretends to describe it. Only the title:
    // the agent is handed what was typed, and here that is nothing.
    const view = await guard(() =>
      from
        ? API.ContinueSession(from.id, from.title ?? "", "", projectID(), from.folderID, "", agentName(), flowID())
        : API.StartTask(text(), t("compose.untitled"), projectID(), folderID(), mode(), agentName(), flowID()));
    setBusy(false);
    if (!view) return;
    try {
      localStorage.setItem(KEY, JSON.stringify({ workMode: workMode(), agent: agentName(), flow: flowID(), folders: folders() }));
    } catch { /* a convenience, not a record */ }
    setText("");
    setStep(null);
    applyCard(view);
    props.onStarted?.();
    showRibbon(view.card.id);
  };

  return (
    <div
      class="compose"
      onKeyDown={(e) => {
        // The ribbon listens on the window, and a key meant for this form stays here.
        if (e.defaultPrevented || e.isComposing || e.keyCode === 229) return;
        if (e.key === "Escape") {
          // With nothing of its own to close, the form lets Escape reach the
          // ribbon: a project panel or the journal opened beside it closes.
          if (!sessionBusy() && !preview() && !confirming() && !step() && !props.onCancel) return;
          e.preventDefault(); e.stopPropagation();
          if (e.repeat || sessionBusy()) return;
          if (preview()) setPreview(undefined);
          else if (confirming()) setConfirming("");
          else if (step()) setStep(null);
          else props.onCancel?.();
        } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.altKey && step() && !editing()) {
          // A chosen session is many Tabs away from the start button.
          e.preventDefault(); e.stopPropagation();
          if (!e.repeat) void start();
        }
      }}
    >
      <Show when={!step()}>
        <div class="compose-entry">
          <div class="compose-entry-content">
            <p class="compose-project">{projectName()}</p>
            <h2>{t("compose.chooseStart")}</h2>
            <Show when={!projectID()}>
              <p class="meta">{t("compose.draftFolder")}</p>
            </Show>
            <div class="compose-choices">
              <button class="compose-choice" ref={(el) => { queueMicrotask(() => { if (el.isConnected) el.focus({ preventScroll: true }); }); }}
                      onClick={() => setStep("new")}>
                <strong>{t("compose.modeNew")}</strong>
                <span>{t("compose.newHint")}</span>
              </button>
              <button class="compose-choice" onClick={() => setStep("existing")}>
                <strong>{t("compose.fromSession")}</strong>
                <span>{t("compose.fromSessionWhy")}</span>
              </button>
            </div>
          </div>
        </div>
      </Show>
      <Show when={step()}>
        <header class="compose-heading row">
          <button class="btn quiet" disabled={sessionBusy()} onClick={() => setStep(null)}>{t("compose.back")}</button>
          <h3>{fromSession() ? t("compose.fromSession") : t("compose.modeNew")}</h3>
          <span class="meta compose-project">{projectName()}</span>
        </header>
        <Show when={!preview()}>
          <div class="compose-settings row wrap">
            <Show when={projectFolders(projectID()).length > 1}>
              <label class="field">
                <span>{t("compose.folder")}</span>
                <select aria-label={t("compose.folder")} value={folderID()} disabled={sessionBusy()}
                        onChange={(e) => { const id = e.currentTarget.value; setFolders((f) => ({ ...f, [projectID()]: id })); }}>
                  <For each={projectFolders(projectID())}>{(f) => <option value={f.id}>{folderName(f)}</option>}</For>
                </select>
              </label>
            </Show>
            <Show when={isRepo() && !fromSession()}>
              <label class="field">
                <span>{t("compose.workMode")}</span>
                <select aria-label={t("compose.workMode")} value={mode()} disabled={sessionBusy()} onChange={(e) => setWorkMode(e.currentTarget.value)}
                        title={workModes().find((m) => m.value === mode())?.why}>
                  <For each={workModes()}>{(m) => <option value={m.value}>{m.label}</option>}</For>
                </select>
              </label>
            </Show>
            <label class="field">
              <span>{t("compose.who")}</span>
              <select aria-label={t("compose.who")} value={agentName()} disabled={sessionBusy()} onChange={(e) => setAgent(e.currentTarget.value)}
                      ref={(el) => { queueMicrotask(() => { if (fromSession() && el.isConnected) el.focus({ preventScroll: true }); }); }}>
                <For each={list(agents().agents)}>{(a) => <option value={a.name}>{a.name}</option>}</For>
              </select>
            </label>
            <Show when={!fromSession()}>
              <label class="field">
                <span>{t("compose.flow")}</span>
                <select aria-label={t("compose.flow")} value={flowID()} disabled={sessionBusy()} onChange={(e) => setFlow(e.currentTarget.value)}>
                  <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
                </select>
              </label>
            </Show>
          </div>
        </Show>
        <Show
          when={fromSession()}
          fallback={
            <textarea
              ref={(el) => { queueMicrotask(() => { if (el.isConnected) el.focus({ preventScroll: true }); }); }}
              class="compose-text"
              disabled={busy()}
              placeholder={t("compose.placeholder")}
              value={text()}
              onInput={(e) => setText(e.currentTarget.value)}
              onKeyDown={(e) => {
                // Enter sends, as in a chat; a new line is Shift+Enter.
                if (e.key === "Enter" && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
                  e.preventDefault(); e.stopPropagation();
                  if (!e.repeat) void start();
                }
              }}
            />
          }
        >
          <div class="compose-sessions">
            <Show when={preview()}>
              {(past) => <SessionPreview agentName={agentName()} session={past()} onClose={() => setPreview(undefined)} />}
            </Show>
            <Show when={!preview()}>
              <Show when={!agentName()}>
                <p class="empty">{t("compose.noAgents")}</p>
              </Show>
              <Show when={sessionsError()}>
                <div class="screen-error row wrap">
                  <span>{sessionsError()}</span>
                  <Show when={installableAdapter()}>
                    {(adapter) => <InstallButton adapter={adapter()} />}
                  </Show>
                </div>
              </Show>
              <Show when={agentName() && !sessionsError() && !sessions()}>
                <p class="empty loading"><span class="spinner" />{t("compose.sessionsLoading")}</p>
              </Show>
              <Show when={!sessionsError() && sessions()?.length === 0}>
                <p class="empty">{t("compose.sessionsNone")}</p>
              </Show>
              <For each={sessions() ?? []}>
                {(s) => (
                  <div
                    class={`compose-session ${session()?.id === s.id && session()?.folderID === s.folderID ? "on" : ""}`}
                  >
                    <Show when={editing() !== s.id} fallback={
                      <form class="compose-session-rename" onSubmit={(e) => { e.preventDefault(); void renameSession(s); }}
                        onKeyDown={(e) => {
                          if (e.key === "Escape" && !e.isComposing && e.keyCode !== 229) {
                            e.preventDefault(); e.stopPropagation();
                            if (!e.repeat && !savingTitle()) setEditing("");
                          }
                        }}>
                        <input value={sessionTitle()} aria-label={t("compose.sessionName")} disabled={sessionBusy()}
                          ref={(el) => { queueMicrotask(() => { el.focus({ preventScroll: true }); el.select(); }); }}
                          onInput={(e) => setSessionTitle(e.currentTarget.value)} />
                        <button class="btn primary" type="submit" disabled={sessionBusy() || !sessionTitle().trim()}>
                          <Show when={savingTitle() === s.id}><span class="spinner" /></Show>
                          {t("common.save")}
                        </button>
                        <button class="btn quiet" type="button" disabled={!!savingTitle()}
                          onClick={() => setEditing("")}>{t("common.cancel")}</button>
                      </form>
                    }>
                      <button class="compose-session-pick" title={s.cwd} disabled={sessionBusy()}
                        onClick={() => { setSession(s); setConfirming(""); }}
                        onDblClick={() => { if (canManage()) setPreview(s); }}>
                        <span class="meta">{when(s.updatedAt, false)}</span>
                        <span class="compose-session-title">{s.title || s.id}</span>
                      </button>
                      <Show when={canManage() && confirming() !== s.id}>
                        <button class="btn quiet" disabled={sessionBusy()}
                          onClick={() => { setConfirming(""); setPreview(s); }}>{t("compose.previewSession")}</button>
                      </Show>
                      <Show when={canManage() && confirming() !== s.id}>
                        <button class="btn quiet" disabled={sessionBusy()}
                          onClick={() => { setConfirming(""); setSessionTitle(s.title ?? ""); setEditing(s.id); }}>
                          {t("compose.renameSession")}
                        </button>
                      </Show>
                      <Show when={canManage()}>
                        <Show when={confirming() === s.id} fallback={
                          <button class="btn quiet" disabled={sessionBusy()}
                            onClick={() => setConfirming(s.id)}>{t("common.delete")}</button>
                        }>
                          <span class="meta">{t("compose.confirmDeleteSession")}</span>
                          <button class="btn quiet" disabled={!!deleting()}
                            onClick={() => setConfirming("")}>{t("common.cancel")}</button>
                          <button class="btn danger" disabled={sessionBusy()}
                            onClick={() => void removeSession(s)}>
                            <Show when={deleting() === s.id}><span class="spinner" /></Show>
                            {t("common.delete")}
                          </button>
                        </Show>
                      </Show>
                    </Show>
                  </div>
                )}
              </For>
            </Show>
          </div>
        </Show>
        <Show when={fromSession() && !preview()}>
          <p class="meta compose-sessions-source">{t("compose.sessionsSource", { folder: folder() ? folderName(folder()!) : "default_project" })}</p>
        </Show>
        <Show when={!preview()}>
          <footer class="compose-actions row">
            <Show when={fromSession() && session()}>
              <span class="meta compose-selected">{session()!.title || session()!.id}</span>
            </Show>
            <div class="spacer" />
            <button class="btn primary" onClick={start} disabled={!ready()}>
              {fromSession() ? t("compose.openSession") : !projectID() ? t("compose.startDraft") : t("compose.start")}
            </button>
          </footer>
        </Show>
      </Show>
    </div>
  );
}
