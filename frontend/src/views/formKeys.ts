type FormActions = {
  submit?: () => unknown;
  cancel?: () => unknown;
  busy?: () => boolean;
};

// Some editors contain independently saved editors (hosting, stage templates),
// so their keyboard scope cannot be represented by nested HTML forms.
export function formKeys(actions: FormActions) {
  return (event: KeyboardEvent) => {
    if (event.defaultPrevented || event.isComposing || event.keyCode === 229) return;
    const target = event.target as HTMLInputElement | HTMLTextAreaElement | null;
    let action: (() => unknown) | undefined;
    if (event.key === "Escape") {
      action = actions.cancel;
    } else if (event.key === "Enter" && !event.altKey && !event.shiftKey && target && !target.disabled && !target.readOnly) {
      const input = target.tagName === "INPUT" && /^(text|password|search|email|url|tel|number)$/.test(target.type);
      const multiline = target.tagName === "TEXTAREA" && (event.metaKey || event.ctrlKey);
      if (input || multiline) action = actions.submit;
    }
    if (!action) return;
    event.preventDefault();
    event.stopPropagation();
    if (!event.repeat && !actions.busy?.()) void action();
  };
}
