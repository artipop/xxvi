import { createMemo, createSignal, For, onSettled, Show } from "solid-js";
import type { JSX } from "@solidjs/web";
import { Events } from "@wailsio/runtime";
import * as API from "../../bindings/github.com/artipop/xxvi/internal/app/api";
import type { LaunchPlan, LaunchRun } from "../../bindings/github.com/artipop/xxvi/internal/app/models";
import type { Profile, Response } from "../../bindings/github.com/artipop/xxvi/internal/launch/models";
import { guard, list, openOutside } from "../state";
import { label, t } from "../i18n";
import TerminalPane from "./terminal";

// The run screen: the project started the way its kind is looked at. What it
// is comes from the backend's reading of the files, and every part of that
// guess is on the bar in plain sight — the kind, what it was recognised by,
// the command — so correcting it is editing a field, not finding a setting.
//
// What sits beside the log is the kind's: a page opens here, a service gets a
// form to send requests from, and an application with a window of its own
// gets our window moved aside for it — its window is placed beside ours, not
// inside, and closing it gives the screen back.

const KINDS = ["web", "backend", "desktop", "mobile", "command"];

export default function RunPane(props: {
  cardId: string;
  screenId: string;
  prefer: string;
  current: boolean;
}): JSX.Element {
  const [plan, setPlan] = createSignal<LaunchPlan | null>(null);
  const [chosen, setChosen] = createSignal<Profile>({ kind: "command", command: "" });
  const [run, setRun] = createSignal<LaunchRun | null>(null);
  const [address, setAddress] = createSignal("");
  const [room, setRoom] = createSignal({ room: "", app: "" });
  const [busy, setBusy] = createSignal(false);

  const ofKind = createMemo(() => list(plan()?.profiles).filter((p) => p.kind === chosen().kind));

  const start = async () => {
    setBusy(true);
    setAddress("");
    const started = await guard(() => API.StartLaunch(props.cardId, props.screenId, chosen()));
    setBusy(false);
    if (!started) return;
    // What went down before is replaced by this run, on the backend as here.
    const p = plan();
    if (p?.lost) setPlan({ ...p, lost: undefined });
    setRun(started);
    setRoom({ room: started.room ?? "", app: "" });
  };

  const stop = async () => {
    await guard(() => API.StopLaunch(props.screenId));
    setRun(null);
    setAddress("");
    setRoom({ room: "", app: "" });
  };

  const pickKind = (kind: string) => {
    const first = list(plan()?.profiles).find((p) => p.kind === kind);
    setChosen(first ? { ...first } : { kind, command: "" });
  };

  onSettled(() => {
    let disposed = false;
    void (async () => {
      const p = await guard(() => API.LaunchPlan(props.cardId, props.screenId, props.prefer));
      if (disposed || !p) return;
      setPlan(p);
      setChosen({ ...(p.running?.profile ?? p.chosen) });
      if (p.running) {
        setRun(p.running);
        setRoom({ room: p.running.room ?? "", app: p.running.app ?? "" });
        return;
      }
      // A choice somebody already made for this project is started as soon
      // as the card arrives — but only one that stays inside the window. A
      // window of ours jumping aside because a card moved is not something a
      // person asked for.
      const kind = p.chosen.kind;
      // Nor one that went down unseen: its last screen is shown instead, and
      // starting again is the person's call.
      if (props.current && p.remembered && !p.lost && (kind === "web" || kind === "backend")) void start();
    })();

    // The address is read off the log: a dev server that found its port busy
    // says where it went only there.
    const poll = setInterval(async () => {
      if (!run() || address()) return;
      const found = await API.LaunchAddress(props.screenId).catch(() => "");
      if (found && !disposed) setAddress(found);
    }, 1000);

    const off = Events.On("launch", (ev: { data: { screenId: string; room?: string; app?: string } }) => {
      if (ev.data?.screenId !== props.screenId) return;
      setRoom({ room: ev.data.room ?? "", app: ev.data.app ?? "" });
    });

    return () => {
      disposed = true;
      clearInterval(poll);
      off?.();
    };
  });

  const url = () => address() || run()?.profile.url || "";

  return (
    <div class="run">
      <div class="run-bar row">
        <select class="fit" value={chosen().kind} disabled={!!run()}
                onChange={(e) => pickKind(e.currentTarget.value)}>
          <For each={KINDS}>{(k) => <option value={k}>{label("launch", k)}</option>}</For>
        </select>
        <Show when={ofKind().length > 1 && !run()}>
          <select class="fit" onChange={(e) => setChosen({ ...ofKind()[Number(e.currentTarget.value)] })}>
            <For each={ofKind()}>
              {(p, i) => <option value={i()}>{profileName(p)}</option>}
            </For>
          </select>
        </Show>
        <Show when={ofKind().length === 1 && chosen().tool}>
          <span class="tag">{profileName(chosen())}</span>
        </Show>
        <input type="text" class="grow mono" value={chosen().command} disabled={!!run()}
               placeholder={t("run.commandHint")}
               onInput={(e) => setChosen({ ...chosen(), command: e.currentTarget.value })}
               onKeyDown={(e) => { if (e.key === "Enter" && !run()) void start(); }} />
        <Show when={run()} fallback={
          <button class="btn primary tiny" disabled={busy() || !plan()} onClick={() => void start()}>
            {t("run.start")}
          </button>
        }>
          <button class="btn tiny" onClick={() => void start()} title={t("run.restartTitle")}>{t("run.restart")}</button>
          <button class="btn tiny" onClick={() => void stop()}>{t("run.stop")}</button>
        </Show>
      </div>

      <Show when={run()} fallback={
        <Show when={plan()?.lost} fallback={<Idle plan={plan()} chosen={chosen()} />}>
          <div class="run-body">
            <div class="meta">{t("run.lost")}</div>
            <div class="run-log">
              <TerminalPane open={() => Promise.resolve(plan()!.lost!)} ended={t("terminal.lost")} />
            </div>
          </div>
        </Show>
      }>
        <Show when={chosen().kind === "desktop" || chosen().kind === "mobile"}>
          <RoomLine screenId={props.screenId} room={room().room} app={room().app} />
        </Show>
        {/* A new terminal per start: the pane is keyed by the terminal it is
            connected to, and a restart is a different one. */}
        <For each={[run()!.terminal.id]}>
          {() => (
            <div class={`run-body run-${chosen().kind}`}>
              <Show when={chosen().kind === "web" && url()}>
                <Page url={url()} guessed={!address()} />
              </Show>
              <div class="run-log">
                <TerminalPane open={() => Promise.resolve(run()!.terminal)} ended={t("run.ended")} />
              </div>
              <Show when={chosen().kind === "backend"}>
                <Requests base={url()} clients={list(plan()?.clients)} />
              </Show>
            </div>
          )}
        </For>
      </Show>
    </div>
  );
}

