import { lazy, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { Notes } from "./views/notes";
import { followSide, resetNative } from "./views/native";
import { loadAll, loadLanguage, subscribe } from "./state";
import InboxView from "./views/inbox";
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
    resetNative();
    followSide();
    void loadAll();
    subscribe();
    void loadLanguage();
  });

  // The ribbon takes the whole window but the sidebar: the sidebar is narrow
  // enough to sit beside the job, and a way out that is always in sight beats
  // one folded behind a chevron. Esc still leads back to the inbox.
  //
  // So does the flow editor: its canvas is the page, and a column of padding
  // around it was width taken from the one thing on it.
  const whole = () => tab() === "ribbon" || tab() === "flows";
  const cardBeside = () => Boolean(openCard()) && tab() === "inbox";

  return (
    <div class="shell">
      <Show
        when={!whole()}
        fallback={
          <div class="full">
            {/* The ribbon gives the notes a line of their own: floating, they
                would go under a terminal Ghostty draws over the page. */}
            <Show when={tab() === "ribbon"} fallback={<>
              <div class="floating-notes"><Notes /></div>
              <FlowsView />
            </>}>
              <RibbonView />
            </Show>
          </div>
        }
      >
        <main class={`main ${cardBeside() ? "with-panel" : ""}`}>
          <div>
            <Notes />

            <Show when={tab() === "inbox"}><InboxView /></Show>
            <Show when={tab() === "attention"}><AttentionView /></Show>
            <Show when={tab() === "projects"}><ProjectsView /></Show>
            <Show when={tab() === "sources"}><SourcesView /></Show>
            <Show when={tab() === "agents"}><AgentsView /></Show>
            <Show when={tab() === "settings"}><SettingsView /></Show>
          </div>

          {/* The card opens beside the inbox rather than instead of it: taking
              a card into work and seeing what else is waiting are the same
              moment. Only there — see openCard. */}
          <Show when={cardBeside()}>
            <CardPanel />
          </Show>
        </main>
      </Show>
      <Sidebar />
    </div>
  );
}
