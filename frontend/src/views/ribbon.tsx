import {
  createEffect, createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch, type JSX,
} from "solid-js";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { RibbonView, ScreenView, Segment } from "../../bindings/github.com/artipop/xxvi/internal/engine/models";
import type { SessionEvent } from "../../bindings/github.com/artipop/xxvi/internal/store/models";
import { guard, list, openRibbon, report, ribbons, setOpenRibbon, setTab } from "../state";
import { NAV } from "../nav";

// The ribbon is the card's journal of transitions made visible: one segment per
// entry onto a stage, and the screens of that stage inside it. Nothing here
// assembles the strip — it is read whole from the backend — so it cannot
// disagree with where the card actually stands.
//
// Two axes, and they mean different things. Sideways is one card's steps, left
// to right in the order they happened. Up and down is between cards: every card
// in work is its own strip, stacked, and moving between them is moving between
// jobs rather than between steps of one.
//
// Everything is rendered straight off the store's own objects rather than off a
// flattened copy of them. That is not a matter of style: the stack re-reads on
// every event, `reconcile` keeps the objects that did not change, and `For`
// tells panes apart by reference — build new wrappers and every pane is new,
// the iframe of a running preview reloads and the cursor jumps out of the notes
// on every step the agent takes.

// How wide a screen is, as a share of the strip. A column that a person can
// widen is the difference between reading a terminal and squinting at one; the
// steps are Niri's, and they are steps rather than a drag because a column that
// lands on the same widths every time is a column you stop thinking about.
const WIDTHS = [0.34, 0.5, 0.67, 1];
const DEFAULT_WIDTH = 2; // two thirds: wide enough for a page, narrow enough to see the next step

/** motion is one behaviour, asked once: the strip is the only thing that moves. */
function motion(): ScrollBehavior {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
}

/** emptyID is what a segment with nothing to show answers to. */
function emptyID(segment: Segment): string {
  return segment.id + "|empty";
}

function paneIDs(view: RibbonView): string[] {
  return list(view.segments).flatMap((segment) => {
    const screens = list(segment.screens);
    return screens.length === 0 ? [emptyID(segment)] : screens.map((s) => s.id);
  });
}

