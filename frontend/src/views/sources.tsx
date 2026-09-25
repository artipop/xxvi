import { createSignal, createStore, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { SourcePlugin } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import type { Rule, Source } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import type { Field } from "../../bindings/github.com/artipop/xxvi/internal/sources/models";
import { flows, guard, loadSources, sourcePlugins, sourceStatus, sources, vocabulary } from "../state";
import { label, plural, say, t, when } from "../i18n";

// The source registry and its rules. Rules are tried in order and the first
// match wins, so the order is the whole of what somebody meant — and the
// catch-all goes last.

export default function SourcesView() {
  const [editing, setEditing] = createSignal<Source | null>(null);

  return (
    <>
      <h1>{t("sources.title")}</h1>

      <div class="row toolbar">
        <div class="spacer" />
        <button class="btn" onClick={() => setEditing({
          name: "", plugin: sourcePlugins()[0]?.name ?? DEMO, enabled: true, noisy: false, update: "update",
          intervalSeconds: 0, config: {}, rules: [],
        } as Source)}>{t("sources.new")}</button>
      </div>

      <For each={sources()}>
        {(src) => (
          <div class="card">
            <div class="row wrap">
              <span class="title">{src.name}</span>
              <Show when={src.enabled} fallback={<span class="tag"><span class="dot" />{t("sources.disabled")}</span>}>
                <span class="tag ok"><span class="dot" />{t("sources.enabled")}</span>
              </Show>
              <Show when={src.noisy}><span class="tag warn">{t("sources.noisy")}</span></Show>
              <span class="tag">{pluginTitle(src.plugin ?? "")}</span>
              <span class="tag">{rulesCount(src.rules?.length ?? 0)}</span>
              <div class="spacer" />
              <Show when={src.enabled && src.plugin !== DEMO}>
                <button class="btn quiet" onClick={() => guard(() => API.PollSource(src.name))}>{t("sources.pollNow")}</button>
              </Show>
              <button class="btn quiet" onClick={() => setEditing(JSON.parse(JSON.stringify(src)))}>{t("common.edit")}</button>
              <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteSource(src.name)); await loadSources(); }}>
                {t("common.delete")}
              </button>
            </div>
            <Show when={src.plugin === DEMO && src.config?.path}>
              <div class="meta mono note">{src.config!.path}</div>
            </Show>
            <StatusLine name={src.name} />
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
  const isNew = props.source.name === "";
  const [token, setToken] = createSignal("");
  const [hasToken, setHasToken] = createSignal(false);
  if (!isNew) void API.SourceHasToken(props.source.name).then(setHasToken).catch(() => setHasToken(false));

  const plugin = () => sourcePlugins().find((p) => p.name === draft.plugin);
  const refused = () => (isNew ? "" : sourceStatus()[props.source.name]?.field ?? "");
  const setConfig = (key: string, value: string) =>
    setDraft((d) => { d.config = { ...(draft.config ?? {}), [key]: value }; });

  const save = async () => {
    const saved = await guard(() => API.SaveSource(JSON.parse(JSON.stringify(draft)) as Source));
    if (!saved) return;
    // The token goes to the keychain on its own, after the source exists: it
    // is never part of the source, which is a row anybody can read.
    if (token().trim() !== "") {
      const ok = await guard(async () => { await API.SetSourceToken(saved.name, token()); return true; });
      if (!ok) return;
    }
    await loadSources();
    props.onDone();
  };

  const addRule = () => {
    setDraft((s) => {
      s.rules = [...(s.rules ?? []), { name: "", then: "card", when: {} } as Rule];
    });
  };

  const setRule = (i: number, patch: Partial<Rule>) => setDraft((d) => { Object.assign((d.rules as Rule[])[i], patch); });

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
      <h3>{t("sources.source")}</h3>
      <div class="grid2">
        <label class="field">
          <span>{t("agents.name")}</span>
          <input type="text" value={draft.name} disabled={!isNew}
                 onInput={(e) => setDraft((d) => { d.name = e.currentTarget.value; })} />
        </label>
        <label class="field">
          <span>{t("sources.plugin")}</span>
          <select value={draft.plugin} disabled={!isNew}
                  onChange={(e) => { setDraft((d) => { d.plugin = e.currentTarget.value; d.config = {}; }); }}>
            <For each={sourcePlugins()}>{(p) => <option value={p.name}>{p.title || p.name}</option>}</For>
            <option value={DEMO}>{t("sources.demoPlugin")}</option>
            <Show when={!isNew && draft.plugin !== DEMO && !plugin()}>
              <option value={draft.plugin}>{pluginTitle(draft.plugin ?? "")}</option>
            </Show>
          </select>
        </label>
      </div>

      <Show when={draft.plugin === DEMO}>
        <label class="field">
          <span>{t("sources.file")}</span>
          <input type="text" value={draft.config?.path ?? ""} onInput={(e) => setConfig("path", e.currentTarget.value)} />
        </label>
      </Show>

      <Show when={plugin()}>
        {(p) => (
          <>
            <div class="grid2">
              <For each={list(p().fields).filter((f) => f.type !== "bool")}>
                {(f) => (
                  <FieldInput field={f} value={draft.config?.[f.key] ?? ""} refused={refused() === f.key}
                              onChange={(v) => setConfig(f.key, v)} />
                )}
              </For>
            </div>
            <div class="grid2">
              <Show when={p().needsToken}>
                <label class="field">
                  <span>{t("sources.token")}</span>
                  <input type="password" autocomplete="off" value={token()}
                         placeholder={hasToken() ? t("sources.tokenKept") : ""}
                         onInput={(e) => setToken(e.currentTarget.value)} />
                </label>
              </Show>
              <label class="field">
                <span>{t("sources.interval")}</span>
                <input type="text" inputmode="numeric" placeholder={t("sources.intervalDefault")}
                       value={draft.intervalSeconds ? String(Math.round(draft.intervalSeconds / 60)) : ""}
                       onInput={(e) => setDraft((d) => { d.intervalSeconds = minutes(e.currentTarget.value); })} />
              </label>
            </div>
          </>
        )}
      </Show>

      <div class="row wrap toolbar">
        {/* A plugin's yes-or-no settings sit with the source's own: a checkbox
            in the grid of inputs would stand a line above its words. */}
        <For each={list(plugin()?.fields).filter((f) => f.type === "bool")}>
          {(f) => (
            <label class="row check">
              <input type="checkbox" checked={(draft.config?.[f.key] || f.default) === "true"}
                     onChange={(e) => setConfig(f.key, e.currentTarget.checked ? "true" : "false")} />
              <span class={refused() === f.key ? "bad-text" : ""}>{f.title || f.key}</span>
            </label>
          )}
        </For>
        <label class="row check">
          <input type="checkbox" checked={draft.enabled}
                 onChange={(e) => setDraft((d) => { d.enabled = e.currentTarget.checked; })} />
          <span>{t("sources.enabled")}</span>
        </label>
        <label class="row check">
          <input type="checkbox" checked={draft.noisy ?? false}
                 onChange={(e) => setDraft((d) => { d.noisy = e.currentTarget.checked; })} />
          <span>{t("sources.noisyToggle")}</span>
        </label>
      </div>
      <div class="meta lead">
        {t("sources.noisyNote")}
      </div>

      <div class="row">
        <h3>{t("sources.rules")}</h3>
        <div class="spacer" />
        <button class="btn quiet" onClick={addRule}>{t("sources.newRule")}</button>
      </div>

      <For each={draft.rules ?? []}>
        {(rule, i) => (
          <div class="rule">
            <div class="row rule-head">
              <input type="text" placeholder={t("sources.ruleName")} value={rule.name ?? ""}
                     onInput={(e) => setRule(i(), { name: e.currentTarget.value })} />
              <button class="btn quiet" onClick={() => move(i(), -1)} disabled={i() === 0}>↑</button>
              <button class="btn quiet" onClick={() => move(i(), 1)}
                      disabled={i() === (draft.rules?.length ?? 0) - 1}>↓</button>
              <button class="btn quiet" onClick={() => removeRule(i())}>×</button>
            </div>

            <div class="grid2">
              <label class="field">
                <span>{t("sources.ifTitle")}</span>
                <input type="text" value={rule.when?.title ?? ""}
                       onInput={(e) => setRule(i(), { when: { ...(rule.when ?? {}), title: e.currentTarget.value } })} />
              </label>
              <label class="field">
                <span>{t("sources.then")}</span>
                <select value={rule.then} onChange={(e) => setRule(i(), { then: e.currentTarget.value })}>
                  <For each={vocabulary().ruleActions}>
                    {(a) => <option value={a}>{label("ruleAction", a)}</option>}
                  </For>
                </select>
              </label>
            </div>

            <Show when={rule.then === "card"}>
              <label class="field">
                <span>{t("sources.suggestFlow")}</span>
                <select value={rule.suggestFlow ?? ""}
                        onChange={(e) => setRule(i(), { suggestFlow: e.currentTarget.value })}>
                  <option value="">{t("sources.noSuggestion")}</option>
                  <For each={flows()}>{(f) => <option value={f.name}>{f.name}</option>}</For>
                </select>
              </label>
            </Show>
          </div>
        )}
      </For>

      <Show when={(draft.rules?.length ?? 0) === 0}>
        <div class="empty">{t("sources.noRules")}</div>
      </Show>

      <div class="row actions">
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onDone}>{t("common.cancel")}</button>
        <button class="btn primary" onClick={save}>{t("common.save")}</button>
      </div>
    </div>
  );
}

