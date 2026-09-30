import {
  createEffect, createMemo, createSignal, For, lazy, Loading, Match, onCleanup, onSettled, Show, Switch,
} from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { RibbonView, ScreenView, Segment } from "../../bindings/github.com/artipop/xxvi/internal/engine/models";
import type { SessionEvent } from "../../bindings/github.com/artipop/xxvi/internal/store/models";
import {
  attention, closedRibbon, done, guard, inWorkspace, leaveRibbons, list, loadAttention, loadRibbons, openRibbon, projects, report,
  ribbons, setOpenRibbon, setTab, setWorkspace, showRibbon, workRibbons, workspace, openOutside,
} from "../state";
import { QuestionForm } from "./attention";
import { JournalOf } from "./journal";
import { Compose } from "./compose";
import { entryText, label, propName, questionText, say, t } from "../i18n";
import { MarkButtons, RemarksForm, sendMark } from "./marks";
import { Icon } from "../icons";
import type { Msg } from "../../bindings/github.com/artipop/xxvi/internal/msg/models";
import type { Mark } from "../../bindings/github.com/artipop/xxvi/internal/model/models";

// The emulator is a large chunk and most screens are not terminals, so it
// arrives only when one is opened.
const TerminalPane = lazy(() => import("./terminal"));

// The same reasoning for the diff: a card whose stage shows no diff never pays
// for the viewer.
const DiffPane = lazy(() => import("./diff"));

// And for the run screen, which brings the terminal and a request form with it.
const RunPane = lazy(() => import("./run"));

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

function centerInBand(el: HTMLElement | undefined) {
  const band = el?.closest<HTMLElement>(".band");
  if (!el || !band) return;
  const by = el.getBoundingClientRect().left + el.offsetWidth / 2
    - (band.getBoundingClientRect().left + band.clientWidth / 2);
  band.scrollTo({ left: band.scrollLeft + by, behavior: "instant" });
}

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

