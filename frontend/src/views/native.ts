import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";

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
