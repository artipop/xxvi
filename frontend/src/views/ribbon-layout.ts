export function centerInBand(el: HTMLElement | undefined, behavior: ScrollBehavior = "instant") {
  const band = el?.closest<HTMLElement>(".band");
  if (!el || !band) return;
  const by = el.getBoundingClientRect().left + el.offsetWidth / 2
    - (band.getBoundingClientRect().left + band.clientWidth / 2);
  band.scrollTo({ left: band.scrollLeft + by, behavior });
}

export function alignRibbon(stack: HTMLElement, id: string, behavior: ScrollBehavior = "instant") {
  const ribbon = Array.from(stack.children).find((el) => (el as HTMLElement).dataset.ribbon === id);
  if (!ribbon) return;
  const top = stack.scrollTop + ribbon.getBoundingClientRect().top - stack.getBoundingClientRect().top;
  stack.scrollTo({ top, behavior });
}

export function followRibbonSize(stack: HTMLElement, selected: () => { ribbon: string; pane?: HTMLElement }) {
  // Scroll offsets are pixels; after resizing they no longer point at the
  // same ribbon. Restore both axes before Ghostty measures its DOM host.
  const observer = new ResizeObserver(() => {
    const { ribbon, pane } = selected();
    alignRibbon(stack, ribbon);
    centerInBand(pane);
  });
  observer.observe(stack);
  return () => observer.disconnect();
}
