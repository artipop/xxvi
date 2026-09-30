import { createSignal, For, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Agent, Proxy } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import type { AgentCheck } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
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
                <span class="tag ok"><span class="dot" />{a.via ? t("agents.via", { runner: a.via }) : t("work.session")}</span>
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
                <Show when={a.proxy}><span class="tag">{t("proxies.via", { proxy: a.proxy! })}</span></Show>
                <div class="spacer" />
                <CheckButton name={a.name} />
                <button class="btn quiet"
                        onClick={() => setEditing({ agent: JSON.parse(JSON.stringify(a)), original: a.name })}>
                  {t("common.edit")}
                </button>
                <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteAgent(a.name)); await loadAgents(); }}>
                  {t("common.delete")}
                </button>
              </div>
              <Show when={a.prompt}><div class="body">{a.prompt}</div></Show>
              <Show when={checks()[a.name]}>{(c) => <CheckResult check={c()} />}</Show>
            </div>
          </Show>
        )}
      </For>

      <ProxiesSection />
    </>
  );
}

type EditingProxy = { proxy: Proxy; original: string };

// Named network paths. An agent picks one by name, so a proxy is described
// once and shared; a rename carries the agents that use it along.
function ProxiesSection(): JSX.Element {
  const [editing, setEditing] = createSignal<EditingProxy | null>(null);
  const blank = (): Proxy => ({ name: "", url: "", noProxy: "", caCert: "", username: "", password: "" } as Proxy);
  return (
    <>
      <div class="list-head">
        <h2>{t("proxies.title")}</h2>
        <div class="spacer" />
        <button class="btn" disabled={Boolean(editing())} onClick={() => setEditing({ proxy: blank(), original: "" })}>
          {t("proxies.new")}
        </button>
      </div>
      <div class="meta">{t("proxies.note")}</div>

      <Show when={editing() && editing()!.original === ""}>
        <ProxyForm proxy={editing()!.proxy} original="" onDone={() => setEditing(null)} />
      </Show>

      <For each={agents().proxies}>
        {(p) => (
          <Show when={editing()?.original !== p.name}
                fallback={<ProxyForm proxy={editing()!.proxy} original={p.name} onDone={() => setEditing(null)} />}>
            <div class="card">
              <div class="row wrap">
                <span class="title">{p.name}</span>
                <Show when={p.url}><span class="tag mono">{p.url}</span></Show>
                <Show when={p.username}><span class="tag">{p.username}</span></Show>
                <Show when={p.caCert}><span class="tag">{t("proxies.hasCA")}</span></Show>
                <div class="spacer" />
                <button class="btn quiet"
                        onClick={() => setEditing({ proxy: JSON.parse(JSON.stringify(p)), original: p.name })}>
                  {t("common.edit")}
                </button>
                <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteProxy(p.name)); await loadAgents(); }}>
                  {t("common.delete")}
                </button>
              </div>
              <Show when={p.noProxy}><div class="body mono">NO_PROXY={p.noProxy}</div></Show>
            </div>
          </Show>
        )}
      </For>
    </>
  );
}

function ProxyForm(props: { proxy: Proxy; original: string; onDone: () => void }): JSX.Element {
  const [p, setP] = createSignal<Proxy>(props.proxy);
  const patch = (x: Partial<Proxy>) => setP({ ...p(), ...x });
  const save = async () => {
    const saved = await guard(() => API.SaveProxy(props.original, p()));
    if (saved) { await loadAgents(); props.onDone(); }
  };
  return (
    <div class="card">
      <h3>{t("proxies.proxy")}</h3>
      <div class="grid2">
        <label class="field">
          <span>{t("proxies.name")}</span>
          <input type="text" value={p().name} onInput={(e) => patch({ name: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>{t("proxies.url")}</span>
          <input type="text" placeholder="http://host:3128" value={p().url ?? ""}
                 onInput={(e) => patch({ url: e.currentTarget.value })} />
        </label>
      </div>
      <div class="grid2">
        <label class="field">
          <span>{t("proxies.username")}</span>
          <input type="text" autocomplete="off" value={p().username ?? ""}
                 onInput={(e) => patch({ username: e.currentTarget.value })} />
        </label>
        <label class="field">
          <span>{t("proxies.password")}</span>
          <input type="password" autocomplete="new-password" value={p().password ?? ""}
                 placeholder={props.original ? t("proxies.passwordKept") : ""}
                 onInput={(e) => patch({ password: e.currentTarget.value })} />
        </label>
      </div>
      <label class="field">
        <span>{t("proxies.noProxy")}</span>
        <input type="text" placeholder="localhost,127.0.0.1,.corp.example" value={p().noProxy ?? ""}
               onInput={(e) => patch({ noProxy: e.currentTarget.value })} />
      </label>
      <label class="field">
        <span>{t("proxies.caCert")}</span>
        <input type="text" value={p().caCert ?? ""} onInput={(e) => patch({ caCert: e.currentTarget.value })} />
      </label>
      <div class="row actions">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>{t("common.cancel")}</button>
        <button class="btn primary" onClick={save}>{t("common.save")}</button>
      </div>
    </div>
  );
}

// What each agent said about itself, by name. Asked on demand: a check starts
// the agent, and one run through npx or uvx downloads itself first.
const [checks, setChecks] = createSignal<Record<string, AgentCheck>>({});

function CheckButton(props: { name: string }): JSX.Element {
  const [busy, setBusy] = createSignal(false);
  const check = async () => {
    setBusy(true);
    const got = await guard(() => API.CheckAgent(props.name));
    setBusy(false);
    if (got) setChecks({ ...checks(), [props.name]: got });
  };
  return (
    <button class="btn quiet" disabled={busy()} onClick={() => void check()} title={t("agents.checkTitle")}>
      {busy() ? t("agents.checking") : t("agents.check")}
    </button>
  );
}

function CheckResult(props: { check: AgentCheck }): JSX.Element {
  const c = () => props.check;
  return (
    <div class="body">
      <div class="row wrap">
        <span class="meta">{[c().title, c().version].filter(Boolean).join(" ")}</span>
        <span class={c().revive ? "tag ok" : "tag"}>
          <span class="dot" />{c().revive ? t("agents.revives", { how: c().revive! }) : t("agents.noRevive")}
        </span>
        <Show when={c().lists}><span class="tag ok"><span class="dot" />{t("agents.lists")}</span></Show>
        <Show when={c().model}>
          <span class="tag" title={(c().models ?? []).join(", ")}>{t("agents.startsOn", { model: c().model! })}</span>
        </Show>
      </div>
      <Show when={c().problem}><div>{say(c().problem)}</div></Show>
    </div>
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

      <label class="field">
        <span>{t("proxies.agentProxy")}</span>
        <select value={a().proxy ?? ""} onChange={(e) => patch({ proxy: e.currentTarget.value })}>
          <option value="">{t("proxies.none")}</option>
          <For each={agents().proxies}>{(p) => <option value={p.name}>{p.name}</option>}</For>
        </select>
      </label>

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
