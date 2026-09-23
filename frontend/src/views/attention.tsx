import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Attention } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
import type { CardView } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { attention, guard, loadAttention, openCardByID } from "../state";

// Everything waiting for a person, oldest first, and it is two things.
//
// A **question** is an ACP session stopped on one: it is answered here, and the
// same question also appears on its own card — they are one question, and
// answering either one lets the agent go on.
//
// A **silent terminal** is a stage being worked in one whose CLI has drawn
// nothing for a while. It carries no question because the agent asked inside its
// own interface, where the question was never ours to carry: the row says where
// to look, and the answer is typed where it was asked.
//
// A **working tree** is a closed card's separate copy of its repository, still
// on disk. Removing it is asked rather than done: somebody may still want to
// look in it, and it may hold what nobody committed.

export default function AttentionView() {
  return (
    <>
      <h1>Требуют внимания</h1>
      <p class="lede">
        Агент остановился и ждёт человека, или закрытая задача оставила рабочее
        дерево. Ничего не решается таймером: без ответа агент услышит отказ и
        доработает без того, что просил.
      </p>
      <Show when={attention().length > 0} fallback={<div class="empty">Никто ничего не ждёт.</div>}>
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
        <Show when={props.a.agent}>
          <span class="tag warn"><span class="dot" />{props.a.agent}</span>
        </Show>
        <button class="btn quiet" onClick={() => openCardByID(props.a.cardId!)}>Открыть карточку</button>
      </div>
      <Show when={!props.a.worktree} fallback={<WorktreeForm a={props.a} />}>
      <Show
        when={props.a.questionId}
        fallback={
          <div class="question" style={{ "margin-top": "10px" }}>
            <div class="ask">{props.a.text}</div>
            <span class="meta">
              Отвечать здесь нечего: агент спросил в своём терминале, там же и ответ.
            </span>
          </div>
        }
      >
        <QuestionForm
          questionId={props.a.questionId}
          text={props.a.text ?? ""}
          options={props.a.options ?? []}
          freeText={props.a.freeText ?? false}
          onAnswered={loadAttention}
        />
      </Show>
      </Show>
    </div>
  );
}

/** WorktreeForm answers a closed card's working tree: remove it or keep it.
 *  Shown in the attention list and on the card itself — one question. */
export function WorktreeForm(props: { a: Attention; onAnswered?: (v: CardView | undefined) => void }) {
  const [busy, setBusy] = createSignal(false);
  const run = async (call: () => Promise<CardView>) => {
    setBusy(true);
    const view = await guard(call);
    setBusy(false);
    await loadAttention();
    props.onAnswered?.(view);
  };
  return (
    <div class="question" style={{ "margin-top": "10px" }}>
      <div class="ask">{props.a.text}</div>
      <div class="options">
        <button class={`btn ${props.a.dirty ? "danger" : "primary"}`} disabled={busy()}
                onClick={() => run(() => API.RemoveCardWorktree(props.a.cardId!, props.a.dirty ?? false))}>
          {props.a.dirty ? "Удалить вместе с изменениями" : "Удалить дерево"}
        </button>
        <button class="btn" disabled={busy()}
                onClick={() => run(() => API.KeepCardWorktree(props.a.cardId!))}>
          Оставить
        </button>
      </div>
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
