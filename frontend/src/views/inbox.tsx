import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Card, InboxGroup } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { applyCard, flows, guard, inbox, list, loadInbox, openCardByID, setInbox, showRibbon, sources } from "../state";

// The inbox: what the sources brought, grouped by what brought it. A card here
// does nothing until somebody takes it into work — that decision is the whole
// of the screen.

/** SUGGESTED is the property a source's rule fills in to prefill this dialog.
 *  It is an ordinary property, so nothing special has to know about it. */
const SUGGESTED = "Флоу";

export default function InboxView() {
  const [own, setOwn] = createSignal(false);

  return (
    <>
      <div class="row">
        <h1>Входящие</h1>
        <div class="spacer" />
        {/* A task a person thought of is not an item somebody sent: it belongs
            to nobody's stream and has no rules to meet. Until this button it
            had to pretend to be one — typed into a source's file and filtered
            by rules written about other people's mail. */}
        <button class="btn" onClick={() => setOwn(!own())}>
          {own() ? "Отмена" : "Своя задача"}
        </button>
      </div>
      <p class="lede">
        Задачи от источников и свои. Ничего не происходит, пока карточку не взяли в работу.
      </p>

      <Show when={own()}>
        <AddOwn onDone={() => setOwn(false)} />
      </Show>
      <Show when={inbox().length > 0} fallback={
        <div class="empty">Пусто. Источники ничего не принесли, своих задач тоже нет.</div>
      }>
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
        <span class="meta">{list(props.group.cards).length}</span>
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

// A card straight into the inbox: no source, no rules, nothing to match. It is
// grouped under «Без источника», which is what a card nobody sent is.
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
        <span>Что нужно сделать</span>
        <input type="text" value={title()} onInput={(e) => setTitle(e.currentTarget.value)} />
      </label>
      <label class="field">
        <span>Подробности</span>
        <textarea value={body()} onInput={(e) => setBody(e.currentTarget.value)} />
      </label>
      <div class="row">
        <span class="meta">Карточка заводится сразу и ничьих правил не проходит.</span>
        <div class="spacer" />
        <button class="btn primary" onClick={submit}>Завести</button>
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
        <span>Заголовок</span>
        <input type="text" value={title()} onInput={(e) => setTitle(e.currentTarget.value)} />
      </label>
      <label class="field">
        <span>Текст</span>
        <textarea value={body()} onInput={(e) => setBody(e.currentTarget.value)} />
      </label>
      {/* A noisy source drops whatever no rule matched, and that is right for a
          stream of notifications — but the button looks the same on every
          source, and typing a task into this one would lose it without a word.
          A rule here is a subscription; saying so is cheaper than the silence. */}
      <Show when={noisy()}>
        <div class="warn-note">
          Источник «{props.source}» помечен шумным: всё, что не совпало ни с одним его
          правилом, отбрасывается молча. Свою задачу лучше завести кнопкой «Своя задача».
        </div>
      </Show>

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

  // «Сделай» is one gesture: the card goes onto the flow and the ribbon that
  // shows it opens. Taking a card into work and watching it start are the same
  // moment, and making them two clicks would be making them two decisions.
  const take = async () => {
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
        <button class="btn primary" onClick={take} disabled={flows().length === 0}>Сделай</button>
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
