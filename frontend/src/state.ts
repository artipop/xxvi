import { createSignal } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { Events } from "@wailsio/runtime";
import * as API from "../bindings/github.com/artipop/xxvi/internal/app/api";
import type { AgentsView, CardView, StageCard, Vocabulary } from "../bindings/github.com/artipop/xxvi/internal/app/models";
import type { Attention } from "../bindings/github.com/artipop/xxvi/internal/acp/models";
import type { Card, Flow, InboxGroup, Source } from "../bindings/github.com/artipop/xxvi/internal/model/models";
import type { CardSummary } from "../bindings/github.com/artipop/xxvi/internal/app/models";
import type { RibbonSummary, RibbonView } from "../bindings/github.com/artipop/xxvi/internal/engine/models";

// Everything the screens read, in one place. The backend is the only copy of
// the truth — nothing here is computed from an earlier answer — so a reload is
// always a re-read rather than a merge, and two screens can never disagree.

export const [inbox, setInbox] = createSignal<InboxGroup[]>([]);
export const [inWork, setInWork] = createSignal<CardSummary[]>([]);
export const [done, setDone] = createSignal<Card[]>([]);
export const [flows, setFlows] = createSignal<Flow[]>([]);
export const [sources, setSources] = createSignal<Source[]>([]);
export const [agents, setAgents] = createSignal<AgentsView>({ agents: [], adapters: [] });
export const [attention, setAttention] = createSignal<Attention[]>([]);
export const [vocabulary, setVocabulary] = createSignal<Vocabulary>({
  triggers: [], actions: [], kinds: [], ruleActions: [],
  outcomeProperty: "", outcomeValues: [], screenKinds: [],
});

// A Go slice that was empty arrives as null, and every screen would otherwise
// have to remember that. `list` is where it is remembered once: an absent list
// and an empty one are the same thing to read, and the difference belongs to
// the wire rather than to the screens.
export function list<T>(v: T[] | null | undefined): T[] {
  return v ?? [];
}

// error is the last thing that went wrong, shown once and dismissible. A
// refusal from the backend is a sentence written for a person — it is displayed
// as it came rather than replaced with something vaguer.
export const [error, setError] = createSignal<string>("");

/** report shows a failure without letting it break the caller's flow. */
export function report(e: unknown) {
  const text = e instanceof Error ? e.message : String(e);
  setError(text);
  console.error(e);
}

/** guard runs an action and reports a refusal instead of throwing it away. */
export async function guard<T>(fn: () => Promise<T>): Promise<T | undefined> {
  try {
    setError("");
    return await fn();
  } catch (e) {
    report(e);
    return undefined;
  }
}

export async function loadInbox() {
  try { setInbox(list(await API.Inbox())); } catch (e) { report(e); }
}
export async function loadInWork() {
  try { setInWork(list(await API.InWork())); } catch (e) { report(e); }
}
export async function loadDone() {
  try { setDone(list(await API.Done())); } catch (e) { report(e); }
}
export async function loadFlows() {
  try { setFlows(list(await API.Flows())); } catch (e) { report(e); }
}
export async function loadSources() {
  try { setSources(list(await API.Sources())); } catch (e) { report(e); }
}
export async function loadAgents() {
  try { setAgents(await API.Agents()); } catch (e) { report(e); }
}
export async function loadAttention() {
  try { setAttention(list(await API.Attention())); } catch (e) { report(e); }
}

/** loadAll re-reads everything. Cheap enough locally, and it cannot go stale. */
export async function loadAll() {
  await Promise.all([
    loadInbox(), loadInWork(), loadDone(), loadFlows(), loadSources(), loadAgents(), loadAttention(),
    loadRibbons(),
  ]);
  try { setVocabulary(await API.Vocabulary()); } catch (e) { report(e); }
}

// The currently open card, which several screens navigate into.
export const [openCard, setOpenCard] = createSignal<CardView | null>(null);

