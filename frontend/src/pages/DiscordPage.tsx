import { useEffect, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import {
  beginDiscordLink,
  cancelDiscordLink,
  getDiscordSessionStatus,
  unlinkDiscordBot,
  reconnectDiscordSession,
  resumeDiscordSession,
  stopDiscordSession,
  type DiscordSessionOperationDTO,
  type DiscordSessionStatusDTO,
} from "../services/backend";

const activeRuntimeStates = new Set(["starting", "linking", "connecting", "connected", "open", "reconnecting", "stopping", "draining", "logging_out"]);

function statusLabel(status: DiscordSessionStatusDTO | null): string {
  if (!status) return "Loading status";
  if (status.bindingState === "linked" && (status.runtimeState === "connected" || status.runtimeState === "open")) return "Online";
  if (status.bindingState === "linked" && status.runtimeState === "reconnecting") return "Reconnecting";
  if (status.bindingState === "linked") return "Bot linked";
  if (status.bindingState === "revoked") return "Link a bot again";
  if (status.runtimeState === "linking") return "Checking the token";
  if (status.runtimeState === "failed") return "Connection failed";
  return "No bot linked";
}

export function DiscordPage() {
  const [status, setStatus] = useState<DiscordSessionStatusDTO | null>(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // The page polls the backend for session status; there is no push channel.
  function refreshStatus() {
    void getDiscordSessionStatus()
      .then(setStatus)
      .catch(() => setError("Could not load the bot status. Try restarting the app."));
  }

  useEffect(() => {
    refreshStatus();
    const timer = window.setInterval(refreshStatus, 1500);
    return () => window.clearInterval(timer);
  }, []);

  const running = status !== null && activeRuntimeStates.has(status.runtimeState);
  const linked = status?.bindingState === "linked";
  const linking = status?.runtimeState === "linking" && Boolean(status.operationID);
  const online = linked && (status?.runtimeState === "connected" || status?.runtimeState === "open");

  async function runOperation(action: () => Promise<DiscordSessionOperationDTO>, failure: string) {
    setBusy(true);
    setError("");
    try {
      setStatus((await action()).status);
    } catch (cause) {
      setError(cause instanceof Error && cause.message ? cause.message : failure);
    } finally {
      setBusy(false);
      refreshStatus();
    }
  }

  async function runStatusOperation(action: () => Promise<DiscordSessionStatusDTO>) {
    setBusy(true);
    setError("");
    try {
      setStatus(await action());
    } catch {
      setError("The operation failed. Try again after the status refreshes.");
    } finally {
      setBusy(false);
      refreshStatus();
    }
  }

  function link() {
    const value = token.trim();
    setToken("");
    void runOperation(() => beginDiscordLink({ token: value }), "Discord did not accept the token. Check it and your internet connection.");
  }

  function unlink() {
    if (window.confirm("Unlink this bot? The saved token is deleted from this device. To make the old token useless everywhere, reset it in the Discord Developer Portal.")) {
      void runOperation(unlinkDiscordBot, "The bot could not be unlinked.");
    }
  }

  function cancelLink() {
    const operationID = status?.operationID;
    if (!operationID) return;
    void runStatusOperation(() => cancelDiscordLink(operationID));
  }

  return <div className="page">
    <header className="page-header">
      <div>
        <p className="eyebrow">DISCORD</p>
        <h1>Discord bot</h1>
        <p className="lede">Link your bot with its token, add it to a server, reconnect, or unlink it.</p>
      </div>
      <StatusBadge tone={online ? "good" : linking || status?.bindingState === "revoked" ? "warn" : "neutral"}>
        {statusLabel(status)}
      </StatusBadge>
    </header>

    <section className="card session-card">
      <div className="card-heading">
        <div>
          <p className="eyebrow">BOT STATUS</p>
          <h2>{statusLabel(status)}</h2>
        </div>
        {linked && (status?.botName || status?.discordBotID) && <span className="session-account">{status?.botName ? `${status.botName} · ` : ""}{status?.discordBotID}</span>}
      </div>
      <p className="muted session-explainer">{status?.agentActive ? "Your assistant is using this bot to receive and answer messages." : "This keeps the bot online only. The Agent will not read or answer messages until it is started from Overview."}</p>

      {linking && <div className="link-panel" aria-live="polite">
        <p className="muted">Checking the token with Discord and connecting…</p>
        <button className="button secondary" disabled={busy} onClick={cancelLink}>Cancel</button>
      </div>}

      {!linking && !linked && <div className="session-actions">
        <label className="token-input">Bot token
          <input type="password" autoComplete="off" spellCheck={false} placeholder="Paste the token from the Developer Portal" value={token} onChange={(event) => setToken(event.target.value)} disabled={busy || running} />
        </label>
        <button className="button primary" disabled={busy || running || token.trim().length < 20} onClick={link}>{busy ? "Linking…" : "Link bot"}</button>
      </div>}

      {linked && status?.inviteURL && <p className="muted invite-link">Add the bot to a server: <a href={status.inviteURL} target="_blank" rel="noreferrer">open the invite link</a>.</p>}

      {status?.agentActive && <p className="muted">To stop the Agent or change the bot, open Overview and stop the Agent first.</p>}

      {linked && !status?.agentActive && <div className="session-actions session-connected-actions">
        {running ? <>
          <button className="button secondary" disabled={busy || status?.runtimeState === "reconnecting"} onClick={() => void runOperation(reconnectDiscordSession, "The bot could not reconnect.")}>{status?.runtimeState === "reconnecting" ? "Connecting…" : "Reconnect"}</button>
          <button className="button secondary" disabled={busy} onClick={() => void runStatusOperation(stopDiscordSession)}>Go offline</button>
        </> : <button className="button primary" disabled={busy} onClick={() => void runOperation(resumeDiscordSession, "The bot could not connect.")}>{busy ? "Connecting…" : "Connect bot"}</button>}
        <button className="button danger" disabled={busy} onClick={unlink}>Unlink bot</button>
      </div>}

      {error && <p className="error-text" role="alert">{error}</p>}
      {status?.runtimeState === "failed" && <p className="error-text" role="status">{status.errorMessage ? `${status.errorMessage.replace(/\.?$/, ".")} ` : "The last connection attempt failed. "}{linked ? "The saved token is kept; you can try again." : "Check the token and try again."}</p>}
    </section>

    <section className="card session-note">
      <strong>Setting up a bot</strong>
      <ol className="setup-steps">
        <li>In the <a href="https://discord.com/developers/applications" target="_blank" rel="noreferrer">Discord Developer Portal</a>, create an application and open <strong>Bot</strong>.</li>
        <li>Turn on <strong>Message Content Intent</strong>. Turn on <strong>Server Members Intent</strong> too if you want the member list in Chat.</li>
        <li>Reset and copy the token, paste it above, and select <strong>Link bot</strong>.</li>
        <li>Use the invite link to add the bot to your servers, then set the owner and allowlist in Settings.</li>
      </ol>
      <p className="muted">The token is stored only on this device, readable by your user account alone. Anyone with the token controls the bot, so never share it.</p>
    </section>
  </div>;
}
