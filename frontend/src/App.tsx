import { onSettled, Show, For } from "solid-js";
import type { JSX } from "@solidjs/web";
import { error, loadAll, loadLanguage, setError, subscribe } from "./state";
import { t } from "./i18n";
import InboxView from "./views/inbox";
import WorkView from "./views/work";
import FlowsView from "./views/flows";
import AgentsView from "./views/agents";
import SourcesView from "./views/sources";
import ProjectsView from "./views/projects";
import AttentionView from "./views/attention";
import RibbonView from "./views/ribbon";
import SettingsView from "./views/settings";
import CardPanel from "./views/card";
import { openCard, tab, setTab } from "./state";
import { NAV } from "./nav";

export default function App(): JSX.Element {
  onSettled(() => {
    void loadAll();
    subscribe();
    void loadLanguage();
  });

  // The ribbon is not a document on a desk, so it does not sit on one: it takes
  // the whole window, the way the thing it is modelled on does. Everything else
  // keeps the sidebar. Esc is the way back, and the ribbon says so when empty.
  return (
    <Show
      when={tab() !== "ribbon"}
      fallback={
        <div class="shell full">
          <Show when={error()}>
            <div class="error floating">
              <pre>{error()}</pre>
              <button class="btn quiet" onClick={() => setError("")}>×</button>
            </div>
          </Show>
          <RibbonView />
        </div>
      }
    >
    <div class="shell">
      <aside class="sidebar">
        <div class="brand">XXVI</div>
        <For each={NAV}>
          {(item) => (
            <>
              <button class={`nav ${tab() === item.tab ? "on" : ""} ${item.apart ? "apart" : ""}`} onClick={() => setTab(item.tab)}>
                <span>{item.label()}</span>
                <Show when={item.count && item.count()! > 0}>
                  <span class={`count ${item.alert ? "alert" : ""}`}>{item.count!()}</span>
                </Show>
                <Show when={item.mark && item.mark()}>
                  <span class="mark" title={t("nav.updateMark")} />
                </Show>
              </button>
            </>
          )}
        </For>
        <div class="spacer" />
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
          <Show when={tab() === "projects"}><ProjectsView /></Show>
          <Show when={tab() === "sources"}><SourcesView /></Show>
          <Show when={tab() === "agents"}><AgentsView /></Show>
          <Show when={tab() === "settings"}><SettingsView /></Show>
        </div>

        {/* The card opens beside what you were looking at rather than instead
            of it: taking a card into work and watching where it went are the
            same moment. */}
        <Show when={openCard()}>
          <CardPanel />
        </Show>
      </main>
    </div>
    </Show>
  );
}
