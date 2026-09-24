import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Mark } from "../../bindings/github.com/artipop/xxvi/internal/model/models";
import { markTitle, t } from "../i18n";

// A waiting stage's two answers. Forward goes at once; back first asks what is
// wrong, because «not accepted» with no reason sends the agent — or the MR's
// author — into a second round that starts exactly like the first.
//
// The buttons and the form are separate pieces: on the ribbon the buttons sit
// in a header one line tall, and the form has to open below it.

export function MarkButtons(props: {
  marks: Mark[];
  tiny?: boolean;
  onMark: (mark: Mark) => void;
}): JSX.Element {
  return (
    <For each={props.marks}>
      {(mark) => (
        <button
          class={`btn ${props.tiny ? "tiny" : ""} ${mark.forward ? "primary" : "quiet"}`}
          title={markTitle(mark)}
          onClick={(e) => {
            e.stopPropagation();
            props.onMark(mark);
          }}
        >
          {mark.forward ? `${mark.stage} →` : `← ${mark.stage}`}
        </button>
      )}
    </For>
  );
}

export function RemarksForm(props: {
  mark: Mark;
  onSend: (remarks: string) => void;
  onCancel: () => void;
}): JSX.Element {
  const [text, setText] = createSignal("");
  let box: HTMLTextAreaElement | undefined;
  onSettled(() => { box?.focus({ preventScroll: true }); });
  return (
    <div class="remarks-form" onClick={(e) => e.stopPropagation()} onMouseDown={(e) => e.stopPropagation()}>
      <textarea
        ref={box}
        placeholder={t("remarks.placeholder")}
        value={text()}
        onInput={(e) => setText(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") { e.stopPropagation(); props.onCancel(); }
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) props.onSend(text());
        }}
      />
      <div class="row">
        <span class="meta">{t("remarks.hint")}</span>
        <div class="spacer" />
        <button class="btn quiet" onClick={props.onCancel}>{t("common.cancel")}</button>
        <button class="btn" onClick={() => props.onSend(text())}>{`← ${props.mark.stage}`}</button>
      </div>
    </div>
  );
}

/** sendMark answers the stage. Remarks go only with the answer that sends the
 *  work back; the backend drops them on a pass anyway. */
export function sendMark(cardId: string, mark: Mark, remarks = "") {
  return API.MarkOutcome(cardId, mark.value, mark.forward ? "" : remarks);
}

/** Marks is the buttons and the form together, for the places with room to
 *  open it right under them. */
export function Marks(props: {
  cardId: string;
  marks: Mark[];
  answer: (fn: () => Promise<any>) => void;
}): JSX.Element {
  const [back, setBack] = createSignal<Mark | null>(null);
  const send = (mark: Mark, remarks = "") => {
    setBack(null);
    props.answer(() => sendMark(props.cardId, mark, remarks));
  };
  return (
    <>
      <div class="row wrap actions">
        <MarkButtons marks={props.marks} onMark={(m) => (m.forward ? send(m) : setBack(m))} />
      </div>
      <Show when={back()}>
        <RemarksForm mark={back()!} onSend={(r) => send(back()!, r)} onCancel={() => setBack(null)} />
      </Show>
    </>
  );
}
