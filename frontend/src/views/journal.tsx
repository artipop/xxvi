import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { JournalEntry } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { guard, list } from "../state";

// The journal is the audit — who, what, when — and nobody watches the work by
// it: the ribbon is for that. So it is one flat list at one weight, opened only
// by somebody who went looking, and nothing in it is set to catch the eye.

const KIND: Record<string, string> = {
  move: "ход", problem: "сбой", report: "итог", ask: "вопрос",
  props: "свойства", source: "источник",
};

export function JournalList(props: { cardId: string }): JSX.Element {
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

  return (
    <Show when={entries().length > 0} fallback={<div class="empty">Пока ничего не происходило.</div>}>
      <ol class="journal-list">
        <For each={entries()}>
          {(e) => (
            <li>
              <div class="journal-head">
                <span>{when(e.createdAt)}</span>
                <span>{e.author || "приложение"}</span>
                <span>{KIND[e.kind] ?? "запись"}</span>
              </div>
              <div class="journal-text">{e.text}</div>
            </li>
          )}
        </For>
      </ol>
    </Show>
  );
}

function when(value: any): string {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
