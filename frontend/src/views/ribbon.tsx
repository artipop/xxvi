import {
  createEffect, createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch, type JSX,
} from "solid-js";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { ScreenView, Segment } from "../../bindings/github.com/artipop/xxvi/internal/engine/models";
import type { SessionEvent } from "../../bindings/github.com/artipop/xxvi/internal/store/models";
import { guard, list, openRibbon, report, ribbon, ribbons, showRibbon } from "../state";

// The ribbon is the card's journal of transitions made visible: one segment per
// entry onto a stage, and the screens of that stage inside it. Nothing here
// assembles the strip — it is read whole from the backend — so it cannot
// disagree with where the card actually stands.
//
// Everything is rendered straight off the store's own objects rather than off a
// flattened copy of them. That is not a matter of style: the strip re-reads on
// every event, `reconcile` keeps the objects that did not change, and `For`
// tells panes apart by reference — build a new array of wrappers and every pane
// is new, the iframe of a running preview reloads and the cursor jumps out of
// the notes on every step the agent takes.

/** motion is one behaviour, asked once: the strip is the only thing that moves. */
function motion(): ScrollBehavior {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
}

/** emptyID is what a segment with nothing to show answers to. */
function emptyID(segment: Segment): string {
  return segment.id + "|empty";
}

