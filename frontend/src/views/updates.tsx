import { createSignal, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { UpdateState } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { loadUpdateState, updateState } from "../state";
import { errorText, t, when } from "../i18n";

// Replacing this application with a newer one, in its own words.
//
// The framework has a window of its own for this and it is not used: it is
// hard-coded English. What the framework does keep is the part worth keeping —
// the download, the signature check, and the helper that swaps the bundle while
// the application is gone.
//
// Nothing here polls. Every step says that something moved, and this screen
// then asks — the same road as every other screen, so a check that started on a
// timer draws exactly what one pressed here does.

// What a person is told is happening. Read off `status` rather than off which
// button was last pressed: a download that finished while this screen was
// closed is already finished when it opens.
function headline(s: UpdateState): string {
  switch (s.status) {
    case "checking": return t("updates.checking");
    case "available": return t("updates.available", { version: s.availableVersion });
    case "downloading": return t("updates.downloading");
    case "verifying": return t("updates.verifying");
    case "installing": return t("updates.installing");
    case "ready": return t("updates.ready", { version: s.availableVersion });
    case "error": return t("updates.error");
    case "up-to-date": return t("updates.upToDate");
    default:
      // Idle. What a previous run found is not carried over — only when it
      // looked — so an installation that has checked before says nothing here
      // and lets the date below speak. Saying «not checked yet» directly above
      // the date it last looked is the application contradicting itself in two
      // adjacent lines.
      return s.lastCheckedAt ? "" : t("updates.neverChecked");
  }
}

// What went wrong, said in a way somebody can act on. The framework's own
// message ("dial tcp: lookup …: no such host") is kept below this, in small
// print — it is the half a bug report needs and the half nobody can do anything
// with.
function reason(s: UpdateState): string {
  switch (s.errorStage) {
    case "check": return t("updates.failCheck");
    case "download": return t("updates.failDownload");
    case "verify": return t("updates.failVerify");
    case "install": return t("updates.failInstall");
    default: return "";
  }
}

function megabytes(bytes?: number): string {
  return bytes ? t("updates.megabytes", { n: (bytes / (1024 * 1024)).toFixed(1) }) : "";
}

export default function UpdatesView() {
  const [failed, setFailed] = createSignal("");
  const s = updateState;

  // Every action is «sent, now wait for the event»: what came of it arrives the same
  // way it would have arrived on a timer. Only a refusal that never got as far
  // as a step is reported here — and it is reported on this screen rather than
  // in the window's error banner, because it is about this screen and says so
  // in a sentence.
  const run = async (action: () => Promise<unknown>) => {
    setFailed("");
    try {
      await action();
    } catch (e) {
      setFailed(errorText(e));
      // A refusal fires no event, so nothing would re-read the state — and the
      // checkbox would stay where the pointer left it rather than where it is.
      void loadUpdateState();
    }
  };

  const busy = () => ["checking", "downloading", "verifying", "installing"].includes(s().status);

  // 0–100 with no total is a bar that sits at zero for the whole download,
  // which reads as stuck. Without a size there is no bar.
  const percent = () => {
    const { sizeBytes, downloaded } = s();
    if (!sizeBytes || !downloaded) return 0;
    return Math.min(100, Math.round((downloaded / sizeBytes) * 100));
  };

  return (
    <>
      <div class="row"><h1>{t("updates.title")}</h1></div>
      <p class="lede">{t("updates.lede")}</p>

      <Show
        when={s().supported}
        fallback={
          <div class="empty">
            {s().currentVersion
              ? t("updates.unsupportedVersion", { version: s().currentVersion })
              : t("updates.unsupported")}
          </div>
        }
      >
        <div class="card">
          <div class="row">
            <span class="title">{t("updates.version", { version: s().currentVersion })}</span>
            <div class="spacer" />
            <Show when={s().status === "available" && s().sizeBytes}>
              <span class="tag">{megabytes(s().sizeBytes)}</span>
            </Show>
          </div>

          <Show when={headline(s())}>
            <div class={`update-status update-status--${s().status}`}>{headline(s())}</div>
          </Show>

          <Show when={s().status === "downloading" && s().sizeBytes}>
            <div class="update-progress"><div class="update-progress__bar" style={{ width: `${percent()}%` }} /></div>
          </Show>

          {/* The release notes are the tag's own annotation, written for
              whoever is about to install it. Shown only while there is
              something to install: after that they describe what is already
              here. */}
          <Show when={s().notes && (s().status === "available" || s().status === "ready")}>
            <pre class="update-notes">{s().notes}</pre>
          </Show>

          <Show when={reason(s())}>
            <div class="warn-note">{reason(s())}</div>
          </Show>
          <Show when={s().error || failed()}>
            <div class="update-detail mono">{s().error || failed()}</div>
          </Show>

          <div class="row">
            <Show when={s().status === "ready"}>
              <button class="btn primary" onClick={() => run(API.RestartToUpdate)}>
                {t("updates.restart")}
              </button>
            </Show>
            <Show when={s().status === "available"}>
              <button class="btn primary" onClick={() => run(API.InstallUpdate)}>{t("updates.install")}</button>
              <button class="btn" onClick={() => run(API.SkipUpdate)}>{t("updates.skip")}</button>
            </Show>
            <Show when={s().status !== "ready"}>
              <button class="btn" disabled={busy()} onClick={() => run(API.CheckForUpdate)}>
                {t("updates.check")}
              </button>
            </Show>
            <div class="spacer" />
            <Show when={s().lastCheckedAt}>
              <span class="update-when">{t("updates.checkedAt", { when: when(s().lastCheckedAt, false) })}</span>
            </Show>
          </div>
        </div>

        <div class="card">
          <label class="row">
            <input
              type="checkbox"
              checked={s().enabled}
              onChange={(e) => run(() => API.SetUpdatesEnabled(e.currentTarget.checked))}
            />
            <span>
              <span class="title">{t("updates.auto")}</span>
              <span class="update-hint">{t("updates.autoHint")}</span>
            </span>
          </label>
        </div>

        <Show when={s().skippedVersion}>
          <p class="lede">
            {t("updates.skipped", { version: s().skippedVersion, path: s().path || "updates.json" })}
          </p>
        </Show>
      </Show>
    </>
  );
}
