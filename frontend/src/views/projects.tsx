import { createSignal, For, Show } from "solid-js";
import { createStore } from "solid-js/store";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Project } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { guard, list, loadProjects, projects, vocabulary } from "../state";

// Where work happens. The card says what, the flow says how it travels, this
// says in which folder it all takes place — and a card names one by id, so a
// project can be renamed without dragging anything behind it.

function blank(): Project {
  return { id: "", name: "", kind: "folder", path: "", createdAt: "" };
}

export default function ProjectsView() {
  const [editing, setEditing] = createSignal<Project | null>(null);

  return (
    <>
      <div class="row">
        <h1>Проекты</h1>
        <div class="spacer" />
        <button class="btn" onClick={() => setEditing(blank())}>+ Проект</button>
      </div>
      <p class="lede">
        Места, где идёт работа. Карточка выбирает одно из них, и там открываются
        агент, терминал и заметки её ленты.
      </p>

      <Show when={editing()}>
        <ProjectForm
          project={editing()!}
          onDone={() => { setEditing(null); void loadProjects(); }}
        />
      </Show>

      <Show when={projects().length > 0} fallback={
        <div class="empty">
          Ни одного проекта. Пока их нет, каждая карточка работает в своей пустой папке —
          это верно для задачи с чистого листа и бесполезно для задачи про существующий код.
        </div>
      }>
        <For each={projects()}>
          {(p) => (
            <div class="card clickable" onClick={() => setEditing({ ...p })}>
              <div class="row">
                <span class="title">{p.name}</span>
                <span class="tag">{kindLabel(p.kind)}</span>
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

function kindLabel(kind: string): string {
  return list(vocabulary().projectKinds).find((k) => k.kind === kind)?.label ?? kind;
}

function ProjectForm(props: { project: Project; onDone: () => void }) {
  const [draft, setDraft] = createStore<Project>({ ...props.project });
  const [error, setError] = createSignal("");
  const [confirming, setConfirming] = createSignal(false);

  const save = async () => {
    setError("");
    try {
      await API.SaveProject({ ...draft });
      props.onDone();
    } catch (e: any) {
      // The refusal is a sentence written for a person — a path with a typo in
      // it says so while they can still see what they typed.
      setError(String(e?.message ?? e));
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
          <span>Название</span>
          <input type="text" value={draft.name}
                 onInput={(e) => setDraft("name", e.currentTarget.value)} />
        </label>
        <label class="field">
          <span>Вид</span>
          <select value={draft.kind} onChange={(e) => setDraft("kind", e.currentTarget.value)}>
            <For each={list(vocabulary().projectKinds)}>
              {(k) => <option value={k.kind}>{k.label}</option>}
            </For>
          </select>
        </label>
      </div>
      <label class="field">
        <span>Папка (абсолютный путь)</span>
        <input type="text" placeholder="/Users/…/sources/проект" value={draft.path}
               onInput={(e) => setDraft("path", e.currentTarget.value)} />
      </label>

      <Show when={error()}>
        <div class="error"><pre>{error()}</pre></div>
      </Show>

      <div class="row">
        <span class="meta">Агент работает прямо в этой папке.</span>
        <div class="spacer" />
        <Show when={draft.id}>
          <Show when={confirming()} fallback={
            <button class="btn quiet" onClick={() => setConfirming(true)}>Удалить</button>
          }>
            <span class="meta">Удалить «{draft.name}»?</span>
            <button class="btn quiet" onClick={() => setConfirming(false)}>Отмена</button>
            <button class="btn danger" onClick={remove}>Удалить</button>
          </Show>
        </Show>
        <button class="btn quiet" onClick={props.onDone}>Закрыть</button>
        <button class="btn primary" onClick={save}>Сохранить</button>
      </div>
    </div>
  );
}
