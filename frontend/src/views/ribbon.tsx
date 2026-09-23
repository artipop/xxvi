import {
  createEffect, createMemo, createSignal, For, lazy, Loading, Match, onSettled, Show, Switch,
} from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { RibbonView, ScreenView, Segment } from "../../bindings/github.com/artipop/xxvi/internal/engine/models";
import type { SessionEvent } from "../../bindings/github.com/artipop/xxvi/internal/store/models";
import { attention, closedRibbon, guard, list, loadAttention, openRibbon, report, ribbons, setOpenRibbon, setTab } from "../state";
import { QuestionForm } from "./attention";
import { NAV } from "../nav";

// The emulator is a large chunk and most screens are not terminals, so it
// arrives only when one is opened.
const TerminalPane = lazy(() => import("./terminal"));

// The same reasoning for the diff: a card whose stage shows no diff never pays
// for the viewer.
const DiffPane = lazy(() => import("./diff"));

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
// A plain scroll is never ours. It belongs to whatever is under the pointer —
// the page in the preview, the agent's stream, the terminal — and taking it to
// move the view is how a ribbon ends up fighting the window inside it. This is
// niri's own rule: every one of its navigation binds is Mod+something, its
// wheel binds are Mod+Wheel with a cooldown, and focus-follows-mouse is off by
// default, so a bare scroll always reaches the application. Moving the view is
// a separate, deliberate gesture here too.
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
  // Which pane has taken the keyboard: a preview or a note that a person
  // clicked into. Worth saying out loud, because from inside a preview the
  // ribbon cannot hear a key at all — the page has it — and a person pressing
  // arrows at a window that is not listening deserves to be told why.
  const [captured, setCaptured] = createSignal("");

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

  // The stack is moved by index rather than by asking an element to bring
  // itself into view: every ribbon is exactly the stack's own height, so the
  // n-th one starts at n heights, and arithmetic cannot land between two of
  // them the way a scroll can.
  const flyToRibbon = (cardID: string) => {
    const at = ribbons.findIndex((r) => r.id === cardID);
    if (at >= 0 && stack) {
      stack.scrollTo({ top: at * stack.clientHeight, behavior: motion() });
    }
    setOpenRibbon(cardID);
    setMoved((m) => ({ ...m, [cardID]: false }));
  };

  // A card moved, so its strip has a new current segment. Flying there is the
  // one movement in this application, and it carries meaning: a screen that
  // appeared instantly at a new scroll position leaves no way to know you
  // moved. A strip nobody is watching is marked instead — jumping somebody to
  // another job because it finished a step would be the application deciding
  // what they are doing.
  //
  // Keyed on where each strip's current step *is*, and on nothing else: which
  // strip is open and whether it is pinned are read when a step has actually
  // moved, not as reasons to look again.
  createEffect(
    () => ribbons.map((view) => [view.id, view.focusId ?? ""] as const),
    (targets) => {
      for (const [id, target] of targets) {
        if (!target || flown[id] === target) continue;
        const first = flown[id] === undefined;
        flown[id] = target;
        if (first) continue;
        if (id !== openRibbon()) {
          setMoved((m) => ({ ...m, [id]: true }));
          continue;
        }
        if (pinned()) continue;
        queueMicrotask(() => flyTo(target));
      }
    },
  );

  // With nothing open, open the first one there is: arriving at an empty screen
  // beside a stack of ribbons would be asking a question with one answer.
  createEffect(
    () => ribbons.map((r) => r.id),
    (ids) => {
      if (ids.length === 0) return;
      // A closed card asked for is on its way into the stack, not missing
      // from it: the next read brings it.
      if (!ids.includes(openRibbon()) && openRibbon() !== closedRibbon()) setOpenRibbon(ids[0]);
    },
  );

  // Asked for from outside — «Сделай», «Лента →» on a closed card — a strip is
  // opened by name, and the stack has to be standing on it. Keyed on where it
  // is in the stack as well, because the one asked for may arrive a read later.
  createEffect(
    () => ribbons.findIndex((r) => r.id === openRibbon()),
    (at) => {
      if (at < 0 || !stack || stack.clientHeight === 0) return;
      if (Math.round(stack.scrollTop / stack.clientHeight) !== at) {
        queueMicrotask(() => stack?.scrollTo({ top: at * stack.clientHeight }));
      }
    },
  );

  // Moving to another job is arriving at it, not carrying the last one's
  // decisions along: whatever was pinned was pinned about a different strip.
  createEffect(openRibbon, () => {
    setPinned(false);
  });

  // Who has the keyboard. Focus moving into a preview blurs the window itself,
  // which is the only way to notice it from out here; everything else in the
  // ribbon is an element of this document and says so directly.
  onSettled(() => {
    const note = () => {
      const el = document.activeElement as HTMLElement | null;
      const inside = el && /^(IFRAME|TEXTAREA|INPUT)$/.test(el.tagName)
        ? (el.closest("[data-pane]") as HTMLElement | null)?.dataset.pane ?? ""
        : "";
      setCaptured(inside);
    };
    const later = () => queueMicrotask(note);
    document.addEventListener("focusin", note);
    document.addEventListener("focusout", later);
    window.addEventListener("blur", note);
    window.addEventListener("focus", note);
    return () => {
      document.removeEventListener("focusin", note);
      document.removeEventListener("focusout", later);
      window.removeEventListener("blur", note);
      window.removeEventListener("focus", note);
    };
  });

  // A wheel with the modifier held moves between jobs; a wheel without it is
  // not addressed to us and is left alone. The cooldown is niri's: without one
  // a single flick carries through several ribbons and lands nowhere in
  // particular.
  let lastWheel = 0;
  const onWheel = (e: WheelEvent) => {
    if (!(e.metaKey || e.ctrlKey)) return;
    if (Math.abs(e.deltaY) < Math.abs(e.deltaX)) return;
    e.preventDefault();
    const now = e.timeStamp;
    if (now - lastWheel < 150) return;
    lastWheel = now;
    stepRibbon(e.deltaY > 0 ? 1 : -1);
  };

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

  // Taking the keyboard back. Blurring whatever holds it is enough for a note;
  // a preview needs the window itself asked for, because the page inside it is
  // what the keyboard is currently talking to.
  const release = () => {
    (document.activeElement as HTMLElement | null)?.blur();
    window.focus();
    setCaptured("");
  };

  const widthOf = (id: string) => widths()[id] ?? DEFAULT_WIDTH;

  const resize = (to: (at: number) => number) => {
    const id = focus();
    if (!id) return;
    setWidths((w) => ({ ...w, [id]: to(widthOf(id)) }));
    queueMicrotask(() => paneEl(id)?.scrollIntoView({ behavior: motion(), inline: "center", block: "nearest" }));
  };

  // Keys are listened for on the window rather than on the strip: the ribbon is
  // the whole screen here.
  //
  // The modifier is what makes a key ours rather than the pane's, and it is the
  // only form that works from inside a note. From inside a preview nothing
  // works — a cross-origin page keeps every key it is given, and no application
  // outside it can take one back. That is why the pane says when it has the
  // keyboard: clicking its header hands it back.
  const onKeyDown = (e: KeyboardEvent) => {
    const target = e.target as HTMLElement | null;
    const typing = target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
    const mod = e.metaKey || e.ctrlKey;
    // A terminal wants every key it is given, Escape most of all: it is how a
    // person leaves insert mode, and stealing it to leave the pane would make
    // an editor unusable inside one. Only the modifier gets through.
    const terminal = !!target?.closest(".terminal");

    if (typing && !mod) {
      if (e.key === "Escape" && !terminal) target.blur();
      return;
    }
    if (e.altKey) return;

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

  onSettled(() => {
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  });

  // The strip has been left behind: the card is somewhere else and the person
  // asked to stay. Offered, not taken.
  const behind = () => {
    const view = current();
    return pinned() && !!view?.focusId && focus() !== view.focusId;
  };

  return (
    <div class="ribbon" onWheel={onWheel}>
      {/* The only chrome: room for the window's own buttons, the name of the
          job in front of you, and the one offer the ribbon ever makes. */}
      <header class="ribbon-bar" style={{ "--wails-draggable": "drag" }}>
        <Sections open={menu()} setOpen={setMenu} />
        <span class="ribbon-where">
          {current()?.title}
          <Show when={current()?.stageName}>
            <span class="ribbon-stage"> · {current()!.stageName}</span>
          </Show>
          {/* A finished job visited for its results: nothing on it moves any
              more, and the bar says so rather than leaving a stage name off. */}
          <Show when={current() && current()!.id === closedRibbon()}>
            <span class="ribbon-stage"> · закрыта</span>
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
                <div class="band">
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
                            cardId={view.cardId}
                            id={emptyID(segment)}
                            title={segment.stageName}
                            first
                            width={widthOf(emptyID(segment))}
                            focused={focus() === emptyID(segment)}
                            captured={false}
                            onFocus={() => { setPinned(true); setFocus(emptyID(segment)); }}
                            onRelease={release}
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
                              captured={captured() === screen.id}
                              onFocus={() => { setPinned(true); setFocus(screen.id); }}
                              onRelease={release}
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
  onSettled(() => {
    const away = (e: MouseEvent) => {
      if (box && !box.contains(e.target as Node)) props.setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
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
                  <Show when={item.mark && item.mark()}>
                    <span class="mark" title="Есть новая версия" />
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
  captured: boolean;
  onFocus: () => void;
  onRelease: () => void;
}): JSX.Element {
  const waiting = () => list(props.screen?.waiting);

  return (
    <section
      class={`screen ${props.focused ? "on" : ""} ${props.captured ? "held" : ""}`}
      style={{ "flex-basis": `calc(100% * ${WIDTHS[props.width] ?? WIDTHS[DEFAULT_WIDTH]})` }}
      data-pane={props.id}
      onMouseDown={props.onFocus}
    >
      {/* Clicking the header is how the keyboard comes back: a person who
          clicked into a preview has nowhere else to press, because the page
          inside it keeps every key. */}
      <header class="screen-head" onClick={() => props.captured && props.onRelease()}>
        <Show when={props.first}>
          <span class={`tag ${props.segment.current ? "accent" : ""}`}>{props.segment.stageName}</span>
        </Show>
        <span class="screen-title">{props.title}</span>
        <Show when={props.captured}>
          <span class="tag warn" title="Клавиши уходят сюда. Нажмите на заголовок, чтобы вернуть их ленте">
            клавиши здесь
          </span>
        </Show>
        <div class="spacer" />
        {/* The two answers a waiting stage is waiting for, on the strip rather
            than on the card screen: what they are about is open in this very
            segment, and the ribbon is the whole window (docs/system.md §12.5).
            Once per segment — they belong to the step, not to the window onto
            it — and only while the card is standing there. */}
        <Show when={props.first}>
          <For each={list(props.segment.marks)}>
            {(mark) => (
              <button
                class={`btn tiny ${mark.forward ? "primary" : "quiet"}`}
                title={`Отметить «${mark.value}» — карточка уедет в «${mark.stage}»`}
                onClick={(e) => {
                  e.stopPropagation();
                  void guard(() => API.MarkOutcome(props.cardId ?? "", mark.value));
                }}
              >
                {mark.forward ? `${mark.stage} →` : `← ${mark.stage}`}
              </button>
            )}
          </For>
        </Show>
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
      {/* The agent's own screen, and which one it is was decided when the step
          ran: a stage worked in a terminal shows that terminal, a session shows
          the stream it left behind (docs/system.md §12.2). */}
      <Match when={props.screen.kind === "agentTerminal"}>
        <Loading fallback={<div class="screen-note">Терминал агента открывается…</div>}>
          <TerminalPane
            open={() => API.AgentTerminal(props.screen.sessionId ?? "")}
            ended="шаг в этом терминале закончен"
          />
        </Loading>
      </Match>
      <Match when={props.screen.kind === "agent"}>
        <AgentPane sessionId={props.screen.sessionId ?? ""} />
      </Match>
      <Match when={props.screen.kind === "notes"}>
        <NotesPane cardId={props.cardId} path={props.screen.ref ?? ""} />
      </Match>
      <Match when={props.screen.kind === "browser"}>
        <BrowserPane url={props.screen.ref ?? ""} />
      </Match>
      <Match when={props.screen.kind === "diff"}>
        <Loading fallback={<div class="screen-note">Читаем изменения…</div>}>
          <DiffPane cardId={props.cardId} rev={props.screen.ref ?? ""} />
        </Loading>
      </Match>
      <Match when={props.screen.kind === "terminal"}>
        <Loading fallback={<div class="screen-note">Терминал открывается…</div>}>
          <TerminalPane
            open={() => API.OpenTerminal(props.cardId, props.screen.id, props.screen.ref ?? "")}
          />
        </Loading>
      </Match>
    </Switch>
  );
}

// ---- what the agent did ----

// The stream is read from the same rows the card's history is made of, and it
// is asked for again whenever the session says something — the event carries no
// content, so the screen asks, like every other screen here.
//
// This is the screen of a stage worked as a *session* (docs/system.md §12.2). A
// session has no interface of its own, so this retelling is all there is — which
// is exactly why it must not be filled with the protocol's own bookkeeping. A
// tool call is one line that ends up saying how it went, not a line per status
// with the call's identifier on it: an identifier is the one thing about a tool
// call that means nothing to anybody reading.
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

  onSettled(() => {
    void pull();
    const off = Events.On("session", () => { void pull(); });
    return () => { if (typeof off === "function") off(); };
  });

  // The last status of every tool call, so the call's own line carries how it
  // ended and the updates that said so are not lines of their own.
  const statuses = createMemo(() => {
    const last: Record<string, string> = {};
    for (const e of events()) {
      if (e.kind !== "tool_call" && e.kind !== "tool_update") continue;
      try {
        const data = JSON.parse(e.payload || "{}");
        if (data.toolCallId && data.status) last[data.toolCallId] = data.status;
      } catch { /* a row we cannot read says nothing about any call */ }
    }
    return last;
  });
  const rows = createMemo(() => events().filter((e) => e.kind !== "tool_update"));

  return (
    <div class="stream" ref={box}>
      <Show when={rows().length > 0} fallback={<div class="screen-note">Агент ещё ничего не сказал.</div>}>
        <For each={rows()}>{(e) => <StreamLine event={e} statuses={statuses()} />}</For>
      </Show>
    </div>
  );
}

// The words the protocol uses, in the words a person reads. A status nobody
// translated is a status nobody can act on.
const STATUS: Record<string, string> = {
  pending: "ждёт", in_progress: "идёт", completed: "готово", failed: "не вышло",
};
const DECISION: Record<string, string> = {
  allow_once: "разрешено", allow_always: "разрешено всегда",
  reject_once: "отказано", reject_always: "отказано всегда",
};

function StreamLine(props: { event: SessionEvent; statuses: Record<string, string> }): JSX.Element {
  const data = createMemo<Record<string, any>>(() => {
    try { return JSON.parse(props.event.payload || "{}"); } catch { return {}; }
  });
  // The question, if it is still open — then it is answered here, in the same
  // form the attention panel and the card use. All three are one question.
  const open = createMemo(() => attention().find((a) => a.questionId === data().questionId));
  const status = createMemo(() => props.statuses[data().toolCallId] || data().status || "");

  return (
    <Switch fallback={<p class="stream-tool dim">{props.event.kind}</p>}>
      <Match when={props.event.kind === "chunk"}>
        <p class="stream-say">{data().text}</p>
      </Match>
      <Match when={props.event.kind === "thought"}>
        <p class="stream-think">{data().text}</p>
      </Match>
      <Match when={props.event.kind === "tool_call"}>
        <p class={`stream-tool ${status() === "completed" ? "dim" : ""}`}>
          <span class={`tag ${status() === "failed" ? "warn" : ""}`}>
            {STATUS[status()] ?? "вызов"}
          </span>{" "}
          {data().title || "инструмент"}
        </p>
      </Match>
      <Match when={props.event.kind === "permission"}>
        <p class="stream-tool dim">
          <span class={`tag ${data().decision?.startsWith("allow") ? "ok" : "warn"}`}>доступ</span>{" "}
          {data().tool} — {DECISION[data().decision] ?? data().decision}
        </p>
      </Match>
      <Match when={props.event.kind === "question"}>
        <Show
          when={open()}
          fallback={<p class="stream-tool dim"><span class="tag">вопрос</span> {data().text}</p>}
        >
          <div class="stream-tool">
            <span class="tag warn">вопрос</span>
            <QuestionForm
              questionId={open()!.questionId}
              text={open()!.text ?? data().text ?? ""}
              options={open()!.options ?? []}
              freeText={open()!.freeText ?? false}
              onAnswered={loadAttention}
            />
          </div>
        </Show>
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

  // An `async` callback cannot be an onSettled callback at all — its promise
  // is read as the cleanup — so the read is started here and awaited inside.
  onSettled(() => {
    void (async () => {
      const have = await guard(() => API.ReadDoc(props.cardId, props.path));
      if (have !== undefined) setText(have);
    })();
    return () => { if (timer) clearTimeout(timer); };
  });

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
