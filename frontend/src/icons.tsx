import { For } from "solid-js";
import type { JSX } from "@solidjs/web";

// Drawn in the lucide manner — 24-unit box, round 2px stroke — so they read as
// one set with each other and take their colour from the text around them.
const PATHS = {
  inbox: [
    "M22 12h-6l-2 3h-4l-2-3H2",
    "M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z",
  ],
  ribbon: ["M2 7v10", "M6 5v14", "M12 3h8a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"],
  attention: ["M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9", "M10.3 21a1.94 1.94 0 0 0 3.4 0"],
  flows: [
    "M5 3h4a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z",
    "M7 11v4a2 2 0 0 0 2 2h4",
    "M15 13h4a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2h-4a2 2 0 0 1-2-2v-4a2 2 0 0 1 2-2z",
  ],
  projects: [
    "M16 20V4a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16",
    "M4 6h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2z",
  ],
  agents: [
    "M12 8V4H8",
    "M6 8h12a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2z",
    "M2 14h2", "M20 14h2", "M15 13v2", "M9 13v2",
  ],
  settings: [
    "M20 7h-9", "M14 17H5",
    "M17 14a3 3 0 1 1 0 6 3 3 0 0 1 0-6z",
    "M7 4a3 3 0 1 1 0 6 3 3 0 0 1 0-6z",
  ],
  expand: ["m11 17-5-5 5-5", "m18 17-5-5 5-5"],
  collapse: ["m6 17 5-5-5-5", "m13 17 5-5-5-5"],
  chevron: ["m6 9 6 6 6-6"],
} as const;

export type IconName = keyof typeof PATHS;

export function Icon(props: { name: IconName }): JSX.Element {
  return (
    <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
         stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <For each={PATHS[props.name]}>{(d) => <path d={d} />}</For>
    </svg>
  );
}
