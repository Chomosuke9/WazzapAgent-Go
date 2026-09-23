export function StatusBadge({ tone = "neutral", children }: { tone?: "neutral" | "good" | "warn"; children: React.ReactNode }) {
  return <span className={`status-badge status-${tone}`}><span aria-hidden="true" className="status-dot" />{children}</span>;
}
