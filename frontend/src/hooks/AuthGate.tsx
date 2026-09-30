import { createContext, useCallback, useContext, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { getAuthStatus, isWebMode, signIn, signOut, unauthorizedEvent } from "../services/backend";

type AuthState = "checking" | "signed-out" | "signed-in";
type AuthContextValue = { required: boolean; signOut: () => Promise<void> };

const AuthContext = createContext<AuthContextValue>({ required: false, signOut: async () => {} });

export const useAuth = () => useContext(AuthContext);

// In the browser build the server may require an access token. The desktop app
// and an open server pass straight through to the children.
export function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>(isWebMode ? "checking" : "signed-in");
  const [required, setRequired] = useState(false);
  const [token, setToken] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isWebMode) return;
    let mounted = true;
    getAuthStatus()
      .then((status) => {
        if (!mounted) return;
        setRequired(status.required);
        setState(status.authenticated ? "signed-in" : "signed-out");
      })
      .catch((cause: unknown) => {
        if (!mounted) return;
        setError(cause instanceof Error ? cause.message : "The server could not be reached.");
        setState("signed-out");
      });
    const expired = () => setState("signed-out");
    window.addEventListener(unauthorizedEvent, expired);
    return () => { mounted = false; window.removeEventListener(unauthorizedEvent, expired); };
  }, []);

  const leave = useCallback(async () => {
    // Only show the signed-out state once the server has removed the cookie;
    // otherwise a reload would silently restore the session.
    await signOut();
    setToken("");
    setState("signed-out");
  }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!token.trim() || pending) return;
    setPending(true);
    setError(null);
    try {
      await signIn(token);
      setToken("");
      setRequired(true);
      setState("signed-in");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Sign-in failed.");
    } finally {
      setPending(false);
    }
  }

  if (state === "checking") return <div className="auth-screen" aria-busy="true" />;
  if (state === "signed-in") return <AuthContext.Provider value={{ required, signOut: leave }}>{children}</AuthContext.Provider>;
  return <div className="auth-screen">
    <form className="card auth-card" onSubmit={submit}>
      <p className="eyebrow">DISCORDAGENT</p>
      <h1>Sign in</h1>
      <p className="muted">Enter the access token printed when the server first started, or run <code>discordagent-web token</code> on the server. This browser will stay signed in.</p>
      <label className="auth-field">
        <span>Access token</span>
        <input type="password" value={token} onChange={(event) => setToken(event.target.value)} autoComplete="current-password" autoFocus spellCheck={false} disabled={pending} />
      </label>
      {error && <p className="error-text" role="alert">{error}</p>}
      <button type="submit" className="button primary" disabled={pending || !token.trim()}>{pending ? "Signing in…" : "Sign in"}</button>
    </form>
  </div>;
}
