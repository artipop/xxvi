import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Attention } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
import { attention, guard, loadAttention, openCardByID } from "../state";

// Everything an agent is waiting to hear, oldest first. The same question also
// appears on its own card — they are one question, and answering either one
// lets the agent go on.

export default function AttentionView() {
  return (
    <>
      <h1>Требуют внимания</h1>
      <p class="lede">
        Агент остановился и ждёт человека. Ничего не решается таймером: без ответа
        агент услышит отказ и доработает без того, что просил.
      </p>
      <Show when={attention().length > 0} fallback={<div class="empty">Никто ничего не спрашивает.</div>}>
        <For each={attention()}>{(a) => <Ask a={a} />}</For>
      </Show>
    </>
  );
}

function Ask(props: { a: Attention }) {
  return (
    <div class="card">
      <div class="row">
        <span class="title">{props.a.cardTitle || "Карточка"}</span>
        <div class="spacer" />
        <span class="tag warn"><span class="dot" />{props.a.agent}</span>
        <button class="btn quiet" onClick={() => openCardByID(props.a.cardId!)}>Открыть карточку</button>
      </div>
      <QuestionForm
        questionId={props.a.questionId}
        text={props.a.text ?? ""}
        options={props.a.options ?? []}
        freeText={props.a.freeText ?? false}
        onAnswered={loadAttention}
      />
    </div>
  );
}

/**
 * QuestionForm is the one way a question is answered, wherever it is shown.
 * Both kinds of ACP question — "may I use this tool" and "which of these" —
 * arrive in the same shape, so they are answered by the same form.
 */
export function QuestionForm(props: {
  questionId: string;
  text: string;
  options: { id: string; label: string; description?: string }[];
  freeText: boolean;
  onAnswered?: () => void;
}) {
  const [text, setText] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const answer = async (optionId?: string) => {
    setBusy(true);
    await guard(() => API.Answer(props.questionId, {
      optionId: optionId ?? "",
      text: optionId ? "" : text(),
      declined: false,
    }));
    setBusy(false);
    setText("");
    props.onAnswered?.();
  };

  const decline = async () => {
    setBusy(true);
    await guard(() => API.Answer(props.questionId, { optionId: "", text: "", declined: true }));
    setBusy(false);
    props.onAnswered?.();
  };

  return (
    <div class="question" style={{ "margin-top": "10px" }}>
      <div class="ask">{props.text}</div>
      <div class="options">
        <For each={props.options}>
          {(opt) => (
            <button class="btn" disabled={busy()} onClick={() => answer(opt.id)}
                    title={opt.description ?? ""}>
              {opt.label}
            </button>
          )}
        </For>
      </div>
      <Show when={props.freeText}>
        <div class="row">
          <input type="text" placeholder="Ответить своими словами" value={text()}
                 onInput={(e) => setText(e.currentTarget.value)}
                 onKeyDown={(e) => { if (e.key === "Enter" && text().trim()) void answer(); }} />
          <button class="btn primary" disabled={busy() || !text().trim()} onClick={() => answer()}>
            Ответить
          </button>
        </div>
      </Show>
      <div class="row" style={{ "margin-top": "8px" }}>
        <span class="meta">Отказ агент воспримет как «нет» и продолжит без этого.</span>
        <div class="spacer" />
        <button class="btn quiet" disabled={busy()} onClick={decline}>Отказать</button>
      </div>
    </div>
  );
}
