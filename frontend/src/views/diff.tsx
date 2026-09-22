import { createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Diff, File } from "../../bindings/github.com/artipop/xxvi/internal/gitdiff/models";
import { list } from "../state";

// The diff screen: what changed in the card's working copy, read from the git
// that is already in that folder.
//
// This is the screen a review stage stands on. The stage itself runs nothing —
// it is a waiting point, and the two buttons that end it are on the strip
// beside this pane — so everything here is about reading: what changed, in
// which file, and enough of the surrounding lines to tell whether it is right.
//
// It is read when it is opened and when the person asks again, and never kept.
// The working copy is the answer, and a diff remembered anywhere would be a
// second answer to the question somebody is deciding by.
//
// A failure of git is not a failure of the screen: a card whose project is not
// a repository is an ordinary card, and the pane says so in the sentence the
// backend wrote rather than pushing it into the application's error bar, where
// it would read as something broken.

const STATUS: Record<string, string> = {
  added: "новый",
  deleted: "удалён",
  modified: "изменён",
  renamed: "переименован",
};

export default function DiffPane(props: { cardId: string; rev: string }): JSX.Element {
  const [diff, setDiff] = createSignal<Diff | null>(null);
  const [failed, setFailed] = createSignal("");
  const [busy, setBusy] = createSignal(true);
  // Which files the person has folded away. Everything opens unfolded: a review
  // that hides a file until it is asked for is a review of the files somebody
  // remembered to open.
  const [folded, setFolded] = createSignal<Record<string, boolean>>({});

  const read = async () => {
    setBusy(true);
    try {
      setDiff(await API.Diff(props.cardId, props.rev));
      setFailed("");
    } catch (e) {
      setFailed(e instanceof Error ? e.message : String(e));
      setDiff(null);
    } finally {
      setBusy(false);
    }
  };

  // An `async` callback cannot be an onSettled callback at all — its promise is
  // read as the cleanup — so the read is started here and awaited inside.
  onSettled(() => { void read(); });

  const files = () => list(diff()?.files);
  const added = () => files().reduce((n, f) => n + f.added, 0);
  const removed = () => files().reduce((n, f) => n + f.removed, 0);
  const fold = (path: string) => setFolded((was) => ({ ...was, [path]: !was[path] }));

  return (
    <div class="diff">
      <div class="diff-bar row">
        <span class="mono" title={diff()?.root}>
          {props.rev.trim() === "" ? "не закоммичено" : props.rev}
        </span>
        <Show when={files().length > 0}>
          <span class="meta">
            {files().length} файл(ов) <span class="plus">+{added()}</span>{" "}
            <span class="minus">−{removed()}</span>
          </span>
        </Show>
        <div class="spacer" />
        <button class="btn quiet tiny" onClick={() => void read()} disabled={busy()} title="Перечитать">↻</button>
      </div>

      <div class="diff-body">
        <Show when={!busy()} fallback={<div class="screen-note">Читаем изменения…</div>}>
          <Show when={failed() === ""} fallback={<div class="screen-note">{failed()}</div>}>
            <Show when={files().length > 0} fallback={<div class="screen-note">Изменений нет.</div>}>
              <For each={files()}>
                {(file) => <FileBlock file={file} folded={!!folded()[file.path]} onFold={() => fold(file.path)} />}
              </For>
              <Show when={diff()?.truncated}>
                <div class="screen-note">
                  Показано не всё: изменений больше, чем помещается на экран. Числа над файлами — настоящие.
                </div>
              </Show>
            </Show>
          </Show>
        </Show>
      </div>
    </div>
  );
}

function FileBlock(props: { file: File; folded: boolean; onFold: () => void }): JSX.Element {
  return (
    <section class="diff-file">
      <header class="diff-file-head" onClick={props.onFold}>
        <span class="tag">{STATUS[props.file.status] ?? props.file.status}</span>
        <span class="mono diff-path">
          <Show when={props.file.oldPath && props.file.oldPath !== props.file.path}>
            <span class="dim">{props.file.oldPath} → </span>
          </Show>
          {props.file.path}
        </span>
        <div class="spacer" />
        <span class="meta">
          <span class="plus">+{props.file.added}</span> <span class="minus">−{props.file.removed}</span>
        </span>
      </header>
      <Show when={!props.folded}>
        <Show when={!props.file.binary} fallback={<div class="screen-note">Двоичный файл — показывать нечего.</div>}>
          <For each={list(props.file.hunks)}>
            {(hunk) => (
              <>
                <div class="diff-hunk mono">
                  {hunk.header}
                  <Show when={hunk.heading}><span class="dim"> {hunk.heading}</span></Show>
                </div>
                <For each={list(hunk.lines)}>
                  {(line) => (
                    <div class={`diff-row ${line.kind}`}>
                      <span class="diff-no">{line.old || ""}</span>
                      <span class="diff-no">{line.new || ""}</span>
                      <span class="diff-text">{line.text}</span>
                    </div>
                  )}
                </For>
              </>
            )}
          </For>
        </Show>
      </Show>
    </section>
  );
}
