import { createSignal, Show } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { UpdateState } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import { loadUpdateState, updateState } from "../state";

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
    case "checking": return "Ищу версию новее…";
    case "available": return `Есть версия ${s.availableVersion ?? ""}`;
    case "downloading": return "Скачиваю…";
    case "verifying": return "Проверяю подпись…";
    case "installing": return "Устанавливаю…";
    case "ready": return `Версия ${s.availableVersion ?? ""} готова. Встанет при перезапуске.`;
    case "error": return "Обновиться не получилось";
    case "up-to-date": return "Это последняя версия";
    default:
      // Idle. What a previous run found is not carried over — only when it
      // looked — so an installation that has checked before says nothing here
      // and lets the date below speak. Saying «ещё не проверяли» directly above
      // the date it last looked is the application contradicting itself in two
      // adjacent lines.
      return s.lastCheckedAt ? "" : "Ещё не проверяли";
  }
}

// What went wrong, said in a way somebody can act on. The framework's own
// message ("dial tcp: lookup …: no such host") is kept below this, in small
// print — it is the half a bug report needs and the half nobody can do anything
// with.
function reason(s: UpdateState): string {
  switch (s.errorStage) {
    case "check": return "Не удалось достучаться до сервера обновлений.";
    case "download": return "Не удалось скачать обновление.";
    case "verify": return "Скачанное не сошлось с подписью и установлено не было.";
    case "install": return "Обновление скачалось, но установить его не удалось.";
    default: return "";
  }
}

function megabytes(bytes?: number): string {
  return bytes ? `${(bytes / (1024 * 1024)).toFixed(1)} МБ` : "";
}

export default function UpdatesView() {
  const [failed, setFailed] = createSignal("");
  const s = updateState;

  // Every action is «отправил и жду события»: what came of it arrives the same
  // way it would have arrived on a timer. Only a refusal that never got as far
  // as a step is reported here — and it is reported on this screen rather than
  // in the window's error banner, because it is about this screen and says so
  // in a sentence.
  const run = async (action: () => Promise<unknown>) => {
    setFailed("");
    try {
      await action();
    } catch (e) {
      setFailed(e instanceof Error ? e.message : String(e));
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
      <div class="row"><h1>Обновление</h1></div>
      <p class="lede">
        Приложение заменяет себя версией новее. Каждый выпуск подписан, и ставится
        только то, что сошлось с ключом, с которым эта сборка собрана.
      </p>

      <Show
        when={s().supported}
        fallback={
          <div class="empty">
            Эта сборка себя не обновляет{s().currentVersion ? `. Установлена версия ${s().currentVersion}` : ""}.
          </div>
        }
      >
        <div class="card">
          <div class="row">
            <span class="title">Версия {s().currentVersion}</span>
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
                Перезапустить и обновиться
              </button>
            </Show>
            <Show when={s().status === "available"}>
              <button class="btn primary" onClick={() => run(API.InstallUpdate)}>Установить</button>
              <button class="btn" onClick={() => run(API.SkipUpdate)}>Пропустить эту версию</button>
            </Show>
            <Show when={s().status !== "ready"}>
              <button class="btn" disabled={busy()} onClick={() => run(API.CheckForUpdate)}>
                Проверить
              </button>
            </Show>
            <div class="spacer" />
            <Show when={s().lastCheckedAt}>
              <span class="update-when">Смотрели {new Date(s().lastCheckedAt!).toLocaleString()}</span>
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
              <span class="title">Проверять самому</span>
              <span class="update-hint"> — раз в несколько часов. Ничего не скачивается, пока вы не попросите.</span>
            </span>
          </label>
        </div>

        <Show when={s().skippedVersion}>
          <p class="lede">
            Версия {s().skippedVersion} пропущена и больше не предлагается. Отменить это
            можно в {s().path || "updates.json"}.
          </p>
        </Show>
      </Show>
    </>
  );
}
