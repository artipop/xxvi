import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";

/** TERMINAL_ENGINE is where the choice between Ghostty and xterm.js is kept:
 *  «web» for xterm.js, anything else for Ghostty where it is available. The
 *  choice is there to try on a Mac what Windows and Linux get. */
export const TERMINAL_ENGINE = "xxvi.terminalEngine";

let answer: Promise<boolean> | undefined;

/** nativeAvailable says Ghostty can draw terminals on this machine
 *  (internal/nativeterm): its native library is there, which is macOS. */
export function nativeAvailable(): Promise<boolean> {
  answer ??= API.NativeTerminals().catch(() => false);
  return answer;
}

/** resetNative closes the native views a page before this one left behind —
 *  a reload does not run the cleanups that would have closed them. Called once,
 *  as the page starts. */
export function resetNative() {
  void API.ResetNativeTerminals().catch(() => { /* none to close */ });
}

/** nativeWanted says the terminal is Ghostty's: it is available and the
 *  settings did not ask for xterm.js. */
export function nativeWanted(): Promise<boolean> {
  let engine = "";
  try { engine = localStorage.getItem(TERMINAL_ENGINE) ?? ""; } catch { /* no storage: the default stands */ }
  return engine === "web" ? Promise.resolve(false) : nativeAvailable();
}
