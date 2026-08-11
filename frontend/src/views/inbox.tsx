import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Card, InboxGroup } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { applyCard, flows, guard, inbox, loadInbox, openCardByID, setInbox } from "../state";

// The inbox: what the sources brought, grouped by what brought it. A card here
// does nothing until somebody takes it into work — that decision is the whole
// of the screen.

/** SUGGESTED is the property a source's rule fills in to prefill this dialog.
 *  It is an ordinary property, so nothing special has to know about it. */
const SUGGESTED = "Флоу";

export default function InboxView() {
  return (
    <>
      <h1>Входящие</h1>
      <p class="lede">
        Задачи от источников. Ничего не происходит, пока карточку не взяли в работу.
      </p>
      <Show when={inbox().length > 0} fallback={<div class="empty">Пусто. Источники ничего не принесли.</div>}>
        <For each={inbox()}>{(group) => <Group group={group} />}</For>
      </Show>
    </>
  );
}

function Group(props: { group: InboxGroup }) {
  const [adding, setAdding] = createSignal(false);
  const name = () => props.group.source || "Без источника";

  const poll = async () => {
    const groups = await guard(() => API.PollSource(props.group.source));
    if (groups) setInbox(groups);
  };

  return (
    <>
      <div class="list-head">
        <h2>{name()}</h2>
        <span class="meta">{props.group.cards.length}</span>
        <div class="spacer" />
        <Show when={props.group.source}>
          <button class="btn quiet" onClick={poll}>Прочитать сейчас</button>
          <button class="btn quiet" onClick={() => setAdding(!adding())}>
            {adding() ? "Отмена" : "Добавить элемент"}
          </button>
        </Show>
      </div>

      <Show when={adding()}>
        <AddItem source={props.group.source} onDone={() => setAdding(false)} />
      </Show>

      <For each={props.group.cards}>{(card) => <InboxCard card={card} />}</For>
    </>
  );
}

function AddItem(props: { source: string; onDone: () => void }) {
  const [title, setTitle] = createSignal("");
  const [body, setBody] = createSignal("");

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
        <span>Заголовок</span>
        <input type="text" value={title()} onInput={(e) => setTitle(e.currentTarget.value)} />
      </label>
      <label class="field">
        <span>Текст</span>
        <textarea value={body()} onInput={(e) => setBody(e.currentTarget.value)} />
      </label>
      <div class="row">
        <span class="meta">Элемент дописывается в файл источника и проходит его правила.</span>
        <div class="spacer" />
        <button class="btn primary" onClick={submit}>Добавить</button>
      </div>
    </div>
  );
}

function InboxCard(props: { card: Card }) {
  const suggested = () => props.card.props?.[SUGGESTED] ?? "";
  const [flowID, setFlowID] = createSignal("");

  const chosen = () => {
    if (flowID()) return flowID();
    const byName = flows().find((f) => f.name === suggested());
    return byName?.id ?? flows()[0]?.id ?? "";
  };

  const take = async () => {
    const view = await guard(() => API.TakeIntoWork(props.card.id, chosen()));
    applyCard(view);
  };

  const drop = async () => {
    await guard(() => API.DropCard(props.card.id));
    await loadInbox();
  };

  return (
    <div class="card">
      <div class="row">
        <span class="title clickable" onClick={() => openCardByID(props.card.id)}
              style={{ cursor: "pointer" }}>{props.card.title}</span>
        <div class="spacer" />
        <For each={Object.entries(props.card.props ?? {})}>
          {([name, value]) => <Show when={name !== SUGGESTED}><span class="tag">{name}: {value}</span></Show>}
        </For>
      </div>

      <Show when={props.card.body}>
        <div class="body">{shorten(props.card.body!)}</div>
      </Show>

      <div class="row wrap" style={{ "margin-top": "10px" }}>
        <select value={chosen()} onChange={(e) => setFlowID(e.currentTarget.value)} style={{ width: "auto" }}>
          <For each={flows()}>{(f) => <option value={f.id}>{f.name}</option>}</For>
        </select>
        <button class="btn primary" onClick={take} disabled={flows().length === 0}>В работу</button>
        <Show when={suggested()}>
          <span class="meta">источник предлагает «{suggested()}»</span>
        </Show>
        <div class="spacer" />
        <button class="btn quiet" onClick={drop}>Отбросить</button>
      </div>
    </div>
  );
}

function shorten(text: string): string {
  const limit = 240;
  return text.length > limit ? text.slice(0, limit) + "…" : text;
}