function profileName(p: Profile): string {
  const tool = p.tool || label("launch", p.kind);
  return p.dir ? `${tool} · ${p.dir}/` : tool;
}

function Idle(props: { plan: LaunchPlan | null; chosen: Profile }): JSX.Element {
  const needsDocker = () =>
    props.plan && !props.plan.docker && /\bdocker\b/.test(props.chosen.command);
  return (
    <div class="screen-note run-idle">
      <Show when={props.plan} fallback={t("run.reading")}>
        <p>{t(`run.about.${props.chosen.kind}`)}</p>
        <Show when={props.chosen.kind === "command" && list(props.plan?.profiles).length === 1}>
          <p>{t("run.nothingFound")}</p>
        </Show>
        <Show when={needsDocker()}>
          <p class="warn">{t("run.noDocker")}</p>
        </Show>
        <Show when={props.plan?.remembered}>
          <p class="meta">{t("run.remembered")}</p>
        </Show>
      </Show>
    </div>
  );
}

function RoomLine(props: { screenId: string; room: string; app: string }): JSX.Element {
  return (
    <div class={`run-room row ${props.room === "noAccess" ? "warn" : ""}`}>
      <span class="grow">
        {props.room ? t(`run.room.${props.room}`, { app: props.app }) : t("run.room.none")}
      </span>
      <Show when={props.room}>
        <button class="btn quiet tiny" onClick={() => void guard(() => API.GiveBackWindow(props.screenId))}>
          {t("run.giveBack")}
        </button>
      </Show>
    </div>
  );
}

