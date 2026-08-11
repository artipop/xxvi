import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Agent } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { agents, guard, loadAgents, vocabulary } from "../state";

// The agent registry, with the adapter check beside it — "can this agent
// actually run here" is the question this screen is opened with, and a missing
// adapter would otherwise surface as a failed card minutes later.

export default function AgentsView() {
  const [editing, setEditing] = createSignal<Agent | null>(null);

  return (
    <>
      <h1>Агенты</h1>
      <p class="lede">Агенты по ACP. Стадия выбирает исполнителя из своего состава.</p>

      <h2>Что установлено на этой машине</h2>
      <For each={agents().adapters}>
        {(a) => (
          <div class="card">
            <div class="row wrap">
              <span class="title">{a.kind}</span>
              <Show when={a.ready} fallback={<span class="tag bad"><span class="dot" />не запустится</span>}>
                <span class="tag ok"><span class="dot" />{a.viaNpx ? "через npx" : "готов"}</span>
              </Show>
              <div class="spacer" />
              <Show when={a.path}><span class="meta mono">{a.path}</span></Show>
            </div>
            <Show when={a.detail}><div class="body">{a.detail}</div></Show>
          </div>
        )}
      </For>

      <div class="list-head">
        <h2>Реестр</h2>
        <div class="spacer" />
        <button class="btn" onClick={() => setEditing({ name: "", kind: "claude" } as Agent)}>+ Агент</button>
      </div>

      <For each={agents().agents}>
        {(a) => (
          <div class="card">
            <div class="row wrap">
              <span class="title">{a.name}</span>
              <span class="tag">{a.kind}</span>
              <Show when={a.model}><span class="tag">{a.model}</span></Show>
              <div class="spacer" />
              <button class="btn quiet" onClick={() => setEditing(JSON.parse(JSON.stringify(a)))}>Изменить</button>
              <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteAgent(a.name)); await loadAgents(); }}>
                Удалить
              </button>
            </div>
            <Show when={a.prompt}><div class="body">{a.prompt}</div></Show>
          </div>
        )}
      </For>

      <Show when={editing()}>
        <AgentForm agent={editing()!} onDone={() => setEditing(null)} />
      </Show>
    </>
  );
}

function AgentForm(props: { agent: Agent; onDone: () => void }) {
  const [a, setA] = createSignal<Agent>(props.agent);
  const patch = (p: Partial<Agent>) => setA({ ...a(), ...p });

  const save = async () => {
    const saved = await guard(() => API.SaveAgent(a()));
    if (saved) { await loadAgents(); props.onDone(); }
  };

  return (
    <div class="panel">
      <h3>Агент</h3>
      <div class="grid2">
        <label class="field">
          <span>Имя</span>
          <input type="text" value={a().name} onInput={(e) => patch({ name: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>Тип</span>
          <select value={a().kind} onChange={(e) => patch({ kind: e.currentTarget.value })}>
            <For each={vocabulary().kinds}>{(k) => <option value={k}>{k}</option>}</For>
          </select>
        </label>
      </div>

      <label class="field">
        <span>Промпт — кто он вообще</span>
        <textarea value={a().prompt ?? ""} onInput={(e) => patch({ prompt: e.currentTarget.value })} />
      </label>

      <div class="grid2">
        <label class="field">
          <span>Модель</span>
          <input type="text" value={a().model ?? ""} onInput={(e) => patch({ model: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>Путь к бинарнику (если не на PATH)</span>
          <input type="text" value={a().binPath ?? ""} onInput={(e) => patch({ binPath: e.currentTarget.value })} />
        </label>
      </div>

      <Show when={a().kind === "acp"}>
        <label class="field">
          <span>Команда запуска — весь argv ACP-агента, через пробел</span>
          <input type="text" value={(a().command ?? []).join(" ")}
                 onInput={(e) => patch({ command: e.currentTarget.value.split(" ").filter(Boolean) })} />
        </label>
      </Show>

      <label class="field">
        <span>Что можно без спроса — по одному в строке, например Bash(git *)</span>
        <textarea value={(a().autoAllowTools ?? []).join("\n")}
                  onInput={(e) => patch({ autoAllowTools: e.currentTarget.value.split("\n").map((s) => s.trim()).filter(Boolean) })} />
      </label>
      <div class="meta">Пусто — действует машинный список: смотреть можно, менять нельзя, остальное агент спросит.</div>

      <div class="row" style={{ "margin-top": "12px" }}>
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>Отмена</button>
        <button class="btn primary" onClick={save}>Сохранить</button>
      </div>
    </div>
  );
}
