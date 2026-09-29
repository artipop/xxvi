import { createSignal, For, onSettled, Show } from "solid-js";
import { chooseLanguage } from "../state";
import { choice, CHOICES, LANG_NAMES, systemLang, t, type Choice } from "../i18n";
import UpdatesSection from "./updates";
import { nativeAvailable, TERMINAL_ENGINE } from "./native";

// What is set once for this machine and then left alone. Each section keeps its
// own words and its own way of saving; this screen only puts them in one place.

export default function SettingsView() {
  const [native, setNative] = createSignal(false);
  const [engine, setEngine] = createSignal(readEngine());
  onSettled(() => { void nativeAvailable().then(setNative); });
  const chooseEngine = (value: string) => {
    setEngine(value);
    try { localStorage.setItem(TERMINAL_ENGINE, value); } catch { /* no storage: the default stands */ }
  };
  return (
    <div class="settings">
      <div class="row"><h1>{t("settings.title")}</h1></div>
      <p class="lede">{t("settings.lede")}</p>

      <section>
        <h2>{t("settings.interface")}</h2>
        <div class="settings-group">
          <div class="setting-row">
            <span class="setting-title">{t("settings.language")}</span>
            {/* A list rather than buttons: there will be more languages than fit
                side by side. Each is named in itself, so a person who cannot read
                the current one still finds theirs. */}
            <select value={choice()}
                    onChange={(e) => void chooseLanguage(e.currentTarget.value as Choice)}>
              <For each={CHOICES}>
                {(c) => (
                  <option value={c}>
                    {c === "system" ? t("lang.system", { name: LANG_NAMES[systemLang()] }) : LANG_NAMES[c]}
                  </option>
                )}
              </For>
            </select>
          </div>
          {/* Only where there is a choice: Ghostty's native library is on macOS. */}
          <Show when={native()}>
            <div class="setting-row">
              <span class="setting-title">{t("settings.terminal")}</span>
              <select value={engine()} onChange={(e) => chooseEngine(e.currentTarget.value)}>
                <option value="native">{t("settings.terminalNative")}</option>
                <option value="web">{t("settings.terminalWeb")}</option>
              </select>
            </div>
          </Show>
        </div>
        <p class="settings-note">{t("settings.languageHint")}</p>
        <Show when={native()}>
          <p class="settings-note">{t("settings.terminalHint")}</p>
        </Show>
      </section>

      <UpdatesSection />
    </div>
  );
}

function readEngine(): string {
  try { return localStorage.getItem(TERMINAL_ENGINE) === "web" ? "web" : "native"; } catch { return "native"; }
}
