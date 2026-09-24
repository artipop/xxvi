import { t } from "./i18n";
import { attention, inbox, inWork, list, updateWaiting, workRibbons, type Tab } from "./state";

// The sections, named once. The sidebar shows them down the left of every other
// screen; the ribbon, which has no sidebar, shows the same list under a chevron.
// Two places, one list — a section that exists in one menu and not the other is
// a section somebody cannot find.
//
// «Sources» is not here on purpose: there is no real source yet, only demo
// ones, and a section whose one use is a demo is a section that misleads. The
// screen stays in the code and comes back with the first real source.

export type NavItem = {
  tab: Tab;
  /** label is a function so the menu follows the language as it changes. */
  label: () => string;
  count?: () => number;
  /** alert colours the count: something is waiting for a person, not just piling up. */
  alert?: boolean;
  /** apart separates what you work in from what you set up. */
  apart?: boolean;
  /** mark is a dot rather than a number: a waiting update is one thing, not a
   *  pile of them, and «1» beside a section reads as a count of nothing. */
  mark?: () => boolean;
};

export const NAV: NavItem[] = [
  { tab: "inbox", label: () => t("nav.inbox"), count: () => inbox().reduce((n, g) => n + list(g.cards).length, 0) },
  { tab: "ribbon", label: () => t("nav.ribbon"), count: () => workRibbons().length },
  { tab: "work", label: () => t("nav.work"), count: () => inWork().length },
  { tab: "attention", label: () => t("nav.attention"), count: () => attention().length, alert: true },
  { tab: "flows", label: () => t("nav.flows"), apart: true },
  { tab: "projects", label: () => t("nav.projects") },
  { tab: "agents", label: () => t("nav.agents") },
  // Updates live in the settings, so the dot that says one is waiting sits
  // there too.
  { tab: "settings", label: () => t("nav.settings"), mark: updateWaiting },
];