function rulesCount(n: number): string {
  return plural("sources.ruleCount", n);
}

// DEMO is the plugin name of the file source, read by the app itself rather
// than through a manifest (internal/inbox/demo.go).
const DEMO = "demo";
const REVIEW = "review";

function list<T>(v: T[] | null | undefined): T[] {
  return v ?? [];
}

function pluginTitle(name: string): string {
  if (name === DEMO) return t("sources.demoPlugin");
  if (name === REVIEW) return t("sources.reviewPlugin");
  const p: SourcePlugin | undefined = sourcePlugins().find((x) => x.name === name);
  return p ? p.title || p.name : name;
}

function minutes(text: string): number {
  const n = Number(text.trim());
  return Number.isFinite(n) && n > 0 ? Math.round(n * 60) : 0;
}

/** FieldInput is one text, number or select setting a plugin declared. A form
 *  has only strings; the MCP bridge turns what looks like a number back into
 *  one. */
function FieldInput(props: { field: Field; value: string; refused: boolean; onChange: (v: string) => void }) {
  return (
    <label class={`field ${props.refused ? "refused" : ""}`}>
      <span>{props.field.title || props.field.key}</span>
      <Show when={props.field.type === "select"} fallback={
        <input type="text" inputmode={props.field.type === "number" ? "numeric" : undefined}
               value={props.value} placeholder={props.field.default ?? ""}
               onInput={(e) => props.onChange(e.currentTarget.value)} />
      }>
        <select value={props.value || props.field.default || ""} onChange={(e) => props.onChange(e.currentTarget.value)}>
          <For each={list(props.field.values)}>{(v) => <option value={v}>{v}</option>}</For>
        </select>
      </Show>
    </label>
  );
}

/** StatusLine is what a plugin source last did, which is the whole of how a
 *  silent integration gets debugged. */
function StatusLine(props: { name: string }) {
  const status = () => sourceStatus()[props.name];
  return (
    <Show when={status() && status()!.state !== "off" ? status() : undefined}>
      {(s) => (
        <div class="row wrap note">
          <span class={`tag ${STATE_TONE[s().state] ?? ""}`}><span class="dot" />{label("sourceState", s().state)}</span>
          <Show when={s().lastPoll}>
            <span class="meta">{t("sources.lastPoll", { when: when(s().lastPoll) })}</span>
          </Show>
          <Show when={s().error}>
            <span class={`meta ${s().state === "error" ? "bad-text" : ""}`}>{say(s().error)}</span>
          </Show>
        </div>
      )}
    </Show>
  );
}

const STATE_TONE: Record<string, string> = {
  running: "ok", starting: "accent", error: "bad", "needs-reauth": "warn",
};
