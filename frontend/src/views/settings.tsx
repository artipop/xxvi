import { For } from "solid-js";
import { chooseLanguage } from "../state";
import { choice, CHOICES, LANG_NAMES, systemLang, t, type Choice } from "../i18n";
import UpdatesSection from "./updates";

// What is set once for this machine and then left alone. Each section keeps its
// own words and its own way of saving; this screen only puts them in one place.

export default function SettingsView() {
  return (
    <>
      <div class="row"><h1>{t("settings.title")}</h1></div>
      <p class="lede">{t("settings.lede")}</p>

      <h2>{t("settings.language")}</h2>
      <p class="lede">{t("settings.languageHint")}</p>
      {/* A list rather than buttons: there will be more languages than fit
          side by side. Each is named in itself, so a person who cannot read
          the current one still finds theirs. */}
      <select class="settings-field" value={choice()}
              onChange={(e) => void chooseLanguage(e.currentTarget.value as Choice)}>
        <For each={CHOICES}>
          {(c) => (
            <option value={c}>
              {c === "system" ? t("lang.system", { name: LANG_NAMES[systemLang()] }) : LANG_NAMES[c]}
            </option>
          )}
        </For>
      </select>

      <UpdatesSection />
    </>
  );
}
