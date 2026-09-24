import { createEffect, createMemo, createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { Diff, File } from "../../bindings/github.com/artipop/xxvi/internal/gitdiff/models";
import { list } from "../state";
import { errorText, label, plural, t } from "../i18n";

// The diff screen: what changed in the card's working copy, read from the git
// that is already in that folder.
//
// This is the screen a review stage stands on. Deliberately a reader and
// nothing else: no staging, no comment on a line, no approve button. How a step
// ended is one field on the card, answered by the two buttons the segment's own
// header already draws (docs/system.md §12.7), and a second way to say the same
// thing would be a second answer to one question.
//
// A failure of git is not a failure of the screen: a card whose project is not
// a repository is an ordinary card, and the pane says so itself rather than
// pushing it into the application's error bar, where it would read as
// something broken.

// How many lines make a file nobody opened this pane to read. A lock file, a
// generated bundle, a vendored blob: a real change that belongs in the list,
// but unfolded it buries the four lines somebody came for.
//
// A number rather than a guess at the path, because "generated" has no spelling
// anybody agrees on — package-lock.json, go.sum, a snapshot — and what actually
// makes a file unreadable here is its size.
const foldedFromLines = 200;

// How long the pane waits after the agent says something before reading again.
// The work moves while this is open — the agent writes, a step ends — and a
// diff answering for the moment it was opened quietly lies. But a session
// speaks many times a second, and git is a process: the answer is asked for
// once the talking stops.
const rereadAfterMs = 800;

export default function DiffPane(props: { cardId: string; rev: string }): JSX.Element {
  const [diff, setDiff] = createSignal<Diff | null>(null);
  const [failed, setFailed] = createSignal<unknown>(null);
  const [busy, setBusy] = createSignal(true);

  let reading = false;
  let timer: number | undefined;

  const read = async () => {
    if (reading) return;
    reading = true;
    setBusy(true);
    try {
      setDiff(await API.Diff(props.cardId, props.rev));
      setFailed(null);
    } catch (e) {
      // What is already on screen stays under the message: a folder that failed
      // to be read once has not stopped holding what it held.
      setFailed(e);
    } finally {
      reading = false;
      setBusy(false);
    }
  };

  const later = () => {
    if (timer) clearTimeout(timer);
    timer = window.setTimeout(() => { void read(); }, rereadAfterMs);
  };

  // An `async` callback cannot be an onSettled callback at all — its promise is
  // read as the cleanup — so the read is started here and awaited inside.
  onSettled(() => {
    void read();
    const off = Events.On("session", () => { later(); });
    return () => {
      if (timer) clearTimeout(timer);
      if (typeof off === "function") off();
    };
  });

  const files = () => list(diff()?.files);
  const added = () => files().reduce((n, f) => n + f.added, 0);
  const removed = () => files().reduce((n, f) => n + f.removed, 0);

  // Counters rather than one open/closed flag shared with every file: pressing
  // a button means "do this to all of them now", and what somebody folds by
  // hand afterwards has to stand.
  const [foldAll, setFoldAll] = createSignal(0);
  const [unfoldAll, setUnfoldAll] = createSignal(0);

  return (
    <div class="diff">
      <div class="diff-bar row">
        <span class="mono" title={diff()?.root}>{compared(props.rev, diff())}</span>
        <Show when={files().length > 0}>
          <span class="meta">
            {plural("diff.files", files().length)} <span class="plus">+{added()}</span>{" "}
            <span class="minus">−{removed()}</span>
          </span>
        </Show>
        <div class="spacer" />
        <Show when={files().length > 1}>
          <button class="btn quiet tiny" onClick={() => setFoldAll(foldAll() + 1)} title={t("diff.foldAll")}>⌃</button>
          <button class="btn quiet tiny" onClick={() => setUnfoldAll(unfoldAll() + 1)} title={t("diff.unfoldAll")}>⌄</button>
        </Show>
        <button class="btn quiet tiny" onClick={() => void read()} disabled={busy()} title={t("diff.reread")}>↻</button>
      </div>

      <div class="diff-body">
        <Show when={failed() !== null}>
          <div class="screen-note">{errorText(failed())}</div>
        </Show>
        <Show when={!busy() || diff()} fallback={<div class="screen-note">{t("diff.reading")}</div>}>
          <Show when={files().length > 0} fallback={<Show when={failed() === null}><div class="screen-note">{t("diff.none")}</div></Show>}>
            <For each={files()}>
              {(file) => <FileBlock file={file} foldAll={foldAll()} unfoldAll={unfoldAll()} />}
            </For>
            <Show when={diff()?.truncated}>
              <div class="screen-note">
                {t("diff.truncated")}
              </div>
            </Show>
          </Show>
        </Show>
      </div>
    </div>
  );
}

// What the pane compared, in the header. An empty ref on a card with a branch is
// the whole branch against where it left its base, and "not committed" there
// would describe the smaller half of what is on screen.
function compared(rev: string, diff: Diff | null): string {
  if (rev.trim() !== "") return rev;
  if (!diff?.base) return t("diff.uncommitted");
  return t("diff.branch", {
    branch: diff.branch || "?", base: diff.base, commits: plural("diff.commits", diff.commits ?? 0),
  });
}

function FileBlock(props: { file: File; foldAll: number; unfoldAll: number }): JSX.Element {
  const lines = createMemo(() =>
    list(props.file.hunks).reduce((n, h) => n + list(h.lines).length, 0));
  const [open, setOpen] = createSignal(lines() <= foldedFromLines);

  createEffect(() => props.foldAll, (fold) => { if (fold) setOpen(false); });
  createEffect(() => props.unfoldAll, (unfold) => { if (unfold) setOpen(true); });

  // A file with nothing to unfold — a binary one, an empty new file — is a row
  // and not a control: a chevron that opens nothing is a promise the row cannot
  // keep.
  const foldable = () => lines() > 0;

  return (
    <section class="diff-file">
      <header
        class={`diff-file-head ${foldable() ? "" : "flat"}`}
        onClick={() => foldable() && setOpen(!open())}
      >
        <span class="diff-chevron">{foldable() ? (open() ? "⌄" : "›") : ""}</span>
        <span class="tag">{label("fileStatus", props.file.status)}</span>
        <span class="mono diff-path">
          <Show when={props.file.oldPath && props.file.oldPath !== props.file.path}>
            <span class="dim">{props.file.oldPath} → </span>
          </Show>
          {props.file.path}
        </span>
        <Show when={props.file.binary}><span class="tag">{t("diff.binary")}</span></Show>
        <div class="spacer" />
        <span class="meta">
          <span class="plus">+{props.file.added}</span> <span class="minus">−{props.file.removed}</span>
        </span>
      </header>
      <Show when={open() && foldable()}>
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
                    {/* The numbers and the sign are their own columns and are
                        not selectable, so a patch copied out of the pane is
                        code and not code with a margin stuck to it. */}
                    <span class="diff-no">{line.old || ""}</span>
                    <span class="diff-no">{line.new || ""}</span>
                    <span class="diff-sign">
                      {line.kind === "add" ? "+" : line.kind === "del" ? "−" : ""}
                    </span>
                    <span class="diff-text">{line.text}</span>
                  </div>
                )}
              </For>
            </>
          )}
        </For>
      </Show>
    </section>
  );
}
