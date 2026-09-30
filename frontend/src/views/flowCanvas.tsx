import { For, Show, createEffect, createMemo, createSignal, onSettled } from "solid-js";
import {
  Background,
  BaseEdge,
  EdgeLabel,
  EdgeReconnectAnchor,
  Connection,
  Controls,
  Edge as FlowEdge,
  EdgeProps,
  Handle,
  MarkerType,
  Node as FlowNode,
  NodeProps,
  Position,
  SolidFlow,
  createEdgeStore,
  createNodeStore,
  getSmoothStepPath,
  useSolidFlow,
  useViewport,
} from "@dschz/solid-flow";

import type { Edge, Flow, Stage, StageTemplate, Trigger } from "../../bindings/github.com/artipop/xxvi/internal/model/models";

import "@dschz/solid-flow/dist/style.css";
import { label, propName, propValue, t } from "../i18n";
import { Tile, nodeDetail, ownsScreen, templateName, templateOf } from "./templates";

// The flow as a graph. Pan, zoom and drag are Solid Flow's; the layout is ours.
//
// A box is a stage — the place a card stands and the work done there — and an
// arrow is what moves a card on. What a stage leaves on the card is drawn on the
// box, and a conditional arrow is pulled from the output it asks about: that is
// where the dataflow becomes visible, instead of being assembled in a panel out
// of a list of property names.

export const NODE_WIDTH = 232;
export const NODE_HEIGHT = 68;

// How much taller a stage stands when it declares outputs: one row of chips,
// which are also the ports a conditional arrow is pulled from.
const WRITE_ROW = 22;

// HANDLE_WRITE is the prefix of a data port's handle id — the property's own
// name follows it. A prefix rather than a second field because a handle id is
// the only thing a connection carries back.
const HANDLE_WRITE = "write:";

// The three ways out of a stage. A route is drawn by pulling from one of them to
// another stage, so the handle carries the meaning of the transition and nothing
// has to be chosen afterwards — except which value, for a fork.
const HANDLE_SUCCESS = "success";
const HANDLE_FAILURE = "failure";
const HANDLE_EVENT = "event";

const GAP_X = 80;
const GAP_Y = 24;

// LANE is how far apart two long-haul arrows are pushed. An arrow that skips
// stages runs the width of the graph, and several of them at the same height
// read as one thick line — which is what a failure arrow from one stage and a
// return arrow from another look like travelling side by side into one box.
const LANE = 26;

// How far under the lowest of the two stages the first lane runs — clear of a
// box, which is 68 tall and 90 with outputs on it.
const LANE_BASE = 48;

// StageWrite is one property the stage leaves on the card, as the box draws it.
// `branchable` is whether an arrow can ask about it: an arrow asks "is it this
// value", so a property with no stated values is drawn and not connectable.
export type StageWrite = {
  name: string;
  branchable: boolean;
};

export type CanvasApi = {
  // right overrides the right inset: a box added by a click is selected, and
  // the inspector that opens for it covers the right whether or not it is open
  // yet.
  visibleCentre: (right?: number) => { x: number; y: number } | undefined;
};

export type Selection = { kind: "stage" | "edge"; id: string } | null;

type Props = {
  flow: Flow;
  triggers: Trigger[];

  // The palette, which is what a box is drawn as: its icon, colour and name.
  templates: StageTemplate[];

  // The name of the field a person answers a stage in. Transitions asking about
  // it are drawn as the outcomes they are rather than as waits — see
  // isOutcomeEdge.
  outcomeProperty?: string;
  outcomePassed?: string;

  // What each stage leaves on the card, resolved by the caller — which is the
  // half that knows which properties have a stated set of values.
  writesOf?: (stage: Stage) => StageWrite[];

  // onChange makes it a builder rather than a picture: stages are dragged,
  // joined by pulling from an output, and removed with the keyboard. Absent
  // means the flow is only being looked at.
  onChange?: (stages: Stage[], edges: Edge[]) => void;

  selected?: Selection;
  onSelect?: (selection: Selection) => void;

  // A template dropped from the palette, at the point of the flow it landed on.
  onDropKind?: (template: string, at: { x: number; y: number }) => void;

  // How much of the canvas' left edge something floats over, in pixels. The
  // picture is fitted clear of it, and the zoom plate moves out from under it.
  insetLeft?: number;
  // …and of its right edge, which only a fit made while it is covered minds.
  insetRight?: number;

  // Hands out what the editor needs of the canvas: where a box added by a
  // click should stand to be seen.
  onApi?: (api: CanvasApi) => void;
};

// sameFold is two names being the same name — trimmed and case-insensitive, the
// way every match on a person's word in this editor is.
function sameFold(a: string | undefined | null, b: string | undefined): boolean {
  return (a || "").trim().toLowerCase() === (b || "").trim().toLowerCase();
}

