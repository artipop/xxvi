import { createSignal, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { report } from "../state";
import { errorText, t } from "../i18n";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { nativeWanted } from "./native";
import { drawInPage } from "./xterm";

// A terminal screen: a real pty on the other end of a socket, drawn by Ghostty —
// the same emulator the holder keeps terminal screens in — in a native view
// laid over this pane (internal/nativeterm), or by xterm.js in the pane itself
// where Ghostty cannot draw (./xterm.ts). Two kinds of terminal arrive here
// and the pane does not tell them apart — a shell somebody opened on a screen,
// and the CLI a stage is being worked in — because from this side they are the
// same thing: bytes out, keystrokes in, and a size to say.
//
// Which one it is, is the `open` the caller hands over. A screen's terminal is
// started by opening it; a stage's terminal was started by the stage and is only
// connected to, and a caller that could start one would be a second way of
// working a step.
//
// With Ghostty, the pane stays in the page as a placeholder that keeps the
// layout. The view over it is moved to wherever the pane is drawn on every frame — the ribbon
// slides, and a view that followed only resizes would stay behind — and its
// size is what the process is told: Ghostty's bridge says it the way a terminal
// window does, from its own pty.

// FONT_SIZE is the terminal's size in points, and so how many columns a pane
// holds — the same for both engines.
const FONT_SIZE = 12;

export default function Terminal(props: {
  // open connects this pane to a terminal and hands back where its socket is.
  open: () => Promise<{ id: string; url: string }>;
  // ended is what to say when the process on the other end has gone. A shell
  // that exited and a step that is over are not the same news.
  ended?: string;
}): JSX.Element {
  const [status, setStatus] = createSignal<"opening" | "live" | "reconnecting" | "closed">("opening");
  const [error, setError] = createSignal("");
  let host: HTMLDivElement | undefined;
  let termId = "";

  // Setup and teardown in one block, which is what onSettled is for: the
  // cleanup is returned rather than registered, and an `async` callback cannot
  // be one at all — its promise would be read as the cleanup.
  onSettled(() => {
    let disposed = false;
    let socket: WebSocket | null = null;
    let stop: (() => void) | undefined;

    const start = async () => {
      const handle = await props.open();
      if (disposed) return;
      if (!(await nativeWanted())) {
        if (disposed || !host) return;
        const stopPage = await drawInPage(handle.url, host, FONT_SIZE, props.ended ?? t("terminal.shellEnded"), setStatus);
        if (disposed) {
          stopPage();
          return;
        }
        stop = stopPage;
        if (host.closest(".screen.on")) host.querySelector<HTMLElement>("textarea")?.focus({ preventScroll: true });
        return;
      }
      if (disposed) return;
      termId = handle.id;
      // The ribbon hands the keyboard to the terminal of the pane it stands on
      // and finds it by this; a pane focused before the terminal was up asks
      // for it here.
      if (host) host.dataset.term = handle.id;
      stop = followNatively(handle, () => host, () => disposed);
      setStatus("live");
      if (host?.closest(".screen.on")) void API.FocusNativeTerminal(handle.id);
      // Only to hear the end, and say it under the view: Ghostty draws, and its
      // bridge has a socket of its own. The view stays — it shows how the
      // terminal ended.
      socket = new WebSocket(handle.url);
      socket.onmessage = (ev) => {
        if (typeof ev.data !== "string") return;
        try {
          if (JSON.parse(ev.data).type === "exit") setStatus("closed");
        } catch { /* not a control message */ }
      };
    };

    start().catch((e) => { setError(errorText(e)); report(e); });

    return () => {
      disposed = true;
      socket?.close();
      stop?.();
    };
  });

  return (
    <div class="terminal">
      <Show when={error()}>
        <div class="screen-error">{error()}</div>
      </Show>
      {/* A click that reaches the pane rather than the view over it — the view
          was catching up with a sliding ribbon — still means «type here». */}
      <div class="terminal-host" ref={host} onMouseDown={() => { if (termId) void API.FocusNativeTerminal(termId); }} />
      <Show when={status() === "reconnecting"}>
        <div class="meta">{t("terminal.reconnecting")}</div>
      </Show>
      <Show when={status() === "closed"}>
        <div class="meta">{props.ended ?? t("terminal.shellEnded")}</div>
      </Show>
    </div>
  );
}

// The part of the pane the page shows: cut by the window and by every ancestor
// that clips — the ribbon's band, a scrolled stack.
function seenOf(el: HTMLElement, r: { left: number; top: number; width: number; height: number }) {
  const c = { left: Math.max(r.left, 0), top: Math.max(r.top, 0),
    right: Math.min(r.left + r.width, window.innerWidth), bottom: Math.min(r.top + r.height, window.innerHeight) };
  for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
    const cs = getComputedStyle(a);
    if (cs.overflowX === "visible" && cs.overflowY === "visible") continue;
    const ab = a.getBoundingClientRect();
    const left = ab.left + a.clientLeft, top = ab.top + a.clientTop;
    if (cs.overflowX !== "visible") {
      c.left = Math.max(c.left, left);
      c.right = Math.min(c.right, left + a.clientWidth);
    }
    if (cs.overflowY !== "visible") {
      c.top = Math.max(c.top, top);
      c.bottom = Math.min(c.bottom, top + a.clientHeight);
    }
  }
  return c;
}

function followNatively(handle: { id: string; url: string }, host: () => HTMLElement | undefined, gone: () => boolean): () => void {
  let last = "";
  let frame = 0;
  const tick = () => {
    if (gone()) return;
    const el = host();
    if (el) {
      // Inside the host's padding, as xterm.js draws: the view is not clipped
      // by the page, and its square edge laid on the pane's rounded border
      // looks like the terminal running off the bottom.
      const b = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      const pl = parseFloat(cs.paddingLeft) || 0, pr = parseFloat(cs.paddingRight) || 0;
      const pt = parseFloat(cs.paddingTop) || 0, pb = parseFloat(cs.paddingBottom) || 0;
      const r = { left: b.left + pl, top: b.top + pt, width: b.width - pl - pr, height: b.height - pt - pb };
      const c = seenOf(el, r);
      // Nothing of the page is checked for lying over the pane: the view is
      // above the whole page, and a menu opened over a terminal goes under
      // it. Accepted for now.
      const visible = r.width > 8 && r.height > 8 && c.right - c.left > 8 && c.bottom - c.top > 8;
      const dpr = window.devicePixelRatio || 1;
      const key = visible
        ? [r.left, r.top, r.width, r.height, c.left, c.top, c.right, c.bottom].map(Math.round).join(",") + "," + dpr
        : "hidden";
      if (key !== last) {
        last = key;
        if (visible) void API.ShowNativeTerminal(handle.id, handle.url, r.left, r.top, r.width, r.height,
          c.left, c.top, c.right - c.left, c.bottom - c.top, dpr, FONT_SIZE);
        else void API.HideNativeTerminal(handle.id);
      }
    }
    frame = requestAnimationFrame(tick);
  };
  frame = requestAnimationFrame(tick);
  return () => {
    cancelAnimationFrame(frame);
    void API.CloseNativeTerminal(handle.id);
  };
}
