import { beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  selected: undefined as ((event: { data: { request: string; index: number } }) => void) | undefined,
  open: vi.fn(), native: vi.fn(), report: vi.fn(),
}));
vi.mock("@wailsio/runtime", () => ({ Events: { On: (_: string, fn: typeof mocks.selected) => { mocks.selected = fn; } } }));
vi.mock("../../bindings/github.com/artipop/gandvik/popupmenu", () => ({ Open: mocks.open }));
vi.mock("./native", () => ({ nativeAvailable: mocks.native }));
vi.mock("../state", () => ({ report: mocks.report }));

import { popup } from "./popup";

const anchor = () => ({
  isConnected: true, getBoundingClientRect: () => ({ left: 40, bottom: 60 }),
}) as HTMLElement;
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  vi.clearAllMocks();
  mocks.native.mockResolvedValue(true);
  mocks.open.mockResolvedValue(undefined);
});

it("routes a selection after a separator to the corresponding action", async () => {
  const action = vi.fn();
  popup(anchor(), [{ label: "", separator: true }, { label: "Во входящие", action }]);
  await settle();
  const request = mocks.open.mock.calls[0][0];
  mocks.selected!({ data: { request, index: 1 } });
  mocks.selected!({ data: { request, index: 1 } });
  expect(action).toHaveBeenCalledOnce();
  expect(mocks.open.mock.calls[0].slice(2)).toEqual([40, 60]);
});

it("does not apply an old menu selection to a newly opened card", async () => {
  const oldAction = vi.fn(), newAction = vi.fn();
  popup(anchor(), [{ label: "Удалить", action: oldAction }]);
  await settle();
  const oldRequest = mocks.open.mock.calls[0][0];
  popup(anchor(), [{ label: "Удалить", action: newAction }]);
  await settle();
  mocks.selected!({ data: { request: oldRequest, index: 0 } });
  expect(oldAction).not.toHaveBeenCalled();
  expect(newAction).not.toHaveBeenCalled();
  mocks.selected!({ data: { request: mocks.open.mock.calls[1][0], index: 0 } });
  expect(newAction).toHaveBeenCalledOnce();
});

it("does not reopen a superseded menu when availability resolves late", async () => {
  let resolve!: (native: boolean) => void;
  mocks.native.mockReturnValueOnce(new Promise<boolean>((r) => { resolve = r; }));
  popup(anchor(), [{ label: "Old" }]);
  popup(anchor(), [{ label: "Current" }]);
  await settle();
  resolve(true);
  await settle();
  expect(mocks.open).toHaveBeenCalledOnce();
  expect(mocks.open.mock.calls[0][1][0].label).toBe("Current");
});