export default function RibbonView(): JSX.Element {
  // Which pane the person is on, and whether they put themselves there. A
  // person who moved on their own is not dragged along by the next step: the
  // ribbon offers instead of yanking, the same way the graph never overrules a
  // person.
  const [focus, setFocus] = createSignal("");
  const [pinned, setPinned] = createSignal(false);
  let strip: HTMLDivElement | undefined;
  let flownTo = "";

  // Only the ids are flattened — a list of strings for the arrow keys, and
  // rebuilding it costs nothing because nothing is rendered from it.
  const paneIDs = createMemo(() =>
    list(ribbon.segments).flatMap((segment) => {
      const screens = list(segment.screens);
      return screens.length === 0 ? [emptyID(segment)] : screens.map((s) => s.id);
    }),
  );

  const flyTo = (id: string, behavior: ScrollBehavior = motion()) => {
    if (!id) return;
    const el = strip?.querySelector<HTMLElement>('[data-pane="' + CSS.escape(id) + '"]');
    if (!el) return;
    el.scrollIntoView({ behavior, inline: "center", block: "nearest" });
    setFocus(id);
  };

  // The card moved, so the strip has a new current segment. Flying there is the
  // one movement in this application, and it carries meaning: a screen that
  // appeared instantly at a new scroll position leaves no way to know you moved.
  createEffect(() => {
    const target = ribbon.focusId ?? "";
    if (!target || target === flownTo) return;
    flownTo = target;
    if (pinned()) return;
    queueMicrotask(() => flyTo(target));
  });

  // Switching ribbons is switching strips: the new one starts at its own
  // current step, and nothing is pinned yet.
  createEffect(() => {
    openRibbon();
    setPinned(false);
    flownTo = "";
  });

  // With nothing open, open the first one there is: arriving at an empty screen
  // beside a list of ribbons would be asking a question with one answer.
  createEffect(() => {
    if (!openRibbon() && ribbons().length > 0) showRibbon(ribbons()[0].cardId);
  });

  const step = (delta: number) => {
    const all = paneIDs();
    if (all.length === 0) return;
    const at = all.indexOf(focus());
    const next = at < 0 ? 0 : Math.min(all.length - 1, Math.max(0, at + delta));
    setPinned(true);
    flyTo(all[next]);
  };

  const switchRibbon = (delta: number) => {
    const all = ribbons();
    if (all.length === 0) return;
    const at = all.findIndex((r) => r.cardId === openRibbon());
    showRibbon(all[(at + delta + all.length) % all.length].cardId);
  };

  const onKeyDown = (e: KeyboardEvent) => {
    // A person typing in the notes is typing, not steering.
    const target = e.target as HTMLElement | null;
    if (target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)) return;
    if (e.metaKey || e.altKey) return;

    if (e.ctrlKey) {
      if (e.key === "ArrowLeft") { e.preventDefault(); switchRibbon(-1); }
      if (e.key === "ArrowRight") { e.preventDefault(); switchRibbon(1); }
      return;
    }
    const all = paneIDs();
    switch (e.key) {
      case "ArrowLeft": e.preventDefault(); step(-1); break;
      case "ArrowRight": e.preventDefault(); step(1); break;
      case "Home": e.preventDefault(); setPinned(true); flyTo(all[0]); break;
      case "End": e.preventDefault(); setPinned(true); flyTo(all[all.length - 1]); break;
    }
  };

  // The strip has been left behind: the card is somewhere else and the person
  // asked to stay. Offered, not taken.
  const behind = () => pinned() && !!ribbon.focusId && focus() !== ribbon.focusId;

  return (
    <div class="ribbon">
      <header class="ribbon-head">
        <div class="ribbons">
          <For each={ribbons()}>
            {(r) => (
              <button
                class={`ribbon-tab ${r.cardId === openRibbon() ? "on" : ""}`}
                onClick={() => showRibbon(r.cardId)}
                title={`${r.flowName} · ${r.stageName}`}
              >
                <span class={`dot ${r.running ? "run" : ""}`} />
                <span class="ribbon-name">{r.title}</span>
              </button>
            )}
          </For>
        </div>
        <div class="spacer" />
        <Show when={behind()}>
          <button class="btn primary" onClick={() => { setPinned(false); flyTo(ribbon.focusId ?? ""); }}>
            Дальше →
          </button>
        </Show>
      </header>

      <Show
        when={openRibbon() && paneIDs().length > 0}
        fallback={
          <div class="empty" style={{ padding: "0 24px" }}>
            {ribbons().length === 0
              ? "Ни одна карточка не в работе. Нажмите «Сделай» во входящих — и здесь появится её лента."
              : "Выберите ленту."}
          </div>
        }
      >
        <div class="strip" ref={strip} tabindex="0" onKeyDown={onKeyDown}>
          <For each={list(ribbon.segments)}>
            {(segment) => (
              <>
                {/* A segment with nothing to show still gets a pane. Every
                    finished step is a step somebody may want to look at, and a
                    step that contributes nothing to the strip is a step that
                    looks like it never happened. */}
                <Show when={list(segment.screens).length === 0}>
                  <Pane
                    segment={segment}
                    id={emptyID(segment)}
                    title={segment.stageName}
                    first
                    focused={focus() === emptyID(segment)}
                    onFocus={() => { setPinned(true); setFocus(emptyID(segment)); }}
                  />
                </Show>
                <For each={list(segment.screens)}>
                  {(screen, i) => (
                    <Pane
                      segment={segment}
                      screen={screen}
                      id={screen.id}
                      title={screen.title}
                      first={i() === 0}
                      focused={focus() === screen.id}
                      onFocus={() => { setPinned(true); setFocus(screen.id); }}
                    />
                  )}
                </For>
              </>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

function Pane(props: {
  segment: Segment;
  screen?: ScreenView;
  id: string;
  title: string;
  first: boolean;
  focused: boolean;
  onFocus: () => void;
}): JSX.Element {
  const waiting = () => list(props.screen?.waiting);

  return (
    <section
      class={`screen ${props.focused ? "on" : ""}`}
      data-pane={props.id}
      onMouseDown={props.onFocus}
    >
      <header class="screen-head">
        <Show when={props.first}>
          <span class={`tag ${props.segment.current ? "accent" : ""}`}>{props.segment.stageName}</span>
        </Show>
        <span class="screen-title">{props.title}</span>
        <div class="spacer" />
        <Show when={props.screen?.kind === "browser" && waiting().length === 0}>
          <a class="btn quiet" href={props.screen!.ref} target="_blank" rel="noreferrer" title="Открыть снаружи">↗</a>
        </Show>
      </header>

      <div class="screen-body">
        <Switch fallback={<Body screen={props.screen!} />}>
          {/* A stage removed from the flow does not take its part of the ribbon
              with it: what happened happened, and the segment says why it is
              empty. */}
          <Match when={props.segment.gone}>
            <div class="screen-note">
              Стадия «{props.segment.stageId}» убрана из флоу. Шаг остаётся в ленте.
            </div>
          </Match>
          <Match when={!props.screen}>
            <div class="screen-note">Этот шаг ничего не показывает.</div>
          </Match>
          {/* A screen whose address is not on the card yet says which property
              it is waiting for. Opening a blank page and staying silent would be
              the same screen with the reason taken out. */}
          <Match when={waiting().length > 0}>
            <div class="screen-note">
              Ждёт, пока стадия запишет на карточку {waiting().map((n) => `«${n}»`).join(", ")}.
            </div>
          </Match>
        </Switch>
      </div>
    </section>
  );
}

function Body(props: { screen: ScreenView }): JSX.Element {
  return (
    <Switch fallback={<div class="screen-note">Неизвестный вид экрана «{props.screen.kind}».</div>}>
      <Match when={props.screen.kind === "agent"}>
        <AgentPane sessionId={props.screen.sessionId ?? ""} />
      </Match>
      <Match when={props.screen.kind === "notes"}>
        <NotesPane cardId={ribbon.cardId} path={props.screen.ref ?? ""} />
      </Match>
      <Match when={props.screen.kind === "browser"}>
        <BrowserPane url={props.screen.ref ?? ""} />
      </Match>
      <Match when={props.screen.kind === "terminal"}>
        <div class="screen-note">
          Терминал: <span class="mono">{props.screen.ref || "шелл в папке карточки"}</span>.
          <br />Экран появится, когда терминалы будут включены.
        </div>
      </Match>
    </Switch>
  );
}

// ---- what the agent did ----

// The stream is read from the same rows the card's history is made of, and it
// is asked for again whenever the session says something — the event carries no
// content, so the screen asks, like every other screen here.
function AgentPane(props: { sessionId: string }): JSX.Element {
  const [events, setEvents] = createSignal<SessionEvent[]>([]);
  let seq = 0;
  let box: HTMLDivElement | undefined;

  const pull = async () => {
    if (!props.sessionId) return;
    const more = await guard(() => API.SessionEvents(props.sessionId, seq));
    const rows = list(more);
    if (rows.length === 0) return;
    seq = rows[rows.length - 1].seq;
    // Following the stream is what a running session is for; a person who
    // scrolled up is reading, and is left where they are.
    const following = !box || box.scrollHeight - box.scrollTop - box.clientHeight < 80;
    setEvents((have) => [...have, ...rows]);
    if (following && box) queueMicrotask(() => { box!.scrollTop = box!.scrollHeight; });
  };

  onMount(() => {
    void pull();
    const off = Events.On("session", () => { void pull(); });
    onCleanup(() => { if (typeof off === "function") off(); });
  });

  return (
    <div class="stream" ref={box}>
      <Show when={events().length > 0} fallback={<div class="screen-note">Агент ещё ничего не сказал.</div>}>
        <For each={events()}>{(e) => <StreamLine event={e} />}</For>
      </Show>
    </div>
  );
}

function StreamLine(props: { event: SessionEvent }): JSX.Element {
  const data = createMemo<Record<string, any>>(() => {
    try { return JSON.parse(props.event.payload || "{}"); } catch { return {}; }
  });

  return (
    <Switch fallback={<p class="stream-tool dim">{props.event.kind}</p>}>
      <Match when={props.event.kind === "chunk"}>
        <p class="stream-say">{data().text}</p>
      </Match>
      <Match when={props.event.kind === "thought"}>
        <p class="stream-think">{data().text}</p>
      </Match>
      <Match when={props.event.kind === "tool_call"}>
        <p class="stream-tool">
          <span class="tag">{data().status || "вызов"}</span> {data().title || data().toolCallId}
        </p>
      </Match>
      <Match when={props.event.kind === "tool_update"}>
        <p class="stream-tool dim">{data().toolCallId}: {data().status}</p>
      </Match>
      <Match when={props.event.kind === "permission"}>
        <p class="stream-tool">
          <span class={`tag ${data().decision === "allow_once" ? "ok" : "warn"}`}>доступ</span>{" "}
          {data().tool}: {data().decision}
        </p>
      </Match>
      <Match when={props.event.kind === "question"}>
        <p class="stream-tool"><span class="tag warn">вопрос</span> {data().text}</p>
      </Match>
    </Switch>
  );
}

// ---- notes ----

// The file is in the card's working folder — the same folder the agent works in
// — so a plan it wrote is the file a person edits rather than a copy of it.
function NotesPane(props: { cardId: string; path: string }): JSX.Element {
  const [text, setText] = createSignal("");
  const [saved, setSaved] = createSignal(true);
  let timer: number | undefined;

  onMount(async () => {
    const have = await guard(() => API.ReadDoc(props.cardId, props.path));
    if (have !== undefined) setText(have);
  });
  onCleanup(() => { if (timer) clearTimeout(timer); });

  const edit = (value: string) => {
    setText(value);
    setSaved(false);
    if (timer) clearTimeout(timer);
    timer = window.setTimeout(() => {
      API.WriteDoc(props.cardId, props.path, value)
        .then(() => setSaved(true))
        .catch(report);
    }, 400);
  };

  return (
    <div class="notes">
      <textarea
        class="notes-text"
        value={text()}
        onInput={(e) => edit(e.currentTarget.value)}
        spellcheck={false}
      />
      <div class="meta">{props.path} · {saved() ? "сохранено" : "…"}</div>
    </div>
  );
}

// ---- browser ----

// An iframe rather than a second webview: a Wails window holds exactly one, so
// the panes are the page rather than windows inside it.
function BrowserPane(props: { url: string }): JSX.Element {
  const [nonce, setNonce] = createSignal(0);
  return (
    <div class="browser">
      <div class="browser-bar row">
        <span class="mono">{props.url}</span>
        <div class="spacer" />
        <button class="btn quiet" onClick={() => setNonce((n) => n + 1)} title="Обновить">↻</button>
      </div>
      {/* Reloading is asked for, never incidental: the frame is rebuilt only
          when the button says so. Numbers compare by value, so a re-read of the
          strip leaves the page a person was looking at alone. */}
      <For each={[nonce()]}>
        {() => <iframe class="browser-frame" src={props.url} referrerpolicy="no-referrer" />}
      </For>
    </div>
  );
}
