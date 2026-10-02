import { Events } from "@wailsio/runtime";
import { report } from "../state";
import { Open } from "../../bindings/github.com/artipop/gandvik/popupmenu";
import { nativeAvailable } from "./native";

export type PopupItem = { label: string; checked?: boolean; separator?: boolean; action?: () => void };
let serial = 0;
let active: { request: string; items: PopupItem[] } | undefined;
let closeWeb: (() => void) | undefined;

Events.On("popup-selected", (event) => {
  const data = event.data as { request: string; index: number };
  if (data.request !== active?.request) return;
  const item = active.items[data.index];
  active = undefined;
  item?.action?.();
});

export function popup(anchor: HTMLElement, items: PopupItem[]) {
  closeWeb?.();
  const request = String(++serial);
  active = { request, items };
  void nativeAvailable().then((native) => {
    if (active?.request !== request || !anchor.isConnected) return;
    if (!native) {
      showWeb(anchor, items, request);
      return;
    }
    const rect = anchor.getBoundingClientRect();
    return Open(request,
      items.map(({ label, checked, separator }) => ({ label, checked: !!checked, separator: !!separator })),
      rect.left, rect.bottom);
  }).catch(report);
}

function showWeb(anchor: HTMLElement, items: PopupItem[], request: string) {
  const menu = document.createElement("div");
  menu.className = "popup-menu";
  menu.setAttribute("role", "menu");
  const buttons: HTMLButtonElement[] = [];
  const close = () => {
    menu.remove();
    document.removeEventListener("pointerdown", away, true);
    document.removeEventListener("keydown", keys, true);
    window.removeEventListener("resize", close);
    window.removeEventListener("scroll", scrolled, true);
    window.removeEventListener("blur", close);
    anchor.setAttribute("aria-expanded", "false");
    if (active?.request === request) active = undefined;
    if (closeWeb === close) closeWeb = undefined;
  };
  const away = (e: PointerEvent) => { if (!menu.contains(e.target as Node)) close(); };
  const scrolled = (e: Event) => { if (!(e.target instanceof Node) || !menu.contains(e.target)) close(); };
  const keys = (e: KeyboardEvent) => {
    const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
    let next: number;
    switch (e.key) {
      case "Escape": close(); anchor.focus(); break;
      case "ArrowDown": next = (at + 1) % buttons.length; buttons[next]?.focus(); break;
      case "ArrowUp": next = (at - 1 + buttons.length) % buttons.length; buttons[next]?.focus(); break;
      case "Home": buttons[0]?.focus(); break;
      case "End": buttons[buttons.length - 1]?.focus(); break;
      case "Tab": close(); anchor.focus(); e.stopPropagation(); return;
      default: e.stopPropagation(); return;
    }
    e.preventDefault();
    e.stopPropagation();
  };
  for (const item of items) {
    if (item.separator) {
      const line = document.createElement("hr");
      line.setAttribute("role", "separator");
      menu.append(line);
      continue;
    }
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = item.label;
    button.setAttribute("role", "menuitem");
    if (item.checked) {
      button.className = "on";
      button.setAttribute("role", "menuitemradio");
      button.setAttribute("aria-checked", "true");
    }
    button.addEventListener("click", () => { close(); anchor.focus(); item.action?.(); });
    buttons.push(button);
    menu.append(button);
  }
  document.body.append(menu);
  const rect = anchor.getBoundingClientRect();
  menu.style.left = `${Math.max(4, Math.min(rect.left, window.innerWidth - menu.offsetWidth - 4))}px`;
  menu.style.top = `${Math.max(4, Math.min(rect.bottom, window.innerHeight - menu.offsetHeight - 4))}px`;
  anchor.setAttribute("aria-expanded", "true");
  closeWeb = close;
  document.addEventListener("pointerdown", away, true);
  document.addEventListener("keydown", keys, true);
  window.addEventListener("resize", close);
  window.addEventListener("scroll", scrolled, true);
  window.addEventListener("blur", close);
  (buttons.find((b) => b.classList.contains("on")) ?? buttons[0])?.focus({ preventScroll: true });
}
