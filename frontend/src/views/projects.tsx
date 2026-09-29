import { createEffect, createSignal, createStore, onSettled, storePath, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Project } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import type { RemoteOption } from "../../bindings/github.com/artipop/xxvi/internal/hosting/models";
import { guard, list, loadProjects, projects, vocabulary, openOutside } from "../state";
import { errorText, label, t } from "../i18n";

// Where work happens. The card says what, the flow says how it travels, this
// says in which folder it all takes place — and a card names one by id, so a
// project can be renamed without dragging anything behind it.

function blank(): Project {
  return { id: "", name: "", kind: "folder", path: "" };
}

export default function ProjectsView() {
  const [editing, setEditing] = createSignal<Project | null>(null);

  return (
    <>
      <div class="row">
        <h1>{t("projects.title")}</h1>
        <div class="spacer" />
        <button class="btn" onClick={() => setEditing(blank())}>{t("projects.new")}</button>
      </div>

      <Show when={editing()}>
        <ProjectForm
          project={editing()!}
          onDone={() => { setEditing(null); void loadProjects(); }}
          onChanged={(p) => { setEditing({ ...p }); void loadProjects(); }}
        />
      </Show>

      <Show when={projects().length > 0} fallback={
        <div class="empty">{t("projects.empty")}</div>
      }>
        <For each={projects()}>
          {(p) => (
            <div class="card clickable" onClick={() => setEditing({ ...p })}>
              <div class="row">
                <span class="title">{p.name}</span>
                <span class="tag">{label("projectKind", p.kind)}</span>
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
              <div class="mono">{p.path}</div>
            </div>
          )}
        </For>
      </Show>
    </>
  );
}

// A project picked and not named yet is named after its folder — which is what
// it is called anyway, and one field fewer to fill in.
function basename(path: string): string {
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] ?? "";
}

function ProjectForm(props: { project: Project; onDone: () => void; onChanged: (p: Project) => void }) {
  const [draft, setDraft] = createStore<Project>({ ...props.project });
  const [error, setError] = createSignal<unknown>(null);
  const [confirming, setConfirming] = createSignal(false);

  const save = async () => {
    setError(null);
    try {
      const saved = await API.SaveProject({ ...draft });
      // A new repository stays open: connecting its hosting is the next thing
      // somebody setting it up does, and it is done here.
      if (!draft.id && saved.repo) {
        setDraft((d) => { d.id = saved.id; });
        props.onChanged(saved);
        return;
      }
      props.onDone();
    } catch (e) {
      // Shown here rather than at the top of the screen: a path with a typo in
      // it is refused while the person can still see what they typed.
      setError(e);
    }
  };

  const pick = async () => {
    const chosen = await guard(() => API.PickFolder(t("projects.pickTitle"), draft.path));
    // Empty is a person closing the dialog without choosing, and then what
    // they had stays what they have.
    if (chosen) {
      setDraft(storePath("path", chosen));
      if (!draft.name.trim()) setDraft(storePath("name", basename(chosen)));
      setError(null);
    }
  };

  const remove = async () => {
    await guard(() => API.DeleteProject(draft.id));
    props.onDone();
  };

  return (
    <div class="panel">
      <div class="grid2">
        <label class="field">
          <span>{t("projects.name")}</span>
          <input type="text" value={draft.name}
                 onInput={(e) => setDraft(storePath("name", e.currentTarget.value))} />
        </label>
        <label class="field">
          <span>{t("projects.kind")}</span>
          <select value={draft.kind} onChange={(e) => setDraft(storePath("kind", e.currentTarget.value))}>
            <For each={list(vocabulary().projectKinds)}>
              {(k) => <option value={k}>{label("projectKind", k)}</option>}
            </For>
          </select>
        </label>
      </div>
      <label class="field">
        <span>{t("projects.folder")}</span>
        <div class="row">
          <input type="text" class="grow" placeholder={t("projects.pathPlaceholder")} value={draft.path}
                 onInput={(e) => setDraft(storePath("path", e.currentTarget.value))} />
          {/* Typed only when there is no other way: somebody who knows where
              their project is knows it as a place they can point at, not as a
              string they can spell. */}
          <button class="btn" onClick={pick}>{t("projects.pick")}</button>
        </div>
      </label>

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
        <Show when={draft.id}>
          <Show when={confirming()} fallback={
            <button class="btn quiet" onClick={() => setConfirming(true)}>{t("common.delete")}</button>
          }>
            <span class="meta">{t("projects.confirmDelete", { name: draft.name })}</span>
            <button class="btn quiet" onClick={() => setConfirming(false)}>{t("common.cancel")}</button>
            <button class="btn danger" onClick={remove}>{t("common.delete")}</button>
          </Show>
        </Show>
        <button class="btn quiet" onClick={props.onDone}>{t("common.close")}</button>
        <button class="btn primary" onClick={save}>{t("common.save")}</button>
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

  return (
    <div class="hosting">
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
                 onInput={(e) => setToken(e.currentTarget.value)}
                 onKeyDown={(e) => { if (e.key === "Enter" && ready()) props.onConnect(remote(), server(), token()); }} />
          <Show when={tokenURL()}>
            <a class="btn quiet" href={tokenURL()} onClick={(e) => openOutside(e, tokenURL())}>{t("projects.makeToken")}</a>
          </Show>
        </div>
      </label>
      <span class="meta">{t("projects.tokenNote")}</span>
      <div class="row">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onCancel}>{t("common.cancel")}</button>
        <button class="btn primary" disabled={props.busy || !ready()}
                onClick={() => props.onConnect(remote(), server(), token())}>
          {t("projects.connect")}
        </button>
      </div>
    </div>
  );
}
