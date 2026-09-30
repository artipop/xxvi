import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Card, InboxGroup } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { applyCard, flows, folderName, guard, inbox, workspace, list, loadInbox, openCardByID, projectFolders, projects, setInbox, showRibbon, sources, openOutside } from "../state";
import { propName, propValue, t } from "../i18n";

// The inbox: what the sources brought, grouped by what brought it. A card here
// does nothing until somebody takes it into work — that decision is the whole
// of the screen.

/** SUGGESTED is the property a source's rule fills in to prefill this dialog.
 *  It is an ordinary property, so nothing special has to know about it. */
const SUGGESTED = "Flow";

export default function InboxView() {
  const [own, setOwn] = createSignal(false);

  return (
    <>
      <div class="row">
        <h1>{t("inbox.title")}</h1>
        <div class="spacer" />
        {/* A task a person thought of is not an item somebody sent: it belongs
            to nobody's stream and has no rules to meet. Until this button it
            had to pretend to be one — typed into a source's file and filtered
            by rules written about other people's mail. */}
        <button class="btn" onClick={() => setOwn(!own())}>
          {own() ? t("common.cancel") : t("inbox.own")}
        </button>
      </div>

      <Show when={own()}>
        <AddOwn onDone={() => setOwn(false)} />
      </Show>
      <Show when={inbox().length > 0} fallback={
        <div class="empty">{t("inbox.empty")}</div>
      }>
        <For each={inbox()}>{(group) => <Group group={group} />}</For>
      </Show>
    </>
  );
}

function Group(props: { group: InboxGroup }) {
  const [adding, setAdding] = createSignal(false);
  const review = () => props.group.plugin === "review";
  const name = () => (review()
    ? t("inbox.reviewGroup", { project: props.group.source })
    : props.group.source || t("inbox.noSource"));

  const poll = async () => {
    const groups = await guard(() => API.PollSource(props.group.source));
    if (groups) setInbox(groups);
  };

  return (
    <>
      <div class="list-head">
        <h2>{name()}</h2>
        <span class="meta">{list(props.group.cards).length}</span>
        <div class="spacer" />
        <Show when={props.group.source}>
          <button class="btn quiet" onClick={poll}>{t("inbox.poll")}</button>
          <Show when={!review()}>
            <button class="btn quiet" onClick={() => setAdding(!adding())}>
              {adding() ? t("common.cancel") : t("inbox.addItem")}
            </button>
          </Show>
        </Show>
      </div>

      <Show when={adding()}>
        <AddItem source={props.group.source} onDone={() => setAdding(false)} />
      </Show>

      <For each={props.group.cards}>{(card) => <InboxCard card={card} />}</For>
    </>
  );
}

// A card straight into the inbox: no source, no rules, nothing to match. It is
// grouped under «No source», which is what a card nobody sent is.
function AddOwn(props: { onDone: () => void }) {
  const [title, setTitle] = createSignal("");
  const [body, setBody] = createSignal("");

  const submit = async () => {
    if (!title().trim()) return;
    const card = await guard(() => API.AddCard("", title(), body()));
    if (!card) return;
    await loadInbox();
    props.onDone();
  };

  return (
    <div class="card">
      <label class="field">
        <span>{t("inbox.whatToDo")}</span>
        <input type="text" value={title()} onInput={(e) => setTitle(e.currentTarget.value)} />
      </label>
      <label class="field">
        <span>{t("inbox.details")}</span>
        <textarea value={body()} onInput={(e) => setBody(e.currentTarget.value)} />
      </label>
      <div class="row">
        <div class="spacer" />
        <button class="btn primary" onClick={submit}>{t("inbox.create")}</button>
      </div>
    </div>
  );
}

function AddItem(props: { source: string; onDone: () => void }) {
  const [title, setTitle] = createSignal("");
  const [body, setBody] = createSignal("");
  const noisy = () => Boolean(sources().find((s) => s.name === props.source)?.noisy);

  const submit = async () => {
    if (!title().trim()) return;
    // It goes into the source's own file, so a hand-added item is an item like
    // any other: it meets the same rules and survives the file being reread.
    const groups = await guard(() => API.AddItem(props.source, title(), body()));
    if (groups) { setInbox(groups); props.onDone(); }
  };

  return (
    <div class="card">
      <label class="field">
        <span>{t("inbox.itemTitle")}</span>
        <input type="text" value={title()} onInput={(e) => setTitle(e.currentTarget.value)} />
      </label>
      <label class="field">
        <span>{t("inbox.itemBody")}</span>
        <textarea value={body()} onInput={(e) => setBody(e.currentTarget.value)} />
      </label>
      {/* A noisy source drops whatever no rule matched, and that is right for a
          stream of notifications — but the button looks the same on every
          source, and typing a task into this one would lose it without a word.
          A rule here is a subscription; saying so is cheaper than the silence. */}
      <Show when={noisy()}>
        <div class="warn-note">{t("inbox.noisy", { source: props.source })}</div>
      </Show>

      <div class="row">
        <span class="meta">{t("inbox.itemNote")}</span>
        <div class="spacer" />
        <button class="btn primary" onClick={submit}>{t("common.add")}</button>
      </div>
    </div>
  );
}

