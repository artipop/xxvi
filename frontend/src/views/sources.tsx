import { createSignal, createStore, storePath, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Rule, Source } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { flows, guard, loadSources, sources, vocabulary } from "../state";

// The source registry and its rules. Rules are tried in order and the first
// match wins, so the order is the whole of what somebody meant — and the
// catch-all goes last.

export default function SourcesView() {
  const [editing, setEditing] = createSignal<Source | null>(null);

  return (
    <>
      <h1>Источники</h1>
      <p class="lede">
        Откуда приходят задачи. Правила решают, что делать с каждым элементом;
        первое совпавшее выигрывает.
      </p>

      <div class="row" style={{ "margin-bottom": "10px" }}>
        <div class="spacer" />
        <button class="btn" onClick={() => setEditing({
          name: "", plugin: "demo", enabled: true, noisy: false, update: "comment",
          intervalSeconds: 30, config: {}, rules: [],
        } as Source)}>+ Источник</button>
      </div>

      <For each={sources()}>
        {(src) => (
          <div class="card">
            <div class="row wrap">
              <span class="title">{src.name}</span>
              <Show when={src.enabled} fallback={<span class="tag"><span class="dot" />выключен</span>}>
                <span class="tag ok"><span class="dot" />включён</span>
              </Show>
              <Show when={src.noisy}><span class="tag warn">шумный</span></Show>
              <span class="tag">{src.rules?.length ?? 0} правил</span>
              <div class="spacer" />
              <button class="btn quiet" onClick={() => setEditing(JSON.parse(JSON.stringify(src)))}>Изменить</button>
              <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteSource(src.name)); await loadSources(); }}>
                Удалить
              </button>
            </div>
            <Show when={src.config?.path}>
              <div class="meta mono" style={{ "margin-top": "4px" }}>{src.config!.path}</div>
            </Show>
          </div>
        )}
      </For>

      <Show when={editing()}>
        <SourceForm source={editing()!} onDone={() => setEditing(null)} />
      </Show>
    </>
  );
}

function SourceForm(props: { source: Source; onDone: () => void }) {
  const [draft, setDraft] = createStore<Source>(props.source);

  const save = async () => {
    const saved = await guard(() => API.SaveSource(JSON.parse(JSON.stringify(draft)) as Source));
    if (saved) { await loadSources(); props.onDone(); }
  };

  const addRule = () => {
    setDraft((s) => {
      s.rules = [...(s.rules ?? []), { name: "", then: "card", when: {} } as Rule];
    });
  };

  const setRule = (i: number, patch: Partial<Rule>) => setDraft(storePath("rules", i, patch));

  const removeRule = (i: number) => setDraft((s) => { s.rules!.splice(i, 1); });

  const move = (i: number, by: number) => {
    const j = i + by;
    if (j < 0 || j >= (draft.rules?.length ?? 0)) return;
    setDraft((s) => {
      const [r] = s.rules!.splice(i, 1);
      s.rules!.splice(j, 0, r);
    });
  };

  return (
    <div class="panel">
      <h3>Источник</h3>
      <div class="grid2">
        <label class="field">
          <span>Имя</span>
          <input type="text" value={draft.name} onInput={(e) => setDraft(storePath("name", e.currentTarget.value))} />
        </label>
        <label class="field">
          <span>Файл с элементами</span>
          <input type="text" value={draft.config?.path ?? ""}
                 onInput={(e) => setDraft(storePath("config", { ...(draft.config ?? {}), path: e.currentTarget.value }))} />
        </label>
      </div>

      <div class="row wrap" style={{ "margin-bottom": "10px" }}>
        <label class="row" style={{ gap: "6px" }}>
          <input type="checkbox" checked={draft.enabled} style={{ width: "auto" }}
                 onChange={(e) => setDraft(storePath("enabled", e.currentTarget.checked))} />
          <span>включён</span>
        </label>
        <label class="row" style={{ gap: "6px" }}>
          <input type="checkbox" checked={draft.noisy ?? false} style={{ width: "auto" }}
                 onChange={(e) => setDraft(storePath("noisy", e.currentTarget.checked))} />
          <span>шумный — несовпавшее отбрасывать</span>
        </label>
      </div>
      <div class="meta" style={{ "margin-bottom": "12px" }}>
        У обычного источника несовпавший элемент уходит во входящие: молча потерянный
        элемент — то, из-за чего интеграцию невозможно отладить. У шумного наоборот —
        там правило это подписка.
      </div>

      <div class="row">
        <h3 style={{ margin: 0 }}>Правила</h3>
        <div class="spacer" />
        <button class="btn quiet" onClick={addRule}>+ Правило</button>
      </div>

      <For each={draft.rules ?? []}>
        {(rule, i) => (
          <div style={{ "border-top": "1px solid var(--line)", "padding-top": "10px", "margin-top": "10px" }}>
            <div class="row" style={{ "margin-bottom": "6px" }}>
              <input type="text" placeholder="название правила" value={rule.name ?? ""}
                     onInput={(e) => setRule(i(), { name: e.currentTarget.value })} />
              <button class="btn quiet" onClick={() => move(i(), -1)} disabled={i() === 0}>↑</button>
              <button class="btn quiet" onClick={() => move(i(), 1)}
                      disabled={i() === (draft.rules?.length ?? 0) - 1}>↓</button>
              <button class="btn quiet" onClick={() => removeRule(i())}>×</button>
            </div>

            <div class="grid2">
              <label class="field">
                <span>Если в заголовке (регэксп)</span>
                <input type="text" value={rule.when?.title ?? ""}
                       onInput={(e) => setRule(i(), { when: { ...(rule.when ?? {}), title: e.currentTarget.value } })} />
              </label>
              <label class="field">
                <span>То</span>
                <select value={rule.then} onChange={(e) => setRule(i(), { then: e.currentTarget.value })}>
                  <For each={vocabulary().ruleActions}>
                    {(a) => <option value={a}>{ruleLabel(a)}</option>}
                  </For>
                </select>
              </label>
            </div>

            <Show when={rule.then === "card"}>
              <label class="field">
                <span>Предложить флоу — подсказка диалогу «В работу», а не запуск</span>
                <select value={rule.suggestFlow ?? ""}
                        onChange={(e) => setRule(i(), { suggestFlow: e.currentTarget.value })}>
                  <option value="">не предлагать</option>
                  <For each={flows()}>{(f) => <option value={f.name}>{f.name}</option>}</For>
                </select>
              </label>
            </Show>
          </div>
        )}
      </For>

      <Show when={(draft.rules?.length ?? 0) === 0}>
        <div class="empty">Правил нет — всё уходит во входящие как есть.</div>
      </Show>

      <div class="row" style={{ "margin-top": "12px" }}>
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>Отмена</button>
        <button class="btn primary" onClick={save}>Сохранить</button>
      </div>
    </div>
  );
}

function ruleLabel(action: string): string {
  switch (action) {
    case "card": return "завести карточку";
    case "comment": return "дописать комментарий";
    case "drop": return "отбросить";
    default: return action;
  }
}
