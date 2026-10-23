import { createEffect, createSignal, createStore, onSettled, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/gandvik/internal/app/api";
import type { Project } from "../../bindings/github.com/artipop/gandvik/internal/model/models";
import type { RemoteOption } from "../../bindings/github.com/artipop/gandvik/internal/hosting/models";
import { folderName, guard, list, loadProjects, projects, openOutside } from "../state";
import { errorText, label, t } from "../i18n";
import { formKeys } from "./formKeys";

// Where work happens. The task says what, the flow says how it travels, this
// says in which folder it all takes place — and a task names one by id, so a
// project can be renamed without dragging anything behind it. A project can
// live in several folders, a front and a back, and a task picks one of them.
//
// Begun the way an IDE begins one: a folder that exists is opened, a new one
// is created where it is asked for. More folders are the project's own
// business afterwards, in its form.

// The form edits a copy of the folders, never the list the registry holds.
function editable(p: Project): Project {
  const folders = list(p.folders).length > 0 ? list(p.folders) : [{ id: "", name: "", path: p.path }];
  return { ...p, folders: folders.map((f) => ({ ...f })) };
}

/** openFolder is «Open folder…»: the system's dialog, and the folder is the project. */
export async function openFolder(): Promise<Project | undefined> {
  const chosen = await guard(() => API.PickFolder(t("projects.openTitle"), ""));
  if (!chosen) return undefined;
  const project = await guard(() => API.OpenProjectFolder(chosen));
  if (project) await loadProjects();
  return project ?? undefined;
}

export default function ProjectsView() {
  const [editing, setEditing] = createSignal<Project | null>(null);
  const [creating, setCreating] = createSignal(false);
  const form = () => (
    <ProjectForm
      project={editing()!}
      onDone={() => { setEditing(null); void loadProjects(); }}
      onChanged={(p) => { setEditing(editable(p)); void loadProjects(); }}
    />
  );

  return (
    <>
      <div class="row">
        <h1>{t("projects.title")}</h1>
        <div class="spacer" />
        <button class="btn" onClick={() => { setEditing(null); setCreating(true); }}>{t("projects.newProject")}</button>
        <button class="btn" onClick={() => void openFolder()}>{t("projects.openFolder")}</button>
      </div>

      <Show when={creating()}>
        <NewProjectForm onDone={() => setCreating(false)}
          onCreated={(p) => { setCreating(false); setEditing(editable(p)); }} />
      </Show>

      {/* One project, one place: the one being edited is its form, where its
          card was. */}
      <Show when={projects().length > 0} fallback={
        <Show when={!creating()}><div class="empty">{t("projects.empty")}</div></Show>
      }>
        <For each={projects()}>
          {(p) => (
            <Show when={editing()?.id === p.id} fallback={
              <div class="card clickable" onClick={() => { setCreating(false); setEditing(editable(p)); }}>
                <div class="row">
                  <span class="title">{p.name}</span>
                  <Show when={p.provider}>
                    <span class="tag">{label("provider", p.provider!)} · {p.repository}</span>
                  </Show>
                  <Show when={p.provider}>
                    <Show when={p.account} fallback={<span class="tag warn">{t("projects.notConnected")}</span>}>
                      <span class="tag ok"><span class="dot" />@{p.account}</span>
                    </Show>
                  </Show>
                  <Show when={p.reviewInbox}>
                    <span class="tag">{t("projects.reviewTag")}</span>
                  </Show>
                  <div class="spacer" />
                </div>
                <For each={list(p.folders)}>
                  {(f) => (
                    <div class="mono">
                      <Show when={list(p.folders).length > 1}>{folderName(f)}: </Show>{f.path}
                    </div>
                  )}
                </For>
              </div>
            }>
              {form()}
            </Show>
          )}
        </For>
      </Show>
    </>
  );
}

const PARENT_KEY = "gandvik.projectsParent";

function rememberedParent(): string {
  try { return localStorage.getItem(PARENT_KEY) ?? ""; } catch { return ""; }
}

// Where new folders are made: the place used last time, else home.
function useParent() {
  const [parent, setParent] = createSignal(rememberedParent());
  if (!parent()) void API.HomeDir().then((home) => { if (!parent()) setParent(home); });
  const pick = async () => {
    const chosen = await guard(() => API.PickFolder(t("projects.pickParentTitle"), parent()));
    if (chosen) setParent(chosen);
  };
  const remember = () => { try { localStorage.setItem(PARENT_KEY, parent()); } catch { /* a convenience */ } };
  const pathOf = (name: string) => parent() && name.trim() ? `${parent().replace(/[\\/]+$/, "")}/${name.trim()}` : "";
  return { parent, setParent, pick, remember, pathOf };
}

function ParentField(props: { where: ReturnType<typeof useParent> }) {
  return (
    <div class="field">
      <span class="field-label">{t("projects.parent")}</span>
      <div class="row">
        <input type="text" class="grow" placeholder={t("projects.pathPlaceholder")} value={props.where.parent()}
               onInput={(e) => props.where.setParent(e.currentTarget.value)} />
        <button class="btn" onClick={() => void props.where.pick()}>{t("projects.pick")}</button>
      </div>
    </div>
  );
}

/** NewProjectForm is «New project…»: a name and where its folder goes. */
export function NewProjectForm(props: { onDone: () => void; onCreated: (p: Project) => void | Promise<void> }) {
  const [name, setName] = createSignal("");
  const [initRepo, setInitRepo] = createSignal(true);
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal<unknown>(null);
  const where = useParent();

  const create = async () => {
    if (saving() || !name().trim()) return;
    setSaving(true);
    setError(null);
    try {
      const saved = await API.CreateProject({ id: "", name: name(), kind: "", path: "", folders: [] }, where.parent(), "", initRepo());
      where.remember();
      await loadProjects();
      await props.onCreated(saved);
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div class="panel project-form" onKeyDown={formKeys({ submit: create, busy: saving, cancel: props.onDone })}>
      <div class="project-create">
        <label class="field">
          <span>{t("projects.name")}</span>
          <input type="text" value={name()} ref={(el) => queueMicrotask(() => el.focus({ preventScroll: true }))}
                 onInput={(e) => setName(e.currentTarget.value)} />
        </label>
        <ParentField where={where} />
        <Show when={where.pathOf(name())}>
          <p class="meta">{t("projects.willCreate", { path: where.pathOf(name()) })}</p>
        </Show>
        <label class="row check">
          <input type="checkbox" checked={initRepo()} onChange={(e) => setInitRepo(e.currentTarget.checked)} />
          <span>{t("projects.initRepo")}</span>
        </label>
      </div>
      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>
      <div class="row">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>{t("common.cancel")}</button>
        <button class="btn primary" disabled={saving() || !name().trim()} onClick={() => void create()}>{t("projects.create")}</button>
      </div>
    </div>
  );
}

export function ProjectForm(props: {
  project: Project;
  onDone: () => void;
  onChanged: (p: Project) => void;
}) {
  const [draft, setDraft] = createStore<Project>({ ...props.project });
  const [error, setError] = createSignal<unknown>(null);
  const [confirming, setConfirming] = createSignal(false);
  const [removing, setRemoving] = createSignal(-1);
  const [adding, setAdding] = createSignal(false);
  const [saving, setSaving] = createSignal(false);

  const save = async () => {
    if (saving()) return;
    setSaving(true);
    setError(null);
    try {
      await API.SaveProject({ ...draft });
      props.onDone();
    } catch (e) {
      // Shown here rather than at the top of the screen: a path with a typo in
      // it is refused while the person can still see what they typed.
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  const addFolder = (path: string) => {
    if (list(draft.folders).some((f) => f.path === path)) return;
    setDraft((d) => { d.folders = [...list(d.folders), { id: "", name: "", path }]; });
    setError(null);
  };
  const openExisting = async () => {
    const chosen = await guard(() => API.PickFolder(t("projects.openTitle"), ""));
    if (chosen) addFolder(chosen);
  };
  const removeFolder = (i: number) => {
    setDraft((d) => { d.folders = list(d.folders).filter((_, j) => j !== i); });
    setRemoving(-1);
  };

  const remove = async () => {
    await guard(() => API.DeleteProject(draft.id));
    props.onDone();
  };

  return (
    <div class="panel project-form" onKeyDown={formKeys({ submit: save, busy: saving, cancel: () => {
      if (removing() >= 0) setRemoving(-1);
      else if (adding()) setAdding(false);
      else if (confirming()) setConfirming(false);
      else props.onDone();
    } })}>
      <label class="field">
        <span>{t("projects.name")}</span>
        <input type="text" value={draft.name}
               onInput={(e) => setDraft((d) => { d.name = e.currentTarget.value; })} />
      </label>

      <div class="folders">
        <span class="field-label">{t("projects.folders")}</span>
        <For each={list(draft.folders)}>
          {(f, i) => (
            <div class="row folder-row">
              {/* A name is asked only when there is something to tell apart. */}
              <Show when={list(draft.folders).length > 1}>
                <input type="text" class="folder-name" placeholder={folderName(f) || t("projects.folderName")}
                       value={f.name} title={t("projects.folderName")}
                       onInput={(e) => { const v = e.currentTarget.value; setDraft((d) => { d.folders![i()].name = v; }); }} />
              </Show>
              <span class="mono grow folder-path" title={f.path}>{f.path}</span>
              <Show when={list(draft.folders).length > 1}>
                <Show when={removing() === i()} fallback={
                  <button class="btn quiet" onClick={() => setRemoving(i())} title={t("projects.removeFolder")}>×</button>
                }>
                  <span class="meta">{t("projects.removeFolderConfirm")}</span>
                  <button class="btn quiet" onClick={() => setRemoving(-1)}>{t("common.cancel")}</button>
                  <button class="btn danger" onClick={() => removeFolder(i())}>{t("projects.removeFolderYes")}</button>
                </Show>
              </Show>
            </div>
          )}
        </For>
        <Show when={adding()} fallback={
          <div class="row">
            <button class="btn quiet" onClick={() => void openExisting()} title={t("projects.addFolderWhy")}>{t("projects.addExisting")}</button>
            <button class="btn quiet" onClick={() => setAdding(true)}>{t("projects.addNew")}</button>
          </div>
        }>
          <NewFolder onCancel={() => setAdding(false)} onCreated={(path) => { addFolder(path); setAdding(false); }} />
        </Show>
      </div>

      {/* Only a saved repository can be connected: the remotes are read
          from its folder. */}
      <Show when={props.project.id && props.project.repo}>
        <Hosting project={props.project} onChanged={props.onChanged} />
      </Show>

      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>

      <div class="row">
        <div class="spacer" />
        <Show when={confirming()} fallback={
          <button class="btn quiet" onClick={() => setConfirming(true)}>{t("common.delete")}</button>
        }>
          <span class="meta">{t("projects.confirmDelete", { name: draft.name })}</span>
          <button class="btn quiet" onClick={() => setConfirming(false)}>{t("common.cancel")}</button>
          <button class="btn danger" onClick={remove}>{t("common.delete")}</button>
        </Show>
        <button class="btn quiet" onClick={props.onDone}>{t("common.close")}</button>
        <button class="btn primary" disabled={saving()} onClick={save}>{t("common.save")}</button>
      </div>
    </div>
  );
}

// A folder made for a project already open: on disk at once, in the project
// once the form is saved.
function NewFolder(props: { onCancel: () => void; onCreated: (path: string) => void }) {
  const [name, setName] = createSignal("");
  const [initRepo, setInitRepo] = createSignal(true);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<unknown>(null);
  const where = useParent();
  const create = async () => {
    if (busy() || !name().trim()) return;
    setBusy(true);
    setError(null);
    try {
      const path = await API.CreateFolder(where.parent(), name(), initRepo());
      where.remember();
      props.onCreated(path);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div class="project-create new-folder" onKeyDown={formKeys({ submit: create, busy, cancel: props.onCancel })}>
      <label class="field">
        <span>{t("projects.folderToCreate")}</span>
        <input type="text" value={name()} ref={(el) => queueMicrotask(() => el.focus({ preventScroll: true }))}
               onInput={(e) => setName(e.currentTarget.value)} />
      </label>
      <ParentField where={where} />
      <Show when={where.pathOf(name())}>
        <p class="meta">{t("projects.willCreate", { path: where.pathOf(name()) })}</p>
      </Show>
      <label class="row check">
        <input type="checkbox" checked={initRepo()} onChange={(e) => setInitRepo(e.currentTarget.checked)} />
        <span>{t("projects.initRepo")}</span>
      </label>
      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>
      <div class="row">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onCancel}>{t("common.cancel")}</button>
        <button class="btn" disabled={busy() || !name().trim()} onClick={() => void create()}>{t("projects.createFolder")}</button>
      </div>
    </div>
  );
}

// The project's hosting. Nothing is guessed: a person picks which of the
// repository's remotes is the one on the hosting, confirms the server its
// address suggests — ssh and the web interface are not always one host — and
// gets a link to the page where the token is made. The token is checked by
// asking the server who it belongs to, and kept in the system keychain, one
// per server.
function Hosting(props: { project: Project; onChanged: (p: Project) => void }) {
  const [open, setOpen] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<unknown>(null);

  const run = async (fn: () => Promise<Project>) => {
    setError(null);
    setBusy(true);
    try {
      props.onChanged(await fn());
      setOpen(false);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="hosting">
      <span class="field-label">{t("projects.hosting")}</span>
      <Show when={!open()}>
        <Show when={props.project.provider} fallback={
          <div class="row">
            <span class="meta">{t("projects.noHosting")}</span>
            <div class="spacer" />
            <button class="btn" onClick={() => setOpen(true)}>{t("projects.connectGitLab")}</button>
          </div>
        }>
          <div class="row">
            <span>
              {label("provider", props.project.provider!)} · <span class="mono">{props.project.repository}</span>
              {" "}<span class="meta">{t("projects.via", { remote: props.project.remote ?? "" })}</span>
            </span>
            <Show when={props.project.account} fallback={<span class="tag warn">{t("projects.notConnected")}</span>}>
              <span class="tag ok"><span class="dot" />@{props.project.account}</span>
            </Show>
            <div class="spacer" />
            <button class="btn quiet" disabled={busy()} onClick={() => setOpen(true)}>{t("common.edit")}</button>
            <button class="btn quiet" disabled={busy()} onClick={() => run(() => API.DisconnectHosting(props.project.id))}>
              {t("projects.disconnect")}
            </button>
          </div>
          <label class="row check">
            <input type="checkbox" checked={props.project.reviewInbox ?? false} disabled={busy()}
                   onChange={(e) => run(() => API.SetReviewInbox(props.project.id, e.currentTarget.checked))} />
            <span>{t("projects.reviewInbox")}</span>
          </label>
        </Show>
      </Show>
      <Show when={open()}>
        <ConnectForm project={props.project} busy={busy()} onCancel={() => setOpen(false)}
                     onConnect={(remote, server, token) =>
                       run(() => API.ConnectHosting(props.project.id, "gitlab", remote, server, token))} />
      </Show>
      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>
    </div>
  );
}

function ConnectForm(props: {
  project: Project;
  busy: boolean;
  onCancel: () => void;
  onConnect: (remote: string, server: string, token: string) => void;
}) {
  const [remotes, setRemotes] = createSignal<RemoteOption[] | null>(null);
  const [remote, setRemote] = createSignal(props.project.remote ?? "");
  const [server, setServer] = createSignal(props.project.server ?? "");
  const [token, setToken] = createSignal("");
  const [tokenURL, setTokenURL] = createSignal("");

  // The server follows the remote until somebody types one: then it is theirs.
  let typed = Boolean(props.project.server);
  const choose = (opt: RemoteOption) => {
    setRemote(opt.name);
    if (!typed) setServer(opt.server ?? "");
  };

  onSettled(() => {
    void guard(() => API.HostingRemotes(props.project.id)).then((list) => {
      const all = list ?? [];
      setRemotes(all);
      const current = all.find((r) => r.name === remote()) ?? all[0];
      if (current) choose(current);
    });
  });

  createEffect(server, (s) => {
    void API.TokenURL("gitlab", s).then(setTokenURL).catch(() => setTokenURL(""));
  });

  const connected = () => Boolean(props.project.account) && server() === props.project.server;
  const ready = () => remote() && server().trim() && (token().trim() || connected());
  const connect = () => {
    if (!props.busy && ready()) props.onConnect(remote(), server(), token());
  };

  return (
    <div class="hosting" onKeyDown={formKeys({ submit: connect, cancel: props.onCancel, busy: () => props.busy })}>
      <Show when={remotes() !== null && remotes()!.length === 0}>
        <span class="meta">{t("projects.noRemotes")}</span>
      </Show>
      <For each={remotes() ?? []}>
        {(opt) => (
          <label class="row check">
            <input type="radio" name="remote" checked={remote() === opt.name} onChange={() => choose(opt)} />
            <span class="mono">{opt.name}</span>
            <span class="meta mono">{opt.url}</span>
          </label>
        )}
      </For>
      <label class="field">
        <span>{t("projects.server")}</span>
        <input type="text" class="mono" placeholder="https://gitlab.company.ru" value={server()}
               onInput={(e) => { typed = true; setServer(e.currentTarget.value); }} />
      </label>
      <label class="field">
        <span>{t("projects.token")}</span>
        <div class="row">
          <input type="password" class="grow" value={token()}
                 placeholder={connected() ? t("projects.tokenKept") : t("projects.tokenPlaceholder")}
                 onInput={(e) => setToken(e.currentTarget.value)} />
          <Show when={tokenURL()}>
            <a class="btn quiet" href={tokenURL()} onClick={(e) => openOutside(e, tokenURL())}>{t("projects.makeToken")}</a>
          </Show>
        </div>
      </label>
      <span class="meta">{t("projects.tokenNote")}</span>
      <div class="row">
        <div class="spacer" />
        <button class="btn quiet" disabled={props.busy} onClick={props.onCancel}>{t("common.cancel")}</button>
        <button class="btn primary" disabled={props.busy || !ready()}
                onClick={connect}>
          {t("projects.connect")}
        </button>
      </div>
    </div>
  );
}
