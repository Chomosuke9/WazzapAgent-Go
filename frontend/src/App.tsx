import { useState } from "react";
import { AppLayout, type PageId } from "./layouts/AppLayout";
import { AppDataPage } from "./pages/AppDataPage";
import { OverviewPage } from "./pages/OverviewPage";
import { SettingsPage } from "./pages/SettingsPage";
import { WhatsAppPage } from "./pages/WhatsAppPage";
import { LogsPage } from "./pages/LogsPage";
import { ChatPage } from "./pages/ChatPage";

export function App() {
  const [page, setPage] = useState<PageId>("overview");
  const current = page === "overview" ? <OverviewPage /> : page === "whatsapp" ? <WhatsAppPage /> : page === "chat" ? <ChatPage /> : page === "settings" ? <SettingsPage /> : page === "logs" ? <LogsPage /> : <AppDataPage />;
  return <AppLayout page={page} onNavigate={setPage}>{current}</AppLayout>;
}
