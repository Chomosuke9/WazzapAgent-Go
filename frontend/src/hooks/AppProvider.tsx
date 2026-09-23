import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { getAppInfo, ping, subscribeToBackendPing, type AppInfo, type PingEvent } from "../services/backend";

type AppContextValue = {
  appInfo: AppInfo | null;
  infoError: string | null;
  loadingInfo: boolean;
  lastPing: PingEvent | null;
  pingPending: boolean;
  pingError: string | null;
  sendPing: () => Promise<void>;
};

const AppContext = createContext<AppContextValue | null>(null);

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "The backend could not be reached.";
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [appInfo, setAppInfo] = useState<AppInfo | null>(null);
  const [infoError, setInfoError] = useState<string | null>(null);
  const [loadingInfo, setLoadingInfo] = useState(true);
  const [lastPing, setLastPing] = useState<PingEvent | null>(null);
  const [pingPending, setPingPending] = useState(false);
  const [pingError, setPingError] = useState<string | null>(null);

  useEffect(() => {
    let mounted = true;
    void getAppInfo()
      .then((info) => { if (mounted) setAppInfo(info); })
      .catch((error: unknown) => { if (mounted) setInfoError(errorMessage(error)); })
      .finally(() => { if (mounted) setLoadingInfo(false); });
    const unsubscribe = subscribeToBackendPing((event) => { if (mounted) setLastPing(event); });
    return () => { mounted = false; unsubscribe(); };
  }, []);

  const sendPing = useCallback(async () => {
    setPingPending(true);
    setPingError(null);
    try { await ping(); } catch (error) { setPingError(errorMessage(error)); }
    finally { setPingPending(false); }
  }, []);

  const value = useMemo(() => ({ appInfo, infoError, loadingInfo, lastPing, pingPending, pingError, sendPing }),
    [appInfo, infoError, loadingInfo, lastPing, pingPending, pingError, sendPing]);
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppContextValue {
  const value = useContext(AppContext);
  if (!value) throw new Error("useApp must be used inside AppProvider.");
  return value;
}
