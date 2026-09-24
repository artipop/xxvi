import { t } from "./i18n";
import type { IconName } from "./icons";
import { attention, inbox, inWork, list, updateWaiting, workRibbons, type Tab } from "./state";

// The sections, named once, for the sidebar on the right of every screen.
//
// «Sources» is not here on purpose. The one real source — the MRs waiting on a
// review — has nothing to set but on and off, and that switch is on the
// project it reads; the rest are demos, and a section whose use is a demo
// misleads. The screen comes back with the first source that has settings.

export type NavItem = {
  tab: Tab;
  /** label is a function so the menu follows the language as it changes. */
  label: () => string;
  icon: IconName;
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
  { tab: "inbox", icon: "inbox", label: () => t("nav.inbox"), count: () => inbox().reduce((n, g) => n + list(g.cards).length, 0) },
  { tab: "ribbon", icon: "ribbon", label: () => t("nav.ribbon"), count: () => workRibbons().length },
  { tab: "work", icon: "work", label: () => t("nav.work"), count: () => inWork().length },
  { tab: "attention", icon: "attention", label: () => t("nav.attention"), count: () => attention().length, alert: true },
  { tab: "flows", icon: "flows", label: () => t("nav.flows"), apart: true },
  { tab: "projects", icon: "projects", label: () => t("nav.projects") },
  { tab: "agents", icon: "agents", label: () => t("nav.agents") },
  // Updates live in the settings, so the dot that says one is waiting sits
  // there too.
  { tab: "settings", icon: "settings", label: () => t("nav.settings"), mark: updateWaiting },
];
