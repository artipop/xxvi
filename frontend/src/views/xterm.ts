import "@xterm/xterm/css/xterm.css";

// The terminal drawn in the page, by xterm.js — where Ghostty cannot draw it:
// Ghostty's own library renders on macOS alone, so on Windows and Linux the
// pane is this. The screen itself is still libghostty's: the holder keeps it,
// and the socket starts with its state.
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

export type PageStatus = "live" | "reconnecting" | "closed";

export async function drawInPage(
  url: string,
  host: HTMLElement,
  fontSize: number,
  ended: string,
  setStatus: (s: PageStatus) => void,
): Promise<() => void> {
  const [{ Terminal }, { FitAddon }] = await Promise.all([
    import("@xterm/xterm"),
    import("@xterm/addon-fit"),
  ]);
  let disposed = false;
  let socket: WebSocket | null = null;
  let retry: ReturnType<typeof setTimeout> | undefined;

  // xterm paints on a canvas and cannot read CSS, so the font and the colours
  // are handed to it as strings — taken off the page rather than written here,
  // so the emulator and the chrome around it cannot drift.
  const style = getComputedStyle(host);
  const value = (token: string, fallback: string) =>
    style.getPropertyValue(token).trim() || fallback;
  const ansi = (name: string) => value(`--ansi-${name}`, "") || undefined;

  const terminal = new Terminal({
    fontFamily: value("--mono", "ui-monospace, SFMono-Regular, Menlo, monospace"),
    fontSize,
    cursorBlink: true,
    convertEol: false,
    scrollback: 5000,
    theme: {
      background: value("--bg", "#111216"),
      foreground: value("--text", "#e6e8ee"),
      black: ansi("black"),
      red: ansi("red"),
      green: ansi("green"),
      yellow: ansi("yellow"),
      blue: ansi("blue"),
      magenta: ansi("magenta"),
      cyan: ansi("cyan"),
      white: ansi("white"),
      brightBlack: ansi("bright-black"),
      brightRed: ansi("bright-red"),
      brightGreen: ansi("bright-green"),
      brightYellow: ansi("bright-yellow"),
      brightBlue: ansi("bright-blue"),
      brightMagenta: ansi("bright-magenta"),
      brightCyan: ansi("bright-cyan"),
      brightWhite: ansi("bright-white"),
    },
  });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  terminal.open(host);
  fit.fit();

  const send = (data: string | Uint8Array) => {
    if (socket?.readyState === WebSocket.OPEN) socket.send(data);
  };
  const sendSize = () => send(JSON.stringify({ type: "resize", cols: terminal.cols, rows: terminal.rows }));

  // The process is gone. A spinner frozen mid-turn looks exactly like one that
  // is still thinking, so the screen itself has to say it stopped, not only the
  // line under it.
  const finish = () => {
    setStatus("closed");
    terminal.options.cursorBlink = false;
    terminal.options.disableStdin = true;
    terminal.write(`${belowContent(terminal)}\x1b[0m\x1b[2m— ${ended} —\x1b[0m\x1b[?25l`);
  };

  // A socket that closes without «exit» is the connection, not the process:
  // the window slept, the webview dropped it. The process is still there, so
  // the pane goes back for it rather than declaring it over.
  let attempt = 0;
  const connect = () => {
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    socket = ws;
    let exited = false;

    ws.onopen = () => {
      // Every socket starts with the whole screen, and drawn over the one the
      // last socket left it would be the same output twice.
      if (attempt > 0) terminal.reset();
      attempt = 0;
      setStatus("live");
      sendSize();
    };
    ws.onmessage = (e: MessageEvent) => {
      if (typeof e.data === "string") {
        // The only text frame is the process saying it has gone.
        if (e.data.includes('"exit"')) { exited = true; finish(); }
        return;
      }
      terminal.write(new Uint8Array(e.data as ArrayBuffer));
    };
    // An error is always followed by a close, so the close alone decides.
    ws.onclose = () => {
      if (disposed || exited || socket !== ws) return;
      setStatus("reconnecting");
      const delay = Math.min(500 * 2 ** attempt, 10_000);
      attempt++;
      retry = setTimeout(connect, delay);
    };
  };
  connect();

  const encoder = new TextEncoder();
  terminal.onData((data: string) => send(encoder.encode(data)));
  // Some mouse reports are not text: the X10 encoding puts coordinates in bytes
  // above 0x7f, and UTF-8 would turn each into two.
  terminal.onBinary((data: string) => {
    const bytes = new Uint8Array(data.length);
    for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff;
    send(bytes);
  });

  // The pane is resized by more than the window: widening a column with R
  // changes it too, and every one of those has to reach the process.
  const observer = new ResizeObserver(() => {
    try { fit.fit(); } catch { /* mid-layout; the next tick fits it */ }
    sendSize();
  });
  observer.observe(host);

  return () => {
    disposed = true;
    clearTimeout(retry);
    observer.disconnect();
    socket?.close();
    terminal.dispose();
  };
}

// belowContent is where the closing line goes: under the last line anything was
// drawn on. A full-screen program that was killed leaves the cursor wherever it
// was drawing — inside its own input box, as often as not — and a line written
// from there would land on top of what it drew.
function belowContent(terminal: any): string {
  const buffer = terminal.buffer.active;
  let last = terminal.rows - 1;
  while (last >= 0 && !buffer.getLine(buffer.baseY + last)?.translateToString(true).trim()) last--;
  if (last >= terminal.rows - 1) return `\x1b[${terminal.rows};1H\r\n`;
  return `\x1b[${last + 2};1H`;
}
