import { createSignal, createStore, storePath, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Project } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { guard, list, loadProjects, projects, vocabulary } from "../state";
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
      <p class="lede">{t("projects.lede")}</p>

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
                <Show when={p.repository}>
                  <span class="tag">{p.provider ? label("provider", p.provider) : t("projects.noProvider")} · {p.repository}</span>
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
      // A project that turned out to be on a hosting nobody is connected to
      // stays open: the token is the next thing to ask, and it is asked here.
      if (saved.provider && saved.server && !saved.account) {
        setDraft((d) => { d.id = saved.id; d.remote = saved.remote; d.provider = saved.provider; });
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

      <div class="grid2">
        <label class="field">
          <span>{t("projects.remote")}</span>
          <input type="text" class="mono" placeholder={t("projects.remotePlaceholder")} value={draft.remote ?? ""}
                 onInput={(e) => setDraft(storePath("remote", e.currentTarget.value))} />
        </label>
        <label class="field">
          <span>{t("projects.provider")}</span>
          <select value={draft.provider ?? ""} onChange={(e) => setDraft(storePath("provider", e.currentTarget.value))}>
            <option value="">{t("projects.noProvider")}</option>
            <For each={list(vocabulary().providers)}>
              {(k) => <option value={k}>{label("provider", k)}</option>}
            </For>
          </select>
        </label>
      </div>

      {/* The token is asked for only once the project is saved with a hosting:
          it belongs to the server, and the server is what the saved remote
          says. */}
      <Show when={draft.id && props.project.provider && props.project.server}>
        <Hosting project={props.project} onChanged={props.onChanged} />
      </Show>

      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>

      <div class="row">
        <span class="meta">{t("projects.note")}</span>
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

// Who the application is on the project's server. The token is checked by
// asking the server who it belongs to, and kept in the system keychain rather
// than in the database — one per server, shared by every project on it.
function Hosting(props: { project: Project; onChanged: (p: Project) => void }) {
  const [token, setToken] = createSignal("");
  const [error, setError] = createSignal<unknown>(null);
  const [busy, setBusy] = createSignal(false);

  const run = async (fn: () => Promise<Project>) => {
    setError(null);
    setBusy(true);
    try {
      const p = await fn();
      setToken("");
      props.onChanged(p);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="field">
      <span>{t("projects.hosting", { server: props.project.server ?? "" })}</span>
      <Show when={props.project.account} fallback={
        <div class="row">
          <input type="password" class="grow" placeholder={t("projects.tokenPlaceholder")} value={token()}
                 onInput={(e) => setToken(e.currentTarget.value)}
                 onKeyDown={(e) => { if (e.key === "Enter") void run(() => API.ConnectHosting(props.project.id, token())); }} />
          <button class="btn" disabled={busy() || !token().trim()}
                  onClick={() => run(() => API.ConnectHosting(props.project.id, token()))}>
            {t("projects.connect")}
          </button>
        </div>
      }>
        <div class="row">
          <span>{t("projects.connectedAs", { account: props.project.account ?? "" })}</span>
          <div class="spacer" />
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
      <Show when={!props.project.account}>
        <span class="meta">{t("projects.tokenNote")}</span>
      </Show>
      <Show when={error() !== null}>
        <div class="error"><pre>{errorText(error())}</pre></div>
      </Show>
    </div>
  );
}
