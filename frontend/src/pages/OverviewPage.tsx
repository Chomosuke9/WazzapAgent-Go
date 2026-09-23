import { useCallback, useEffect, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import { useApp } from "../hooks/AppProvider";
import { getAgentRuntimeStatus, startAgent, stopAgent, type AgentRuntimeStatusDTO } from "../services/backend";

const agentLabels: Record<string, string> = {
  stopped: "Stopped",
  starting: "Starting",
  running: "Running",
  stopping: "Stopping",
  failed: "Failed",
};

function whatsappLabel(state: string): string {
  const labels: Record<string, string> = {
    stopped: "Inactive",
    starting: "Starting connection",
    pairing: "Waiting for pairing",
    connecting: "Connecting",
    connected: "Connected",
    reconnecting: "Reconnecting",
    disconnected: "Disconnected",
    failed: "Connection failed",
    open: "Client active",
  };
  return labels[state] ?? (state || "Unknown");
}

export function OverviewPage() {
  const { appInfo, infoError, loadingInfo, lastPing, pingPending, pingError, sendPing } = useApp();
  const [runtime, setRuntime] = useState<AgentRuntimeStatusDTO | null>(null);
  const [operationError, setOperationError] = useState<string | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const refreshRuntime = useCallback(async () => {
    try {
      setRuntime(await getAgentRuntimeStatus());
      setStatusError(null);
    } catch (reason: unknown) {
      setStatusError(reason instanceof Error ? reason.message : "Could not read Agent status.");
    }
  }, []);

  useEffect(() => {
    void refreshRuntime();
    const timer = window.setInterval(() => void refreshRuntime(), 1500);
    return () => window.clearInterval(timer);
  }, [refreshRuntime]);

  const runtimeActive = runtime?.state === "running" || runtime?.state === "starting" || runtime?.state === "stopping";
  const inTransition = runtime?.state === "starting" || runtime?.state === "stopping";
  const canStart = runtime?.state === "stopped" || runtime?.state === "failed";
  const badgeTone = runtime?.state === "running" ? "good" : runtime?.state === "failed" ? "warn" : "neutral";

  async function toggleAgent() {
    setBusy(true);
    setOperationError(null);
    try {
      setRuntime(runtimeActive ? await stopAgent() : await startAgent());
      setOperationError(null);
    } catch (reason: unknown) {
      setOperationError(reason instanceof Error ? reason.message : "Agent operation failed.");
      await refreshRuntime();
    } finally {
      setBusy(false);
    }
  }

  return <div className="page">
    <header className="page-header"><div><p className="eyebrow">OVERVIEW</p><h1>Welcome back.</h1><p className="lede">Run the bot using the existing WazzapAgent headless pipeline.</p></div><StatusBadge tone={appInfo ? "good" : infoError ? "warn" : "neutral"}>{appInfo ? "Shell ready" : infoError ? "Could not read info" : "Loading info"}</StatusBadge></header>
    <section className="hero-card"><div><p className="eyebrow light">APP STATUS</p><h2>{appInfo?.name ?? "WazzapAgent"}</h2><p>{appInfo ? `${appInfo.platform} · version ${appInfo.version}` : loadingInfo ? "Reading info from backend…" : infoError ?? "Backend info is not available."}</p></div><div className="hero-orb" aria-hidden="true"><span>✦</span></div></section>
    <div className="card-grid two">
      <section className="card"><div className="card-heading"><div><p className="eyebrow">AGENT</p><h3>{agentLabels[runtime?.state ?? ""] ?? "Loading status"}</h3></div><StatusBadge tone={badgeTone}>{agentLabels[runtime?.state ?? ""] ?? "Loading"}</StatusBadge></div>
        <p className="muted">{runtime?.state === "running" ? `The bot pipeline is active on settings revision ${runtime.activeRevision}.` : runtime?.state === "starting" ? "The runtime is preparing the WhatsApp client and message pipeline." : runtime?.state === "failed" ? `The runtime stopped with status ${runtime.errorCode || "error"}.` : "The bot is stopped. Starting it will use the saved WhatsApp session."}</p>
        {operationError && <p className="error-text" role="alert">{operationError}</p>}
        {statusError && <p className="error-text" role="status">{statusError}</p>}
        <div className="session-actions"><button className={`button ${runtimeActive ? "danger" : "primary"}`} onClick={() => void toggleAgent()} disabled={busy || inTransition || (!runtimeActive && !canStart)}>{busy ? "Working…" : runtimeActive ? "Stop Agent" : "Start Agent"}</button>{runtime?.pendingChanges && <StatusBadge tone="warn">Settings need to be applied</StatusBadge>}</div>
      </section>
      <section className="card"><div className="card-heading"><div><p className="eyebrow">WHATSAPP</p><h3>{whatsappLabel(runtime?.whatsAppState ?? "")}</h3></div><StatusBadge tone={runtime?.whatsAppState === "connected" || runtime?.whatsAppState === "open" ? "good" : "neutral"}>{whatsappLabel(runtime?.whatsAppState ?? "")}</StatusBadge></div><p className="muted">Connection status comes from the client used by the Agent runtime. Pairing and account management are available on the WhatsApp page while the Agent is stopped.</p></section>
    </div>
    <section className="card probe-card"><div><p className="eyebrow">APP CONNECTION TEST</p><h3>Check that the Go service is active</h3><p className="muted">Ping calls the Go service and displays the <code>app:ping</code> event sent by the backend.</p>{lastPing && <p className="event-line"><span className="event-check">✓</span>{lastPing.message} <span>#{lastPing.sequence}</span></p>}{pingError && <p className="error-text">{pingError}</p>}</div><button className="button primary" onClick={() => void sendPing()} disabled={pingPending}>{pingPending ? "Sending…" : "Send ping"}</button></section>
  </div>;
}
