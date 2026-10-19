import { expect, it, vi } from "vitest";
import { formKeys } from "./formKeys";

function key(key: string, tagName = "INPUT", options: Partial<KeyboardEvent> = {}, type = "text") {
  const event = new Event("keydown", { cancelable: true });
  Object.defineProperties(event, Object.fromEntries(Object.entries({
    key, target: { tagName, type }, ...options,
  }).map(([name, value]) => [name, { value }])));
  vi.spyOn(event, "stopPropagation");
  return event as KeyboardEvent;
}

it.each(["text", "password", "search", "email", "url", "tel", "number"])("submits from a %s field", (type) => {
  const submit = vi.fn();
  const event = key("Enter", "INPUT", {}, type);
  formKeys({ submit })(event);
  expect(submit).toHaveBeenCalledOnce();
  expect(event.defaultPrevented).toBe(true);
  expect(event.stopPropagation).toHaveBeenCalledOnce();
});

it.each([{}, { shiftKey: true }, { altKey: true }])("keeps textarea line breaks with %j", (modifiers) => {
  const submit = vi.fn();
  const event = key("Enter", "TEXTAREA", modifiers);
  formKeys({ submit })(event);
  expect(submit).not.toHaveBeenCalled();
  expect(event.defaultPrevented).toBe(false);
});

it.each([{ metaKey: true }, { ctrlKey: true }])("submits multiline text with %j", (modifiers) => {
  const submit = vi.fn();
  const event = key("Enter", "TEXTAREA", modifiers);
  formKeys({ submit })(event);
  expect(submit).toHaveBeenCalledOnce();
  expect(event.defaultPrevented).toBe(true);
});

it.each(["BUTTON", "SELECT", "A", "DIV"])("leaves Enter on %s to the control", (tag) => {
  const submit = vi.fn();
  const event = key("Enter", tag);
  formKeys({ submit })(event);
  expect(submit).not.toHaveBeenCalled();
  expect(event.defaultPrevented).toBe(false);
});

it.each(["checkbox", "radio", "color", "file"])("does not submit from a %s picker", (type) => {
  const submit = vi.fn();
  formKeys({ submit })(key("Enter", "INPUT", {}, type));
  expect(submit).not.toHaveBeenCalled();
});

it.each(["INPUT", "TEXTAREA", "SELECT", "BUTTON"])("cancels from %s without escaping to the parent", (tag) => {
  const cancel = vi.fn(), parent = vi.fn();
  const event = key("Escape", tag);
  formKeys({ cancel })(event);
  formKeys({ cancel: parent })(event);
  expect(cancel).toHaveBeenCalledOnce();
  expect(parent).not.toHaveBeenCalled();
  expect(event.stopPropagation).toHaveBeenCalledOnce();
});

it.each(["Enter", "Escape"])("ignores %s during IME composition", (name) => {
  const action = vi.fn();
  const handle = formKeys({ submit: action, cancel: action });
  for (const options of [{ isComposing: true }, { keyCode: 229 }]) {
    const event = key(name, "INPUT", options);
    handle(event);
    expect(event.defaultPrevented).toBe(false);
  }
  expect(action).not.toHaveBeenCalled();
});

it("blocks repeated and busy submissions without submitting the surrounding editor", () => {
  const submit = vi.fn(), parent = vi.fn();
  let busy = true;
  const handle = formKeys({ submit, busy: () => busy });
  const event = key("Enter");
  handle(event);
  formKeys({ submit: parent })(event);
  expect(submit).not.toHaveBeenCalled();
  expect(parent).not.toHaveBeenCalled();
  busy = false;
  handle(key("Enter", "INPUT", { repeat: true }));
  expect(submit).not.toHaveBeenCalled();
  handle(key("Enter"));
  expect(submit).toHaveBeenCalledOnce();
});

it("leaves Tab and Shift+Tab alone", () => {
  const action = vi.fn();
  for (const shiftKey of [false, true]) {
    const event = key("Tab", "INPUT", { shiftKey });
    formKeys({ submit: action, cancel: action })(event);
    expect(event.defaultPrevented).toBe(false);
  }
  expect(action).not.toHaveBeenCalled();
});
