import ELK from "elkjs/lib/elk.bundled.js";
import type { ElkExtendedEdge, ElkNode, ElkPoint } from "elkjs/lib/elk-api";

// Where the boxes stand and how the arrows run between them, worked out by ELK's
// layered algorithm rather than by hand.
//
// Solid Flow draws an arrow from one point to another and knows nothing of the
// arrows beside it: two of them into one box lay on top of each other, and a way
// back went wherever the library's step path happened to go. ELK lays the whole
// graph at once — stages in columns, each arrow in a corridor of its own,
// arrows back to an earlier stage gathered under the graph into one bus that
// joins at a marked point — and hands back the bends, which the canvas draws.
//
// The ports are stated, not left to ELK, and they are the same points the
// canvas' handles stand at, so a route starts and ends exactly where the box
// says it does.

export type Point = { x: number; y: number };

export type LayoutNode = {
  id: string;
  width: number;
  height: number;
};

export type LayoutEdge = {
  id: string;
  from: string;
  to: string;
  // Which of the source's ports the arrow leaves by.
  port: "success" | "failure" | "event";
  // The caption and its size. A forward arrow's caption is laid out by ELK,
  // which leaves room for it between the columns. A way back and a hosting
  // event are captioned by the canvas instead (see Route.label): ELK makes room
  // for a caption by adding a column, and the dozen events of a review flow,
  // each captioned, pushed its boxes apart and scattered its buses.
  label?: { width: number; height: number; text: string };
  back: boolean;
};

export type Route = {
  points: Point[];
  junctions: Point[];
  // The caption's centre, when ELK placed one.
  label?: Point;
};

export type Layout = {
  key: string;
  nodes: Map<string, Point>;
  routes: Map<string, Route>;
};

// Where the ports sit on a box, as fractions of the box's base height. The way
// in and «passed» are at one height, so a straight run of stages is one straight
// line through them and the boxes stand on one row; «failed» sits below it.
export const PORT_IN = 0.5;
export const PORT_SUCCESS = 0.5;
export const PORT_FAILURE = 0.8;

const OPTIONS: Record<string, string> = {
  "elk.algorithm": "layered",
  "elk.direction": "RIGHT",
  "elk.edgeRouting": "ORTHOGONAL",
  "elk.layered.spacing.nodeNodeBetweenLayers": "96",
  "elk.spacing.nodeNode": "48",
  "elk.layered.spacing.edgeNodeBetweenLayers": "24",
  "elk.layered.spacing.edgeEdgeBetweenLayers": "16",
  "elk.spacing.edgeEdge": "16",
  "elk.spacing.edgeNode": "24",
  "elk.spacing.edgeLabel": "4",
  "elk.edgeLabels.placement": "CENTER",
  // Network simplex rather than Brandes–Köpf: it puts the way a card goes on
  // one straight row and bends the rest around it, where Brandes–Köpf
  // stepped the main path up and down to straighten a hosting event.
  "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
  // Cycles are broken by the order the stages are handed over in, which is
  // the order a card meets them: the arrow back from a failed check is the
  // one reversed, never the way forward. Left to itself ELK once laid the
  // entry out last.
  "elk.layered.cycleBreaking.strategy": "MODEL_ORDER",
  "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
  "elk.separateConnectedComponents": "false",
};

const elk = new ELK();

/** layoutFlow lays out one flow. `baseHeight` is where the side ports are
 *  measured from — the box without its row of outputs. */
export async function layoutFlow(
  key: string,
  nodes: LayoutNode[],
  edges: LayoutEdge[],
  baseHeight: number,
): Promise<Layout> {
  const known = new Set(nodes.map((n) => n.id));
  const graph: ElkNode = {
    id: "root",
    layoutOptions: OPTIONS,
    children: nodes.map((n) => ({
      id: n.id,
      width: n.width,
      height: n.height,
      layoutOptions: { "elk.portConstraints": "FIXED_POS" },
      ports: [
        port(n.id, "in", 0, baseHeight * PORT_IN, "WEST"),
        port(n.id, "success", n.width, baseHeight * PORT_SUCCESS, "EAST"),
        port(n.id, "failure", n.width, baseHeight * PORT_FAILURE, "EAST"),
        port(n.id, "event", n.width / 2, n.height, "SOUTH"),
      ],
    })),
    edges: edges
      .filter((e) => known.has(e.from) && known.has(e.to))
      .map((e): ElkExtendedEdge => {
        // The way a card goes is what stays straight: everything else bends
        // around it.
        const main = e.port === "success" && !e.back;
        return {
          id: e.id,
          sources: [`${e.from}:${e.port}`],
          targets: [`${e.to}:in`],
          layoutOptions: {
            "elk.layered.priority.straightness": main ? "10" : "0",
            "elk.layered.priority.direction": main ? "10" : "0",
          },
          // A label without text is a label ELK leaves at the origin.
          labels: e.label && elkCaptions(e)
            ? [{ text: e.label.text, width: e.label.width, height: e.label.height, layoutOptions: { "elk.edgeLabels.inline": "true" } }]
            : [],
        };
      }),
  };

  const laid = await elk.layout(graph);
  const out: Layout = { key, nodes: new Map(), routes: new Map() };
  for (const c of laid.children ?? []) out.nodes.set(c.id, { x: c.x ?? 0, y: c.y ?? 0 });
  for (const e of (laid.edges ?? []) as ElkExtendedEdge[]) {
    const section = e.sections?.[0];
    if (!section) continue;
    const points = [section.startPoint, ...(section.bendPoints ?? []), section.endPoint].map(pt);
    const lab = e.labels?.[0];
    out.routes.set(e.id, {
      points,
      junctions: (e.junctionPoints ?? []).map(pt),
      label: lab && lab.width ? { x: (lab.x ?? 0) + lab.width / 2, y: (lab.y ?? 0) + (lab.height ?? 0) / 2 } : undefined,
    });
  }
  return out;
}

