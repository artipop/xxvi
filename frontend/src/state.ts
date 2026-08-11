import { createSignal } from "solid-js";
import { Events } from "@wailsio/runtime";
import * as API from "../bindings/github.com/artipop/xxvi/internal/app/api";
import type { AgentsView, CardView, StageCard, Vocabulary } from "../bindings/github.com/artipop/xxvi/internal/app/models";
import type { Attention } from "../bindings/github.com/artipop/xxvi/internal/acp/models";
import type { Card, Flow, InboxGroup, Source } from "../bindings/github.com/artipop/xxvi/internal/model/models";
import type { CardSummary } from "../bindings/github.com/artipop/xxvi/internal/app/models";

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
});

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
  try { setInbox(await API.Inbox()); } catch (e) { report(e); }
}
export async function loadInWork() {
  try { setInWork(await API.InWork()); } catch (e) { report(e); }
}
export async function loadDone() {
  try { setDone(await API.Done()); } catch (e) { report(e); }
}
export async function loadFlows() {
  try { setFlows(await API.Flows()); } catch (e) { report(e); }
}
export async function loadSources() {
  try { setSources(await API.Sources()); } catch (e) { report(e); }
}
export async function loadAgents() {
  try { setAgents(await API.Agents()); } catch (e) { report(e); }
}
export async function loadAttention() {
  try { setAttention(await API.Attention()); } catch (e) { report(e); }
}

/** loadAll re-reads everything. Cheap enough locally, and it cannot go stale. */
export async function loadAll() {
  await Promise.all([
    loadInbox(), loadInWork(), loadDone(), loadFlows(), loadSources(), loadAgents(), loadAttention(),
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
  Events.On("card", () => { void loadInbox(); void loadInWork(); void loadDone(); void refreshCard(); });
  Events.On("inbox", () => { void loadInbox(); });
  Events.On("session", () => { void loadInWork(); void refreshCard(); });
  Events.On("attention", () => { void loadAttention(); void refreshCard(); });
  Events.On("flows", () => { void loadFlows(); });
  Events.On("sources", () => { void loadSources(); });
  Events.On("agents", () => { void loadAgents(); });
}

export const [stageCards, setStageCards] = createSignal<StageCard[]>([]);

export async function loadFlowCards(flowID: string) {
  const cards = await guard(() => API.FlowCards(flowID));
  setStageCards(cards ?? []);
}