export default function Ribbon(): JSX.Element {
  // Where the person is: which strip, which pane of it, and whether they put
  // themselves there. Somebody who moved on their own is not dragged along by
  // the next step — the ribbon offers instead of yanking, the same way the
  // graph never overrules a person.
  // Focus is kept per ribbon, not once for the whole stack: coming back to a
  // job should put you where you left it, and a single focus would drag every
  // strip to the same column. Without a remembered one, the current step is
  // where a strip opens.
  const [focusAt, setFocusAt] = createSignal<Record<string, string>>({});
  const [pinned, setPinned] = createSignal(false);
  // Which ribbons have moved on since the person last looked at them. A step
  // finishing on a strip somebody is not watching is worth a mark, not a jump.
  const [moved, setMoved] = createSignal<Record<string, boolean>>({});
  const [widths, setWidths] = createSignal<Record<string, number>>({});

  const [menu, setMenu] = createSignal(false);

  let stack: HTMLDivElement | undefined;
  const flown: Record<string, string> = {};

  const current = () => ribbons.find((r) => r.id === openRibbon());

  const focus = () => focusAt()[openRibbon()] ?? current()?.focusId ?? "";
  const setFocus = (id: string) => setFocusAt((f) => ({ ...f, [openRibbon()]: id }));

  const paneEl = (id: string) =>
    stack?.querySelector<HTMLElement>('[data-pane="' + CSS.escape(id) + '"]') ?? undefined;

  const flyTo = (id: string) => {
    if (!id) return;
    const el = paneEl(id);
    if (!el) return;
    el.scrollIntoView({ behavior: motion(), inline: "center", block: "nearest" });
    setFocus(id);
  };

  const flyToRibbon = (cardID: string) => {
    const el = stack?.querySelector<HTMLElement>('[data-ribbon="' + CSS.escape(cardID) + '"]');
    el?.scrollIntoView({ behavior: motion(), block: "start" });
    setOpenRibbon(cardID);
    setMoved((m) => ({ ...m, [cardID]: false }));
  };

  // A card moved, so its strip has a new current segment. Flying there is the
  // one movement in this application, and it carries meaning: a screen that
  // appeared instantly at a new scroll position leaves no way to know you
  // moved. A strip nobody is watching is marked instead — jumping somebody to
  // another job because it finished a step would be the application deciding
  // what they are doing.
  createEffect(() => {
    for (const view of ribbons) {
      const target = view.focusId ?? "";
      if (!target || flown[view.id] === target) continue;
      const first = flown[view.id] === undefined;
      flown[view.id] = target;
      if (first) continue;
      if (view.id !== openRibbon()) {
        setMoved((m) => ({ ...m, [view.id]: true }));
        continue;
      }
      if (pinned()) continue;
      queueMicrotask(() => flyTo(target));
    }
  });

  // With nothing open, open the first one there is: arriving at an empty screen
  // beside a stack of ribbons would be asking a question with one answer.
  createEffect(() => {
    const all = ribbons;
    if (all.length === 0) return;
    if (!all.some((r) => r.id === openRibbon())) setOpenRibbon(all[0].id);
  });

  // Moving to another job is arriving at it, not carrying the last one's
  // decisions along: whatever was pinned was pinned about a different strip.
  createEffect(() => {
    openRibbon();
    setPinned(false);
  });

  // Scrolling is a way of choosing too: the strip filling the screen is the one
  // the person is on, however they got there.
  onMount(() => {
    if (!stack) return;
    const seen = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          const id = (e.target as HTMLElement).dataset.ribbon ?? "";
          if (id && id !== openRibbon()) {
            setOpenRibbon(id);
            setMoved((m) => ({ ...m, [id]: false }));
          }
        }
      },
      { root: stack, threshold: 0.6 },
    );
    // Observing is re-run whenever the stack changes shape, which is cheap and
    // spares keeping a second list of the elements in it.
    const watch = () => {
      seen.disconnect();
      stack!.querySelectorAll<HTMLElement>("[data-ribbon]").forEach((el) => seen.observe(el));
    };
    watch();
    const shape = new MutationObserver(watch);
    shape.observe(stack, { childList: true });
    onCleanup(() => { seen.disconnect(); shape.disconnect(); });
  });

  const stepPane = (delta: number) => {
    const view = current();
    if (!view) return;
    const all = paneIDs(view);
    if (all.length === 0) return;
    const at = all.indexOf(focus());
    setPinned(true);
    flyTo(all[at < 0 ? 0 : Math.min(all.length - 1, Math.max(0, at + delta))]);
  };

  const stepRibbon = (delta: number) => {
    const all = ribbons;
    if (all.length === 0) return;
    const at = all.findIndex((r) => r.id === openRibbon());
    setPinned(false);
    flyToRibbon(all[(at + delta + all.length) % all.length].id);
  };

  const widthOf = (id: string) => widths()[id] ?? DEFAULT_WIDTH;

  const resize = (to: (at: number) => number) => {
    const id = focus();
    if (!id) return;
    setWidths((w) => ({ ...w, [id]: to(widthOf(id)) }));
    queueMicrotask(() => paneEl(id)?.scrollIntoView({ behavior: motion(), inline: "center", block: "nearest" }));
  };

  // Keys are listened for on the window rather than on the strip: the ribbon is
  // the whole screen here, and a person who clicked into a terminal should
  // still be able to leave it.
  const onKeyDown = (e: KeyboardEvent) => {
    const target = e.target as HTMLElement | null;
    if (target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)) {
      if (e.key === "Escape") target.blur();
      return;
    }
    if (e.metaKey || e.altKey) return;

    switch (e.key) {
      case "ArrowLeft": e.preventDefault(); stepPane(-1); break;
      case "ArrowRight": e.preventDefault(); stepPane(1); break;
      case "ArrowUp": e.preventDefault(); stepRibbon(-1); break;
      case "ArrowDown": e.preventDefault(); stepRibbon(1); break;
      case "Home": {
        const view = current();
        if (!view) break;
        e.preventDefault(); setPinned(true); flyTo(paneIDs(view)[0]);
        break;
      }
      case "End": {
        const view = current();
        if (!view) break;
        const all = paneIDs(view);
        e.preventDefault(); setPinned(true); flyTo(all[all.length - 1]);
        break;
      }
      case "r": case "R": case "к": case "К":
        e.preventDefault(); resize((at) => (at + 1) % WIDTHS.length); break;
      case "f": case "F": case "а": case "А":
        e.preventDefault();
        resize((at) => (at === WIDTHS.length - 1 ? DEFAULT_WIDTH : WIDTHS.length - 1));
        break;
      case "Escape":
        e.preventDefault();
        menu() ? setMenu(false) : setTab("inbox");
        break;
    }
  };

  onMount(() => {
    window.addEventListener("keydown", onKeyDown);
    onCleanup(() => window.removeEventListener("keydown", onKeyDown));
  });

  // The strip has been left behind: the card is somewhere else and the person
  // asked to stay. Offered, not taken.
  const behind = () => {
    const view = current();
    return pinned() && !!view?.focusId && focus() !== view.focusId;
  };

  return (
    <div class="ribbon">
      {/* The only chrome: room for the window's own buttons, the name of the
          job in front of you, and the one offer the ribbon ever makes. */}
      <header class="ribbon-bar" style={{ "--wails-draggable": "drag" }}>
        <Sections open={menu()} setOpen={setMenu} />
        <span class="ribbon-where">
          {current()?.title}
          <Show when={current()?.stageName}>
            <span class="ribbon-stage"> · {current()!.stageName}</span>
          </Show>
        </span>
        <div class="spacer" />
        <Show when={behind()}>
          {/* The bar is the window's drag handle, and a button inside one has
              to say it is not: dragging the window from a button is not what
              pressing it means. */}
          <button
            class="btn primary tiny"
            style={{ "--wails-draggable": "no-drag" }}
            onClick={() => { setPinned(false); flyTo(current()!.focusId ?? ""); }}
          >
            Дальше →
          </button>
        </Show>
      </header>

      <Show
        when={ribbons.length > 0}
        fallback={
          <div class="ribbon-blank">
            Ни одна карточка не в работе.
            <br />Нажмите «Сделай» во входящих — и здесь появится её лента.
            <br /><span class="meta">Esc — назад во входящие</span>
          </div>
        }
      >
        <div class="stack" ref={stack}>
          <For each={ribbons}>
            {(view) => (
              <section class="workspace" data-ribbon={view.id}>
                <div class="strip">
                  <For each={list(view.segments)}>
                    {(segment) => (
                      <>
                        {/* A segment with nothing to show still gets a pane.
                            Every finished step is a step somebody may want to
                            look at, and a step that contributes nothing to the
                            strip is a step that looks like it never happened. */}
                        <Show when={list(segment.screens).length === 0}>
                          <Pane
                            segment={segment}
                            id={emptyID(segment)}
                            title={segment.stageName}
                            first
                            width={widthOf(emptyID(segment))}
                            focused={focus() === emptyID(segment)}
                            onFocus={() => { setPinned(true); setFocus(emptyID(segment)); }}
                          />
                        </Show>
                        <For each={list(segment.screens)}>
                          {(screen, i) => (
                            <Pane
                              segment={segment}
                              screen={screen}
                              cardId={view.cardId}
                              id={screen.id}
                              title={screen.title}
                              first={i() === 0}
                              width={widthOf(screen.id)}
                              focused={focus() === screen.id}
                              onFocus={() => { setPinned(true); setFocus(screen.id); }}
                            />
                          )}
                        </For>
                      </>
                    )}
                  </For>
                </div>
              </section>
            )}
          </For>
        </div>

        {/* Where you are in the stack, and which other jobs moved while you
            were not looking. One job needs no map of itself. */}
        <Show when={ribbons.length > 1}>
        <nav class="rail">
          <For each={ribbons}>
            {(view) => (
              <button
                class={`rail-dot ${view.id === openRibbon() ? "on" : ""} ${moved()[view.id] ? "moved" : ""} ${view.running ? "run" : ""}`}
                title={`${view.title} · ${view.flowName} · ${view.stageName}`}
                onClick={() => flyToRibbon(view.id)}
              />
            )}
          </For>
        </nav>
        </Show>
      </Show>
    </div>
  );
}

