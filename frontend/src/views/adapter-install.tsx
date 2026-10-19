import { createSignal } from "solid-js";
import type { JSX } from "@solidjs/web";
import * as API from "../../bindings/github.com/artipop/gandvik/internal/app/api";
import type { AdapterStatus } from "../../bindings/github.com/artipop/gandvik/internal/acp/models";
import { guard, loadAgents } from "../state";
import { t } from "../i18n";

export function InstallButton(props: { adapter: AdapterStatus; onInstalled?: (message: string) => void }): JSX.Element {
  const [busy, setBusy] = createSignal(false);
  const install = async () => {
    setBusy(true);
    try {
      const got = await guard(() => API.InstallAdapter(props.adapter.kind));
      if (!got) return;
      const now = got.status.version ?? "";
      const said = !got.was ? t("agents.installedNow", { version: now })
        : got.was === now ? t("agents.upToDate", { version: now })
        : t("agents.updated", { was: got.was, version: now });
      await loadAgents();
      props.onInstalled?.(said);
    } finally {
      setBusy(false);
    }
  };
  return (
    <button class="btn quiet" disabled={busy()} onClick={() => void install()}
            title={t("agents.installTitle", { pkg: props.adapter.package ?? "" })}>
      {busy() ? t("agents.installing") : props.adapter.installed ? t("agents.update") : t("agents.install")}
    </button>
  );
}