// isOutcomeEdge says this transition is a person answering on the card rather
// than the stage reporting for itself. It is drawn like any other outcome —
// green on, red back, out of the same sockets — because that is what it is: on a
// stage where nothing runs, these two *are* the success and the failure.
//
// What it is not is a wait. It rides card.changed, and colouring by the trigger
// drew the one arrow anybody looks for — the way back from a review — as a grey
// dashed line among the events.
export function isOutcomeEdge(edge: Edge, outcomeProperty: string | undefined): boolean {
  return Boolean(outcomeProperty && edge.on === "card.changed" && sameFold(edge.if?.property, outcomeProperty));
}

// stageHeight is how tall this stage stands: the base, plus the row of outputs
// when it has any. Stated rather than measured, like everything else about the
// box — the arrows are then drawn on the first paint instead of after the
// browser reports a layout.
function stageHeight(writes?: StageWrite[]): number {
  return NODE_HEIGHT + (writes && writes.length > 0 ? WRITE_ROW : 0);
}

// edgeId names a transition on the canvas. Conditions allow several edges per
// (from, on), so the identity is the edge's index in the flow — which is also
// how the panel addresses it.
export function edgeId(edge: Edge, index: number): string {
  return `${edge.from}-${edge.on}-${index}`;
}

// edgeIndexOf reads the index back out of a canvas id.
export function edgeIndexOf(id: string): number {
  const at = id.lastIndexOf("-");
  return at < 0 ? -1 : Number(id.slice(at + 1));
}

// forward drops the transitions that lead back the way the card came — a failed
// check returning to the agent is a normal route, and laying it out as progress
// would push every later stage off to the right for ever.
function forward(stages: Stage[], edges: Edge[]): Edge[] {
  const out = new Map<string, Edge[]>(stages.map((s) => [s.id, []]));
  for (const edge of edges) {
    if (out.has(edge.from) && out.has(edge.to) && edge.from !== edge.to) {
      (out.get(edge.from) as Edge[]).push(edge);
    }
  }

  const kept: Edge[] = [];
  const state = new Map<string, "open" | "done">();

  // Depth-first, iteratively: an edge into a stage still on the stack closes a
  // loop and is left out of the layout.
  const walk = (start: string) => {
    const stack: Array<{ id: string; next: number }> = [{ id: start, next: 0 }];
    state.set(start, "open");
    while (stack.length > 0) {
      const top = stack[stack.length - 1];
      const outgoing = out.get(top.id) as Edge[];
      if (top.next >= outgoing.length) {
        state.set(top.id, "done");
        stack.pop();
        continue;
      }
      const edge = outgoing[top.next++];
      if (state.get(edge.to) === "open") continue;
      kept.push(edge);
      if (!state.has(edge.to)) {
        state.set(edge.to, "open");
        stack.push({ id: edge.to, next: 0 });
      }
    }
  };

  // Start where a card would: at the stages nothing leads to, then at whatever
  // the walk has not reached (a flow that is one closed loop).
  const reached = new Set(edges.map((e) => e.to));
  for (const stage of stages.filter((s) => !reached.has(s.id))) walk(stage.id);
  for (const stage of stages) if (!state.has(stage.id)) walk(stage.id);
  return kept;
}

// depths puts every stage as far right as the longest path that reaches it, so
// an arrow points forward wherever the flow does.
function depths(stages: Stage[], edges: Edge[]): Map<string, number> {
  const depth = new Map<string, number>(stages.map((s) => [s.id, 0]));
  const dag = forward(stages, edges);
  for (let pass = 0; pass < stages.length; pass++) {
    let moved = false;
    for (const edge of dag) {
      const next = (depth.get(edge.from) as number) + 1;
      if ((depth.get(edge.to) as number) < next) {
        depth.set(edge.to, next);
        moved = true;
      }
    }
    if (!moved) break;
  }
  return depth;
}

// longHaul says which arrows need a corridor of their own rather than the direct
// route between two boxes: anything going backwards, and anything jumping past a
// stage. A step to the next stage is the one case that never crosses anything.
//
// Backwards counts however short it is. An arrow one stage back still has to
// travel the width of both boxes to get there, and the library draws it through
// them: opaque boxes then cut it into pieces, which reads as two broken stubs
// rather than as one arrow.
function longHaul(stages: Stage[], edges: Edge[]): Map<string, number> {
  const depth = depths(stages, edges);
  const out = new Map<string, number>();
  let lane = 0;
  edges.forEach((edge, index) => {
    const from = depth.get(edge.from);
    const to = depth.get(edge.to);
    if (from === undefined || to === undefined) return;
    if (to > from + 1) {
      // forward, past at least one stage
    } else if (to > from) {
      return; // the next stage along: the direct route crosses nothing
    }
    out.set(edgeId(edge, index), lane);
    lane += 1;
  });
  return out;
}

