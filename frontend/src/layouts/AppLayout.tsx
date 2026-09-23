import type { ReactNode } from "react";

export type PageId = "overview" | "whatsapp" | "chat" | "settings" | "data" | "logs";
const navigation: Array<{ id: PageId; label: string; icon: string }> = [
  { id: "overview", label: "Overview", icon: "⌂" },
  { id: "whatsapp", label: "WhatsApp", icon: "◌" },
  { id: "chat", label: "Chat", icon: "▤" },
  { id: "settings", label: "Settings", icon: "⚙" },
  { id: "data", label: "App & Data", icon: "▣" },
  { id: "logs", label: "Log", icon: "≡" },
];

export function AppLayout({ page, onNavigate, children }: { page: PageId; onNavigate: (page: PageId) => void; children: ReactNode }) {
  return <div className={page === "chat" ? "app-shell chat-mode" : "app-shell"}>
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark">W</span><div><strong>WazzapAgent</strong><small>Control center</small></div></div>
      <nav aria-label="Main navigation">{navigation.map((item) => <button key={item.id} className={page === item.id ? "nav-item active" : "nav-item"} onClick={() => onNavigate(item.id)}><span aria-hidden="true">{item.icon}</span>{item.label}</button>)}</nav>
      <div className="sidebar-foot"><span className="tiny-dot" /> Local app</div>
    </aside>
    <main className="main-content">{children}</main>
    <nav className="mobile-nav" aria-label="Mobile navigation">{navigation.map((item) => <button key={item.id} className={page === item.id ? "active" : ""} onClick={() => onNavigate(item.id)}><span aria-hidden="true">{item.icon}</span><small>{item.label}</small></button>)}</nav>
  </div>;
}
