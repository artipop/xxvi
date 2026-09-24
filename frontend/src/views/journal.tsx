import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { JournalEntry } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { guard, list } from "../state";
import { entryText, has, t, when } from "../i18n";

// The journal is the audit — who, what, when — and nobody watches the work by
// it: the ribbon is for that. So it is one flat list at one weight, opened only
// by somebody who went looking, and nothing in it is set to catch the eye.

// JournalOf is the journal of a card nothing else on screen has read — the
// ribbon's, which carries no journal of its own.
export function JournalOf(props: { cardId: string }): JSX.Element {
  const [entries, setEntries] = createSignal<JournalEntry[]>([]);

  const pull = async () => {
    const got = await guard(() => API.Journal(props.cardId));
    if (got !== undefined) setEntries(list(got));
  };

  // Asked again on every event rather than filtered by card: the events carry
  // no content, and the read is one indexed query.
  onSettled(() => {
    void pull();
    const offs = [Events.On("card", () => { void pull(); }), Events.On("session", () => { void pull(); })];
    return () => offs.forEach((off) => { if (typeof off === "function") off(); });
  });

  return <JournalList entries={entries()} />;
}

export function JournalList(props: { entries: JournalEntry[] }): JSX.Element {
  return (
    <Show when={props.entries.length > 0} fallback={<div class="empty">{t("journal.empty")}</div>}>
      <ol class="journal-list">
        <For each={props.entries}>
          {(e) => (
            <li>
              <div class="journal-head">
                <span>{when(e.createdAt)}</span>
                <span>{e.author || t("journal.app")}</span>
                <span>{has(`entry.${e.kind}`) ? t(`entry.${e.kind}`) : t("entry._")}</span>
              </div>
              <div class="journal-text">{entryText(e)}</div>
            </li>
          )}
        </For>
      </ol>
    </Show>
  );
}
