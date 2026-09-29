import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Agent } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { agents, guard, loadAgents, vocabulary } from "../state";
import { say, t } from "../i18n";

// The agent registry, with the adapter check beside it — "can this agent
// actually run here" is the question this screen is opened with, and a missing
// adapter would otherwise surface as a failed card minutes later.

type Editing = { agent: Agent; original: string | null };

export default function AgentsView() {
  const [editing, setEditing] = createSignal<Editing | null>(null);
  const editingName = () => editing()?.original ?? null;

  return (
    <>
      <h1>{t("agents.title")}</h1>

      <h2>{t("agents.installed")}</h2>
      <For each={agents().adapters}>
        {(a) => (
          <div class="card">
            <div class="row wrap">
              <span class="title">{a.kind}</span>
              <Show when={a.ready} fallback={<span class="tag bad"><span class="dot" />{t("agents.wontStart")}</span>}>
                <span class="tag ok"><span class="dot" />{a.viaNpx ? t("agents.viaNpx") : t("work.session")}</span>
              </Show>
              {/* Two questions with two answers: the vendor's ACP adapter and
                  the vendor's interactive CLI are different programs, and a
                  machine can have one without the other. */}
              <Show when={a.terminal} fallback={<span class="tag"><span class="dot" />{t("agents.noTerminal")}</span>}>
                <span class="tag ok"><span class="dot" />{t("work.terminal")}</span>
              </Show>
              <div class="spacer" />
              <Show when={a.path}><span class="meta mono">{a.path}</span></Show>
            </div>
            <Show when={a.detail}><div class="body">{say(a.detail)}</div></Show>
            <Show when={!a.terminal && a.terminalDetail}>
              <div class="body">{say(a.terminalDetail)}</div>
            </Show>
          </div>
        )}
      </For>

      <div class="list-head">
        <h2>{t("agents.registry")}</h2>
        <div class="spacer" />
        <button class="btn" disabled={Boolean(editing())}
                onClick={() => setEditing({ agent: { name: "", kind: "claude" } as Agent, original: null })}>
          {t("agents.new")}
        </button>
      </div>

      <Show when={editing() && editing()!.original === null}>
        <AgentForm agent={editing()!.agent} onDone={() => setEditing(null)} />
      </Show>

      <For each={agents().agents}>
        {(a) => (
          <Show
            when={editingName() !== a.name}
            fallback={<AgentForm agent={editing()!.agent} onDone={() => setEditing(null)} />}
          >
            <div class="card">
              <div class="row wrap">
                <span class="title">{a.name}</span>
                <span class="tag">{a.kind}</span>
                <Show when={a.model}><span class="tag">{a.model}</span></Show>
                <div class="spacer" />
                <button class="btn quiet"
                        onClick={() => setEditing({ agent: JSON.parse(JSON.stringify(a)), original: a.name })}>
                  {t("common.edit")}
                </button>
                <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteAgent(a.name)); await loadAgents(); }}>
                  {t("common.delete")}
                </button>
              </div>
              <Show when={a.prompt}><div class="body">{a.prompt}</div></Show>
            </div>
          </Show>
        )}
      </For>
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
    <div class="card">
      <h3>{t("agents.agent")}</h3>
      <div class="grid2">
        <label class="field">
          <span>{t("agents.name")}</span>
          <input type="text" value={a().name} onInput={(e) => patch({ name: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>{t("agents.kind")}</span>
          <select value={a().kind} onChange={(e) => patch({ kind: e.currentTarget.value })}>
            <For each={vocabulary().kinds}>{(k) => <option value={k}>{k}</option>}</For>
          </select>
        </label>
      </div>

      <label class="field">
        <span>{t("agents.prompt")}</span>
        <textarea value={a().prompt ?? ""} onInput={(e) => patch({ prompt: e.currentTarget.value })} />
      </label>

      <div class="grid2">
        <label class="field">
          <span>{t("agents.model")}</span>
          <input type="text" value={a().model ?? ""} onInput={(e) => patch({ model: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>{t("agents.binPath")}</span>
          <input type="text" value={a().binPath ?? ""} onInput={(e) => patch({ binPath: e.currentTarget.value })} />
        </label>
      </div>

      <Show when={a().kind === "acp"}>
        <label class="field">
          <span>{t("agents.command")}</span>
          <input type="text" value={(a().command ?? []).join(" ")}
                 onInput={(e) => patch({ command: e.currentTarget.value.split(" ").filter(Boolean) })} />
        </label>
      </Show>

      <label class="field">
        <span>{t("agents.autoAllow")}</span>
        <textarea value={(a().autoAllowTools ?? []).join("\n")}
                  onInput={(e) => patch({ autoAllowTools: e.currentTarget.value.split("\n").map((s) => s.trim()).filter(Boolean) })} />
      </label>
      <div class="meta">{t("agents.autoAllowNote")}</div>

      <div class="row actions">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>{t("common.cancel")}</button>
        <button class="btn primary" onClick={save}>{t("common.save")}</button>
      </div>
    </div>
  );
}
