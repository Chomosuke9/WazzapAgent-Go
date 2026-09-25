import { useEffect, useRef, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import {
  beginWhatsAppPairing,
  cancelWhatsAppPairing,
  getWhatsAppSessionStatus,
  logoutWhatsAppSession,
  reconnectWhatsAppSession,
  resumeWhatsAppSession,
  stopWhatsAppSession,
  subscribeToBackendWhatsAppSession,
  type WhatsAppSessionOperationDTO,
  type WhatsAppSessionStatusDTO,
} from "../services/backend";

const activeRuntimeStates = new Set(["starting", "pairing", "connecting", "connected", "open", "reconnecting", "stopping", "draining", "logging_out"]);

function statusLabel(status: WhatsAppSessionStatusDTO | null): string {
  if (!status) return "Loading status";
  if (status.bindingState === "paired" && (status.runtimeState === "connected" || status.runtimeState === "open")) return "Connected";
  if (status.bindingState === "paired" && status.runtimeState === "reconnecting") return "Reconnecting";
  if (status.bindingState === "paired") return "Session saved";
  if (status.bindingState === "revoked") return "Pairing required";
  if (status.runtimeState === "pairing") return "Waiting for pairing";
  if (status.runtimeState === "failed") return "Connection failed";
  return "Not connected";
}

function expired(expiresAt?: string): boolean {
  return !expiresAt || Date.parse(expiresAt) <= Date.now();
}

export function WhatsAppPage() {
  const [status, setStatus] = useState<WhatsAppSessionStatusDTO | null>(null);
  const [method, setMethod] = useState<"qr" | "phone_code">("qr");
  const [phone, setPhone] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const activeOperationID = useRef<string | null>(null);
  const ignoredOperationIDs = useRef(new Set<string>());
  const eventRevision = useRef(0);

  function applyStatus(next: WhatsAppSessionStatusDTO) {
    activeOperationID.current = next.operationID || null;
    setStatus(next);
  }

  useEffect(() => {
    const unsubscribe = subscribeToBackendWhatsAppSession((event) => {
      eventRevision.current += 1;
      if (ignoredOperationIDs.current.has(event.operationID)) return;
      if (activeOperationID.current && activeOperationID.current !== event.operationID) return;
      activeOperationID.current = event.operationID;
      setStatus(event.status);
    });
    const refreshStatus = () => {
      const initialRevision = eventRevision.current;
      void getWhatsAppSessionStatus()
        .then((snapshot) => {
          if (eventRevision.current === initialRevision) applyStatus(snapshot);
        })
        .catch(() => setError("Could not load session status. Try restarting the app."));
    };
    refreshStatus();
    const timer = window.setInterval(refreshStatus, 1500);
    return () => { window.clearInterval(timer); unsubscribe(); };
  }, []);

  const running = status !== null && activeRuntimeStates.has(status.runtimeState);
  const paired = status?.bindingState === "paired";
  const pairingActive = status?.runtimeState === "pairing" && Boolean(status.operationID);
  const pairingExpired = pairingActive && expired(status?.pairing?.expiresAt);
  async function runOperation(action: () => Promise<WhatsAppSessionOperationDTO>) {
    setBusy(true);
    setError("");
    const revisionAtStart = eventRevision.current;
    try {
      const result = await action();
      if (eventRevision.current === revisionAtStart || activeOperationID.current !== result.operationID) {
        activeOperationID.current = result.operationID;
        setStatus(result.status);
      }
    } catch {
      setError("The session operation failed. Check your internet connection and try again.");
    } finally {
      setBusy(false);
      const refreshRevision = eventRevision.current;
      void getWhatsAppSessionStatus().then((snapshot) => {
        if (eventRevision.current === refreshRevision) applyStatus(snapshot);
      }).catch(() => undefined);
    }
  }

  async function runStatusOperation(action: () => Promise<WhatsAppSessionStatusDTO>, applyResult = false) {
    setBusy(true);
    setError("");
    const revisionAtStart = eventRevision.current;
    try {
      const result = await action();
      if (applyResult || eventRevision.current === revisionAtStart) applyStatus(result);
    } catch {
      setError("The session operation failed. Try again after the status refreshes.");
    } finally {
      setBusy(false);
      const refreshRevision = eventRevision.current;
      void getWhatsAppSessionStatus().then((snapshot) => {
        if (eventRevision.current === refreshRevision) applyStatus(snapshot);
      }).catch(() => undefined);
    }
  }

  function pair() {
    void runOperation(() => beginWhatsAppPairing({ method, phone: method === "phone_code" ? phone : "" }));
  }

  function logout() {
    if (window.confirm("Remove the WazzapAgent device from this WhatsApp account?")) {
      retireCurrentOperation();
      void runOperation(logoutWhatsAppSession);
    }
  }

  function retireCurrentOperation() {
    if (activeOperationID.current) ignoredOperationIDs.current.add(activeOperationID.current);
    activeOperationID.current = null;
  }

  function cancelPairing() {
    const operationID = status?.operationID;
    if (!operationID) return;
    ignoredOperationIDs.current.add(operationID);
    activeOperationID.current = null;
    void runStatusOperation(() => cancelWhatsAppPairing(operationID), true);
  }

  function stopSession() {
    retireCurrentOperation();
    void runStatusOperation(stopWhatsAppSession, true);
  }

  return <div className="page">
    <header className="page-header">
      <div>
        <p className="eyebrow">WHATSAPP</p>
        <h1>WhatsApp connection</h1>
        <p className="lede">Pair a device, reconnect, or remove the session from your WhatsApp account.</p>
      </div>
      <StatusBadge tone={paired && (status?.runtimeState === "connected" || status?.runtimeState === "open") ? "good" : pairingActive || status?.bindingState === "revoked" ? "warn" : "neutral"}>
        {statusLabel(status)}
      </StatusBadge>
    </header>

    <section className="card session-card">
      <div className="card-heading">
        <div>
          <p className="eyebrow">DEVICE STATUS</p>
          <h2>{statusLabel(status)}</h2>
        </div>
        {paired && status?.whatsAppAccountID && <span className="session-account">{status.whatsAppAccountID}</span>}
      </div>
      <p className="muted session-explainer">{status?.agentActive ? "Your assistant is using this connection to receive and respond to messages." : "This session only maintains the WhatsApp connection. The Agent will not process or reply to messages until it is started."}</p>

      {pairingActive && <div className="pairing-panel" aria-live="polite">
        {status?.pairing?.method === "qr" && status.pairing.qrCodeDataURL && !pairingExpired ? <>
          <img className="pairing-qr" src={status.pairing.qrCodeDataURL} alt="QR code to connect WhatsApp" />
          <p className="muted">In WhatsApp, open <strong>Linked devices</strong>, select <strong>Link a device</strong>, then scan this QR code.</p>
        </> : status?.pairing?.method === "phone_code" && status.pairing.code && !pairingExpired ? <>
          <p className="muted">Open <strong>Linked devices</strong> in WhatsApp, choose to link with a phone number, then enter this code.</p>
          <div className="phone-pairing-code" aria-label="Pairing code">{status.pairing.code}</div>
        </> : <p className="muted">Waiting for a pairing code from WhatsApp…</p>}
        {status?.pairing?.expiresAt && <p className="pairing-expiry">{pairingExpired ? status.pairing.method === "qr" ? "QR code expired; waiting for the next one…" : "Code expired. Cancel pairing and try again." : `Expires at ${new Date(status.pairing.expiresAt).toLocaleTimeString()}`}</p>}
        <button className="button secondary" disabled={busy} onClick={cancelPairing}>Cancel pairing</button>
      </div>}

      {!pairingActive && !paired && <div className="session-actions">
        <div className="pairing-methods" role="group" aria-label="Pairing method">
          <button className={method === "qr" ? "method-choice selected" : "method-choice"} disabled={busy || running} onClick={() => setMethod("qr")}>QR code</button>
          <button className={method === "phone_code" ? "method-choice selected" : "method-choice"} disabled={busy || running} onClick={() => setMethod("phone_code")}>Phone code</button>
        </div>
        {method === "phone_code" && <label className="phone-input">WhatsApp number with country code
          <input autoComplete="tel" inputMode="tel" type="tel" placeholder="+62 812 3456 7890" value={phone} onChange={(event) => setPhone(event.target.value)} disabled={busy || running} />
        </label>}
        <button className="button primary" disabled={busy || running || (method === "phone_code" && phone.trim().length < 7)} onClick={pair}>{busy ? "Starting…" : "Connect WhatsApp"}</button>
      </div>}

      {status?.agentActive && <p className="muted">To stop the Agent or change the session, open Overview and stop the Agent first.</p>}

      {paired && !status?.agentActive && <div className="session-actions session-connected-actions">
        {running ? <>
          <button className="button secondary" disabled={busy || status?.runtimeState === "reconnecting"} onClick={() => void runOperation(reconnectWhatsAppSession)}>{status?.runtimeState === "reconnecting" ? "Connecting…" : "Reconnect"}</button>
          <button className="button secondary" disabled={busy} onClick={stopSession}>Stop connection</button>
        </> : <button className="button primary" disabled={busy} onClick={() => void runOperation(resumeWhatsAppSession)}>{busy ? "Connecting…" : "Reconnect saved session"}</button>}
        <button className="button danger" disabled={busy} onClick={logout}>Log out of WhatsApp</button>
      </div>}

      {error && <p className="error-text" role="alert">{error}</p>}
      {status?.runtimeState === "failed" && <p className="error-text" role="status">The last connection attempt failed. Your saved session is still available; you can try reconnecting.</p>}
    </section>

    <section className="card session-note">
      <strong>Session data is stored on this device.</strong>
      <p className="muted">Closing the app stops the connection without deleting the session. Use “Log out of WhatsApp” to unlink the device and delete the local session credentials.</p>
    </section>
  </div>;
}
