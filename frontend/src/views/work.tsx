import { For, Show } from "solid-js";
import type { CardSummary } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { done, guard, inWork, list, loadDone, loadInWork, openCardByID, showRibbon } from "../state";
import { t, waitText } from "../i18n";
import { Marks } from "./marks";

// What is moving right now. Every row answers the same question the card screen
// answers in full: where it stands, and what it is waiting for.

export default function WorkView() {
  return (
    <>
      <h1>{t("work.title")}</h1>
      <p class="lede">{t("work.lede")}</p>

      <Show when={inWork().length > 0} fallback={<div class="empty">{t("work.empty")}</div>}>
        <For each={inWork()}>{(row) => <WorkRow row={row} />}</For>
      </Show>

      <Show when={done().length > 0}>
        <div class="list-head"><h2>{t("work.closed")}</h2><span class="meta">{done().length}</span></div>
        <For each={done().slice(0, 20)}>
          {(card) => (
            <div class="card clickable" onClick={() => openCardByID(card.id)}>
              <div class="row">
                <span class="title">{card.title}</span>
                <div class="spacer" />
                <span class="tag ok"><span class="dot" />{t("work.done")}</span>
                {/* What the steps left behind — terminals, diffs, notes — is on
                    the strip, not on the card: the card says that it finished,
                    the strip shows what was done. */}
                <button class="btn quiet tiny" onClick={(e) => { e.stopPropagation(); showRibbon(card.id); }}>
                  {t("common.toRibbon")}
                </button>
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
          <span class="tag warn"><span class="dot" />{t("work.asking")}</span>
        </Show>
        <Show when={flow()?.running}>
          <span class="tag accent"><span class="dot" />{t("common.agentWorking")}</span>
        </Show>
        <Show when={flow()?.queued}>
          <span class="tag"><span class="dot" />{t("work.queued")}</span>
        </Show>
        <span class="tag">{flow()?.flowName} · {stage()?.name ?? "—"}</span>
      </div>

      <Show when={(flow()?.waitingFor?.length ?? 0) > 0}>
        <div class="meta note">
          {t("work.waits", { what: list(flow()!.waitingFor).map(waitText).join("; ") })}
        </div>
      </Show>

      {/* The two answers a waiting stage asks for, in the list rather than only
          on the card. A row that says «waits for a person» and gives no way to
          answer it sends somebody into the card to press one of two buttons,
          and this list is where they are looking. */}
      <Show when={list(flow()?.marks).length > 0}>
        {/* Re-read rather than patch the row: the answer goes to the flow, the
            flow moves the card, and what comes back is a card standing
            somewhere else — with different answers, or none. */}
        <Marks
          cardId={props.row.card.id}
          marks={list(flow()?.marks)}
          answer={(fn) => void guard(async () => { await fn(); await Promise.all([loadInWork(), loadDone()]); })}
        />
      </Show>
    </div>
  );
}