// layout places the stages in columns of equal depth, keeping the editor's own
// order within a column. A stage placed by hand stays where it was put.
function layout(stages: Stage[], edges: Edge[], rowHeight: number): Map<string, { x: number; y: number }> {
  const depth = depths(stages, edges);
  const taken = new Map<number, number>();
  const out = new Map<string, { x: number; y: number }>();
  for (const stage of stages) {
    // A stage that was placed by hand stays where it was put. Absent means "lay
    // it out for me" — a flow written by hand never has to place anything.
    if (stage.x !== undefined && stage.y !== undefined) {
      out.set(stage.id, { x: stage.x, y: stage.y });
      continue;
    }
    const column = depth.get(stage.id) || 0;
    const row = taken.get(column) || 0;
    taken.set(column, row + 1);
    out.set(stage.id, { x: column * (NODE_WIDTH + GAP_X), y: row * (rowHeight + GAP_Y) });
  }
  return out;
}

// An arrow and its head must be one colour, and the head is drawn from a shared
// SVG marker that no class of ours can reach — so the colours are literals here
// rather than CSS variables.
const EDGE_COLOR: Record<string, string> = {
  success: "#3db887",
  failure: "#d24b4e",
  event: "#8b8d94",
};

// edgeKind styles a transition by what produces it: the stage's own outcome, or
// somebody's answer on the card.
function edgeKind(on: string): string {
  if (on === "success") return "success";
  if (on === "failure") return "failure";
  return "event";
}

type StageData = {
  name: string;
  template?: StageTemplate;
  kind: string;
  kindLabel: string;
  detail: string;
  writes?: StageWrite[];
  entry?: boolean;
  editable?: boolean;
  selected?: boolean;
};

