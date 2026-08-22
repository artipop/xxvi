import { createSignal, onCleanup, onMount, Show, type JSX } from "solid-js";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import { report } from "../state";
import "@xterm/xterm/css/xterm.css";

// A terminal screen: a real pty on the other end of a socket, and xterm drawing
// what comes back.
//
// The socket rather than the window's event bus, because this is a byte stream:
// events reach the page by splicing JavaScript into the webview, and a build log
// would become thousands of strings executed as code.
//
// The one thing that has to be right, and the thing that is wrong when a
// terminal "wraps strangely": the process must be told how wide the window is.
// A shell breaks its lines at the column count it was given and keeps the
// default eighty until told otherwise, so a terminal that fits itself to the
// pane and never says so draws eighty-column text into a much wider box. Hence
// a resize on open and on every change of size, sent from the same numbers
// xterm itself is drawing with.

export default function Terminal(props: {
  cardId: string;
  screenId: string;
  command: string;
}): JSX.Element {
  const [status, setStatus] = createSignal<"opening" | "live" | "closed">("opening");
  const [error, setError] = createSignal("");
  let host: HTMLDivElement | undefined;

  onMount(() => {
    let disposed = false;
    let terminal: any = null;
    let observer: ResizeObserver | null = null;
    let socket: WebSocket | null = null;

    const start = async () => {
      const handle = await API.OpenTerminal(props.cardId, props.screenId, props.command);
      if (disposed) return;

      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
      ]);
      if (disposed || !host) return;

      // xterm paints on a canvas and cannot read CSS, so the font and the two
      // colours are handed to it as strings — taken off the page rather than
      // written here, so the emulator and the chrome around it cannot drift.
      const style = getComputedStyle(host);
      const value = (token: string, fallback: string) =>
        style.getPropertyValue(token).trim() || fallback;

      terminal = new Terminal({
        fontFamily: value("--mono", 'ui-monospace, SFMono-Regular, Menlo, monospace'),
        fontSize: 12,
        cursorBlink: true,
        convertEol: false,
        scrollback: 5000,
        theme: {
          background: value("--bg", "#111216"),
          foreground: value("--text", "#e6e8ee"),
        },
      });
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(host);
      fit.fit();

      const ws = new WebSocket(handle.url);
      ws.binaryType = "arraybuffer";
      socket = ws;

      const sendSize = () => {
        if (ws.readyState === WebSocket.OPEN && terminal) {
          ws.send(JSON.stringify({ type: "resize", cols: terminal.cols, rows: terminal.rows }));
        }
      };

      ws.onopen = () => { setStatus("live"); sendSize(); };
      ws.onmessage = (e: MessageEvent) => {
        if (typeof e.data === "string") {
          // The only text frame is the shell saying it has gone.
          if (e.data.includes('"exit"')) setStatus("closed");
          return;
        }
        terminal.write(new Uint8Array(e.data as ArrayBuffer));
      };
      ws.onclose = () => setStatus("closed");
      ws.onerror = () => setStatus("closed");

      const encoder = new TextEncoder();
      terminal.onData((data: string) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data));
      });

      // The pane is resized by more than the window: widening a column with R
      // changes it too, and every one of those has to reach the process.
      observer = new ResizeObserver(() => {
        try { fit.fit(); } catch { /* mid-layout; the next tick fits it */ }
        sendSize();
      });
      observer.observe(host);
    };

    start().catch((e) => { setError(String(e?.message ?? e)); report(e); });

    onCleanup(() => {
      disposed = true;
      observer?.disconnect();
      socket?.close();
      terminal?.dispose();
    });
  });

  return (
    <div class="terminal">
      <Show when={error()}>
        <div class="screen-note">{error()}</div>
      </Show>
      <div class="terminal-host" ref={host} />
      <Show when={status() === "closed"}>
        <div class="meta">шелл завершился</div>
      </Show>
    </div>
  );
}
