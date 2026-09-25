import { t } from "./i18n";
import type { IconName } from "./icons";
import { attention, inbox, list, updateWaiting, workRibbons, type Tab } from "./state";

// The sections, named once, for the sidebar on the right of every screen.
//
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
  { tab: "attention", icon: "attention", label: () => t("nav.attention"), count: () => attention().length, alert: true },
  { tab: "projects", icon: "projects", label: () => t("nav.projects"), apart: true },
  { tab: "sources", icon: "sources", label: () => t("nav.sources") },
  { tab: "agents", icon: "agents", label: () => t("nav.agents") },
  { tab: "flows", icon: "flows", label: () => t("nav.flows") },
  // Updates live in the settings, so the dot that says one is waiting sits
  // there too.
  { tab: "settings", icon: "settings", label: () => t("nav.settings"), mark: updateWaiting },
];
