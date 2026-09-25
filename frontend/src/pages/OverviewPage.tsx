import { useCallback, useEffect, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import { NavIcon, type PageId } from "../layouts/AppLayout";
import { getAgentRuntimeStatus, getWhatsAppUsage, startAgent, stopAgent, type AgentRuntimeStatusDTO, type WhatsAppUsageDTO } from "../services/backend";

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

const numberFormat = new Intl.NumberFormat("id-ID");

export function OverviewPage({ onNavigate }: { onNavigate: (page: PageId) => void }) {
  const [runtime, setRuntime] = useState<AgentRuntimeStatusDTO | null>(null);
  const [operationError, setOperationError] = useState<string | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [usage, setUsage] = useState<WhatsAppUsageDTO | null>(null);
  const [usageError, setUsageError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const refreshRuntime = useCallback(async () => {
    try {
      setRuntime(await getAgentRuntimeStatus());
      setStatusError(null);
    } catch (reason: unknown) {
      setStatusError(reason instanceof Error ? reason.message : "Could not read Agent status.");
    }
  }, []);

  const refreshUsage = useCallback(async () => {
    try {
      setUsage(await getWhatsAppUsage(1));
      setUsageError(null);
    } catch (reason: unknown) {
      setUsageError(reason instanceof Error ? reason.message : "Could not load message activity.");
    }
  }, []);

  useEffect(() => {
    void refreshRuntime();
    const timer = window.setInterval(() => void refreshRuntime(), 1500);
    return () => window.clearInterval(timer);
  }, [refreshRuntime]);

  useEffect(() => {
    void refreshUsage();
    const timer = window.setInterval(() => void refreshUsage(), 30000);
    window.addEventListener("focus", refreshUsage);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", refreshUsage);
    };
  }, [refreshUsage]);

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

  const connected = runtime?.whatsAppState === "connected" || runtime?.whatsAppState === "open";
  const running = runtime?.state === "running";
  const heading = statusError ? "Status unavailable" : running ? "Your assistant is on duty." : runtime?.state === "starting" ? "Getting things ready…" : runtime?.state === "stopping" ? "Wrapping things up…" : runtime?.state === "failed" ? "Your assistant needs attention." : runtime ? "Ready when you are." : "Checking your workspace…";
  const description = statusError ? "We couldn't check your assistant. Try refreshing its status." : running ? "Your assistant is active. Keep an eye on conversations and manage your messages from here." : runtime?.state === "failed" ? "Check your settings and recent activity, then try starting your assistant again." : inTransition ? "This usually takes a moment. Your status will update automatically." : "Connect WhatsApp, set up your assistant, and start handling conversations in one place.";

  return <div className="page overview-page">
    <header className="page-header"><div><p className="eyebrow">YOUR WORKSPACE</p><h1>Overview</h1><p className="lede">A clear view of your assistant and conversations.</p></div><button className="button secondary" onClick={() => onNavigate("chat")}>Open inbox <span aria-hidden="true">↗</span></button></header>
    <section className="agent-hero" aria-label="Assistant status">
      <div className="agent-hero-copy">
        <StatusBadge tone={statusError ? "warn" : badgeTone}>{statusError ? "Status unavailable" : agentLabels[runtime?.state ?? ""] ?? "Checking status"}</StatusBadge>
        <h2>{heading}</h2><p>{description}</p>
        <div className="session-actions">
          <button className={`button ${runtimeActive ? "secondary" : "primary"}`} onClick={() => void toggleAgent()} disabled={busy || inTransition || Boolean(statusError) || (!runtimeActive && !canStart)}>{busy ? "Working…" : runtimeActive ? "Stop Agent" : "Start Agent"}</button>
          <button className="text-button" onClick={() => onNavigate(runtime?.state === "failed" ? "logs" : "settings")}>{runtime?.state === "failed" ? "View activity" : "Configure assistant"} <span aria-hidden="true">→</span></button>
          {statusError && <button className="text-button" onClick={() => void refreshRuntime()}>Retry status</button>}
        </div>
        {operationError && <p className="error-text" role="alert">{operationError}</p>}
        {statusError && <p className="error-text" role="status">{statusError}</p>}
      </div>
      <div className={`assistant-emblem${running && !statusError ? " is-running" : ""}`} aria-hidden="true"><NavIcon id="whatsapp" /><span /></div>
    </section>
    <div className="workspace-stats">
      <section className="card status-card"><span className="section-icon"><NavIcon id="whatsapp" /></span><p>WhatsApp</p><h3>{statusError ? "Unavailable" : runtime ? whatsappLabel(runtime.whatsAppState) : "Checking…"}</h3><button className="text-button" onClick={() => onNavigate("whatsapp")}>{connected ? "Manage connection" : "Set up connection"} <span aria-hidden="true">→</span></button></section>
      <section className="card status-card"><span className="section-icon"><NavIcon id="settings" /></span><p>Configuration</p><h3>{statusError ? "Unavailable" : !runtime ? "Checking…" : runtime.pendingChanges ? "Changes to apply" : running ? "Up to date" : "Saved settings"}</h3><button className="text-button" onClick={() => onNavigate("settings")}>{runtime?.pendingChanges ? "Review changes" : "Manage settings"} <span aria-hidden="true">→</span></button></section>
      <section className="card status-card"><span className="section-icon"><NavIcon id="logs" /></span><p>Assistant</p><h3>{statusError ? "Unavailable" : running ? "Active" : agentLabels[runtime?.state ?? ""] ?? "Checking…"}</h3><button className="text-button" onClick={() => onNavigate("logs")}>View recent activity <span aria-hidden="true">→</span></button></section>
    </div>
    <section className="usage-section" aria-labelledby="today-usage-heading">
      <div className="section-heading usage-title-row"><div><h2 id="today-usage-heading">Today</h2></div><button className="text-button" onClick={() => onNavigate("analytics")}>Open analytics <span aria-hidden="true">→</span></button></div>
      {usageError ? <div className="card usage-error" role="alert"><div><strong>Today’s numbers are unavailable</strong><p className="muted">{usageError}</p></div><button className="button secondary" onClick={() => void refreshUsage()}>Try again</button></div>
        : !usage ? <div className="card usage-loading" role="status">Loading today’s numbers…</div>
          : <div className="today-usage-stats">
            <article className="card usage-stat"><span>Messages today</span><strong>{numberFormat.format(usage.messagesInPeriod)}</strong></article>
            <article className="card usage-stat"><span>Agent invokes today</span><strong>{numberFormat.format(usage.invocationsInPeriod)}</strong></article>
          </div>}
    </section>
    {runtime?.pendingChanges && <div className="workspace-notice"><span><strong>Settings are ready to apply</strong><span>{running ? "Apply your saved changes in Settings to update the running assistant." : "Your saved changes will be used the next time you start the Agent."}</span></span><button className="button secondary" onClick={() => onNavigate("settings")}>Review settings</button></div>}
    <div className="section-heading"><div><h2>Make room for the conversation.</h2><p className="muted">Everything you need for your day-to-day messaging.</p></div></div>
    <div className="card-grid two quick-access">
      <button className="card quick-access-card" onClick={() => onNavigate("chat")}><span className="section-icon"><NavIcon id="chat" /></span><span><strong>Your inbox, in one place</strong><span>Read conversations, reply to messages, and manage each chat.</span></span><span className="quick-arrow" aria-hidden="true">↗</span></button>
      <button className="card quick-access-card" onClick={() => onNavigate("broadcast")}><span className="section-icon"><NavIcon id="broadcast" /></span><span><strong>Reach your groups</strong><span>Compose a broadcast and send it now or schedule it for later.</span></span><span className="quick-arrow" aria-hidden="true">↗</span></button>
    </div>
  </div>;
}
