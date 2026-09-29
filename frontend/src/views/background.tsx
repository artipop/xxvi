import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { BackgroundTerminal } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { guard, list } from "../state";
import { plural, t } from "../i18n";

/** BackgroundNotice says what the last run of the application left running.
 *  Closing it leaves shells and started projects running on purpose, and this
 *  is where somebody who forgot them finds out, once, at the start. */
export function BackgroundNotice(props: { floating?: boolean }): JSX.Element {
  const [left, setLeft] = createSignal<BackgroundTerminal[]>([]);
  const [hidden, setHidden] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  onSettled(() => { void guard(() => API.BackgroundTerminals()).then((v) => setLeft(list(v))); });

  const stop = async () => {
    setBusy(true);
    await guard(() => API.StopBackgroundTerminals());
    setLeft(list(await guard(() => API.BackgroundTerminals())));
    setBusy(false);
  };
  const what = (b: BackgroundTerminal) =>
    [b.cardTitle, b.command || t("background.shell")].filter(Boolean).join(" — ");

  return (
    <Show when={!hidden() && left().length > 0}>
      <div class={`notice ${props.floating ? "floating" : ""}`}>
        <div class="notice-text">
          <b>{plural("background.running", left().length)}</b>
          <span class="meta">
            <For each={left().slice(0, 3)}>{(b, i) => <>{i() > 0 ? ", " : ""}{what(b)}</>}</For>
            <Show when={left().length > 3}>{` ${t("background.more", { n: left().length - 3 })}`}</Show>
          </span>
        </div>
        <button class="btn tiny" disabled={busy()} onClick={() => void stop()}>{t("background.stop")}</button>
        <button class="btn quiet tiny" onClick={() => setHidden(true)}>{t("background.hide")}</button>
      </div>
    </Show>
  );
}
