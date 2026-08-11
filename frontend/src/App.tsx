import { createSignal, onMount, Show, type JSX } from "solid-js";
import {
  attention, error, inbox, inWork, loadAll, setError, subscribe,
} from "./state";
import InboxView from "./views/inbox";
import WorkView from "./views/work";
import FlowsView from "./views/flows";
import AgentsView from "./views/agents";
import SourcesView from "./views/sources";
import AttentionView from "./views/attention";
import CardPanel from "./views/card";
import { openCard } from "./state";

type Tab = "inbox" | "work" | "attention" | "flows" | "sources" | "agents";

export default function App(): JSX.Element {
  const [tab, setTab] = createSignal<Tab>("inbox");

  onMount(() => {
    void loadAll();
    subscribe();
  });

  const inboxCount = () => inbox().reduce((n, g) => n + g.cards.length, 0);

  const nav = (id: Tab, label: string, count?: () => number, alert = false) => (
    <button class={`nav ${tab() === id ? "on" : ""}`} onClick={() => setTab(id)}>
      <span>{label}</span>
      <Show when={count && count()! > 0}>
        <span class={`count ${alert ? "alert" : ""}`}>{count!()}</span>
      </Show>
    </button>
  );

  return (
    <div class="shell">
      <aside class="sidebar">
        <div class="brand">XXVI</div>
        {nav("inbox", "Входящие", inboxCount)}
        {nav("work", "В работе", () => inWork().length)}
        {nav("attention", "Требуют внимания", () => attention().length, true)}
        <div style={{ height: "14px" }} />
        {nav("flows", "Флоу")}
        {nav("sources", "Источники")}
        {nav("agents", "Агенты")}
      </aside>

      <main class={`main ${openCard() ? "with-panel" : ""}`}>
        <div>
          <Show when={error()}>
            <div class="error">
              <pre>{error()}</pre>
              <button class="btn quiet" onClick={() => setError("")}>×</button>
            </div>
          </Show>

          <Show when={tab() === "inbox"}><InboxView /></Show>
          <Show when={tab() === "work"}><WorkView /></Show>
          <Show when={tab() === "attention"}><AttentionView /></Show>
          <Show when={tab() === "flows"}><FlowsView /></Show>
          <Show when={tab() === "sources"}><SourcesView /></Show>
          <Show when={tab() === "agents"}><AgentsView /></Show>
        </div>

        {/* The card opens beside what you were looking at rather than instead
            of it: taking a card into work and watching where it went are the
            same moment. */}
        <Show when={openCard()}>
          <CardPanel />
        </Show>
      </main>
    </div>
  );
}
