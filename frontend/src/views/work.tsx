import { For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { CardSummary } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { done, guard, inWork, list, loadDone, loadInWork, openCardByID } from "../state";

// What is moving right now. Every row answers the same question the card screen
// answers in full: where it stands, and what it is waiting for.

export default function WorkView() {
  return (
    <>
      <h1>В работе</h1>
      <p class="lede">Карточки, едущие по флоу. Нажмите, чтобы открыть.</p>

      <Show when={inWork().length > 0} fallback={<div class="empty">Никто никуда не едет.</div>}>
        <For each={inWork()}>{(row) => <WorkRow row={row} />}</For>
      </Show>

      <Show when={done().length > 0}>
        <div class="list-head"><h2>Закрытые</h2><span class="meta">{done().length}</span></div>
        <For each={done().slice(0, 20)}>
          {(card) => (
            <div class="card clickable" onClick={() => openCardByID(card.id)}>
              <div class="row">
                <span class="title">{card.title}</span>
                <div class="spacer" />
                <span class="tag ok"><span class="dot" />готово</span>
              </div>
            </div>
          )}
        </For>
      </Show>
    </>
  );
}

function WorkRow(props: { row: CardSummary }) {
  const flow = () => props.row.flow;
  const stage = () => list(flow()?.stages).find((s) => s.current);

  return (
    <div class="card clickable" onClick={() => openCardByID(props.row.card.id)}>
      <div class="row wrap">
        <span class="title">{props.row.card.title}</span>
        <div class="spacer" />

        {/* Asking is the one state a card cannot be inferred to be in: it
            happens inside a session nobody may be watching. */}
        <Show when={props.row.asking}>
          <span class="tag warn"><span class="dot" />ждёт ответа</span>
        </Show>
        <Show when={flow()?.running}>
          <span class="tag accent"><span class="dot" />агент работает</span>
        </Show>
        <Show when={flow()?.queued}>
          <span class="tag"><span class="dot" />ждёт места</span>
        </Show>
        <span class="tag">{flow()?.flowName} · {stage()?.name ?? "—"}</span>
      </div>

      <Show when={(flow()?.waitingFor?.length ?? 0) > 0}>
        <div class="meta" style={{ "margin-top": "6px" }}>
          Ждёт: {flow()!.waitingFor!.join("; ")}
        </div>
      </Show>

      {/* The two answers a waiting stage asks for, in the list rather than only
          on the card. A row that says «ждёт ответа человека» and gives no way to
          answer it sends somebody into the card to press one of two buttons,
          and this list is where they are looking. */}
      <Show when={list(flow()?.marks).length > 0}>
        <div class="row wrap" style={{ "margin-top": "8px" }}>
          <For each={list(flow()?.marks)}>
            {(mark) => (
              <button
                class={`btn ${mark.forward ? "primary" : "quiet"}`}
                title={`Отметить «${mark.value}» — карточка уедет в «${mark.stage}»`}
                onClick={(e) => {
                  // The row itself opens the card; the button answers it. Both
                  // on one element would make the answer a way to open the card
                  // that also changed it.
                  e.stopPropagation();
                  void guard(async () => {
                    await API.MarkOutcome(props.row.card.id, mark.value);
                    // Re-read rather than patch the row: the answer goes to the
                    // flow, the flow moves the card, and what comes back is a
                    // card standing somewhere else — with different answers, or
                    // none. The card is not opened, only the list refreshed.
                    await Promise.all([loadInWork(), loadDone()]);
                  });
                }}
              >
                {mark.forward ? `${mark.stage} →` : `← ${mark.stage}`}
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}
