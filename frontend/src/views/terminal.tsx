import { createSignal, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { report } from "../state";
import { errorText, t } from "../i18n";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { nativeAvailable } from "./native";

// A terminal screen: a real pty on the other end of a socket, drawn by Ghostty —
// the same emulator the holder keeps terminal screens in — in a native view
// laid over this pane (internal/nativeterm). Two kinds of terminal arrive here
// and the pane does not tell them apart — a shell somebody opened on a screen,
// and the CLI a stage is being worked in — because from this side they are the
// same thing: bytes out, keystrokes in, and a size to say.
//
// Which one it is, is the `open` the caller hands over. A screen's terminal is
// started by opening it; a stage's terminal was started by the stage and is only
// connected to, and a caller that could start one would be a second way of
// working a step.
//
// The pane stays in the page as a placeholder that keeps the layout. The view
// over it is moved to wherever the pane is drawn on every frame — the ribbon
// slides, and a view that followed only resizes would stay behind — and its
// size is what the process is told: Ghostty's bridge says it the way a terminal
// window does, from its own pty.

// FONT_SIZE is the terminal's size in points, and so how many columns a pane
// holds.
const FONT_SIZE = 12;

export default function Terminal(props: {
  // open connects this pane to a terminal and hands back where its socket is.
  open: () => Promise<{ id: string; url: string }>;
  // ended is what to say when the process on the other end has gone. A shell
  // that exited and a step that is over are not the same news.
  ended?: string;
}): JSX.Element {
  const [status, setStatus] = createSignal<"opening" | "live" | "closed" | "unavailable">("opening");
  const [error, setError] = createSignal("");
  let host: HTMLDivElement | undefined;

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
      if (!(await nativeAvailable())) {
        setStatus("unavailable");
        return;
      }
      if (disposed) return;
      stop = followNatively(handle, () => host, () => disposed);
      setStatus("live");
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
        <div class="screen-note">{error()}</div>
      </Show>
      <Show when={status() === "unavailable"}>
        <div class="screen-note">{t("terminal.unavailable")}</div>
      </Show>
      <div class="terminal-host" ref={host} />
      <Show when={status() === "closed"}>
        <div class="meta">{props.ended ?? t("terminal.shellEnded")}</div>
      </Show>
    </div>
  );
}

// covered says something of the page lies over the pane — a menu, a panel, a
// floating notice. The native view is above the whole page, so it would hide
// that thing rather than be hidden by it; it steps aside instead.
//
// Looked for on a grid over the whole pane rather than at its corners: a menu
// or a notice is often smaller than the pane and would slip between them. A
// hit test is cheap enough to do this every frame.
function covered(el: HTMLElement, r: DOMRect): boolean {
  const step = 80, inset = 3;
  const cols = Math.max(2, Math.ceil(r.width / step)), rows = Math.max(2, Math.ceil(r.height / step));
  for (let i = 0; i <= cols; i++) {
    for (let j = 0; j <= rows; j++) {
      const x = r.left + inset + (r.width - 2 * inset) * (i / cols);
      const y = r.top + inset + (r.height - 2 * inset) * (j / rows);
      const hit = document.elementFromPoint(x, y);
      if (hit !== null && hit !== el && !el.contains(hit)) return true;
    }
  }
  return false;
}

function followNatively(handle: { id: string; url: string }, host: () => HTMLElement | undefined, gone: () => boolean): () => void {
  let last = "";
  let frame = 0;
  const tick = () => {
    if (gone()) return;
    const el = host();
    if (el) {
      const r = el.getBoundingClientRect();
      const onScreen = r.width > 8 && r.height > 8 && r.right > 0 && r.bottom > 0
        && r.left < window.innerWidth && r.top < window.innerHeight;
      const visible = onScreen && !covered(el, r);
      const dpr = window.devicePixelRatio || 1;
      const key = visible ? `${Math.round(r.left)},${Math.round(r.top)},${Math.round(r.width)},${Math.round(r.height)},${dpr}` : "hidden";
      if (key !== last) {
        last = key;
        if (visible) void API.ShowNativeTerminal(handle.id, handle.url, r.left, r.top, r.width, r.height, dpr, FONT_SIZE);
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
