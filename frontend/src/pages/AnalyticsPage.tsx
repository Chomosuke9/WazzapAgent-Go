import { useCallback, useEffect, useRef, useState } from "react";
import { getWhatsAppUsage, type WhatsAppDailyUsageDTO, type WhatsAppGroupUsageDTO, type WhatsAppUsageDTO } from "../services/backend";

type ChartMetric = "messages" | "invocations";
type ChartPeriod = 1 | 7 | 30;

const numberFormat = new Intl.NumberFormat("en-US");
const metricStorageKey = "wazzapagent.analytics.metric.v2";
const periodStorageKey = "wazzapagent.analytics.period";

function readPeriod(): ChartPeriod {
  try {
    const value = Number(window.localStorage.getItem(periodStorageKey));
    return value === 1 || value === 7 || value === 30 ? value : 7;
  } catch {
    return 7;
  }
}

function readMetric(): ChartMetric {
  try {
    return window.localStorage.getItem(metricStorageKey) === "messages" ? "messages" : "invocations";
  } catch {
    return "invocations";
  }
}

function metricValue(item: WhatsAppDailyUsageDTO | WhatsAppGroupUsageDTO, metric: ChartMetric): number {
  return metric === "messages" ? item.messages : item.invocations;
}

function UsageChart({ days, metric }: { days: WhatsAppDailyUsageDTO[]; metric: ChartMetric }) {
  const valueLabel = metric === "messages" ? "messages" : "Agent invokes";
  const peak = Math.max(1, ...days.map((day) => metricValue(day, metric)));
  const formatDay = (value: string, compact: boolean) => {
    const date = new Date(`${value}T12:00:00`);
    if (Number.isNaN(date.getTime())) return value;
    return compact ? String(date.getDate()) : new Intl.DateTimeFormat("en-US", { weekday: "short" }).format(date);
  };
  return <svg className="usage-chart" viewBox="0 0 700 190" role="img" aria-label={`${valueLabel} per day during the selected period`}>
    {[24, 82, 140].map((y, index) => <g key={y}>
      <line x1="42" y1={y} x2="692" y2={y} />
      <text x="34" y={y + 4} textAnchor="end">{numberFormat.format(Math.round(peak * (2 - index) / 2))}</text>
    </g>)}
    {days.map((day, index) => {
      const value = metricValue(day, metric);
      const slot = 650 / Math.max(days.length, 1);
      const width = Math.min(48, Math.max(6, slot * 0.68));
      const x = 42 + index * slot + (slot - width) / 2;
      const height = value ? Math.max(5, value / peak * 108) : 2;
      const y = 140 - height;
      const showDayLabel = days.length <= 7 || index % 5 === 0 || index === days.length - 1;
      return <g key={day.date}>
        <rect className="usage-bar" x={x} y={y} width={width} height={height} rx="5"><title>{`${day.date}: ${numberFormat.format(value)} ${valueLabel}`}</title></rect>
        {value > 0 && days.length <= 7 && <text className="usage-bar-value" x={x + width / 2} y={y - 8} textAnchor="middle">{numberFormat.format(value)}</text>}
        {showDayLabel && <text className="usage-day-label" x={x + width / 2} y="165" textAnchor="middle">{formatDay(day.date, days.length > 7)}</text>}
      </g>;
    })}
  </svg>;
}