// StageBox is one stage: what kind of node it is, what it runs or opens, and
// what it leaves on the card.
const StageBox = (props: NodeProps) => {
  const data = () => props.data as unknown as StageData;
  const classes = () =>
    ["flowbox", `flowbox--${data().kind}`, data().selected ? "flowbox--selected" : ""]
      .filter(Boolean)
      .join(" ");

  return (
    <div class={classes()} style={{ "--kind": data().template?.color || "#949aab" }}>
      <Handle type="target" position="left" isConnectable={Boolean(data().editable)} />

      <div class="flowbox__head">
        <Tile tpl={data().template} />
        <div class="flowbox__text">
          <div class="flowbox__name">
            {data().name || "—"}
            <Show when={data().entry}>
              <span class="flowbox__entry" title={t("flows.entryStage")}> ▸</span>
            </Show>
          </div>
          <div class="flowbox__action">
            <span class="flowbox__kind">{data().kindLabel}</span>
            <Show when={data().detail}>
              <span class="flowbox__detail">{` · ${data().detail}`}</span>
            </Show>
          </div>
        </div>
      </div>

      <Handle
        id={HANDLE_SUCCESS} type="source" position="right"
        style={{ top: `${NODE_HEIGHT * 0.3}px` }} class="flowbox__out flowbox__out--success"
        isConnectable={Boolean(data().editable)}
      />
      <Handle
        id={HANDLE_FAILURE} type="source" position="right"
        style={{ top: `${NODE_HEIGHT * 0.7}px` }} class="flowbox__out flowbox__out--failure"
        isConnectable={Boolean(data().editable)}
      />
      <Handle
        id={HANDLE_EVENT} type="source" position="bottom"
        class="flowbox__out flowbox__out--event"
        isConnectable={Boolean(data().editable)}
      />

      {/* What the stage leaves on the card, and — where the property has stated
          values — the port a fork is pulled from. This is where the dataflow
          becomes visible: the arrow asking «Verdict = fail» is drawn from the
          thing that produces «Verdict». */}
      <Show when={data().writes && data().writes!.length > 0}>
        <div class="flowbox__writes">
          <For each={data().writes}>
            {(w) => (
              <span
                class={`flowbox__write${w.branchable ? " flowbox__write--branchable" : ""}`}
                title={t(w.branchable ? "flows.writeBranchable" : "flows.writeCarried", { name: w.name })}
              >
                {w.name}
                <Show when={w.branchable && data().editable}>
                  <Handle
                    id={HANDLE_WRITE + w.name} type="source" position="bottom"
                    class="flowbox__out flowbox__out--write" isConnectable={true}
                  />
                </Show>
              </span>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
};

const nodeTypes = { stage: StageBox };

// The ring on an arrow's end, in screen pixels: GRAB is what the pointer has to
// land in, MARK is what a person sees. Two numbers because one is a target and
// the other is a mark on a picture — a ring drawn as big as it needs to be
// grabbed sits on the arrow like a bead.
//
// Both are divided by the zoom, because the canvas draws in flow units and a
// stated size shrinks with the picture: 14 units came out 11px on a route fitted
// at 0.78 and 4px at minZoom, which is a thing to grab that a hand cannot land
// on.
const GRAB = 20;

// DRAG_KIND is the type a palette card puts on the drag, so the canvas takes a
// node kind and nothing else a person drags over it.
export const DRAG_KIND = "application/x-xxvi-stage-kind";
const MARK = 9;

// How thick the mark's own ring is drawn, in screen pixels — divided like the
// rest of it, or the outline thins away as the picture is zoomed out.
const MARK_RING = 2;

// lanePath is a long arrow's own way round: out of the handle, down into a
// corridor of its own under the graph, across, and up into the target. Written
// by hand because the corridor is the point — the library puts every arrow
// between the same two rows at the same height, so two long arrows travelled the
// picture side by side and read as one thick line.
export function lanePath(sx: number, sy: number, tx: number, ty: number, lane: number) {
  const out = 18;
  const y = Math.max(sy, ty) + LANE_BASE + lane * LANE;
  const x1 = sx + out;
  const x2 = tx - out;
  return {
    d: `M ${sx},${sy} L ${x1},${sy} L ${x1},${y} L ${x2},${y} L ${x2},${ty} L ${tx},${ty}`,
    labelX: (x1 + x2) / 2,
    labelY: y,
  };
}

// LaneEdge draws a short arrow the way the library would and a long one through
// a lane of its own. It exists at all because the library's edge wrapper
// forwards a fixed list of props, and nothing that shapes the path is on it.
const LaneEdge = (props: EdgeProps) => {
  const path = createMemo(() => {
    const lane = (props.data as { lane?: number } | undefined)?.lane;
    if (lane !== undefined) {
      return lanePath(props.sourceX, props.sourceY, props.targetX, props.targetY, lane);
    }
    const [d, labelX, labelY] = getSmoothStepPath({
      sourceX: props.sourceX,
      sourceY: props.sourceY,
      targetX: props.targetX,
      targetY: props.targetY,
      sourcePosition: props.sourcePosition,
      targetPosition: props.targetPosition,
    });
    return { d, labelX, labelY };
  });

  // A route being read has no way to redraw itself, so the ends are the canvas'
  // own answer to whether this is an editor: `editable` rides in the edge's data
  // the way a stage's does. Nothing but data goes there — the canvas owns that
  // store and copies it, and a callback in one is a function surviving a proxy
  // by luck.
  const editable = () => Boolean((props.data as { editable?: boolean } | undefined)?.editable);

  const viewport = useViewport();
  const zoom = () => viewport().zoom || 1;
  const mark = () => ({
    width: `${MARK / zoom()}px`,
    height: `${MARK / zoom()}px`,
    "--mark-ring": `${MARK_RING / zoom()}px`,
  });
  return (
    <>
      <BaseEdge
        path={path().d}
        style={props.style}
        markerEnd={props.markerEnd}
        interactionWidth={props.interactionWidth}
      />

      {/* How an arrow is broken or re-pointed: grab its end and drag. Dropping
          it on another stage moves the arrow there; dropping it on nothing at
          all breaks it (onReconnectEnd). Per-end rather than per-socket on
          purpose — a socket holds as many arrows as the stage forks into, so a
          control on the socket could not say which one it meant. */}
      <Show when={props.selected && editable()}>
        <EdgeReconnectAnchor type="source" class="flowedge__anchor" size={GRAB / zoom()}
          position={{ x: props.sourceX, y: props.sourceY }}>
          <span class="flowedge__anchorMark" style={mark()} />
        </EdgeReconnectAnchor>
        <EdgeReconnectAnchor type="target" class="flowedge__anchor" size={GRAB / zoom()}
          position={{ x: props.targetX, y: props.targetY }}>
          <span class="flowedge__anchorMark" style={mark()} />
        </EdgeReconnectAnchor>
      </Show>

      <Show when={props.label}>
        <EdgeLabel x={path().labelX} y={path().labelY} style={props.labelStyle}>
          <span class="flowedge__label">{props.label}</span>
        </EdgeLabel>
      </Show>
    </>
  );
};

const edgeTypes = { lane: LaneEdge };

// The part of the canvas API this component reaches for. `fitView` is optional
// because the handle is also read where the canvas measures nothing.
//
// What a box measured is read off a record rather than asked for: Solid Flow 1.0
// dropped the imperative getters, because a getter hands back a snapshot and the
// record is the store the canvas keeps its answers in.
type FitOptions = {
  padding?: number | { top?: `${number}px`; right?: `${number}px`; bottom?: `${number}px`; left?: `${number}px` };
  maxZoom?: number;
  nodes?: Array<{ id: string }>;
};

type FlowHandle = {
  screenToFlowPosition?: (at: { x: number; y: number }) => { x: number; y: number };
  fitView?: (options?: FitOptions) => Promise<boolean> | void;
  internalNodes?: Record<string, { measured?: { width?: number; height?: number } } | undefined>;
};

// How the graph sits in the canvas: the whole picture, with a margin small
// enough that the boxes are the point and the emptiness around them is not.
//
// maxZoom is 1 — "the box at the size it was drawn". Magnifying a flow of three
// stages to fill the canvas reads as a mistake rather than as a short flow, and
// makes one stage change size depending on which flow it is on.
const FIT_VIEW = { padding: 0.16, maxZoom: 1 };

// FIT_MARGIN is the room left around the picture when something floats over
// one side of the canvas: the stated inset, and this much more on every side.
const FIT_MARGIN = 48;

type Inset = { left: number; right: number };

// fitOptions keeps the picture out from under whatever floats over the
// canvas' edges — the palette, the inspector — so a flow is fitted to the part
// of the canvas that is actually visible.
function fitOptions(inset: Inset): FitOptions {
  if (inset.left <= 0 && inset.right <= 0) return FIT_VIEW;
  const m = `${FIT_MARGIN}px` as const;
  return {
    maxZoom: 1,
    padding: { top: m, bottom: m, left: `${inset.left + FIT_MARGIN}px`, right: `${inset.right + FIT_MARGIN}px` },
  };
}

// How many frames a re-fit waits for the canvas to measure the boxes it has just
// been handed. Generous, because being late costs nothing and being early costs
// the whole picture: `fitView` fits to what the canvas *knows*, and it learns a
// box's size from a resize observer a frame or more after being handed it — so
// fitting straight away fits to boxes of no size. Bounded, so a box that is
// never measured does not spin for ever.
const FIT_FRAMES = 30;

const fitWhenMeasured = (handle: FlowHandle, ids: string[], inset: Inset, frame: number) => {
  const measured = ids.every((id) => (handle.internalNodes?.[id]?.measured?.width || 0) > 0);
  if (!measured && frame < FIT_FRAMES) {
    requestAnimationFrame(() => fitWhenMeasured(handle, ids, inset, frame + 1));
    return;
  }
  void handle.fitView?.({ ...fitOptions(inset), nodes: ids.map((id) => ({ id })) });
};

// CanvasHook runs inside the canvas' context and hands its API out.
//
// The API is read in the body, because that is the only place the canvas'
// context is in scope; handing it out is a write, which Solid 2 refuses during
// render, so that half waits for the settle.
const CanvasHook = (props: { onReady: (flow: FlowHandle) => void }) => {
  const flow = useSolidFlow() as unknown as FlowHandle;
  onSettled(() => {
    props.onReady(flow);
  });
  return null;
};

// connectEdge is what pulling a connection means: the handle says which
// transition it is, so nothing has to be chosen afterwards — except which value,
// which is the one thing the gesture cannot carry.
export function connectEdge(
  edges: Edge[],
  from: string,
  to: string,
  handle: string | null | undefined,
  waitTriggers: Trigger[],
): Edge[] {
  if (!from || !to || from === to) return edges;

  // A connection pulled from a data port is the fork the whole row of outputs
  // exists for: "the card goes here depending on that property". The value is
  // left empty and the caption says so, because which value is chosen where the
  // arrow is, in the panel.
  if (handle && handle.startsWith(HANDLE_WRITE)) {
    const property = handle.slice(HANDLE_WRITE.length);
    return [...edges, { from, to, on: "success", if: { property, value: "" } } as Edge];
  }

  let on = handle || HANDLE_SUCCESS;
  if (on === HANDLE_EVENT) {
    // card.changed is never auto-picked: it is meaningless without its
    // condition, and the panel is where to state one.
    const used = new Set(edges.filter((e) => e.from === from).map((e) => e.on));
    const free = waitTriggers.find((t) => t.kind !== "card.changed" && !used.has(t.kind));
    if (!free) return edges;
    on = free.kind;
  }

  // Pulling the same output again redraws the unconditional edge; the
  // conditional ones are the fork's branches and stay as they are.
  return [...edges.filter((e) => !(e.from === from && e.on === on && !e.if)), { from, to, on } as Edge];
}

// condLabel is a condition as an edge caption: the question, not a sentence.
export function condLabel(edge: Edge): string {
  const cond = edge.if;
  if (!cond) return "";
  if (cond.commentContains) return t("flows.condComment", { text: cond.commentContains });
  // A fork pulled from a data port arrives without its value, and an arrow
  // captioned «Verdict = » reads as a bug rather than as a question waiting to
  // be answered.
  if (!cond.value) return `${propName(cond.property ?? "")} = ?`;
  return `${propName(cond.property ?? "")} = ${propValue(cond.property ?? "", cond.value)}`;
}

export default function FlowCanvas(props: Props) {
  const editable = () => Boolean(props.onChange);
  const stages = () => props.flow.stages || [];
  const edges = () => props.flow.edges || [];
  const waitTriggers = () => props.triggers.filter((t) => t.source !== "outcome");

  // The transitions that are answers rather than wiring are kept off the lane
  // bookkeeping: they are drawn as outcomes, out of the outcome sockets.
  const wiring = createMemo(() => edges().filter((e) => !isOutcomeEdge(e, props.outcomeProperty)));
  const lanes = createMemo(() => longHaul(stages(), wiring()));

  const graph = createMemo(() => {
    const tallest = stages().reduce((at, s) => Math.max(at, stageHeight(props.writesOf?.(s))), NODE_HEIGHT);
    const positions = layout(stages(), edges(), tallest);

    const drawnNodes: FlowNode[] = stages().map((stage) => {
      const writes = props.writesOf?.(stage);
      const data: StageData = {
        name: stage.name,
        template: templateOf(stage, props.templates),
        kind: stage.final ? "final" : stage.action || "none",
        kindLabel: templateName(templateOf(stage, props.templates)),
        detail: nodeDetail(stage, ownsScreen(templateOf(stage, props.templates))),
        writes,
        entry: props.flow.entryStage === stage.id,
        editable: editable(),
        selected: props.selected?.kind === "stage" && props.selected.id === stage.id,
      };
      return {
        id: stage.id,
        type: "stage",
        position: positions.get(stage.id) || { x: 0, y: 0 },

        // Stated rather than measured: the box is a fixed size and its handles
        // sit at fixed points, so the arrows are drawn on the first paint.
        //
        // Every handle states its own size too. The canvas finds where an arrow
        // starts by adding the handle's width and height to its corner, and the
        // library defaults those by writing them back into the node — which a
        // Solid store's proxy ignores, silently. The sum is then NaN and the
        // path is drawn as `MNaN NaN…`, which renders as nothing.
        width: NODE_WIDTH,
        height: stageHeight(writes),
        handles: [
          { type: "target", position: Position.Left, x: 0, y: NODE_HEIGHT / 2, width: 1, height: 1 },
          { type: "source", id: HANDLE_SUCCESS, position: Position.Right, x: NODE_WIDTH, y: NODE_HEIGHT * 0.3, width: 1, height: 1 },
          { type: "source", id: HANDLE_FAILURE, position: Position.Right, x: NODE_WIDTH, y: NODE_HEIGHT * 0.7, width: 1, height: 1 },
          { type: "source", id: HANDLE_EVENT, position: Position.Bottom, x: NODE_WIDTH / 2, y: NODE_HEIGHT, width: 1, height: 1 },

          // The data ports, spread along the bottom of the row of chips. Their x
          // is an even share of the width rather than the chip's real position:
          // the box is stated and not measured, and an arrow leaving a little to
          // one side of its own chip is a smaller lie than one drawn from NaN.
          ...(writes || []).filter((w) => w.branchable).map((w, i, all) => ({
            type: "source" as const,
            id: HANDLE_WRITE + w.name,
            position: Position.Bottom,
            x: (NODE_WIDTH * (i + 1)) / (all.length + 1),
            y: stageHeight(writes),
            width: 1,
            height: 1,
          })),
        ],
        data: data as unknown as Record<string, unknown>,
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      } as FlowNode;
    });

    const known = new Set(stages().map((s) => s.id));
    const drawnEdges: FlowEdge[] = edges()
      .map((edge, index) => ({ edge, index }))
      .filter(({ edge }) => known.has(edge.from) && known.has(edge.to))
      .map(({ edge, index }) => {
        // An outcome answered on the card is a success or a failure like any
        // other, and is drawn as one: green on, red back, out of the same two
        // sockets. It rides card.changed because that is how a person's answer
        // reaches the engine.
        const answered = isOutcomeEdge(edge, props.outcomeProperty);
        let kind = edgeKind(edge.on);
        if (answered) kind = sameFold(edge.if?.value, props.outcomePassed) ? "success" : "failure";
        const color = EDGE_COLOR[kind];

        // The caption is what decides where the card goes: the event for a wait,
        // the condition for a fork — both where the arrow is, not three clicks
        // away.
        const parts: string[] = [];
        const cond = answered ? "" : condLabel(edge);

        // For card.changed the condition is not a guard on the event, it *is*
        // the event — «set on the card» followed by «Outcome = failed» says
        // the same thing twice, in a caption that then fits nowhere.
        if (kind === "event" && !(edge.on === "card.changed" && cond)) {
          parts.push(label("trigger", edge.on));
        }
        if (cond) parts.push(edge.on === "card.changed" ? cond : t("flows.ifCond", { cond }));

        const id = edgeId(edge, index);
        const chosen = props.selected?.kind === "edge" && props.selected.id === id;

        // A fork pulled from a data port arrives without its value. Amber rather
        // than red: the arrow is not wrong, it is waiting for the one thing the
        // gesture could not carry. A style rather than a class, because the
        // label is rendered into a portal of the library's own.
        const unanswered = Boolean(edge.if && !edge.if.value && !edge.if.commentContains);

        return {
          id,
          source: edge.from,
          target: edge.to,
          sourceHandle: kind === "event" ? HANDLE_EVENT : kind,
          type: "lane",
          class: `flowedge flowedge--${kind}${chosen ? " flowedge--selected" : ""}`,
          selected: chosen,
          label: parts.join(" · "),
          labelStyle: unanswered ? { color: "#d9903d", "font-weight": "600" } : undefined,
          style: {
            stroke: color,
            "stroke-width": chosen ? "3" : "1.5",
            "stroke-dasharray": kind === "event" ? "4 3" : undefined,
          },
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color },
          data: { lane: lanes().get(id), editable: editable() },
        } as FlowEdge;
      });

    return { drawnNodes, drawnEdges };
  });

  // Stages can always be dragged apart when arrows overlap: the canvas moves
  // them inside these stores. Where the flow is being edited the position is
  // part of it and is saved.
  const [drawnNodes, setDrawnNodes] = createNodeStore([]) as unknown as [FlowNode[], (next: () => FlowNode[]) => void];
  const [drawnEdges, setDrawnEdges] = createEdgeStore([]) as unknown as [FlowEdge[], (next: () => FlowEdge[]) => void];

  createEffect(graph, (drawn) => {
    setDrawnNodes(() => drawn.drawnNodes);
    setDrawnEdges(() => drawn.drawnEdges);
  });

  const onConnect = (connection: Connection) => {
    props.onChange?.(stages(), connectEdge(edges(), connection.source, connection.target, connection.sourceHandle, waitTriggers()));
  };

  const onNodeDragStop = ({ targetNode }: { targetNode: FlowNode | null }) => {
    if (!props.onChange || !targetNode) return;
    props.onChange(
      stages().map((s) => (s.id === targetNode.id ? { ...s, x: targetNode.position.x, y: targetNode.position.y } : s)),
      edges(),
    );
  };

  const onNodesDelete = (deleted: FlowNode[]) => {
    if (!props.onChange) return;
    const gone = new Set(deleted.map((n) => n.id));
    props.onChange(
      stages().filter((s) => !gone.has(s.id)),
      // A transition to a stage that is no longer there is exactly what the
      // engine refuses to save.
      edges().filter((e) => !gone.has(e.from) && !gone.has(e.to)),
    );
  };

  // Breaking one arrow, by the id the canvas knows it as — which carries the
  // index the flow stores it at.
  const removeEdgeById = (id: string) => {
    if (!props.onChange) return;
    props.onChange(stages(), edges().filter((e, i) => edgeId(e, i) !== id));
    props.onSelect?.(null);
  };

  // An arrow's end dropped on another stage: the arrow moves there rather than a
  // second one appearing beside it.
  const onReconnect = (edge: FlowEdge, connection: Connection) => {
    props.onChange?.(
      stages(),
      edges().map((e, i) => (edgeId(e, i) === edge.id ? { ...e, from: connection.source, to: connection.target } : e)),
    );
  };

  // …and dropped on nothing: the arrow is broken. That is the gesture the anchor
  // exists for — cutting a link where the link is, without hunting for a control
  // first. The state a reconnect ends with is `boolean | null`, not `boolean`.
  const onReconnectEnd = (_e: MouseEvent | TouchEvent, edge: FlowEdge, _type: string, state: { isValid?: boolean | null }) => {
    if (!props.onChange || state?.isValid) return;
    removeEdgeById(edge.id);
  };

  const onEdgesDelete = (deleted: FlowEdge[]) => {
    if (!props.onChange) return;
    const gone = new Set(deleted.map((e) => e.id));
    props.onChange(stages(), edges().filter((e, i) => !gone.has(edgeId(e, i))));
  };

  const [flowHandle, setFlowHandle] = createSignal<FlowHandle | null>(null);

  // `fitView` is an init option, and what is on the canvas changes after the
  // canvas is built: the editor opens on one flow and every click replaces every
  // box. So the fit is redone rather than configured once.
  //
  // Keyed on *which flow* this is, and not on its stages: a fit in the middle of
  // a drag would pull the canvas out from under the pointer, and one after every
  // box added shrank the picture each time a person built a flow out to the
  // right. A new box is put where it can be seen instead (onApi). Whether there
  // is anything to fit is in the key too, for the flow whose stages arrive after
  // it does; the handle, because it arrives from inside the canvas and is not
  // there on the first run.
  const [paneSize, setPaneSize] = createSignal("");
  //
  // The key is a memo of a string: an effect applies again every time its
  // source is recomputed, equal or not, and only a memo stops an equal answer
  // there. Without it every box added re-fitted the picture.
  const fitKey = createMemo(() =>
    [props.flow.id, stages().length > 0, paneSize(), Boolean(flowHandle()), props.insetLeft ?? 0].join("|"));
  createEffect(
    fitKey,
    () => {
      // Everything but the key is read here, untracked. The right inset among
      // them: the inspector opening is no reason to move the picture, but a
      // fit that happens while it is open should not put a box under it.
      const handle = flowHandle();
      const ids = stages().map((s) => s.id);
      if (handle?.fitView && ids.length > 0) {
        fitWhenMeasured(handle, ids, { left: props.insetLeft ?? 0, right: props.insetRight ?? 0 }, 0);
      }
    },
  );

  // Where a new box should go to be seen: the middle of the part of the canvas
  // no panel covers, in the flow's own coordinates, less half a box so the box
  // and not its corner lands there.
  createEffect(flowHandle, (handle) => {
    if (!handle?.screenToFlowPosition || !props.onApi) return;
    const toFlow = handle.screenToFlowPosition;
    props.onApi({
      visibleCentre: (rightInset?: number) => {
        const box = pane()?.getBoundingClientRect();
        if (!box) return undefined;
        const left = box.left + (props.insetLeft ?? 0);
        const right = box.right - (rightInset ?? props.insetRight ?? 0);
        const at = toFlow({ x: (left + right) / 2, y: box.top + box.height / 2 });
        return { x: Math.round(at.x - NODE_WIDTH / 2), y: Math.round(at.y - NODE_HEIGHT / 2) };
      },
    });
  });

  // …and when the canvas itself changes size. A window resized leaves the
  // picture where it was: correct for the box it was fitted to and off centre in
  // the new one. Rounded to whole pixels so a sub-pixel reflow is not a re-fit.
  //
  // The pane arrives through a signal rather than a ref callback that observes
  // it there: a ref runs outside any owner in Solid 2, so a cleanup registered
  // in one never runs and the observer outlives the canvas.
  const [pane, setPane] = createSignal<HTMLDivElement>();
  createEffect(pane, (el) => {
    if (!el || typeof ResizeObserver !== "function") return undefined;
    const observer = new ResizeObserver(([entry]) => {
      const box = entry.contentRect;
      setPaneSize(`${Math.round(box.width)}x${Math.round(box.height)}`);
    });
    observer.observe(el);
    return () => observer.disconnect();
  });

  // A card from the palette dragged onto the canvas becomes a node where it was
  // let go — the box's corner under the pointer, less half a box so the pointer
  // ends up on the middle of it rather than on its edge.
  const onDragOver = (e: DragEvent) => {
    if (!props.onDropKind || !e.dataTransfer?.types.includes(DRAG_KIND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  const onDrop = (e: DragEvent) => {
    const kind = e.dataTransfer?.getData(DRAG_KIND);
    const toFlow = flowHandle()?.screenToFlowPosition;
    if (!kind || !props.onDropKind || !toFlow) return;
    e.preventDefault();
    const at = toFlow({ x: e.clientX, y: e.clientY });
    props.onDropKind(kind, { x: Math.round(at.x - NODE_WIDTH / 2), y: Math.round(at.y - NODE_HEIGHT / 2) });
  };

  return (
    <div ref={setPane} class={`canvas${editable() ? " canvas--editable" : ""}`} data-testid="flow-canvas"
         style={{ "--inset-left": `${props.insetLeft ?? 0}px` }}
         onDragOver={onDragOver} onDrop={onDrop}>
      <SolidFlow
        nodes={drawnNodes}
        edges={drawnEdges}
        onConnect={onConnect}
        onNodeDragStop={onNodeDragStop}
        onNodesDelete={onNodesDelete}
        onEdgesDelete={onEdgesDelete}
        onNodeClick={({ node }: { node: FlowNode }) => props.onSelect?.({ kind: "stage", id: node.id })}
        onEdgeClick={({ edge }: { edge: FlowEdge }) => props.onSelect?.({ kind: "edge", id: edge.id })}
        onReconnect={onReconnect}
        onReconnectEnd={onReconnectEnd}
        onPaneClick={() => props.onSelect?.(null)}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        nodesConnectable={editable()}
        elementsSelectable={editable()}
        edgesFocusable={editable()}
        deleteKey={editable() ? ["Backspace", "Delete"] : null}
        fitView={true}
        fitViewOptions={fitOptions({ left: props.insetLeft ?? 0, right: 0 })}
        minZoom={0.3}

        // Solid Flow is MIT and its own attribution says to feel free to
        // remove it; the canvas is small and the plate sits over the boxes.
        proOptions={{ hideAttribution: true }}
      >
        <Background />
        <Controls showLock={false} />
        <CanvasHook onReady={setFlowHandle} />
      </SolidFlow>
    </div>
  );
}