// The ribbon has no sidebar, so the way out of it is a chevron: the sections
// are still one list (see nav.ts), just folded away until asked for. A bar with
// seven buttons in it would be the sidebar again, lying down.
function Sections(props: { open: boolean; setOpen: (v: boolean) => void }): JSX.Element {
  let box: HTMLDivElement | undefined;

  // Clicking anywhere else is an answer too — «not this», and a menu that
  // needs to be dismissed on its own terms is a menu in the way.
  onMount(() => {
    const away = (e: MouseEvent) => {
      if (box && !box.contains(e.target as Node)) props.setOpen(false);
    };
    document.addEventListener("mousedown", away);
    onCleanup(() => document.removeEventListener("mousedown", away));
  });

  return (
    <div class="sections" ref={box} style={{ "--wails-draggable": "no-drag" }}>
      <button class="chevron" onClick={() => props.setOpen(!props.open)} title="Разделы (Esc — во входящие)">
        XXVI <span class={`caret ${props.open ? "up" : ""}`}>⌄</span>
      </button>
      <Show when={props.open}>
        <div class="menu">
          <For each={NAV}>
            {(item) => (
              <>
                <Show when={item.apart}><hr /></Show>
                <button
                  class={item.tab === "ribbon" ? "on" : ""}
                  onClick={() => { props.setOpen(false); setTab(item.tab); }}
                >
                  <span>{item.label}</span>
                  <Show when={item.count && item.count()! > 0}>
                    <span class={`count ${item.alert ? "alert" : ""}`}>{item.count!()}</span>
                  </Show>
                </button>
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
  cardId?: string;
  id: string;
  title: string;
  first: boolean;
  width: number;
  focused: boolean;
  onFocus: () => void;
}): JSX.Element {
  const waiting = () => list(props.screen?.waiting);

  return (
    <section
      class={`screen ${props.focused ? "on" : ""}`}
      style={{ "flex-basis": `calc(100% * ${WIDTHS[props.width] ?? WIDTHS[DEFAULT_WIDTH]})` }}
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
          <a class="btn quiet tiny" href={props.screen!.ref} target="_blank" rel="noreferrer" title="Открыть снаружи">↗</a>
        </Show>
      </header>

      <div class="screen-body">
        <Switch fallback={<Body screen={props.screen!} cardId={props.cardId ?? ""} />}>
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

function Body(props: { screen: ScreenView; cardId: string }): JSX.Element {
  return (
    <Switch fallback={<div class="screen-note">Неизвестный вид экрана «{props.screen.kind}».</div>}>
      <Match when={props.screen.kind === "agent"}>
        <AgentPane sessionId={props.screen.sessionId ?? ""} />
      </Match>
      <Match when={props.screen.kind === "notes"}>
        <NotesPane cardId={props.cardId} path={props.screen.ref ?? ""} />
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
        <button class="btn quiet tiny" onClick={() => setNonce((n) => n + 1)} title="Обновить">↻</button>
      </div>
      {/* Reloading is asked for, never incidental: the frame is rebuilt only
          when the button says so. Numbers compare by value, so a re-read of the
          stack leaves the page a person was looking at alone. */}
      <For each={[nonce()]}>
        {() => <iframe class="browser-frame" src={props.url} referrerpolicy="no-referrer" />}
      </For>
    </div>
  );
}
