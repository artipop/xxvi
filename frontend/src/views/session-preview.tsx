import { createEffect, createSignal, onCleanup, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { PastSession } from "../../bindings/github.com/artipop/xxvi/internal/acp/models";
import { errorText, t, when } from "../i18n";

export function SessionPreview(props: { agentName: string; session: PastSession; onClose: () => void }): JSX.Element {
  const [text, setText] = createSignal<string | undefined>();
  const [error, setError] = createSignal("");
  let asked = 0;
  let close: HTMLButtonElement | undefined;
  onCleanup(() => { ++asked; });
  onSettled(() => { close?.focus({ preventScroll: true }); });

  createEffect(() => [props.agentName, props.session.id] as const, ([agent, id]) => {
    const n = ++asked;
    setText(undefined);
    setError("");
    API.PastSessionPreview(agent, id).then(
      (got) => { if (n === asked) setText(got); },
      (err) => { if (n === asked) setError(errorText(err)); },
    );
  });

  return (
    <section class="session-preview" aria-label={t("compose.previewSession")}>
      <header class="row">
        <div class="session-preview-heading">
          <h3>{props.session.title || props.session.id}</h3>
          <div class="meta">{props.agentName} · {when(props.session.updatedAt, false)} · {props.session.cwd}</div>
          <div class="meta">{t("compose.previewHint")}</div>
        </div>
        <button ref={close} class="btn quiet" onClick={props.onClose}>{t("common.close")}</button>
      </header>
      <Show when={error()}>
        <div class="screen-error" role="alert">{error()}</div>
      </Show>
      <Show when={!error() && text() === undefined}>
        <p class="empty loading"><span class="spinner" />{t("compose.previewLoading")}</p>
      </Show>
      <Show when={text() !== undefined}>
        <pre class="session-preview-history" tabindex={0}>{text()}</pre>
      </Show>
    </section>
  );
}
