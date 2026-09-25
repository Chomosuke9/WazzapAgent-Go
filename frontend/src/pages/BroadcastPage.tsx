import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { cancelWhatsAppBroadcastSchedule, getWhatsAppBroadcastGroups, getWhatsAppBroadcastSchedules, normalizeWhatsAppBroadcastPayload, scheduleWhatsAppBroadcast, sendWhatsAppBroadcast, type WhatsAppBroadcastGroupDTO, type WhatsAppBroadcastGroupResultDTO, type WhatsAppBroadcastScheduleDTO } from "../services/backend";

type MessageFormat = "text" | "payload";

function errorLabel(code: string): string {
  switch (code) {
    case "not_found": return "This account is no longer in the group.";
    case "timeout": return "WhatsApp did not confirm before the timeout.";
    case "cancelled": return "The send was cancelled.";
    case "unknown_outcome": return "The delivery status could not be confirmed.";
    default: return "WhatsApp could not confirm this message.";
  }
}

function defaultScheduleTime(): string {
  const date = new Date(Date.now() + 60 * 60 * 1000);
  date.setSeconds(0, 0);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function minimumScheduleTime(): string {
  const date = new Date(Date.now() + 60_000);
  date.setSeconds(0, 0);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function scheduleStatus(status: string): string {
  switch (status) {
    case "scheduled": return "Scheduled";
    case "sending": return "Sending";
    case "completed": return "Completed";
    case "partial": return "Partially sent";
    case "failed": return "Failed";
    case "cancelled": return "Cancelled";
    default: return status;
  }
}

export function BroadcastPage() {
  const [groups, setGroups] = useState<WhatsAppBroadcastGroupDTO[]>([]);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [format, setFormat] = useState<MessageFormat>("text");
  const [text, setText] = useState("");
  const [payload, setPayload] = useState('{\n  "conversation": "Halo semuanya!"\n}');
  const [query, setQuery] = useState("");
  const [batchSize, setBatchSize] = useState(20);
  const [batchDelaySeconds, setBatchDelaySeconds] = useState(5);
  const [scheduleTime, setScheduleTime] = useState(defaultScheduleTime);
  const [loading, setLoading] = useState(false);
  const [loadingSchedules, setLoadingSchedules] = useState(false);
  const [sending, setSending] = useState(false);
  const [scheduling, setScheduling] = useState(false);
  const [normalizingPayload, setNormalizingPayload] = useState(false);
  const [payloadNormalizationMessage, setPayloadNormalizationMessage] = useState("");
  const [payloadNormalizationFailed, setPayloadNormalizationFailed] = useState(false);
  const [cancellingSchedule, setCancellingSchedule] = useState("");
  const [error, setError] = useState("");
  const [scheduleError, setScheduleError] = useState("");
  const [results, setResults] = useState<WhatsAppBroadcastGroupResultDTO[]>([]);
  const [schedules, setSchedules] = useState<WhatsAppBroadcastScheduleDTO[]>([]);

  const refreshGroups = useCallback(async () => {
    setLoading(true);
    setError("");
    setGroups([]);
    setSelected(new Set());
    setResults([]);
    try {
      setGroups(await getWhatsAppBroadcastGroups());
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not load WhatsApp groups.");
    } finally {
      setLoading(false);
    }
  }, []);

  const refreshSchedules = useCallback(async () => {
    setLoadingSchedules(true);
    setScheduleError("");
    try {
      setSchedules(await getWhatsAppBroadcastSchedules());
    } catch (reason: unknown) {
      setScheduleError(reason instanceof Error ? reason.message : "Could not load scheduled broadcasts.");
    } finally {
      setLoadingSchedules(false);
    }
  }, []);

  useEffect(() => { void refreshGroups(); void refreshSchedules(); }, [refreshGroups, refreshSchedules]);

  const visibleGroups = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return groups.filter((group) => group.name.toLocaleLowerCase().includes(needle));
  }, [groups, query]);
  const selectedIDs = groups.filter((group) => selected.has(group.id)).map((group) => group.id);
  const allSelected = groups.length > 0 && selectedIDs.length === groups.length;
  const messageValue = format === "text" ? text : payload;
  const sentCount = results.filter((item) => item.sent).length;
  const busy = sending || scheduling || normalizingPayload || cancellingSchedule !== "";

  function toggleGroup(id: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    setResults([]);
  }

  function toggleAllGroups() {
    setSelected(allSelected ? new Set() : new Set(groups.map((group) => group.id)));
    setResults([]);
  }

  async function normalizePayload() {
    if (normalizingPayload || busy || !payload.trim()) return;
    setNormalizingPayload(true);
    setPayloadNormalizationMessage("");
    setPayloadNormalizationFailed(false);
    try {
      const normalized = await normalizeWhatsAppBroadcastPayload(payload);
      setPayload(normalized);
      setResults([]);
      setPayloadNormalizationMessage("Normalized and validated as a WhatsApp message payload.");
    } catch (reason: unknown) {
      setPayloadNormalizationFailed(true);
      setPayloadNormalizationMessage(`${reason instanceof Error ? reason.message : "Could not normalize this JSON."} The original text is unchanged.`);
    } finally {
      setNormalizingPayload(false);
    }
  }

  function confirmReady(action: "send" | "schedule"): string | null {
    const body = messageValue.trim();
    if (!selectedIDs.length) return "Select at least one group.";
    if (!body) return "Add a message before continuing.";
    if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 100) return "Batch size must be between 1 and 100 groups.";
    if (!Number.isInteger(batchDelaySeconds) || batchDelaySeconds < 0 || batchDelaySeconds > 300) return "Batch delay must be between 0 and 300 seconds.";
    if (action === "schedule" && !scheduleTime) return "Choose a date and time for the schedule.";
    return null;
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || loading) return;
    const validation = confirmReady("send");
    if (validation) { setError(validation); return; }
    const formatLabel = format === "text" ? "plain text" : "JSON payload";
    const batches = Math.ceil(selectedIDs.length / batchSize);
    const pause = batches > 1 ? `, in batches of ${batchSize} with a ${batchDelaySeconds}-second pause` : "";
    if (!window.confirm(`Send ${formatLabel} to ${selectedIDs.length} selected groups${pause}?`)) return;

    setSending(true);
    setError("");
    setResults([]);
    try {
      const sent = await sendWhatsAppBroadcast({ groupIDs: selectedIDs, format, payload: messageValue, batchSize, batchDelaySeconds });
      setResults(sent);
      void refreshSchedules();
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not send the broadcast.");
    } finally {
      setSending(false);
    }
  }

  async function submitSchedule() {
    if (busy || loading) return;
    const validation = confirmReady("schedule");
    if (validation) { setError(validation); return; }
    const scheduledAt = new Date(scheduleTime);
    if (Number.isNaN(scheduledAt.getTime()) || scheduledAt.getTime() <= Date.now()) {
      setError("Choose a future date and time.");
      return;
    }
    const formatLabel = format === "text" ? "plain text" : "JSON payload";
    if (!window.confirm(`Schedule ${formatLabel} for ${selectedIDs.length} groups at ${scheduledAt.toLocaleString()}?`)) return;

    setScheduling(true);
    setError("");
    setScheduleError("");
    try {
      await scheduleWhatsAppBroadcast({ groupIDs: selectedIDs, format, payload: messageValue, batchSize, batchDelaySeconds, scheduledAt: scheduledAt.toISOString() });
      await refreshSchedules();
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not schedule the broadcast.");
    } finally {
      setScheduling(false);
    }
  }

  async function cancelSchedule(id: string) {
    if (busy) return;
    setCancellingSchedule(id);
    setScheduleError("");
    try {
      await cancelWhatsAppBroadcastSchedule(id);
      await refreshSchedules();
    } catch (reason: unknown) {
      setScheduleError(reason instanceof Error ? reason.message : "Could not cancel this schedule.");
    } finally {
      setCancellingSchedule("");
    }
  }

  return <div className="page">
    <header className="page-header">
      <div>
        <p className="eyebrow">WHATSAPP</p>
        <h1>Send messages to groups.</h1>
        <p className="lede">Choose groups, then send plain text or a WhatsApp message payload in JSON format.</p>
      </div>
      <span className="broadcast-selected-count">{selectedIDs.length} selected</span>
    </header>
    {error && <p className="error-text" role="alert">{error}</p>}

    <form className="broadcast-form" onSubmit={(event) => void submit(event)}>
      <section className="card broadcast-card">
        <header className="broadcast-card-header">
          <div><h2>Recipient groups</h2><p className="muted">This list shows groups the connected WhatsApp account has joined.</p></div>
          <button type="button" className="button secondary" onClick={() => void refreshGroups()} disabled={loading || busy}>{loading ? "Loading…" : "Refresh groups"}</button>
        </header>
        <div className="broadcast-toolbar">
          <label className="chat-list-search"><span aria-hidden="true">⌕</span><span className="visually-hidden">Search groups</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search group names…" disabled={busy} /></label>
          <button type="button" className="button secondary broadcast-select-all" onClick={toggleAllGroups} disabled={loading || busy || groups.length === 0}>{allSelected ? "Clear selection" : "Select all groups"}</button>
        </div>
        {loading ? <p className="broadcast-empty">Loading groups from WhatsApp…</p> : groups.length === 0 && error ? <div className="broadcast-empty">The group list is unavailable.</div> : groups.length === 0 ? <div className="broadcast-empty"><strong>No groups are available.</strong><span>Make sure the Agent is running and WhatsApp is connected.</span></div> : visibleGroups.length === 0 ? <div className="broadcast-empty">No matching group names.</div> : <ul className="broadcast-group-list">
          {visibleGroups.map((group) => <li key={group.id}>
            <label className="broadcast-group-option">
              <input type="checkbox" checked={selected.has(group.id)} onChange={() => toggleGroup(group.id)} disabled={busy} />
              <span className="broadcast-group-avatar" aria-hidden="true">{group.name.trim().charAt(0).toLocaleUpperCase() || "G"}</span>
              <span className="broadcast-group-name">{group.name}</span>
            </label>
          </li>)}
        </ul>}
        {groups.length > 0 && <p className="broadcast-list-footer">Showing {visibleGroups.length} of {groups.length} groups.</p>}
      </section>

      <section className="card broadcast-card broadcast-composer">
        <header className="broadcast-card-header"><div><h2>Message</h2><p className="muted">The selected format is used for every group.</p></div></header>
        <div className="broadcast-format" role="group" aria-label="Message format">
          <button type="button" className={format === "text" ? "method-choice selected" : "method-choice"} onClick={() => { setFormat("text"); setResults([]); setPayloadNormalizationMessage(""); }} disabled={busy}>Plain text</button>
          <button type="button" className={format === "payload" ? "method-choice selected" : "method-choice"} onClick={() => { setFormat("payload"); setResults([]); setPayloadNormalizationMessage(""); }} disabled={busy}>JSON payload</button>
        </div>
        {format === "text" ? <label className="broadcast-editor">Message text
          <textarea value={text} onChange={(event) => setText(event.target.value)} placeholder="Write a broadcast message…" maxLength={32768} rows={9} disabled={busy} />
          <small>Text is sent as a regular WhatsApp text message.</small>
        </label> : <label className="broadcast-editor">WhatsApp message JSON
          <textarea className="broadcast-json" value={payload} onChange={(event) => { setPayload(event.target.value); setPayloadNormalizationMessage(""); }} spellCheck={false} maxLength={262144} rows={9} disabled={busy} />
          <small>Use ProtoJSON for <code>waE2E.Message</code>. Text example: <code>{'{"conversation":"Hello everyone!"}'}</code>. Protobuf <code>bytes</code> fields use base64; media requires a complete WhatsApp payload.</small>
        </label>}
        {format === "payload" && <div className="broadcast-normalizer-row">
          <div><button type="button" className="button secondary" onClick={() => void normalizePayload()} disabled={busy || !payload.trim()}>{normalizingPayload ? "Normalizing…" : "Normalize JSON"}</button><small>Extracts JSON from code fences or a raw-message envelope, fixes safe formatting issues, and checks WhatsApp message fields.</small></div>
          {payloadNormalizationMessage && <p className={payloadNormalizationFailed ? "broadcast-normalizer-error" : "broadcast-normalizer-success"} role={payloadNormalizationFailed ? "alert" : "status"}>{payloadNormalizationMessage}</p>}
        </div>}

        <div className="broadcast-timing">
          <header><h3>Batch and timing</h3><p className="muted">Groups in each batch are sent at the same time. The pause applies between batches.</p></header>
          <div className="broadcast-timing-grid">
            <label>Groups per batch<input type="number" min={1} max={100} step={1} value={batchSize} onChange={(event) => setBatchSize(Number(event.target.value))} disabled={busy} /><small>Default: 20 groups.</small></label>
            <label>Pause between batches (seconds)<input type="number" min={0} max={300} step={1} value={batchDelaySeconds} onChange={(event) => setBatchDelaySeconds(Number(event.target.value))} disabled={busy} /><small>0 seconds continues immediately.</small></label>
          </div>
        </div>

        <div className="broadcast-schedule-input">
          <label>Schedule for<input type="datetime-local" value={scheduleTime} min={minimumScheduleTime()} onChange={(event) => setScheduleTime(event.target.value)} disabled={busy} /><small>If the Agent is offline at that time, it sends after the Agent reconnects.</small></label>
        </div>
        <div className="broadcast-send-row">
          <p className="muted">{sending ? `Sending batch of up to ${batchSize} groups…` : scheduling ? "Saving schedule…" : `Recipients: ${selectedIDs.length} groups`}</p>
          <div className="broadcast-actions">
            <button type="button" className="button secondary" onClick={() => void submitSchedule()} disabled={busy || loading || selectedIDs.length === 0 || !messageValue.trim()}>{scheduling ? "Scheduling…" : "Schedule message"}</button>
            <button type="submit" className="button primary" disabled={busy || loading || selectedIDs.length === 0 || !messageValue.trim()}>{sending ? "Sending…" : `Send to ${selectedIDs.length} groups`}</button>
          </div>
        </div>
      </section>
    </form>

    <section className="card broadcast-results broadcast-schedules" aria-live="polite">
      <div className="broadcast-card-header">
        <div><h2>Scheduled broadcasts</h2><p className="muted">Saved schedules are sent by the Agent when WhatsApp is connected.</p></div>
        <button type="button" className="button secondary" onClick={() => void refreshSchedules()} disabled={loadingSchedules || busy}>{loadingSchedules ? "Refreshing…" : "Refresh schedules"}</button>
      </div>
      {scheduleError && <p className="error-text" role="alert">{scheduleError}</p>}
      {loadingSchedules && schedules.length === 0 ? <p className="broadcast-empty">Loading scheduled broadcasts…</p> : schedules.length === 0 ? <p className="broadcast-empty">There are no scheduled broadcasts.</p> : <ul className="broadcast-schedule-list">
        {schedules.map((schedule) => {
          const failed = (schedule.results ?? []).filter((item) => !item.sent);
          return <li key={schedule.id}>
            <div className="broadcast-schedule-summary">
              <strong>{new Date(schedule.scheduledAt).toLocaleString()}</strong>
              <span>{schedule.groupCount} groups · batches of {schedule.batchSize} · {schedule.batchDelaySeconds}s pause</span>
              {failed.length > 0 && schedule.status !== "scheduled" && <details className="broadcast-schedule-errors">
                <summary>{failed.length} group{failed.length === 1 ? "" : "s"} need attention</summary>
                <ul>{failed.map((item, index) => <li key={`${item.name}-${index}`}><strong>{item.name}</strong><span>{errorLabel(item.errorCode)}</span></li>)}</ul>
              </details>}
            </div>
            <div className="broadcast-schedule-actions">
              <b className={`broadcast-schedule-status status-${schedule.status}`}>{scheduleStatus(schedule.status)}</b>
              {schedule.status === "scheduled" && <button type="button" className="button secondary" onClick={() => void cancelSchedule(schedule.id)} disabled={busy}>{cancellingSchedule === schedule.id ? "Cancelling…" : "Cancel"}</button>}
            </div>
          </li>;
        })}
      </ul>}
    </section>

    {results.length > 0 && <section className="card broadcast-results" aria-live="polite">
      <div className="broadcast-card-header"><div><h2>Delivery results</h2><p className="muted">Sent to {sentCount} of {results.length} groups.</p></div></div>
      <ul className="broadcast-result-list">{results.map((item) => <li key={item.id} className={item.sent ? "broadcast-result-sent" : "broadcast-result-failed"}><span><strong>{item.name}</strong>{!item.sent && <small>{errorLabel(item.errorCode)}</small>}</span><b>{item.sent ? "Sent" : "Failed"}</b></li>)}</ul>
    </section>}
  </div>;
}
