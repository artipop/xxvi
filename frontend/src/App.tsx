import { lazy, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { BackgroundNotice } from "./views/background";
import { error, loadAll, loadLanguage, setError, subscribe } from "./state";
import InboxView from "./views/inbox";
import WorkView from "./views/work";
import AgentsView from "./views/agents";
import SourcesView from "./views/sources";
import ProjectsView from "./views/projects";
import AttentionView from "./views/attention";
import SettingsView from "./views/settings";
import CardPanel from "./views/card";
import { openCard, tab } from "./state";
import Sidebar from "./sidebar";

const FlowsView = lazy(() => import("./views/flows"));
const RibbonView = lazy(() => import("./views/ribbon"));

export default function App(): JSX.Element {
  onSettled(() => {
    void loadAll();
    subscribe();
    void loadLanguage();
  });

  // The ribbon takes the whole window but the sidebar: the sidebar is narrow
  // enough to sit beside the job, and a way out that is always in sight beats
  // one folded behind a chevron. Esc still leads back to the inbox.
  return (
    <div class="shell">
      <Show
        when={tab() !== "ribbon"}
        fallback={
          <div class="full">
            <Show when={error()}>
              <div class="error floating">
                <pre>{error()}</pre>
                <button class="btn quiet" onClick={() => setError("")}>×</button>
              </div>
            </Show>
            <BackgroundNotice floating />
            <RibbonView />
          </div>
        }
      >
        <main class={`main ${openCard() ? "with-panel" : ""}`}>
          <div>
            <Show when={error()}>
              <div class="error">
                <pre>{error()}</pre>
                <button class="btn quiet" onClick={() => setError("")}>×</button>
              </div>
            </Show>
            <BackgroundNotice />

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
      </Show>
      <Sidebar />
    </div>
  );
}
