import { createEffect, createSignal, createStore, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/gandvik/internal/app/api";
import type { SourcePlugin } from "../../bindings/github.com/artipop/gandvik/internal/app/models";
import type { Rule, Source } from "../../bindings/github.com/artipop/gandvik/internal/model/models";
import type { Field, Option } from "../../bindings/github.com/artipop/gandvik/internal/sources/models";
import { flows, guard, loadSources, sourcePlugins, sourceStatus, sources, vocabulary } from "../state";
import { errorText, has, label, plural, say, t, when } from "../i18n";

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
          name: "", plugin: creatable()[0]?.name ?? "", enabled: true, noisy: false, update: "update",
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
              <Show when={src.enabled}>
                <button class="btn quiet" onClick={() => guard(() => API.PollSource(src.name))}>{t("sources.pollNow")}</button>
              </Show>
              <button class="btn quiet" onClick={() => setEditing(JSON.parse(JSON.stringify(src)))}>{t("common.edit")}</button>
              <button class="btn quiet" onClick={async () => { await guard(() => API.DeleteSource(src.name)); await loadSources(); }}>
                {t("common.delete")}
              </button>
            </div>
            <Show when={pluginOf(src.plugin)?.builtin && settingsLine(src)}>
              <div class="meta mono note">{settingsLine(src)}</div>
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
  // The token as it stood when its box was left, not as it is typed: a list
  // is asked for with it, and asking at every keystroke would start the
  // plugin's process that many times.
  const [askToken, setAskToken] = createSignal("");
  const [hasToken, setHasToken] = createSignal(false);
  if (!isNew) void API.SourceHasToken(props.source.name).then(setHasToken).catch(() => setHasToken(false));

  const plugin = () => sourcePlugins().find((p) => p.name === draft.plugin);
  const refused = () => (isNew ? "" : sourceStatus()[props.source.name]?.field ?? "");
  // A field that waits for another one's value is not in the form until it has
  // it; the other field's default counts, or the form would open without it.
  const shown = (f: Field) => {
    const w = f.showWhen;
    if (!w) return true;
    const dep = list(plugin()?.fields).find((o) => o.key === w.key);
    return (draft.config?.[w.key] || dep?.default || "") === w.value;
  };

  const setConfig = (key: string, value: string) =>
    setDraft((d) => {
      const config = { ...(draft.config ?? {}), [key]: value };
      // What was chosen under another choice no longer holds: the columns
      // are those of the board that was picked.
      for (const f of list(plugin()?.fields)) {
        if (list(f.options?.dependsOn).includes(key)) config[f.key] = "";
      }
      d.config = config;
    });

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
            <For each={creatable().concat(plugin() && plugin()!.managed ? [plugin()!] : [])}>
              {(p) => <option value={p.name}>{pluginTitle(p.name)}</option>}
            </For>
            <Show when={!isNew && !plugin()}>
              <option value={draft.plugin}>{pluginTitle(draft.plugin ?? "")}</option>
            </Show>
          </select>
        </label>
      </div>

      <Show when={plugin()}>
        {(p) => (
          <>
            <div class="grid2">
              <For each={list(p().fields).filter((f) => f.type !== "bool" && shown(f))}>
                {(f) => (
                  <FieldInput plugin={p()} field={f} value={draft.config?.[f.key] ?? ""} refused={refused() === f.key}
                              source={() => JSON.parse(JSON.stringify(draft)) as Source} token={askToken}
                              tokenKept={hasToken} onChange={(v) => setConfig(f.key, v)} />
                )}
              </For>
            </div>
            <div class="grid2">
              <Show when={p().takesToken}>
                <label class="field">
                  <span>{t("sources.token")}</span>
                  <input type="password" autocomplete="off" value={token()}
                         placeholder={hasToken() ? t("sources.tokenKept") : p().needsToken ? "" : t("sources.tokenOptional")}
                         onInput={(e) => setToken(e.currentTarget.value)}
                         onChange={(e) => setAskToken(e.currentTarget.value.trim())} />
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
        <For each={list(plugin()?.fields).filter((f) => f.type === "bool" && shown(f))}>
          {(f) => (
            <label class="row check">
              <input type="checkbox" checked={(draft.config?.[f.key] || f.default) === "true"}
                     onChange={(e) => setConfig(f.key, e.currentTarget.checked ? "true" : "false")} />
              <span class={refused() === f.key ? "bad-text" : ""}>{fieldTitle(plugin()!.name, f)}</span>
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

function list<T>(v: T[] | null | undefined): T[] {
  return v ?? [];
}

/** creatable is what the form offers to make a source of: everything but what
 *  is made elsewhere (a project's review queue). */
function creatable(): SourcePlugin[] {
  return sourcePlugins().filter((p) => !p.managed);
}

function pluginOf(name: string | undefined): SourcePlugin | undefined {
  return sourcePlugins().find((x) => x.name === name);
}

// A plugin that ships with the app is worded in both languages; one somebody
// wrote carries its own title, in the language they wrote it in.
function pluginTitle(name: string): string {
  const key = `plugin.${name}`;
  if (has(key)) return t(key);
  const p = pluginOf(name);
  return p ? p.title || p.name : name;
}

function fieldTitle(plugin: string, f: Field): string {
  const key = `plugin.${plugin}.${f.key}`;
  return has(key) ? t(key) : f.title || f.key;
}

/** valueTitle words one value of a select, falling back to the value itself. */
function valueTitle(plugin: string, key: string, value: string): string {
  const k = `plugin.${plugin}.${key}.${value}`;
  return has(k) ? t(k) : value;
}

/** settingsLine is what a source the app reads itself was set to, for the card. */
function settingsLine(src: Source): string {
  return list(pluginOf(src.plugin)?.fields)
    .map((f) => src.config?.[f.key])
    .filter((v) => v)
    .join(" · ");
}

function minutes(text: string): number {
  const n = Number(text.trim());
  return Number.isFinite(n) && n > 0 ? Math.round(n * 60) : 0;
}

/** FieldInput is one text, number or select setting a plugin declared. A form
 *  has only strings; the MCP bridge turns what looks like a number back into
 *  one. */
function FieldInput(props: {
  plugin: SourcePlugin; field: Field; value: string; refused: boolean;
  source: () => Source; token: () => string; tokenKept: () => boolean;
  onChange: (v: string) => void;
}) {
  const title = () => fieldTitle(props.plugin.name, props.field);
  return (
    <Show when={props.field.options} fallback={
      <label class={`field ${props.refused ? "refused" : ""}`}>
        <span>{title()}</span>
        <Show when={props.field.type === "select"} fallback={
          <input type="text" inputmode={props.field.type === "number" ? "numeric" : undefined}
                 value={props.value} placeholder={props.field.default ?? ""}
                 onInput={(e) => props.onChange(e.currentTarget.value)} />
        }>
          <select value={props.value || props.field.default || ""} onChange={(e) => props.onChange(e.currentTarget.value)}>
            <For each={list(props.field.values)}>{(v) => <option value={v}>{valueTitle(props.plugin.name, props.field.key, v)}</option>}</For>
          </select>
        </Show>
      </label>
    }>
      <OptionsInput {...props} title={title()} />
    </Show>
  );
}

/** OptionsInput is a field whose values the service itself knows: its boards,
 *  its columns. They are asked for when the form opens, and again when what
 *  they depend on changes. Without an answer — no token yet, the program not
 *  running — it is a text box with the reason beside it, so a value can still be
 *  typed. */
function OptionsInput(props: {
  plugin: SourcePlugin; field: Field; title: string; value: string; refused: boolean;
  source: () => Source; token: () => string; tokenKept: () => boolean;
  onChange: (v: string) => void;
}) {
  const [options, setOptions] = createSignal<Option[] | undefined>(undefined);
  const [failure, setFailure] = createSignal("");
  const [loading, setLoading] = createSignal(false);
  const [again, setAgain] = createSignal(0);
  const needs = () => list(props.field.options?.dependsOn);
  const waitingFor = () => needs().some((k) => !(props.source().config?.[k] ?? "").trim());
  const needsToken = () => props.plugin.needsToken && !props.token() && !props.tokenKept();

  // The answer to an older question is dropped: a slow program must not fill
  // the list for a board that is no longer chosen.
  let asked = 0;
  createEffect(
    () => [props.source().plugin, ...needs().map((k) => props.source().config?.[k] ?? ""), props.token(), again()] as const,
    () => {
      const n = ++asked;
      setOptions(undefined);
      setFailure("");
      if (waitingFor() || needsToken()) { setLoading(false); return; }
      setLoading(true);
      API.SourceFieldOptions(props.source(), props.token(), props.field.key).then(
        (got) => { if (n === asked) { setOptions(list(got)); setLoading(false); } },
        (err) => { if (n === asked) { setFailure(errorText(err)); setLoading(false); } },
      );
    },
  );

  const shown = () => {
    const got = options() ?? [];
    // A value stored earlier that the service no longer lists stays visible
    // rather than being swapped for the first one on the list.
    return props.value && !got.some((o) => o.value === props.value) ? [{ value: props.value, label: props.value }, ...got] : got;
  };
  const hint = () => (needsToken() ? t("sources.optionsNeedToken") : failure());

  return (
    <label class={`field ${props.refused ? "refused" : ""}`}>
      <span>{props.title}</span>
      <div class="row options-row">
        <Show when={options() !== undefined} fallback={
          <input type="text" value={props.value} disabled={waitingFor() && !props.value}
                 placeholder={loading() ? t("sources.optionsLoading") : ""}
                 onInput={(e) => props.onChange(e.currentTarget.value)} />
        }>
          <select value={props.value} onChange={(e) => props.onChange(e.currentTarget.value)}>
            <option value="">—</option>
            <For each={shown()}>{(o) => <option value={o.value}>{o.label}</option>}</For>
          </select>
        </Show>
        <button type="button" class="btn quiet" title={t("sources.optionsRefresh")} disabled={loading()}
                onClick={(e) => { e.preventDefault(); setAgain((n) => n + 1); }}>↻</button>
      </div>
      <Show when={hint()}><div class="meta bad-text">{hint()}</div></Show>
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