export async function openCardByID(id: string) {
  const view = await guard(() => API.Card(id));
  if (view) setOpenCard(view);
}

export function closeCard() { setOpenCard(null); }

/** applyCard takes a view the backend just returned, so an action's own answer
 *  refreshes the card without a second round trip. */
export function applyCard(view: CardView | undefined) {
  if (!view) return;
  setOpenCard(view);
  void loadInbox();
  void loadInWork();
  void loadDone();
}

/**
 * subscribe wires the backend's events to reloads. Events say *that* something
 * changed, never what it now is: the screen then asks. Carrying the new state
 * in the event would be a second way to learn it, and the two would drift.
 */
export function subscribe() {
  const refreshCard = async () => {
    const current = openCard();
    if (current) await openCardByID(current.card.id);
  };
  // The strip re-reads on the same events the rest of the screens do: a card
  // that moved is a segment that was added, and a session that said something
  // is a screen that has more to show.
  const refreshRibbon = () => {
    void loadRibbons();
    if (openRibbon()) void loadRibbon(openRibbon());
  };
  Events.On("card", () => {
    void loadInbox(); void loadInWork(); void loadDone(); void refreshCard(); refreshRibbon();
  });
  Events.On("inbox", () => { void loadInbox(); });
  Events.On("session", () => { void loadInWork(); void refreshCard(); refreshRibbon(); });
  Events.On("attention", () => { void loadAttention(); void refreshCard(); });
  Events.On("flows", () => { void loadFlows(); });
  Events.On("sources", () => { void loadSources(); });
  Events.On("agents", () => { void loadAgents(); });
}

export const [stageCards, setStageCards] = createSignal<StageCard[]>([]);

export async function loadFlowCards(flowID: string) {
  const cards = await guard(() => API.FlowCards(flowID));
  setStageCards(list(cards));
}

// ---- the ribbon ----
//
// One card in work is one ribbon, and there is no other kind. Which one is open
// is a second axis of navigation, like the card panel: the strip stays where it
// was while the tabs change around it.

export const [ribbons, setRibbons] = createSignal<RibbonSummary[]>([]);
export const [openRibbon, setOpenRibbon] = createSignal<string>("");

// The ribbon is a store rather than a signal, and it is updated by reconcile
// keyed on the screen id. Every backend event makes the strip re-read itself,
// and a plain replacement would rebuild the DOM: the iframe of a running
// preview would reload and the cursor would jump out of the notes on every
// step the agent takes. Reconcile keeps the panes that did not change.
const empty: RibbonView = { cardId: "", title: "", flowId: "", flowName: "", segments: [], focusId: "" };
export const [ribbon, setRibbonStore] = createStore<RibbonView>({ ...empty });

export async function loadRibbons() {
  try { setRibbons(list(await API.Ribbons())); } catch (e) { report(e); }
}

export async function loadRibbon(cardID: string) {
  if (!cardID) { setRibbonStore(reconcile({ ...empty }, { key: "id" })); return; }
  const view = await guard(() => API.Ribbon(cardID));
  if (!view) return;
  // Reconcile only makes sense against the same ribbon; switching cards is a
  // different strip, and every pane in it is genuinely new.
  if (ribbon.cardId !== view.cardId) setRibbonStore({ ...empty, cardId: view.cardId });
  setRibbonStore(reconcile(view, { key: "id" }));
}

// Which screen is open. A signal rather than a local of the shell, because
// «Сделай» is one gesture that ends on another screen: taking a card into work
// and watching it start are the same moment.
export type Tab = "inbox" | "ribbon" | "work" | "attention" | "flows" | "sources" | "agents";
export const [tab, setTab] = createSignal<Tab>("inbox");

/** showRibbon opens one card's strip, which is what «Сделай» ends in. */
export function showRibbon(cardID: string) {
  setTab("ribbon");
  setOpenRibbon(cardID);
  void loadRibbon(cardID);
}