const DRAFT = "draft";

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
  onCleanup(leaveRibbons);

  const [journal, setJournal] = createSignal(false);
  // A new task is a ribbon of its own, not a dialog over somebody else's: it
  // sits at the end of the stack with one empty window to write it in, and
  // becomes the card's real strip the moment it is started. With nothing in
  // work it is the whole stack.
  const [drafting, setDrafting] = createSignal(false);
  const showDraft = () => drafting() || inWorkspace().length === 0;
  const stackIDs = () => [...inWorkspace().map((r) => r.id), ...(showDraft() ? [DRAFT] : [])];
  // Which pane has taken the keyboard: a preview or a note that a person
  // clicked into. Worth saying out loud, because from inside a preview the
  // ribbon cannot hear a key at all — the page has it — and a person pressing
  // arrows at a window that is not listening deserves to be told why.
  const [captured, setCaptured] = createSignal("");

  let stack: HTMLDivElement | undefined;
  const flown: Record<string, string> = {};

  const current = () => inWorkspace().find((r) => r.id === openRibbon());

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
    const at = stackIDs().indexOf(cardID);
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
        // A strip first seen is arrived at, not moved along: its band is put on
        // the step at once, or that step sits off the band's edge. Only the
        // band is scrolled — scrollIntoView would move the stack to this strip
        // too.
        if (first) {
          queueMicrotask(() => centerInBand(paneEl(target)));
          continue;
        }
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
    () => inWorkspace().map((r) => r.id),
    (ids) => {
      if (ids.length === 0) return;
      // A closed card asked for is on its way into the stack, not missing
      // from it: the next read brings it.
      if (!ids.includes(openRibbon()) && openRibbon() !== closedRibbon() && openRibbon() !== DRAFT) {
        setOpenRibbon(ids[0]);
      }
    },
  );

  // Asked for from outside — «Do it», «Ribbon →» on a closed card — a strip is
  // opened by name, and the stack has to be standing on it. Keyed on where it
  // is in the stack as well, because the one asked for may arrive a read later.
  createEffect(
    () => stackIDs().indexOf(openRibbon()),
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
    const all = stackIDs();
    if (all.length === 0) return;
    const at = all.indexOf(openRibbon());
    setPinned(false);
    flyToRibbon(all[(at + delta + all.length) % all.length]);
  };

  const newTask = () => {
    setDrafting(true);
    setPinned(false);
    queueMicrotask(() => flyToRibbon(DRAFT));
  };

  // Given up, the draft goes and the stack stands on real work again.
  const dropDraft = () => {
    setDrafting(false);
    const shown = inWorkspace();
    if (shown.length > 0) flyToRibbon(shown[shown.length - 1].id);
  };

  // Taking the keyboard back. Blurring whatever holds it is enough for a note;
  // a preview needs the window itself asked for, because the page inside it is
  // what the keyboard is currently talking to.
  const release = () => {
    (document.activeElement as HTMLElement | null)?.blur();
    window.focus();
    setCaptured("");
  };

  // The keyboard follows the ribbon: standing on a terminal's pane is being in
  // that terminal, as a person sees a CLI waiting in it and just types.
  // Anywhere else the keys are the page's again. The terminal is a native view
  // over the page, so this is said to it rather than done with DOM focus.
  // xterm.js, where it draws instead, is focused like any element of the page.
  const keyboardTo = (id: string) => {
    const host = paneEl(id)?.querySelector<HTMLElement>(".terminal-host");
    const term = host?.dataset.term ?? "";
    void API.FocusNativeTerminal(term);
    if (host && !term) {
      host.querySelector<HTMLElement>("textarea")?.focus({ preventScroll: true });
      return;
    }
    const held = document.activeElement as HTMLElement | null;
    if (held?.closest(".terminal")) held.blur();
  };
  createEffect(focus, (id) => { queueMicrotask(() => keyboardTo(id)); });

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
    // From inside a terminal only with shift as well: ⌘← and ⌘→ are the ends of
    // the line there, and on Windows, where the modifier is Ctrl, Ctrl+R, Ctrl+J
    // and Ctrl+← are the shell's. Ghostty's view hands the same keys over
    // (internal/nativeterm).
    if (terminal && !e.shiftKey) return;
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
      case "n": case "N": case "т": case "Т":
        e.preventDefault(); newTask(); break;
      case "j": case "J": case "о": case "О":
        e.preventDefault(); setJournal(!journal()); break;
      case "Escape":
        e.preventDefault();
        if (journal()) setJournal(false);
        else setTab("inbox");
        break;
    }
    // A ⌘ key comes here from a terminal that had the keyboard, and one that
    // did not move the ribbon has to give it back.
    if (mod) keyboardTo(focus());
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
      <header class="ribbon-bar">
        <span class="ribbon-where">
          {current()?.title}
          <Show when={current()?.stageName}>
            <span class="ribbon-stage"> · {current()!.stageName}</span>
          </Show>
          {/* A finished job visited for its results: nothing on it moves any
              more, and the bar says so rather than leaving a stage name off. */}
          <Show when={current() && current()!.id === closedRibbon()}>
            <span class="ribbon-stage"> · {current()!.returnable ? t("ribbon.inInbox") : t("ribbon.closed")}</span>
          </Show>
        </span>
        <div class="spacer" />
        <Closed />
        <Show when={inWorkspace().length > 0}>
          <button class="btn quiet tiny"
                  onClick={newTask} title={t("ribbon.newTaskKey")}>{t("ribbon.addTask")}</button>
        </Show>
        <Show when={current()}>
          <CardMenu
            cardId={current()!.cardId}
            closed={current()!.id === closedRibbon()}
            onJournal={() => setJournal(!journal())}
          />
        </Show>
        <Show when={current()?.returnable}>
          <button class="btn primary tiny" title={t("ribbon.returnHint")}
                  onClick={() => void guard(() => API.ReturnToFlow(current()!.cardId)).then(() => loadRibbons())}>
            {t("ribbon.return")}
          </button>
        </Show>
        <Show when={behind()}>
          <button
            class="btn primary tiny"
            onClick={() => { setPinned(false); flyTo(current()!.focusId ?? ""); }}
          >
            {t("ribbon.next")}
          </button>
        </Show>
        <Workspaces />
      </header>

        <div class="stack" ref={stack}>
          <For each={inWorkspace()}>
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
                              title={screenTitle(screen)}
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
          <Show when={showDraft()}>
            <section class="workspace" data-ribbon={DRAFT}>
              <div class="band">
                <section class="screen on draft">
                  <header class="screen-head">
                    <span class="tag accent">{t("ribbon.newTask")}</span>
                    <span class="screen-title">{t("ribbon.draftKeys")}</span>
                  </header>
                  <div class="screen-body">
                    <Compose
                      onStarted={() => setDrafting(false)}
                      onCancel={inWorkspace().length > 0 ? dropDraft : undefined}
                    />
                  </div>
                </section>
              </div>
            </section>
          </Show>
        </div>

        {/* Keyed on the card, so moving to another job shows that job's
            journal rather than the last one's. */}
        <Show when={journal() && current()}>
          <For each={[current()!.cardId]}>
            {(cardId) => (
              <aside class="ribbon-journal">
                <header class="row">
                  <h3>{t("card.journal")}</h3>
                  <div class="spacer" />
                  <button class="btn quiet tiny" onClick={() => setJournal(false)} title={t("ribbon.journalKeys")}>{t("common.close")}</button>
                </header>
                <JournalOf cardId={cardId} />
              </aside>
            )}
          </For>
        </Show>

        {/* Where you are in the stack, and which other jobs moved while you
            were not looking. One job needs no map of itself. */}
        <Show when={inWorkspace().length > 1}>
        <nav class="rail">
          <For each={inWorkspace()}>
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
    </div>
  );
}

