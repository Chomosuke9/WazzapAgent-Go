import { useEffect, useState, type ReactNode } from "react";
import { useApp } from "../hooks/AppProvider";

export type PageId = "overview" | "whatsapp" | "broadcast" | "chat" | "analytics" | "settings" | "data" | "logs";
type Theme = "light" | "dark";

const navigation: Array<{ id: PageId; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "chat", label: "Inbox" },
  { id: "broadcast", label: "Broadcasts" },
  { id: "analytics", label: "Analytics" },
  { id: "whatsapp", label: "WhatsApp" },
  { id: "settings", label: "Settings" },
  { id: "data", label: "App & data" },
  { id: "logs", label: "Activity" },
];
const navigationGroups: Array<{ label: string; items: Array<{ id: PageId; label: string }> }> = [
  { label: "WORKSPACE", items: navigation.slice(0, 3) },
  { label: "ANALYTICS", items: navigation.slice(3, 4) },
  { label: "MANAGE", items: navigation.slice(4, 6) },
  { label: "SYSTEM", items: navigation.slice(6) },
];
const mobileNavigation = navigation.filter((item) => ["overview", "chat", "broadcast", "whatsapp"].includes(item.id));
const secondaryNavigation = navigation.filter((item) => !mobileNavigation.includes(item));

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

export function NavIcon({ id }: { id: PageId }) {
  const paths: Record<PageId, ReactNode> = {
    overview: <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>,
    whatsapp: <><path d="M20 11.5a8 8 0 0 1-11.8 7L4 20l1.5-4.2A8 8 0 1 1 20 11.5Z" /><path d="M9 9.5c.8 2.2 2.3 3.7 4.5 4.5l1.2-1" /></>,
    broadcast: <><path d="M4 12h11" /><path d="m11 6 6 6-6 6" /><path d="M18 5.5 21 4v16l-3-1.5" /></>,
    chat: <><path d="M4 5.5h16v12H8l-4 3v-15Z" /><path d="M8 10h8M8 14h6" /></>,
    analytics: <><path d="M4 19V5M4 19h17" /><path d="m7 15 4-4 3 2 6-7" /><path d="M17 6h3v3" /></>,
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
  const { appInfo } = useApp();
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [theme, setTheme] = useState<Theme>(readTheme);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  useEffect(() => {
    if (!mobileMenuOpen) return;
    const dismiss = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setMobileMenuOpen(false);
        document.getElementById("mobile-more-toggle")?.focus();
      }
    };
    window.addEventListener("keydown", dismiss);
    return () => window.removeEventListener("keydown", dismiss);
  }, [mobileMenuOpen]);

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
        <div className="brand"><span className="brand-mark"><NavIcon id="whatsapp" /></span><div className="brand-copy"><strong>WazzapAgent</strong><small>Your messaging workspace</small></div></div>
        <button type="button" className="sidebar-toggle" onClick={toggleSidebar} aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"} title={collapsed ? "Expand sidebar" : "Collapse sidebar"} aria-expanded={!collapsed}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={collapsed ? "M9 5l7 7-7 7" : "M15 5l-7 7 7 7"} /></svg>
        </button>
      </div>
      <nav aria-label="Main navigation">{navigationGroups.map((group) => <div className="nav-group" key={group.label}>
        <div className="nav-caption">{group.label}</div>
        {group.items.map((item) => <button key={item.id} type="button" className={page === item.id ? "nav-item active" : "nav-item"} onClick={() => onNavigate(item.id)} title={collapsed ? item.label : undefined} aria-label={item.label} aria-current={page === item.id ? "page" : undefined}><span className="nav-icon"><NavIcon id={item.id} /></span><span className="nav-label">{item.label}</span></button>)}
      </div>)}</nav>
      <div className="sidebar-bottom">
        <button type="button" className="theme-toggle" onClick={toggleTheme} aria-label={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"} title={theme === "dark" ? "Light mode" : "Dark mode"}><span className="nav-icon"><ThemeIcon theme={theme} /></span><span className="nav-label">{theme === "dark" ? "Light mode" : "Dark mode"}</span></button>
        <div className="sidebar-foot"><span className="nav-icon"><NavIcon id="data" /></span><span className="nav-label">{appInfo ? `v${appInfo.version}` : "WazzapAgent"}</span></div>
      </div>
    </aside>
    <main className="main-content"><div className="workspace-bar"><span>Workspace <span className="breadcrumb-divider">/</span> <strong>{navigation.find((item) => item.id === page)?.label}</strong></span><span className="workspace-caption">WazzapAgent</span></div>{children}</main>
    {mobileMenuOpen && <div className="mobile-more" id="mobile-more-panel"><nav aria-label="More navigation">{secondaryNavigation.map((item) => <button key={item.id} className={page === item.id ? "active" : ""} onClick={() => { onNavigate(item.id); setMobileMenuOpen(false); }}><NavIcon id={item.id} />{item.label}</button>)}<button onClick={toggleTheme}><ThemeIcon theme={theme} />{theme === "dark" ? "Light mode" : "Dark mode"}</button></nav></div>}
    <nav className="mobile-nav" aria-label="Mobile navigation">{mobileNavigation.map((item) => <button key={item.id} type="button" className={page === item.id ? "active" : ""} onClick={() => { onNavigate(item.id); setMobileMenuOpen(false); }} aria-label={item.label} aria-current={page === item.id ? "page" : undefined}><NavIcon id={item.id} /><small>{item.label}</small></button>)}<button id="mobile-more-toggle" type="button" className={secondaryNavigation.some((item) => item.id === page) || mobileMenuOpen ? "active" : ""} onClick={() => setMobileMenuOpen((open) => !open)} aria-expanded={mobileMenuOpen} aria-controls="mobile-more-panel" aria-label="More"><svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="2" /><circle cx="12" cy="12" r="2" /><circle cx="19" cy="12" r="2" /></svg><small>More</small></button></nav>
  </div>;
}
