import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { getAppInfo, type AppInfo } from "../services/backend";

type AppContextValue = {
  appInfo: AppInfo | null;
  infoError: string | null;
  loadingInfo: boolean;
  lastCheck: Date | null;
  checkPending: boolean;
  checkError: string | null;
  checkConnection: () => Promise<void>;
};

const AppContext = createContext<AppContextValue | null>(null);

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "The backend could not be reached.";
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [appInfo, setAppInfo] = useState<AppInfo | null>(null);
  const [infoError, setInfoError] = useState<string | null>(null);
  const [loadingInfo, setLoadingInfo] = useState(true);
  const [lastCheck, setLastCheck] = useState<Date | null>(null);
  const [checkPending, setCheckPending] = useState(false);
  const [checkError, setCheckError] = useState<string | null>(null);

  useEffect(() => {
    let mounted = true;
    void getAppInfo()
      .then((info) => { if (mounted) setAppInfo(info); })
      .catch((error: unknown) => { if (mounted) setInfoError(errorMessage(error)); })
      .finally(() => { if (mounted) setLoadingInfo(false); });
    return () => { mounted = false; };
  }, []);

  // A round trip to the service is the whole connection check.
  const checkConnection = useCallback(async () => {
    setCheckPending(true);
    setCheckError(null);
    try { setAppInfo(await getAppInfo()); setLastCheck(new Date()); } catch (error) { setCheckError(errorMessage(error)); }
    finally { setCheckPending(false); }
  }, []);

  const value = useMemo(() => ({ appInfo, infoError, loadingInfo, lastCheck, checkPending, checkError, checkConnection }),
    [appInfo, infoError, loadingInfo, lastCheck, checkPending, checkError, checkConnection]);
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppContextValue {
  const value = useContext(AppContext);
  if (!value) throw new Error("useApp must be used inside AppProvider.");
  return value;
}
