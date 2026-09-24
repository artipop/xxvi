import { createSignal, For, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { t } from "./i18n";
import { Icon } from "./icons";
import { NAV } from "./nav";
import { setTab, tab } from "./state";

// Narrow by default: on every screen, the ribbon included, so leaving a
// section never takes a menu to be opened first. Names come as pills over the
// work on hover, and a click on the top button spreads the sidebar out when
// the icons are not enough.
const KEY = "xxvi.sidebar.wide";

function rememberedWide(): boolean {
  try { return localStorage.getItem(KEY) === "1"; } catch { return false; }
}

export default function Sidebar(): JSX.Element {
  const [wide, setWide] = createSignal(rememberedWide());

  const toggle = () => {
    const next = !wide();
    setWide(next);
    try { localStorage.setItem(KEY, next ? "1" : "0"); } catch { /* the choice lasts this run */ }
  };

  return (
    <aside class={`sidebar ${wide() ? "wide" : ""}`}>
      <div class="sidebar-head">
        <Show when={wide()}><span class="brand">XXVI</span></Show>
        <button class="nav toggle" onClick={toggle} aria-label={wide() ? t("nav.collapse") : t("nav.expand")}>
          <Icon name={wide() ? "collapse" : "expand"} />
          <span class="tip">{t("nav.expand")}</span>
        </button>
      </div>
      <For each={NAV}>
        {(item) => (
          <button
            class={`nav ${tab() === item.tab ? "on" : ""} ${item.apart ? "apart" : ""}`}
            onClick={() => setTab(item.tab)}
            aria-label={item.label()}
          >
            <Icon name={item.icon} />
            <span class="nav-label">{item.label()}</span>
            <Show when={item.count && item.count()! > 0}>
              <span class={`count ${item.alert ? "alert" : ""}`}>{item.count!()}</span>
            </Show>
            <Show when={item.mark && item.mark()}>
              <span class="mark" />
            </Show>
            <span class="tip">
              {item.label()}
              <Show when={item.mark && item.mark()}><span class="tip-note">{t("nav.updateMark")}</span></Show>
            </span>
          </button>
        )}
      </For>
    </aside>
  );
}
