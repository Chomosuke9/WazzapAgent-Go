import { useCallback, useEffect, useMemo, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import { getLogs, type LogEntryDTO } from "../services/backend";

type LevelFilter = "all" | "ERROR" | "WARN";

function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(date);
}

export function LogsPage() {
  const [entries, setEntries] = useState<LogEntryDTO[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<LevelFilter>("all");
  const [query, setQuery] = useState("");

  const refresh = useCallback(async () => {
    try {
      setEntries(await getLogs());
      setError(null);
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not read app logs.");
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 1500);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const visibleEntries = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return [...entries].reverse().filter((entry) => {
      if (filter !== "all" && entry.level !== filter) return false;
      if (!needle) return true;
      return `${entry.message} ${entry.details}`.toLocaleLowerCase().includes(needle);
    });
  }, [entries, filter, query]);

  const errorCount = entries.filter((entry) => entry.level === "ERROR").length;

  return <div className="page">
    <header className="page-header"><div><p className="eyebrow">APP ACTIVITY</p><h1>App logs.</h1><p className="lede">Monitor the Agent, WhatsApp connection, and settings changes while the app is running.</p></div><StatusBadge tone={errorCount ? "warn" : "good"}>{errorCount ? `${errorCount} errors` : `${entries.length} events`}</StatusBadge></header>
    <section className="card logs-card">
      <div className="logs-toolbar">
        <div><h3>Recent activity</h3><p className="muted small">Showing up to 500 recent events. The list refreshes automatically and resets when the app closes.</p></div>
        <button className="button secondary" onClick={() => void refresh()}>Refresh</button>
      </div>
      <div className="logs-filters">
        <label className="logs-search"><span className="visually-hidden">Search logs</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search activity…" /></label>
        <div className="logs-level-filter" aria-label="Filter log level">
          {(["all", "WARN", "ERROR"] as const).map((level) => <button key={level} className={filter === level ? "selected" : ""} onClick={() => setFilter(level)}>{level === "all" ? "All" : level === "WARN" ? "Warning" : "Error"}</button>)}
        </div>
      </div>
      {error && <p className="error-text" role="alert">{error}</p>}
      {visibleEntries.length ? <ol className="logs-list">{visibleEntries.map((entry, index) => <li className={`log-entry log-${entry.level.toLowerCase()}`} key={`${entry.time}-${index}`}>
        <time dateTime={entry.time}>{formatTimestamp(entry.time)}</time>
        <span className="log-level">{entry.level}</span>
        <div className="log-copy"><strong>{entry.message}</strong>{entry.details && <p>{entry.details}</p>}</div>
      </li>)}</ol> : <div className="logs-empty"><span aria-hidden="true">≡</span><strong>{entries.length ? "No matching logs" : "No activity yet"}</strong><p>{entries.length ? "Change your search or filter to see other activity." : "Logs will appear here when the Agent or WhatsApp session does something."}</p></div>}
    </section>
  </div>;
}
