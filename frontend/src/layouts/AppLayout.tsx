import { useEffect, useState, type ReactNode } from "react";

export type PageId = "overview" | "whatsapp" | "chat" | "settings" | "data" | "logs";
type Theme = "light" | "dark";

const navigation: Array<{ id: PageId; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "whatsapp", label: "WhatsApp" },
  { id: "chat", label: "Chat" },
  { id: "settings", label: "Settings" },
  { id: "data", label: "App & Data" },
  { id: "logs", label: "Log" },
];

function readTheme(): Theme {
  try {
    const saved = window.localStorage.getItem("wazzapagent.theme");
    if (saved === "light" || saved === "dark") return saved;
  } catch { /* App remains usable when storage is unavailable. */ }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function readCollapsed(): boolean {
  try { return window.localStorage.getItem("wazzapagent.sidebar.collapsed") === "true"; }
  catch { return false; }
}

function NavIcon({ id }: { id: PageId }) {
  const paths: Record<PageId, ReactNode> = {
    overview: <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>,
    whatsapp: <><path d="M20 11.5a8 8 0 0 1-11.8 7L4 20l1.5-4.2A8 8 0 1 1 20 11.5Z" /><path d="M9 9.5c.8 2.2 2.3 3.7 4.5 4.5l1.2-1" /></>,
    chat: <><path d="M4 5.5h16v12H8l-4 3v-15Z" /><path d="M8 10h8M8 14h6" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4m10.6 10.6 1.4 1.4M18.7 5.3l-1.4 1.4M6.7 17.3l-1.4 1.4" /></>,
    data: <><rect x="4" y="3" width="16" height="18" rx="2" /><path d="M8 8h8M8 12h8M8 16h5" /></>,
    logs: <><path d="M5 6h14M5 12h14M5 18h10" /><circle cx="3" cy="6" r=".5" /><circle cx="3" cy="12" r=".5" /><circle cx="3" cy="18" r=".5" /></>,
  };
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[id]}</svg>;
}

function ThemeIcon({ theme }: { theme: Theme }) {
  return theme === "dark"
    ? <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4" /><path d="M12 2v2m0 16v2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M2 12h2m16 0h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>
    : <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M20.5 15.6A8.5 8.5 0 0 1 8.4 3.5 8.5 8.5 0 1 0 20.5 15.6Z" /></svg>;
}

export function AppLayout({ page, onNavigate, children }: { page: PageId; onNavigate: (page: PageId) => void; children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [theme, setTheme] = useState<Theme>(readTheme);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.style.colorScheme = theme;
    try { window.localStorage.setItem("wazzapagent.theme", theme); } catch { /* Theme still works for this session. */ }
  }, [theme]);

  function toggleSidebar() {
    setCollapsed((current) => {
      const next = !current;
      try { window.localStorage.setItem("wazzapagent.sidebar.collapsed", String(next)); } catch { /* Keep in-session state. */ }
      return next;
    });
  }

  function toggleTheme() { setTheme((current) => current === "light" ? "dark" : "light"); }

  return <div className={`app-shell${page === "chat" ? " chat-mode" : ""}${collapsed ? " sidebar-collapsed" : ""}`}>
    <aside className="sidebar" aria-label="Application sidebar">
      <div className="sidebar-top">
        <div className="brand"><span className="brand-mark">W</span><div className="brand-copy"><strong>WazzapAgent</strong><small>Control center</small></div></div>
        <button type="button" className="sidebar-toggle" onClick={toggleSidebar} aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"} title={collapsed ? "Expand sidebar" : "Collapse sidebar"} aria-expanded={!collapsed}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={collapsed ? "M9 5l7 7-7 7" : "M15 5l-7 7 7 7"} /></svg>
        </button>
      </div>
      <div className="nav-caption">WORKSPACE</div>
      <nav aria-label="Main navigation">{navigation.map((item) => <button key={item.id} type="button" className={page === item.id ? "nav-item active" : "nav-item"} onClick={() => onNavigate(item.id)} title={collapsed ? item.label : undefined} aria-label={item.label} aria-current={page === item.id ? "page" : undefined}><span className="nav-icon"><NavIcon id={item.id} /></span><span className="nav-label">{item.label}</span></button>)}</nav>
      <div className="sidebar-bottom">
        <button type="button" className="theme-toggle" onClick={toggleTheme} aria-label={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"} title={theme === "dark" ? "Light mode" : "Dark mode"}><span className="nav-icon"><ThemeIcon theme={theme} /></span><span className="nav-label">{theme === "dark" ? "Light mode" : "Dark mode"}</span></button>
        <div className="sidebar-foot"><span className="tiny-dot" /><span className="nav-label">Local app</span></div>
      </div>
    </aside>
    <main className="main-content">{children}</main>
    <nav className="mobile-nav" aria-label="Mobile navigation">{navigation.map((item) => <button key={item.id} type="button" className={page === item.id ? "active" : ""} onClick={() => onNavigate(item.id)} aria-label={item.label} aria-current={page === item.id ? "page" : undefined}><NavIcon id={item.id} /><small>{item.label}</small></button>)}<button type="button" onClick={toggleTheme} aria-label={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"}><ThemeIcon theme={theme} /><small>{theme === "dark" ? "Light" : "Dark"}</small></button></nav>
  </div>;
}