function InboxCard(props: { card: Card }) {
  const suggested = () => props.card.props?.[SUGGESTED] ?? "";
  const [flowID, setFlowID] = createSignal("");
  const [projectID, setProjectID] = createSignal(workspace());
  const [folderID, setFolderID] = createSignal("");

  const chosen = () => {
    if (flowID()) return flowID();
    const byName = flows().find((f) => f.name === suggested());
    return byName?.id ?? flows()[0]?.id ?? "";
  };

  // «Do it» is one gesture: the card goes onto the flow and the ribbon that
  // shows it opens. Taking a card into work and watching it start are the same
  // moment, and making them two clicks would be making them two decisions.
  // Where and by which route, answered in one gesture. The project is a
  // person's answer like the flow is, and this is the moment both are real.
  const take = async () => {
    if (projectID()) {
      if (!(await guard(() => API.SetCardProject(props.card.id, projectID(), folderID())))) return;
    }
    const view = await guard(() => API.TakeIntoWork(props.card.id, chosen()));
    if (!view) return;
    applyCard(view);
    showRibbon(props.card.id);
  };

  const drop = async () => {
    await guard(() => API.DropCard(props.card.id));
    await loadInbox();
  };

  return (
    <div class="card">
      <div class="row">
        <span class="title clickable" onClick={() => openCardByID(props.card.id)}>{props.card.title}</span>
        <div class="spacer" />
        <For each={Object.entries(props.card.props ?? {})}>
          {([name, value]) => (
            <Show when={name !== SUGGESTED && name !== "MR"}>
              <span class="tag">{propName(name)}: {propValue(name, value ?? "")}</span>
            </Show>
          )}
        </For>
        <Show when={props.card.props?.MR}>
          <a class="tag" href={props.card.props!.MR} onClick={(e) => openOutside(e, props.card.props!.MR)}>{mrNumber(props.card.externalId, props.card.props!.MR!)} ↗</a>
        </Show>
      </div>

      <Show when={props.card.body}>
        <div class="body">{shorten(props.card.body!)}</div>
      </Show>

      <div class="row wrap actions">
        <select value={chosen()} onChange={(e) => setFlowID(e.currentTarget.value)} class="fit">
          <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
        </select>
        {/* An MR under review is in its own repository, on its own branch:
            where it is worked is not a question. */}
        <Show when={projects().length > 0 && props.card.workMode !== "review"}>
          <select value={projectID()} onChange={(e) => { setProjectID(e.currentTarget.value); setFolderID(""); }}
                  class="fit" title={t("inbox.where")}>
            <option value="">{t("common.ownFolder")}</option>
            <For each={projects()}>{(p) => <option value={p.id}>{p.name}</option>}</For>
          </select>
          <Show when={projectFolders(projectID()).length > 1}>
            <select value={folderID() || projectFolders(projectID())[0]?.id} onChange={(e) => setFolderID(e.currentTarget.value)}
                    class="fit" title={t("compose.folder")}>
              <For each={projectFolders(projectID())}>{(f) => <option value={f.id}>{folderName(f)}</option>}</For>
            </select>
          </Show>
        </Show>
        <button class="btn primary" onClick={take} disabled={flows().length === 0}>{t("inbox.doIt")}</button>
        <Show when={suggested()}>
          <span class="meta">{t("inbox.suggests", { flow: suggested() })}</span>
        </Show>
        <div class="spacer" />
        <button class="btn quiet" onClick={drop}>{t("common.drop")}</button>
      </div>
    </div>
  );
}

function mrNumber(externalId: string | undefined, url: string): string {
  if (externalId?.startsWith("!")) return externalId;
  const m = url.match(/merge_requests\/(\d+)/);
  return m ? `!${m[1]}` : "MR";
}

function shorten(text: string): string {
  const limit = 240;
  return text.length > limit ? text.slice(0, limit) + "…" : text;
}
