import { Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { error, setError } from "../state";
import { BackgroundNotice } from "./background";

/** Notes is what the application has to say whatever screen is open: the last
 *  thing that failed, and what the last run left running. */
export function Notes(): JSX.Element {
  return (
    <>
      <Show when={error()}>
        <div class="error">
          <pre>{error()}</pre>
          <button class="btn quiet tiny" onClick={() => setError("")}>×</button>
        </div>
      </Show>
      <BackgroundNotice />
    </>
  );
}
