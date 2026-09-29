import { createSignal, For, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Attention } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
import type { CardView } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { attention, guard, loadAttention, openCardByID, showRibbon } from "../state";
import { questionText, say, t } from "../i18n";

// Everything waiting for a person, oldest first, and it is two things.
//
// A **question** is an ACP session stopped on one: it is answered here, and the
// same question also appears on its own card — they are one question, and
// answering either one lets the agent go on.
//
// A **waiting terminal** is a stage being worked in one whose CLI stopped for a
// person: its hooks said it is asking or its turn ended, or — for a CLI whose
// hooks never spoke — it has drawn nothing for a while. It carries no question
// because the agent asked inside its own interface, where the question was
// never ours to carry: the row says where to look, and the answer is typed
// where it was asked.
//
// A **working tree** is a closed card's separate copy of its repository, still
// on disk. Removing it is asked rather than done: somebody may still want to
// look in it, and it may hold what nobody committed.
//
// A **standing task** is a card in work that no agent is waiting on and still
// does not move: a review waiting for its verdict, a step that stopped with
// nowhere to go. It is answered on the task's own strip, where the diff and the
// buttons are, so that is where the row leads.

export default function AttentionView() {
  return (
    <>
      <h1>{t("attention.title")}</h1>
      <Show when={attention().length > 0} fallback={<div class="empty">{t("attention.empty")}</div>}>
        <For each={attention()}>{(a) => <Ask a={a} />}</For>
      </Show>
    </>
  );
}

function Ask(props: { a: Attention }) {
  return (
    <div class="card">
      <div class="row">
        <span class="title">{props.a.cardTitle || t("attention.card")}</span>
        <div class="spacer" />
        <Show when={props.a.agent}>
          <span class="tag warn"><span class="dot" />{props.a.agent}</span>
        </Show>
        <Show when={props.a.standing}
              fallback={<button class="btn quiet" onClick={() => openCardByID(props.a.cardId!)}>{t("attention.openCard")}</button>}>
          <button class="btn" onClick={() => showRibbon(props.a.cardId!)}>{t("attention.openTask")}</button>
        </Show>
      </div>
      <Show when={props.a.standing}>
        <div class="question">
          <div class="ask">{t(standingText(props.a.standing), { stage: props.a.stage ?? "" })}</div>
          <Show when={props.a.problem}><span class="meta">{say(props.a.problem)}</span></Show>
        </div>
      </Show>
      <Show when={!props.a.standing}>
      <Show when={!props.a.worktree} fallback={<WorktreeForm a={props.a} />}>
      <Show
        when={props.a.questionId}
        fallback={
          <div class="question">
            <div class="ask">{t(terminalWait(props.a))}</div>
          </div>
        }
      >
        <QuestionForm
          questionId={props.a.questionId}
          kind={props.a.kind}
          tool={props.a.tool}
          text={props.a.text ?? ""}
          options={props.a.options ?? []}
          freeText={props.a.freeText ?? false}
          onAnswered={loadAttention}
        />
      </Show>
      </Show>
      </Show>
    </div>
  );
}

function standingText(why: string | undefined): string {
  switch (why) {
    case "answer": return "attention.standAnswer";
    case "paused": return "attention.standPaused";
    default: return "attention.standStopped";
  }
}

function terminalWait(a: Attention): string {
  switch (a.terminal) {
    case "asking": return "attention.terminalAsking";
    default: return "attention.quiet";
  }
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
    <div class="question">
      <div class="ask">
        {t(props.a.dirty ? "attention.worktreeDirty" : "attention.worktree",
           { path: props.a.worktree, branch: props.a.branch })}
      </div>
      <div class="options">
        <button class={`btn ${props.a.dirty ? "danger" : "primary"}`} disabled={busy()}
                onClick={() => run(() => API.RemoveCardWorktree(props.a.cardId!, props.a.dirty ?? false))}>
          {props.a.dirty ? t("attention.removeWithChanges") : t("attention.removeTree")}
        </button>
        <button class="btn" disabled={busy()}
                onClick={() => run(() => API.KeepCardWorktree(props.a.cardId!))}>
          {t("attention.keep")}
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
  kind?: string;
  tool?: string;
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
    <div class="question">
      <div class="ask">{questionText(props.kind, props.tool, props.text)}</div>
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
          <input type="text" placeholder={t("notify.replyPlaceholder")} value={text()}
                 onInput={(e) => setText(e.currentTarget.value)}
                 onKeyDown={(e) => { if (e.key === "Enter" && text().trim()) void answer(); }} />
          <button class="btn primary" disabled={busy() || !text().trim()} onClick={() => answer()}>
            {t("notify.reply")}
          </button>
        </div>
      </Show>
      <div class="row actions">
        <div class="spacer" />
        <button class="btn quiet" disabled={busy()} onClick={decline}>{t("question.decline")}</button>
      </div>
    </div>
  );
}
