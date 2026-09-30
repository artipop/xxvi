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
import { themeColor } from "../theme";
import { KIND_FALLBACK, Tile, nodeDetail, ownsScreen, templateName, templateOf } from "./templates";
import {
  CAPTION_HEIGHT, type Layout, type LayoutEdge, type Point, PORT_FAILURE, PORT_IN, PORT_SUCCESS, type Route,
  backLabelSpot, captionWidth, elkCaptions, entryLabelSpot, layoutFlow, roundedPath,
} from "./flowLayout";

// The flow as a graph. Pan, zoom and drag are Solid Flow's; where the boxes
// stand and how the arrows run is ELK's (flowLayout.ts).
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

// StageWrite is one property the stage leaves on the card, as the box draws it.
// `branchable` is whether an arrow can ask about it: an arrow asks "is it this
// value", so a property with no stated values is drawn and not connectable.
export type StageWrite = {
  name: string;
  branchable: boolean;
};

export type CanvasApi = {
  // reveal brings a box into sight once the layout has placed it: a stage just
  // added goes to the end of the flow, which may be off the picture. `right` is
  // how much of the right edge will be covered — by the inspector that opens
  // for the new stage, open or not yet.
  reveal: (stageId: string, right?: number) => void;
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

  // A template dropped from the palette onto the canvas. Where it lands is
  // not handed over: the layout places it.
  onDropKind?: (template: string) => void;

  // How much of the canvas' left edge something floats over, in pixels. The
  // picture is fitted clear of it, and the zoom plate moves out from under it.
  insetLeft?: number;
  // …and of its right edge, which only a fit made while it is covered minds.
  insetRight?: number;

  // Hands out what the editor needs of the canvas: bringing a new box into
  // sight.
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

// layout places the stages in columns of equal depth, keeping the editor's own
// order within a column, and a stage placed by hand where it was put. It is what
// the canvas shows for the moment before ELK has answered, and nothing more.
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
// SVG marker that no class of ours can reach — so the colour is the token's
// value, read off the page, rather than the variable.
const EDGE_TOKEN: Record<string, string> = {
  success: "--ok",
  failure: "--bad",
  event: "--dim",
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
    <div class={classes()} style={{ "--kind": data().template?.color || KIND_FALLBACK }}>
      {/* At the port's height rather than the library's middle of the box: a
          box with a row of outputs is taller, and its middle is not where ELK
          brings the arrows in. */}
      <Handle type="target" position="left" style={{ top: `${NODE_HEIGHT * PORT_IN}px` }}
              isConnectable={Boolean(data().editable)} />

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
        style={{ top: `${NODE_HEIGHT * PORT_SUCCESS}px` }} class="flowbox__out flowbox__out--success"
        isConnectable={Boolean(data().editable)}
      />
      <Handle
        id={HANDLE_FAILURE} type="source" position="right"
        style={{ top: `${NODE_HEIGHT * PORT_FAILURE}px` }} class="flowbox__out flowbox__out--failure"
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
                title={t(w.branchable ? "flows.writeBranchable" : "flows.writeCarried", { name: propName(w.name) })}
              >
                {propName(w.name)}
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

type EdgeData = {
  route?: Route;
  back?: boolean;
  // Who places the caption: ELK (in the route), or the canvas — by the bus a
  // way back runs along, or at the box an event comes into.
  captionAt?: "elk" | "back" | "entry";
  captionWidth?: number;
  editable?: boolean;
};

// RoutedEdge draws an arrow along the route ELK laid for it: straight runs,
// rounded corners, and a dot where arrows join. It exists at all because the
// library's edge wrapper forwards a fixed list of props, and nothing that
// shapes the path is on it.
const RoutedEdge = (props: EdgeProps) => {
  const data = () => (props.data ?? {}) as EdgeData;
  const path = createMemo(() => {
    const route = data().route;
    const pts = route?.points;
    if (pts && pts.length >= 2) {
      // Drawn as ELK laid it, ends included: its ports are where the lines
      // meet the boxes, and the library's measured dots only mark them.
      const points = pts;
      const width = data().captionWidth ?? 0;
      const at = data().captionAt;
      const label = at === "back" ? backLabelSpot(points, width)
        : at === "entry" ? entryLabelSpot(points, width)
        : route!.label;
      const mid = points[Math.floor(points.length / 2)];
      return {
        d: roundedPath(points),
        labelX: label?.x ?? mid.x,
        labelY: label?.y ?? mid.y,
        junctions: route!.junctions,
      };
    }
    const [d, labelX, labelY] = getSmoothStepPath({
      sourceX: props.sourceX,
      sourceY: props.sourceY,
      targetX: props.targetX,
      targetY: props.targetY,
      sourcePosition: props.sourcePosition,
      targetPosition: props.targetPosition,
      borderRadius: 10,
    });
    return { d, labelX, labelY, junctions: [] as Point[] };
  });

  // A route being read has no way to redraw itself, so the ends are the canvas'
  // own answer to whether this is an editor: `editable` rides in the edge's data
  // the way a stage's does. Nothing but data goes there — the canvas owns that
  // store and copies it, and a callback in one is a function surviving a proxy
  // by luck.
  const editable = () => Boolean(data().editable);

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

      {/* Where arrows run together into one: the dot says they join here
          rather than cross. */}
      <For each={path().junctions}>
        {(j) => <circle class="flowedge__junction" cx={j.x} cy={j.y} r={3.5} fill={(props.style as { stroke?: string } | undefined)?.stroke} />}
      </For>

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

const edgeTypes = { routed: RoutedEdge };

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
  setCenter?: (x: number, y: number, options?: { zoom?: number; duration?: number }) => Promise<boolean>;
  fitView?: (options?: FitOptions) => Promise<boolean> | void;
  // The read surface. Solid Flow 1.0 keeps it under `flow` and spreads only
  // the commands at the top; read at the top, both of these were undefined.
  flow?: {
    internalNodes?: Record<string, { measured?: { width?: number; height?: number } } | undefined>;
    viewport?: { x: number; y: number; zoom: number };
  };
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
  const measured = ids.every((id) => (handle.flow?.internalNodes?.[id]?.measured?.width || 0) > 0);
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

// modelOrder is the stages in the order a card meets them: from the entry,
// depth first, «passed» before «failed» before everything else, then whatever
// nothing reaches. ELK reads it as the direction of travel — which arrow of a
// cycle is the way back — and as the order of stages it has no other reason to
// order.
function modelOrder(stages: Stage[], edges: Edge[], entry: string): Stage[] {
  const byId = new Map(stages.map((s) => [s.id, s]));
  const rankOn = (on: string) => (on === "success" ? 0 : on === "failure" ? 1 : 2);
  const out: Stage[] = [];
  const seen = new Set<string>();
  const visit = (id: string) => {
    const stage = byId.get(id);
    if (!stage || seen.has(id)) return;
    seen.add(id);
    out.push(stage);
    edges
      .filter((e) => e.from === id)
      .sort((a, b) => rankOn(a.on) - rankOn(b.on))
      .forEach((e) => visit(e.to));
  };
  visit(entry);
  for (const s of stages) visit(s.id);
  return out;
}

export default function FlowCanvas(props: Props) {
  const editable = () => Boolean(props.onChange);
  const stages = () => props.flow.stages || [];
  const edges = () => props.flow.edges || [];
  const waitTriggers = () => props.triggers.filter((t) => t.source !== "outcome");

  // What each arrow is, worked out once for both the layout and the drawing:
  // its colour, its port, its caption and whether it leads back.
  const arrows = createMemo(() => {
    const order = modelOrder(stages(), edges(), props.flow.entryStage);
    const rank = new Map(order.map((s, i) => [s.id, i]));
    return edges().map((edge, index) => {
      // An outcome answered on the card is a success or a failure like any
      // other, and is drawn as one: green on, red back, out of the same two
      // sockets. It rides card.changed because that is how a person's answer
      // reaches the engine.
      const answered = isOutcomeEdge(edge, props.outcomeProperty);
      let kind = edgeKind(edge.on);
      if (answered) kind = sameFold(edge.if?.value, props.outcomePassed) ? "success" : "failure";

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
      const caption = parts.join(" · ");

      // Back is back the way a card came: to a stage it meets no later than
      // this one. The same test ELK breaks cycles by, since both read the
      // stages in the order modelOrder gives.
      const back = (rank.get(edge.to) ?? 0) <= (rank.get(edge.from) ?? 0);
      return { edge, index, id: edgeId(edge, index), kind, caption, back, answered };
    });
  });

  // Which arrows carry their caption. An event said once per box it leads
  // into: «MR merged» from every stage of a review flow runs into one bus, and
  // a caption on each of them wrote it along the bus half a dozen times.
  const captioned = createMemo(() => {
    const said = new Set<string>();
    const out = new Set<string>();
    for (const a of arrows()) {
      if (!a.caption) continue;
      if (a.kind === "event") {
        const key = `${a.edge.to}\u0000${a.caption}`;
        if (said.has(key)) continue;
        said.add(key);
      }
      out.add(a.id);
    }
    return out;
  });

  // The layout, as ELK last answered it. Asked again whenever anything it
  // depends on changes — the key says what that is — and an answer that
  // arrives after a newer question was asked is dropped.
  const [laid, setLaid] = createSignal<Layout | null>(null);
  const layoutInput = createMemo(() => {
    const order = modelOrder(stages(), edges(), props.flow.entryStage);
    const nodes = order.map((s) => ({
      id: s.id,
      width: NODE_WIDTH,
      height: stageHeight(props.writesOf?.(s)),
    }));
    const known = new Set(nodes.map((n) => n.id));
    // Edges are handed over in the order of their source, for the same reason
    // the stages are: ELK reads the order as the direction of travel.
    const laidEdges: LayoutEdge[] = arrows()
      .filter((a) => known.has(a.edge.from) && known.has(a.edge.to))
      .sort((a, b) => order.findIndex((s) => s.id === a.edge.from) - order.findIndex((s) => s.id === b.edge.from))
      .map((a) => ({
        id: a.id,
        from: a.edge.from,
        to: a.edge.to,
        port: a.kind === "event" ? "event" : (a.kind as "success" | "failure"),
        trigger: a.kind === "event" ? a.edge.on : undefined,
        label: a.caption ? { width: captionWidth(a.caption), height: CAPTION_HEIGHT, text: a.caption } : undefined,
        back: a.back,
      }));
    return { key: JSON.stringify([nodes, laidEdges]), nodes, edges: laidEdges };
  });
  createEffect(layoutInput, (input) => {
    let current = true;

    layoutFlow(input.key, input.nodes, input.edges, NODE_HEIGHT)
      .then((result) => { if (current) setLaid(result); })
      .catch((err) => console.error("flow layout", err));
    return () => { current = false; };
  });

  const graph = createMemo(() => {
    const tallest = stages().reduce((at, s) => Math.max(at, stageHeight(props.writesOf?.(s))), NODE_HEIGHT);
    const fallback = layout(stages(), edges(), tallest);
    const done = laid();
    // A route is only as good as the layout it came from: one laid for the
    // graph before this change joins boxes that may since have moved. Until
    // ELK answers, the library's own path stands in for it.
    const fresh = done !== null && done.key === layoutInput().key;

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
        position: done?.nodes.get(stage.id) ?? fallback.get(stage.id) ?? { x: 0, y: 0 },

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
          { type: "source", id: HANDLE_SUCCESS, position: Position.Right, x: NODE_WIDTH, y: NODE_HEIGHT * PORT_SUCCESS, width: 1, height: 1 },
          { type: "source", id: HANDLE_FAILURE, position: Position.Right, x: NODE_WIDTH, y: NODE_HEIGHT * PORT_FAILURE, width: 1, height: 1 },
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
    const drawnEdges: FlowEdge[] = arrows()
      .filter(({ edge }) => known.has(edge.from) && known.has(edge.to))
      .map(({ edge, id, kind, caption, back }) => {
        const color = themeColor(EDGE_TOKEN[kind]);
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
          type: "routed",
          class: `flowedge flowedge--${kind}${back ? " flowedge--back" : ""}${chosen ? " flowedge--selected" : ""}`,
          selected: chosen,
          label: captioned().has(id) ? caption : "",
          labelStyle: unanswered ? { color: "var(--warn)", "font-weight": "600" } : undefined,
          style: {
            stroke: color,
            "stroke-width": chosen ? "2.75" : "1.75",
            "stroke-linejoin": "round",
            "stroke-linecap": "round",
            "stroke-dasharray": kind === "event" ? "5 4" : undefined,
          },
          markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14, color },
          data: {
            route: fresh ? done!.routes.get(id) : undefined,
            back,
            captionAt: back ? "back" : elkCaptions({ back, port: kind === "event" ? "event" : "success" }) ? "elk" : "entry",
            captionWidth: caption ? captionWidth(caption) : 0,
            editable: editable(),
          } satisfies EdgeData,
        } as FlowEdge;
      });

    return { drawnNodes, drawnEdges };
  });

  const [drawnNodes, setDrawnNodes] = createNodeStore([]) as unknown as [FlowNode[], (next: () => FlowNode[]) => void];
  const [drawnEdges, setDrawnEdges] = createEdgeStore([]) as unknown as [FlowEdge[], (next: () => FlowEdge[]) => void];

  createEffect(graph, (drawn) => {
    setDrawnNodes(() => drawn.drawnNodes);
    setDrawnEdges(() => drawn.drawnEdges);
  });

  const onConnect = (connection: Connection) => {
    props.onChange?.(stages(), connectEdge(edges(), connection.source, connection.target, connection.sourceHandle, waitTriggers()));
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
    [props.flow.id, stages().length > 0 && laid() !== null, paneSize(), Boolean(flowHandle()), props.insetLeft ?? 0].join("|"));
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

  // A box to bring into sight when the layout has placed it — asked for before
  // ELK has answered, so kept until it does.
  const [revealing, setRevealing] = createSignal<{ id: string; right: number } | null>(null);
  createEffect(flowHandle, (handle) => {
    if (!props.onApi) return;
    props.onApi({ reveal: (id, right) => setRevealing({ id, right: right ?? props.insetRight ?? 0 }) });
    void handle;
  });
  createEffect(
    () => {
      const want = revealing();
      const done = laid();
      return want && done?.nodes.has(want.id) ? { want, at: done.nodes.get(want.id)! } : null;
    },
    (found) => {
      if (!found) return;
      setRevealing(null);
      const handle = flowHandle();
      const box = pane()?.getBoundingClientRect();
      const vp = handle?.flow?.viewport;
      if (!handle?.setCenter || !box || !vp) return;
      // What of the pane no panel covers, in screen pixels from its corner.
      const left = props.insetLeft ?? 0;
      const right = box.width - found.want.right;
      const height = stageHeight(props.writesOf?.(stages().find((s) => s.id === found.want.id) ?? ({} as Stage)));
      const sx = found.at.x * vp.zoom + vp.x;
      const sy = found.at.y * vp.zoom + vp.y;
      const inside = sx >= left && sx + NODE_WIDTH * vp.zoom <= right && sy >= 0 && sy + height * vp.zoom <= box.height;
      if (inside) return;
      // Centred in the uncovered part rather than in the pane: setCenter
      // centres in the whole pane, so the point handed to it is shifted by how
      // far the uncovered part's middle stands from the pane's.
      const shift = (box.width / 2 - (left + right) / 2) / vp.zoom;
      void handle.setCenter(found.at.x + NODE_WIDTH / 2 + shift, found.at.y + height / 2, { zoom: vp.zoom, duration: 300 });
    },
  );

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

  // A card from the palette dragged onto the canvas becomes a node.
  const onDragOver = (e: DragEvent) => {
    if (!props.onDropKind || !e.dataTransfer?.types.includes(DRAG_KIND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  const onDrop = (e: DragEvent) => {
    const kind = e.dataTransfer?.getData(DRAG_KIND);
    if (!kind || !props.onDropKind) return;
    e.preventDefault();
    props.onDropKind(kind);
  };


  return (
    <div ref={setPane} class={`canvas${editable() ? " canvas--editable" : ""}`} data-testid="flow-canvas"
         style={{ "--inset-left": `${props.insetLeft ?? 0}px` }}
         onDragOver={onDragOver} onDrop={onDrop}>
      <SolidFlow
        nodes={drawnNodes}
        edges={drawnEdges}
        onConnect={onConnect}
        // Where a box stands is the layout's, not the hand's: a box dragged
        // aside went straight back, or — taken as a hint — pulled the row
        // out of line with positions left over from the old editor.
        nodesDraggable={false}
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