// An iframe, like the browser screen's, for the same reason: a Wails window
// holds exactly one webview.
function Page(props: { url: string; guessed: boolean }): JSX.Element {
  const [nonce, setNonce] = createSignal(0);
  return (
    <div class="browser run-page">
      <div class="browser-bar row">
        <span class="mono">{props.url}</span>
        <Show when={props.guessed}>
          <span class="meta">{t("run.addressGuessed")}</span>
        </Show>
        <div class="spacer" />
        <button class="btn quiet tiny" onClick={() => setNonce((n) => n + 1)} title={t("browser.reload")}>↻</button>
        <a class="btn quiet tiny" href={props.url} onClick={(e) => openOutside(e, props.url)} title={t("ribbon.openOutside")}>↗</a>
      </div>
      <For each={[`${props.url}#${nonce()}`]}>
        {() => <iframe class="browser-frame" src={props.url} referrerpolicy="no-referrer" />}
      </For>
    </div>
  );
}

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

// A form rather than a client: what a reviewer sends to a branch under review
// is a request or three, and an external client is one button away for more.
function Requests(props: { base: string; clients: string[] }): JSX.Element {
  const [method, setMethod] = createSignal("GET");
  const [path, setPath] = createSignal("");
  const [headers, setHeaders] = createSignal("");
  const [body, setBody] = createSignal("");
  const [more, setMore] = createSignal(false);
  const [answer, setAnswer] = createSignal<Response | null>(null);
  const [sending, setSending] = createSignal(false);

  // The field starts empty and shows the service's address as its hint, so
  // the address the log reports later still fills in what is not typed.
  const target = () => path() || props.base;

  const send = async () => {
    setSending(true);
    const r = await guard(() => API.SendRequest({ method: method(), url: target(), headers: headers(), body: body() }));
    setSending(false);
    if (r) setAnswer(r);
  };

  const openClient = async (name: string) => {
    if (target()) await navigator.clipboard?.writeText(target()).catch(() => undefined);
    await guard(() => API.OpenClient(name));
  };

  return (
    <div class="run-requests">
      <div class="row">
        <select class="fit" value={method()} onChange={(e) => setMethod(e.currentTarget.value)}>
          <For each={METHODS}>{(m) => <option>{m}</option>}</For>
        </select>
        <input type="text" class="grow mono" value={path()} placeholder={props.base || "http://localhost:8080/"}
               onInput={(e) => setPath(e.currentTarget.value)}
               onKeyDown={(e) => { if (e.key === "Enter") void send(); }} />
        <button class="btn quiet tiny" onClick={() => setMore(!more())}>{more() ? t("run.less") : t("run.more")}</button>
        <button class="btn primary tiny" disabled={sending() || !target()} onClick={() => void send()}>{t("run.send")}</button>
      </div>
      <Show when={more()}>
        <div class="row run-request-extra">
          <textarea class="grow mono" placeholder={t("run.headersHint")} value={headers()}
                    onInput={(e) => setHeaders(e.currentTarget.value)} />
          <textarea class="grow mono" placeholder={t("run.bodyHint")} value={body()}
                    onInput={(e) => setBody(e.currentTarget.value)} />
        </div>
      </Show>
      <Show when={props.clients.length > 0}>
        <div class="row meta">
          <span>{t("run.openIn")}</span>
          <For each={props.clients}>
            {(name) => (
              <button class="btn quiet tiny" title={t("run.openInTitle")} onClick={() => void openClient(name)}>{name} ↗</button>
            )}
          </For>
        </div>
      </Show>
      <Show when={answer()}>
        <div class="run-response">
          <div class="row">
            <span class={`tag ${answer()!.code < 300 ? "ok" : answer()!.code < 500 ? "warn" : "bad"}`}>{answer()!.status}</span>
            <span class="meta">{answer()!.millis} ms</span>
            <Show when={answer()!.truncated}><span class="meta">{t("run.truncated")}</span></Show>
          </div>
          <pre class="mono">{pretty(answer()!.body)}</pre>
        </div>
      </Show>
    </div>
  );
}

function pretty(body: string): string {
  const s = body.trim();
  if (!s.startsWith("{") && !s.startsWith("[")) return body;
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return body;
  }
}
