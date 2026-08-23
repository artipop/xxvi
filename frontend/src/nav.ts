import { attention, inbox, inWork, list, ribbons, updateWaiting, type Tab } from "./state";

// The sections, named once. The sidebar shows them down the left of every other
// screen; the ribbon, which has no sidebar, shows the same list under a chevron.
// Two places, one list — a section that exists in one menu and not the other is
// a section somebody cannot find.

export type NavItem = {
  tab: Tab;
  label: string;
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
  { tab: "inbox", label: "Входящие", count: () => inbox().reduce((n, g) => n + list(g.cards).length, 0) },
  { tab: "ribbon", label: "Лента", count: () => ribbons.length },
  { tab: "work", label: "В работе", count: () => inWork().length },
  { tab: "attention", label: "Требуют внимания", count: () => attention().length, alert: true },
  { tab: "flows", label: "Флоу", apart: true },
  { tab: "projects", label: "Проекты" },
  { tab: "sources", label: "Источники" },
  { tab: "agents", label: "Агенты" },
  { tab: "updates", label: "Обновление", mark: updateWaiting },
];
