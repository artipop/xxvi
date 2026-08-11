import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { agents, applyCard, closeCard, guard, openCard } from "../state";
import { QuestionForm } from "./attention";

// One card, in full: where it stands, what it is waiting for, what has been
// said about it and why it moved. Every "why is this card here" has an answer
// on this panel — that is what it is for.

export default function CardPanel() {
  const view = () => openCard()!;
  const card = () => view().card;
  const flow = () => view().flow;

  const act = async (fn: () => Promise<any>) => applyCard(await guard(fn));

  return (
    <div>
      <div class="panel">
        <div class="row">
          <h3 style={{ margin: 0 }}>{card().source || "Своя карточка"}</h3>
          <div class="spacer" />
          <button class="btn quiet" onClick={closeCard}>Закрыть</button>
        </div>

        <div class="title" style={{ "margin-top": "6px", "font-size": "16px" }}>{card().title}</div>
        <Show when={card().body}><div class="body">{card().body}</div></Show>
        <Show when={card().url}>
          <div class="meta mono" style={{ "margin-top": "6px" }}>{card().url}</div>
        </Show>

        <Show when={flow()}>
          <div class="strip">
            <For each={flow()!.stages}>
              {(s) => (
                <button
                  class={`stage ${s.current ? "current" : ""} ${s.done ? "done" : ""} ${s.final ? "final" : ""}`}
                  title="Перевести карточку сюда"
                  onClick={() => act(() => API.MoveTo(card().id, s.id))}
                >
                  {s.name}
                </button>
              )}
            </For>
          </div>

          <div class="row wrap">
            <span class="tag">{flow()!.flowName}</span>
            <Show when={flow()!.running}><span class="tag accent"><span class="dot" />агент работает</span></Show>
            <Show when={flow()!.queued}><span class="tag"><span class="dot" />ждёт места на стадии</span></Show>
          </div>

          <Show when={(flow()!.waitingFor?.length ?? 0) > 0}>
            <div class="meta" style={{ "margin-top": "8px" }}>
              Стадия ждёт: {flow()!.waitingFor!.join("; ")}
            </div>
          </Show>

          <div class="row wrap" style={{ "margin-top": "10px" }}>
            <Show when={flow()!.running}>
              <button class="btn quiet" onClick={() => act(async () => {
                await API.CancelCard(card().id);
                return API.Card(card().id);
              })}>Остановить агента</button>
            </Show>
            <button class="btn quiet" onClick={() => act(() => API.RemoveFromFlow(card().id))}>
              Снять с флоу
            </button>
          </div>
        </Show>
      </div>

      {/* The question, on the card it belongs to. It is the same question the
          attention panel shows — answering either lets the agent go on. */}
      <Show when={view().question}>
        <div class="panel">
          <h3>Агент спрашивает</h3>
          <QuestionForm
            questionId={view().question!.id}
            text={view().question!.text}
            options={view().question!.options ?? []}
            freeText={view().question!.freeText}
          />
        </div>
      </Show>

      <Props />
      <Assignee />

      <div class="panel">
        <h3>История</h3>
        <NewComment />
        <div class="comments">
          <For each={[...view().comments].reverse()}>
            {(c) => (
              <div class="comment">
                <div class="who">{c.author || "система"} · {when(c.createdAt)}</div>
                <div class="text">{c.text}</div>
              </div>
            )}
          </For>
        </div>
        <Show when={view().comments.length === 0}>
          <div class="empty">Пока ничего не происходило.</div>
        </Show>
      </div>
    </div>
  );
}

/** Props are how a person answers a waiting stage: setting one is the event a
 *  card.changed edge is listening for. */
function Props() {
  const view = () => openCard()!;
  const [name, setName] = createSignal("");
  const [value, setValue] = createSignal("");

  const set = async (n: string, v: string) => applyCard(await guard(() => API.SetProp(view().card.id, n, v)));

  const add = async () => {
    if (!name().trim()) return;
    await set(name(), value());
    setName(""); setValue("");
  };

  // The values the current stage is waiting for, offered as buttons: a person
  // answering a stage should not have to retype what the flow already names.
  const awaited = () => {
    const flow = view().flow;
    if (!flow) return [] as { property: string; value: string }[];
    const out: { property: string; value: string }[] = [];
    for (const wait of flow.waitingFor ?? []) {
      const m = wait.match(/«([^»]+)»\s*=\s*«([^»]+)»/);
      if (m) out.push({ property: m[1], value: m[2] });
    }
    return out;
  };

  return (
    <div class="panel">
      <h3>Свойства</h3>

      <Show when={awaited().length > 0}>
        <div class="row wrap" style={{ "margin-bottom": "10px" }}>
          <For each={awaited()}>
            {(a) => (
              <button class="btn primary" onClick={() => set(a.property, a.value)}>
                {a.property}: {a.value}
              </button>
            )}
          </For>
        </div>
      </Show>

      <div class="props">
        <For each={Object.entries(view().card.props ?? {})}>
          {([n, v]) => (
            <>
              <span class="name">{n}</span>
              <input type="text" value={v} onChange={(e) => set(n, e.currentTarget.value)} />
              <button class="btn quiet" title="Убрать" onClick={() => set(n, "")}>×</button>
            </>
          )}
        </For>
      </div>

      <div class="row" style={{ "margin-top": "10px" }}>
        <input type="text" placeholder="свойство" value={name()} onInput={(e) => setName(e.currentTarget.value)} />
        <input type="text" placeholder="значение" value={value()} onInput={(e) => setValue(e.currentTarget.value)} />
        <button class="btn" onClick={add}>Задать</button>
      </div>
    </div>
  );
}

/** Assignee is who the card is for. An agent's name means "let this agent work
 *  it"; anything else means a person took it, and then no agent starts. */
function Assignee() {
  const view = () => openCard()!;
  const set = async (who: string) =>
    applyCard(await guard(() => API.SetAssignee(view().card.id, who)));

  return (
    <div class="panel">
      <h3>Исполнитель</h3>
      <div class="row wrap">
        <select value={view().card.assignee ?? ""} onChange={(e) => set(e.currentTarget.value)}
                style={{ width: "auto" }}>
          <option value="">не назначен</option>
          <For each={agents().agents}>{(a) => <option value={a.name}>{a.name}</option>}</For>
        </select>
        <input type="text" placeholder="или имя человека"
               value={isAgent(view().card.assignee ?? "") ? "" : (view().card.assignee ?? "")}
               onChange={(e) => set(e.currentTarget.value)} />
      </div>
      <div class="meta" style={{ "margin-top": "6px" }}>
        Имя агента — «пусть работает он». Имя человека — карточку взяли, и агент на ней не запускается.
      </div>
    </div>
  );
}

function isAgent(name: string): boolean {
  return agents().agents.some((a) => a.name.toLowerCase() === name.toLowerCase());
}

function NewComment() {
  const view = () => openCard()!;
  const [text, setText] = createSignal("");

  const send = async () => {
    if (!text().trim()) return;
    applyCard(await guard(() => API.AddComment(view().card.id, text())));
    setText("");
  };

  return (
    <div class="row" style={{ "margin-bottom": "8px" }}>
      <input type="text" placeholder="Заметка" value={text()}
             onInput={(e) => setText(e.currentTarget.value)}
             onKeyDown={(e) => { if (e.key === "Enter") void send(); }} />
      <button class="btn" onClick={send}>Записать</button>
    </div>
  );
}

function when(value: any): string {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}
