import type { JSX } from "@solidjs/web";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { openOutside } from "../state";

// A card's text as its author wrote it — an agent writes Markdown, and so do
// the sources that bring mail and MRs. Sanitized: the text comes from outside
// the application, and it lands in the page as HTML.
export default function Markdown(props: { text: string; class?: string }): JSX.Element {
  const html = () => DOMPurify.sanitize(marked.parse(props.text, { async: false, gfm: true, breaks: true }));
  return (
    <div
      class={`body md ${props.class ?? ""}`}
      innerHTML={html()}
      // A link followed inside the window would replace the application.
      onClick={(e) => {
        const a = (e.target as HTMLElement).closest("a");
        if (a) openOutside(e, a.href);
      }}
    />
  );
}