export function AnalyticsPage() {
  const [usage, setUsage] = useState<WhatsAppUsageDTO | null>(null);
  const [usageError, setUsageError] = useState<string | null>(null);
  const [metric, setMetric] = useState<ChartMetric>(readMetric);
  const [periodDays, setPeriodDays] = useState<ChartPeriod>(readPeriod);
  const requestSequence = useRef(0);

  const refreshUsage = useCallback(async () => {
    const request = ++requestSequence.current;
    try {
      const result = await getWhatsAppUsage(periodDays);
      if (request !== requestSequence.current) return;
      setUsage(result);
      setUsageError(null);
    } catch (reason: unknown) {
      if (request !== requestSequence.current) return;
      setUsageError(reason instanceof Error ? reason.message : "Could not load analytics.");
    }
  }, [periodDays]);

  useEffect(() => {
    try { window.localStorage.setItem(metricStorageKey, metric); } catch { /* Keep the selection for this session. */ }
  }, [metric]);

  useEffect(() => {
    try { window.localStorage.setItem(periodStorageKey, String(periodDays)); } catch { /* Keep the selection for this session. */ }
  }, [periodDays]);

  useEffect(() => {
    void refreshUsage();
    const timer = window.setInterval(() => void refreshUsage(), 30000);
    window.addEventListener("focus", refreshUsage);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", refreshUsage);
    };
  }, [refreshUsage]);

  const topGroups = metric === "messages" ? usage?.groups ?? [] : usage?.invocationGroups ?? [];
  const groupPeriodTotal = (group: WhatsAppGroupUsageDTO) => metric === "messages" ? group.messagesInPeriod : group.invocationsInPeriod;
  const groupAllTimeTotal = (group: WhatsAppGroupUsageDTO) => metric === "messages" ? group.messages : group.invocations;
  const metricName = metric === "messages" ? "messages" : "Agent invokes";
  const periodLabel = periodDays === 1 ? "1 day" : `${periodDays} days`;
  const periodStart = usage?.periodStart ? new Intl.DateTimeFormat("en-US", { day: "numeric", month: "short" }).format(new Date(`${usage.periodStart}T12:00:00`)) : "";

  return <div className="page analytics-page">
    <header className="page-header"><div><p className="eyebrow">INSIGHTS</p><h1>Analytics</h1><p className="lede">Follow message activity and Agent invokes across your conversations.</p></div></header>
    <section className="usage-section analytics-section" aria-labelledby="analytics-heading">
      <div className="section-heading usage-title-row analytics-toolbar">
        <div><h2 id="analytics-heading">Last {periodLabel}</h2><p className="muted">Choose the time range and activity for both charts.</p></div>
        <div className="analytics-control-row">
          <div className="analytics-period-control"><span>Time range</span><div className="metric-toggle" role="group" aria-label="Choose chart time range">
            {([30, 7, 1] as const).map((days) => <button key={days} type="button" className={periodDays === days ? "selected" : ""} aria-pressed={periodDays === days} onClick={() => setPeriodDays(days)}>{days} {days === 1 ? "day" : "days"}</button>)}
          </div></div>
          <div className="analytics-metric-control"><span>Chart metric</span><div className="metric-toggle" role="group" aria-label="Choose chart metric">
            <button type="button" className={metric === "invocations" ? "selected" : ""} aria-pressed={metric === "invocations"} onClick={() => setMetric("invocations")}>Agent invokes</button>
            <button type="button" className={metric === "messages" ? "selected" : ""} aria-pressed={metric === "messages"} onClick={() => setMetric("messages")}>Messages</button>
          </div></div>
        </div>
      </div>
      {usageError ? <div className="card usage-error" role="alert"><div><strong>Analytics are temporarily unavailable</strong><p className="muted">{usageError}</p></div><button className="button secondary" onClick={() => void refreshUsage()}>Try again</button></div>
        : !usage || usage.periodDays !== periodDays ? <div className="card usage-loading" role="status">Loading analytics…</div>
          : <>
            <div className="usage-stats">
              <article className="card usage-stat"><span>Messages · {periodLabel}</span><strong>{numberFormat.format(usage.messagesInPeriod)}</strong><small>{numberFormat.format(usage.totalMessages)} across saved history</small></article>
              <article className="card usage-stat"><span>Agent invokes · {periodLabel}</span><strong>{numberFormat.format(usage.invocationsInPeriod)}</strong><small>{numberFormat.format(usage.totalInvocations)} across saved history</small></article>
              <article className="card usage-stat"><span>Groups with history</span><strong>{numberFormat.format(usage.totalGroups)}</strong><small>{numberFormat.format(usage.activeGroupsInPeriod)} active in this period</small></article>
              <article className="card usage-stat"><span>Active conversations</span><strong>{numberFormat.format(usage.activeChatsInPeriod)}</strong><small>Conversations with saved activity</small></article>
            </div>
            <div className="usage-panels">
              <section className="card usage-panel" aria-labelledby="activity-chart-heading"><header><div><h3 id="activity-chart-heading">{metric === "messages" ? "Message activity" : "Agent invokes"}</h3><p className="muted">{metricName} per day over the last {periodLabel}.</p></div><span className="usage-period">From {periodStart}</span></header>
                {usage.dailyActivity?.length ? <UsageChart days={usage.dailyActivity} metric={metric} /> : <p className="usage-empty">Daily activity will appear here once the Agent has saved history.</p>}
              </section>
              <section className="card usage-panel" aria-labelledby="top-groups-heading"><header><div><h3 id="top-groups-heading">Top groups by {metric === "messages" ? "messages" : "Agent invokes"}</h3><p className="muted">Ranked within the selected period.</p></div><span className="usage-period">{periodLabel}</span></header>
                {topGroups.length && groupPeriodTotal(topGroups[0]) > 0 ? <ol className="usage-groups">{topGroups.map((group, index) => {
                  const value = groupPeriodTotal(group);
                  const maximum = Math.max(1, groupPeriodTotal(topGroups[0]));
                  return <li key={`${group.name}-${index}`}><div className="usage-group-heading"><span className="usage-rank">{index + 1}</span><strong title={group.name}>{group.name}</strong><span>{numberFormat.format(value)}</span></div><div className="usage-group-track" role="progressbar" aria-valuenow={value} aria-valuemin={0} aria-valuemax={maximum} aria-label={`${group.name}: ${numberFormat.format(value)} ${metricName} in ${periodLabel}`}><span style={{ width: `${Math.max(3, value / maximum * 100)}%` }} /></div><small>{numberFormat.format(groupAllTimeTotal(group))} all time</small></li>;
                })}</ol> : <div className="usage-empty"><strong>No {metricName.toLowerCase()} yet</strong><span>Group activity will appear here as it is saved by the Agent.</span></div>}
              </section>
            </div>
            <p className="usage-footnote">Messages include incoming messages and Agent replies. An Agent invoke is counted only when the Agent starts a model run; ordinary group messages do not count as invokes. Chat resets and history retention can remove older activity.</p>
          </>}
    </section>
  </div>;
}
