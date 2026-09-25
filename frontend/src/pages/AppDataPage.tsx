import { StatusBadge } from "../components/StatusBadge";
import { useApp } from "../hooks/AppProvider";
import { NavIcon } from "../layouts/AppLayout";

export function AppDataPage() {
  const { appInfo, infoError, loadingInfo, lastPing, pingPending, pingError, sendPing } = useApp();
  return <div className="page">
    <header className="page-header"><div><p className="eyebrow">SYSTEM</p><h1>App & data</h1><p className="lede">Application information and the data behind your workspace.</p></div></header>
    <section className="card app-about"><span className="brand-mark"><NavIcon id="whatsapp" /></span><div><h2>{appInfo?.name ?? "WazzapAgent"}</h2><p className="muted">Your WhatsApp assistant workspace</p></div><StatusBadge>{loadingInfo ? "Loading…" : appInfo ? `v${appInfo.version}` : "Unavailable"}</StatusBadge></section>
    {infoError && <p className="error-text" role="alert">{infoError}</p>}
    <section className="card data-details"><h2>Workspace data</h2><p className="muted">Settings are saved on the device running WazzapAgent and are available the next time you open the app.</p><dl><div><dt>Platform</dt><dd>{appInfo?.platform ?? "Unavailable"}</dd></div><div><dt>Settings</dt><dd>Saved when you select Save settings</dd></div><div><dt>WhatsApp session</dt><dd>Kept until you log out or WhatsApp revokes access</dd></div></dl><p className="muted small">Backup, restore, and changing the data location are not available in the app yet.</p></section>
    <details className="card diagnostics"><summary><span>Connection diagnostics<small>Check communication with the application service</small></span></summary><div className="diagnostics-body"><p className="muted">Use this check if the app stops responding or cannot load your workspace.</p><button className="button secondary" onClick={() => void sendPing()} disabled={pingPending}>{pingPending ? "Checking…" : "Check connection"}</button>{lastPing && <p className="event-line" role="status"><span className="event-check">✓</span>Service responded · {lastPing.message}</p>}{pingError && <p className="error-text" role="alert">{pingError}</p>}</div></details>
  </div>;
}
