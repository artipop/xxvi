import { createSignal, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { UpdateState } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { loadUpdateState, updateState } from "../state";
import { errorText, t, when } from "../i18n";

// Replacing this application with a newer one, in its own words. A section of
// the settings rather than a screen: it is set up once and then left alone.
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

export default function UpdatesSection() {
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

  const offering = () => s().status === "available" || s().status === "ready";
  const extra = () =>
    offering() || (s().status === "downloading" && s().sizeBytes) || reason(s()) || s().error || failed();

  return (
    <section>
      <h2>{t("updates.title")}</h2>

      <Show
        when={s().supported}
        fallback={
          <div class="settings-group">
            <div class="setting-row">
              <span class="hint">
                {s().currentVersion
                  ? t("updates.unsupportedVersion", { version: s().currentVersion })
                  : t("updates.unsupported")}
              </span>
            </div>
          </div>
        }
      >
        <div class="settings-group">
          <div class="setting-row">
            <div class="setting-label">
              <span class="setting-title">{t("updates.version", { version: s().currentVersion })}</span>
              <span class="hint update-line">
                <Show when={headline(s())}>
                  <span class={`update-status--${s().status}`}>{headline(s())}</span>
                </Show>
                <Show when={s().status === "available" && s().sizeBytes}>
                  <span>{megabytes(s().sizeBytes)}</span>
                </Show>
                <Show when={s().lastCheckedAt}>
                  <span>{t("updates.checkedAt", { when: when(s().lastCheckedAt, false) })}</span>
                </Show>
              </span>
            </div>
            <Show when={s().status !== "ready"}>
              <button class="btn" disabled={busy()} onClick={() => run(API.CheckForUpdate)}>
                {t("updates.check")}
              </button>
            </Show>
          </div>

          <Show when={extra()}>
            <div class="setting-block">
              <Show when={s().status === "downloading" && s().sizeBytes}>
                <div class="update-progress"><div class="update-progress__bar" style={{ width: `${percent()}%` }} /></div>
              </Show>

              {/* The release notes are the tag's own annotation, written for
                  whoever is about to install it. Shown only while there is
                  something to install: after that they describe what is already
                  here. */}
              <Show when={s().notes && offering()}>
                <pre class="update-notes">{s().notes}</pre>
              </Show>

              <Show when={reason(s())}>
                <div class="warn-note">{reason(s())}</div>
              </Show>
              <Show when={s().error || failed()}>
                <div class="update-detail mono">{s().error || failed()}</div>
              </Show>

              <Show when={offering()}>
                <div class="row">
                  <Show when={s().status === "ready"}>
                    <button class="btn primary" onClick={() => run(API.RestartToUpdate)}>
                      {t("updates.restart")}
                    </button>
                  </Show>
                  <Show when={s().status === "available"}>
                    <button class="btn primary" onClick={() => run(API.InstallUpdate)}>{t("updates.install")}</button>
                    <button class="btn quiet" onClick={() => run(API.SkipUpdate)}>{t("updates.skip")}</button>
                  </Show>
                </div>
              </Show>
            </div>
          </Show>

          <label class="setting-row">
            <span class="setting-title">{t("updates.auto")}</span>
            <input
              type="checkbox"
              class="switch"
              checked={s().enabled}
              onChange={(e) => run(() => API.SetUpdatesEnabled(e.currentTarget.checked))}
            />
          </label>
        </div>

        <p class="settings-note">{t("updates.lede")}</p>
        <Show when={s().skippedVersion}>
          <p class="settings-note">
            {t("updates.skipped", { version: s().skippedVersion, path: s().path || "updates.json" })}
          </p>
        </Show>
      </Show>
    </section>
  );
}