/** Folded is a button that opens a short list under itself, and closes on a
 *  click anywhere else. */
function Folded(props: { label: JSX.Element; title?: string; class?: string; children: (close: () => void) => JSX.Element }): JSX.Element {
  const [open, setOpen] = createSignal(false);
  let box: HTMLDivElement | undefined;
  onSettled(() => {
    const away = (e: MouseEvent) => { if (box && !box.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  });
  return (
    <div class={`card-menu ${props.class ?? ""}`} ref={box}>
      <button class="btn quiet tiny" onClick={() => setOpen(!open())} title={props.title}>{props.label}</button>
      <Show when={open()}>
        <div class="menu">{props.children(() => setOpen(false))}</div>
      </Show>
    </div>
  );
}

// Which project's ribbons are shown. With no projects there is one workspace
// and nothing to switch to.
function Workspaces(): JSX.Element {
  const count = (id: string) => workRibbons().filter((r) => (r.project ?? "") === id).length;
  const name = (id: string) => projects().find((p) => p.id === id)?.name ?? t("ribbon.noProject");
  // Cards without a project are a workspace only while there are some, or
  // while standing in it: otherwise it is an empty entry in every list.
  const ids = () => [
    ...projects().map((p) => p.id),
    ...(count("") > 0 || workspace() === "" ? [""] : []),
  ];
  return (
    <Show when={projects().length > 0}>
      <Folded class="workspaces" label={<>{name(workspace())}<Icon name="chevron" /></>} title={t("ribbon.workspace")}>
        {(close) => (
          <For each={ids()}>
            {(id) => (
              <button class={id === workspace() ? "on" : ""} onClick={() => { close(); setWorkspace(id); }}>
                <span>{name(id)}</span>
                <Show when={count(id) > 0}><span class="count">{count(id)}</span></Show>
              </button>
            )}
          </For>
        )}
      </Folded>
    </Show>
  );
}

// Finished tasks of this workspace, folded: they are visited for their
// results, not worked in, and the stack is for what is still moving.
function Closed(): JSX.Element {
  const here = () => done().filter((c) => (c.project ?? "") === workspace());
  return (
    <Show when={here().length > 0}>
      <Folded label={t("ribbon.closedList", { n: here().length })}>
        {(close) => (
          <For each={here().slice(0, 30)}>
            {(card) => (
              <button onClick={() => { close(); showRibbon(card.id); }}>
                <span class="menu-title">{card.title}</span>
              </button>
            )}
          </For>
        )}
      </Folded>
    </Show>
  );
}

// What can be done to the job as a whole, as opposed to its current step. Few
// and rare, so folded away: the bar is the window's drag handle and the name of
// the job, not a toolbar.
function CardMenu(props: { cardId: string; closed: boolean; onJournal: () => void }): JSX.Element {
  const [open, setOpen] = createSignal(false);
  // Dropping cannot be taken back from here, so it takes a second press — a
  // dialog would be a second window for one word.
  const [sure, setSure] = createSignal(false);
  let box: HTMLDivElement | undefined;

  onSettled(() => {
    const away = (e: MouseEvent) => {
      if (box && !box.contains(e.target as Node)) { setOpen(false); setSure(false); }
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  });

  const run = (fn: () => Promise<unknown>) => {
    setOpen(false); setSure(false);
    void guard(fn).then(() => loadRibbons());
  };

  return (
    <div class="card-menu" ref={box}>
      <button class="btn quiet tiny" onClick={() => { setOpen(!open()); setSure(false); }} title={t("attention.card")}>⋯</button>
      <Show when={open()}>
        <div class="menu">
          <button onClick={() => { setOpen(false); props.onJournal(); }}>
            <span>{t("card.journal")}</span><span class="count">J</span>
          </button>
          <Show when={!props.closed}>
            <hr />
            <button onClick={() => run(() => API.RemoveFromFlow(props.cardId))}>{t("card.removeFromFlow")}</button>
            <button
              class={sure() ? "danger" : ""}
              onClick={() => (sure() ? run(() => API.DropCard(props.cardId)) : setSure(true))}
            >
              {sure() ? t("ribbon.sureDrop") : t("common.drop")}
            </button>
          </Show>
        </div>
      </Show>
    </div>
  );
}

/** screenTitle is what a pane is called: the stage's own title for it, or —
 *  when it gave none, and on an agent's own screen — its kind. */
function screenTitle(screen: ScreenView): string {
  if (screen.title) return screen.title;
  if (screen.kind === "agent") return t("ribbon.agentRun", { agent: screen.agent });
  if (screen.kind === "agentTerminal") return t("ribbon.agentTerminal", { agent: screen.agent });
  return label("screen", screen.kind);
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
  const [back, setBack] = createSignal<Mark | null>(null);

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
        {/* How the card got here — «came back: failed» reads differently from
            «taken into work», and it is the one line of the journal that is
            about the whole segment. */}
        <Show when={props.first && props.segment.detail}>
          <span class="screen-why" title={say(props.segment.detail)}>{say(props.segment.detail)}</span>
        </Show>
        <Show when={props.captured}>
          <span class="tag warn" title={t("ribbon.keysHereTitle")}>
            {t("ribbon.keysHere")}
          </span>
        </Show>
        <div class="spacer" />
        {/* The two answers a waiting stage is waiting for, on the strip rather
            than on the card screen: what they are about is open in this very
            segment, and the ribbon is the whole window (docs/system.md §12.5).
            Once per segment — they belong to the step, not to the window onto
            it — and only while the card is standing there. */}
        <Show when={props.first}>
          <MarkButtons
            marks={list(props.segment.marks)}
            tiny
            onMark={(mark) => (mark.forward
              ? void guard(() => sendMark(props.cardId ?? "", mark))
              : setBack(mark))}
          />
        </Show>
        <Show when={props.screen?.kind === "browser" && waiting().length === 0}>
          <a class="btn quiet tiny" href={props.screen!.ref} onClick={(e) => openOutside(e, props.screen!.ref)} title={t("ribbon.openOutside")}>↗</a>
        </Show>
      </header>

      {/* Why the card stands, or why the step broke, on the step itself:
          without it a stopped strip looks exactly like a working one. */}
      <Show when={props.first && back()}>
        <div class="screen-remarks">
          <RemarksForm
            mark={back()!}
            onSend={(remarks) => {
              const mark = back()!;
              setBack(null);
              void guard(() => sendMark(props.cardId ?? "", mark, remarks));
            }}
            onCancel={() => setBack(null)}
          />
        </div>
      </Show>

      <Show when={props.first && list(props.segment.problems).length > 0}>
        <div class={`screen-problems ${props.segment.current ? "now" : ""}`}>
          <For each={list(props.segment.problems)}>
            {(p) => (
              <Switch fallback={<p>{entryText(p)}</p>}>
                <Match when={p.kind === "review"}>
                  <p class="review"><b>{t("remarks.said")}</b> {entryText(p)}</p>
                </Match>
                <Match when={p.kind === "source"}>
                  <p class="source">{entryText(p)}</p>
                </Match>
              </Switch>
            )}
          </For>
        </div>
      </Show>

      <div class="screen-body">
        <Switch fallback={<Body screen={props.screen!} cardId={props.cardId ?? ""} current={props.segment.current} />}>
          {/* A stage removed from the flow does not take its part of the ribbon
              with it: what happened happened, and the segment says why it is
              empty. */}
          <Match when={props.segment.gone}>
            <div class="screen-note">
              {t("ribbon.stageGone", { stage: props.segment.stageId })}
            </div>
          </Match>
          <Match when={!props.screen}>
            <div class="screen-note">{t("ribbon.nothingShown")}</div>
          </Match>
          {/* A screen whose address is not on the card yet says which property
              it is waiting for. Opening a blank page and staying silent would be
              the same screen with the reason taken out. */}
          <Match when={waiting().length > 0}>
            <div class="screen-note">
              {t("ribbon.waitingFor", { properties: waiting().map((n) => `«${propName(n)}»`).join(", ") })}
            </div>
          </Match>
        </Switch>
      </div>

      <Show when={props.screen?.report}>
        <Report report={props.screen!.report!} />
      </Show>
      <Show when={props.screen?.paused}>
        <ContinueBar cardId={props.cardId ?? ""} canReopen={props.screen!.kind === "agentTerminal"} />
      </Show>
    </section>
  );
}

// A stage the application closed on waits here for a person: it goes on only
// when somebody says so, in the conversation it stopped in. What is typed is
// the next thing the agent reads; nothing typed means «go on».
// Reopening brings the CLI back with nothing said, for a look first — which a
// session has no CLI for: its conversation is the stream already on screen.
function ContinueBar(props: { cardId: string; canReopen: boolean }): JSX.Element {
  const [text, setText] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const go = async () => {
    setBusy(true);
    const done = await guard(() => API.ContinueStage(props.cardId, text()));
    setBusy(false);
    if (done) {
      setText("");
      await loadRibbons();
    }
  };
  const reopen = async () => {
    setBusy(true);
    const done = await guard(() => API.ReopenStage(props.cardId));
    setBusy(false);
    if (done) await loadRibbons();
  };
  return (
    <div class="screen-continue">
      <span class="meta" title={t("ribbon.pausedTitle")}>{t("ribbon.paused")}</span>
      <div class="row">
        <textarea
          rows={1}
          placeholder={t("ribbon.continuePlaceholder")}
          value={text()}
          onInput={(e) => setText(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void go(); }}
        />
        <Show when={props.canReopen}>
          <button class="btn tiny" disabled={busy()} onClick={() => void reopen()} title={t("ribbon.reopenTitle")}>
            {t("ribbon.reopen")}
          </button>
        </Show>
        <button class="btn primary tiny" disabled={busy()} onClick={() => void go()} title={t("ribbon.continueTitle")}>
          {t("ribbon.continue")}
        </button>
      </div>
    </div>
  );
}

// A terminal screen. One that went down with the application or its holder
// comes back as its last screen with nothing running — its command is somebody's,
// and running it again is theirs to say — until «start again».
function ScreenTerminal(props: { cardId: string; screenId: string; command: string }): JSX.Element {
  const [restarts, setRestarts] = createSignal(0);
  const [lost, setLost] = createSignal(false);
  const open = async () => {
    const handle = restarts() > 0
      ? await API.RestartTerminal(props.cardId, props.screenId, props.command)
      : await API.OpenTerminal(props.cardId, props.screenId, props.command);
    setLost(!!handle.lost);
    return handle;
  };
  return (
    <>
      {/* Keyed by the restart: a new shell is a new terminal, not the lost one
          coming back to life. */}
      <For each={[restarts()]}>
        {() => <TerminalPane open={open} ended={lost() ? t("terminal.lost") : undefined} />}
      </For>
      <Show when={lost()}>
        <div class="screen-continue">
          <div class="row">
            <span class="meta grow">{t("terminal.lostHint")}</span>
            <button class="btn primary tiny" onClick={() => setRestarts(restarts() + 1)}>{t("terminal.restart")}</button>
          </div>
        </div>
      </Show>
    </>
  );
}

// Folded to one line: the screen above is the step,// Folded to one line: the screen above is the step, and this is what the agent
// said about it — read after the work, not instead of it.
function Report(props: { report: Msg }): JSX.Element {
  const [open, setOpen] = createSignal(false);
  return (
    <div class={`screen-report ${open() ? "open" : ""}`} onClick={() => setOpen(!open())}>
      <span class="meta">{t("ribbon.result")}</span> {say(props.report)}
    </div>
  );
}

function Body(props: { screen: ScreenView; cardId: string; current: boolean }): JSX.Element {
  return (
    <Switch fallback={<div class="screen-note">{t("ribbon.unknownScreen", { kind: props.screen.kind })}</div>}>
      {/* The agent's own screen, and which one it is was decided when the step
          ran: a stage worked in a terminal shows that terminal, a session shows
          the stream it left behind (docs/system.md §12.2). */}
      <Match when={props.screen.kind === "agentTerminal"}>
        <Loading fallback={<div class="screen-note">{t("ribbon.agentTerminalOpening")}</div>}>
          <TerminalPane
            open={() => API.AgentTerminal(props.screen.sessionId ?? "")}
            ended={t("ribbon.stepEnded")}
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
        <Loading fallback={<div class="screen-note">{t("diff.reading")}</div>}>
          <DiffPane cardId={props.cardId} rev={props.screen.ref ?? ""} />
        </Loading>
      </Match>
      <Match when={props.screen.kind === "run"}>
        <Loading fallback={<div class="screen-note">{t("run.reading")}</div>}>
          <RunPane cardId={props.cardId} screenId={props.screen.id} prefer={props.screen.ref ?? ""} current={props.current} />
        </Loading>
      </Match>
      <Match when={props.screen.kind === "terminal"}>
        <Loading fallback={<div class="screen-note">{t("ribbon.terminalOpening")}</div>}>
          <ScreenTerminal cardId={props.cardId} screenId={props.screen.id} command={props.screen.ref ?? ""} />
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
      <Show when={rows().length > 0} fallback={<div class="screen-note">{t("ribbon.silentYet")}</div>}>
        <For each={rows()}>{(e) => <StreamLine event={e} statuses={statuses()} />}</For>
      </Show>
    </div>
  );
}

// The words the protocol uses, in the words a person reads (the «toolStatus.»
// and «decision.» keys). A status nobody translated is a status nobody can act on.

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
            {status() ? label("toolStatus", status()) : t("stream.call")}
          </span>{" "}
          {data().title || t("stream.tool")}
        </p>
      </Match>
      <Match when={props.event.kind === "permission"}>
        <p class="stream-tool dim">
          <span class={`tag ${data().decision?.startsWith("allow") ? "ok" : "warn"}`}>{t("stream.access")}</span>{" "}
          {data().tool} — {label("decision", data().decision ?? "")}
        </p>
      </Match>
      <Match when={props.event.kind === "answer"}>
        <p class="stream-tool dim">
          <span class={`tag ${data().declined ? "warn" : "ok"}`}>{t("stream.answer")}</span>{" "}
          {data().declined ? t("stream.noAnswer") : data().text || data().label || data().optionId}
        </p>
      </Match>
      <Match when={props.event.kind === "question"}>
        <Show
          when={open()}
          fallback={<p class="stream-tool dim"><span class="tag">{t("stream.question")}</span> {questionText(data().kind, data().tool, data().text)}</p>}
        >
          <div class="stream-tool">
            <span class="tag warn">{t("stream.question")}</span>
            <QuestionForm
              questionId={open()!.questionId}
              kind={open()!.kind}
              tool={open()!.tool}
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
      <div class="meta">{props.path} · {saved() ? t("notes.saved") : "…"}</div>
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
        <button class="btn quiet tiny" onClick={() => setNonce((n) => n + 1)} title={t("browser.reload")}>↻</button>
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