/** elkCaptions says ELK places this arrow's caption, rather than the canvas. */
export function elkCaptions(e: Pick<LayoutEdge, "back" | "port">): boolean {
  return !e.back && e.port !== "event";
}

function port(node: string, name: string, x: number, y: number, side: string): ElkNode {
  return { id: `${node}:${name}`, x, y, width: 0, height: 0, layoutOptions: { "elk.port.side": side } };
}

function pt(p: ElkPoint): Point {
  return { x: p.x, y: p.y };
}

/** roundedPath draws a route of straight runs with its corners rounded, the
 *  radius shrunk where two bends stand closer than two radii. */
export function roundedPath(points: Point[], radius = 10): string {
  if (points.length === 0) return "";
  let d = `M ${points[0].x},${points[0].y}`;
  for (let i = 1; i < points.length - 1; i++) {
    const prev = points[i - 1];
    const at = points[i];
    const next = points[i + 1];
    const r = Math.min(radius, dist(prev, at) / 2, dist(at, next) / 2);
    const a = towards(at, prev, r);
    const b = towards(at, next, r);
    d += ` L ${a.x},${a.y} Q ${at.x},${at.y} ${b.x},${b.y}`;
  }
  const last = points[points.length - 1];
  return `${d} L ${last.x},${last.y}`;
}

function dist(a: Point, b: Point): number {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

function towards(from: Point, to: Point, by: number): Point {
  const len = dist(from, to) || 1;
  return { x: from.x + ((to.x - from.x) * by) / len, y: from.y + ((to.y - from.y) * by) / len };
}

/** backLabelSpot is where a way back is captioned: on the bus it runs along,
 *  just before the point it drops into it. Several ways back share the bus, so
 *  the middle of it would put their captions on top of one another; the drop
 *  point is each one's own. */
export function backLabelSpot(points: Point[], width: number): Point | undefined {
  // The bus is the longest horizontal run.
  let best = -1;
  let bestLen = 0;
  for (let i = 0; i < points.length - 1; i++) {
    const a = points[i];
    const b = points[i + 1];
    if (Math.abs(a.y - b.y) > 0.5) continue;
    const len = Math.abs(a.x - b.x);
    if (len > bestLen) { bestLen = len; best = i; }
  }
  if (best < 0) return undefined;
  const a = points[best];
  const b = points[best + 1];
  const dir = Math.sign(b.x - a.x) || -1;
  const x = a.x + dir * (width / 2 + 14);
  return { x, y: a.y };
}

/** entryLabelSpot is where an event is captioned: on the last run into the
 *  box, just before the arrowhead. The events of one kind into one box join
 *  into one bus and are captioned once, at its end. */
export function entryLabelSpot(points: Point[], width: number): Point | undefined {
  const end = points[points.length - 1];
  if (!end) return undefined;
  return { x: end.x - width / 2 - 12, y: end.y };
}

// The font the captions are drawn in, to measure them by: ELK leaves exactly
// the room it is told a caption needs.
let measurer: CanvasRenderingContext2D | null | undefined;

/** captionWidth is how wide a caption renders, padding included. */
export function captionWidth(text: string): number {
  if (measurer === undefined) {
    measurer = typeof document !== "undefined" ? document.createElement("canvas").getContext("2d") : null;
    if (measurer) measurer.font = `10px -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, system-ui, sans-serif`;
  }
  const w = measurer ? measurer.measureText(text).width : text.length * 6;
  return Math.ceil(w) + 12;
}

export const CAPTION_HEIGHT = 16;
