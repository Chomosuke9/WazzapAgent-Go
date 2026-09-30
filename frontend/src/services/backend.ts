import { AppService } from "../../bindings/github.com/Chomosuke9/DiscordAgent-Go/internal/ui";
import type { AgentRuntimeStatusDTO, AppInfo, WhatsAppSessionOperationDTO, WhatsAppSessionStatusDTO } from "../../bindings/github.com/Chomosuke9/DiscordAgent-Go/internal/ui/models";
export type * from "../../bindings/github.com/Chomosuke9/DiscordAgent-Go/internal/ui/models";

// Every backend operation is a method on the Go ui.AppService. The desktop app
// calls it through the generated Wails binding; the browser build posts the
// same method name and arguments to /api/call.
type Service = typeof AppService;
type Method = keyof Service;
type Result<M extends Method> = Awaited<ReturnType<Service[M]>>;

const webMode = import.meta.env.MODE === "web";

// Fired when the server no longer accepts the browser's login (for example
// after the access token was rotated), so the sign-in screen can come back.
export const unauthorizedEvent = "discordagent:unauthorized";

async function webPost<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
  });
  const payload = await response.json().catch(() => ({})) as { result?: T; error?: string };
  if (response.status === 401 && path === "/api/call") window.dispatchEvent(new Event(unauthorizedEvent));
  if (!response.ok) throw new Error(payload.error || `Request failed (${response.status}).`);
  return payload as T;
}

async function webCall<T>(method: string, args: unknown[]): Promise<T> {
  return (await webPost<{ result?: T }>("/api/call", { method, args })).result as T;
}

// Browser-only sign-in with the server's access token. The server answers with
// a session cookie, so the token is typed once and never kept by the page.
export type AuthStatus = { required: boolean; authenticated: boolean };
export const isWebMode = webMode;

export async function getAuthStatus(): Promise<AuthStatus> {
  const response = await fetch("/api/auth/status", { credentials: "same-origin" });
  if (!response.ok) throw new Error(`Request failed (${response.status}).`);
  return response.json() as Promise<AuthStatus>;
}
export const signIn = (token: string) => webPost<AuthStatus>("/api/auth/login", { token });
export const signOut = () => webPost<AuthStatus>("/api/auth/logout", {});

function call<M extends Method>(method: M, ...args: Parameters<Service[M]>): Promise<Result<M>> {
  if (webMode) return webCall<Result<M>>(method, args);
  const native = AppService[method] as unknown as (...values: Parameters<Service[M]>) => Promise<Result<M>>;
  return native(...args);
}

// Go returns nil slices as null; the pages expect arrays.
const list = <T>(items: T[] | null): T[] => items ?? [];

export const getAppInfo = (): Promise<AppInfo> => call("GetAppInfo");
export const getSettings = () => call("GetSettings");
export const getSettingsSchema = () => call("GetSettingsSchema");
export const validateSettings = (...args: Parameters<Service["ValidateSettings"]>) => call("ValidateSettings", ...args);
export const saveSettings = (...args: Parameters<Service["SaveSettings"]>) => call("SaveSettings", ...args);
export const getLogs = () => call("GetLogs").then(list);
export const getLogDetails = (id: number) => call("GetLogDetails", id);
export const getAgentRuntimeStatus = (): Promise<AgentRuntimeStatusDTO> => call("GetAgentRuntimeStatus");
export const startAgent = () => call("StartAgent");
export const stopAgent = () => call("StopAgent");
export const applyAgentSettings = (...args: Parameters<Service["ApplyAgentSettings"]>) => call("ApplyAgentSettings", ...args);
export const getWhatsAppSessionStatus = (): Promise<WhatsAppSessionStatusDTO> => call("GetWhatsAppSessionStatus");
export const beginWhatsAppPairing = (...args: Parameters<Service["BeginWhatsAppPairing"]>): Promise<WhatsAppSessionOperationDTO> => call("BeginWhatsAppPairing", ...args);
export const resumeWhatsAppSession = () => call("ResumeWhatsAppSession");
export const stopWhatsAppSession = () => call("StopWhatsAppSession");
export const cancelWhatsAppPairing = (operationID: string) => call("CancelWhatsAppPairing", operationID);
export const reconnectWhatsAppSession = () => call("ReconnectWhatsAppSession");
export const logoutWhatsAppSession = () => call("LogoutWhatsAppSession");
export const getWhatsAppConversations = () => call("GetWhatsAppConversations").then(list);
export const getWhatsAppUsage = (periodDays = 7) => call("GetWhatsAppUsage", periodDays);
export const getWhatsAppMessages = (chatID: string) => call("GetWhatsAppMessages", chatID).then(list);
export const getWhatsAppGroupMembers = (chatID: string) => call("GetWhatsAppGroupMembers", chatID);
export const getWhatsAppBroadcastGroups = () => call("GetWhatsAppBroadcastGroups").then(list);
export const normalizeWhatsAppBroadcastPayload = (payload: string) => call("NormalizeWhatsAppBroadcastPayload", payload);
export const getWhatsAppBroadcastSchedules = () => call("GetWhatsAppBroadcastSchedules").then(list);
export const scheduleWhatsAppBroadcast = (...args: Parameters<Service["ScheduleWhatsAppBroadcast"]>) => call("ScheduleWhatsAppBroadcast", ...args);
export const cancelWhatsAppBroadcastSchedule = (id: string) => call("CancelWhatsAppBroadcastSchedule", id);
export const sendWhatsAppBroadcast = (...args: Parameters<Service["SendWhatsAppBroadcast"]>) => call("SendWhatsAppBroadcast", ...args).then(list);
export const getWhatsAppChatSettings = (chatID: string) => call("GetWhatsAppChatSettings", chatID);
export const saveWhatsAppChatSettings = (...args: Parameters<Service["SaveWhatsAppChatSettings"]>) => call("SaveWhatsAppChatSettings", ...args);
export const resetWhatsAppChatSettings = (...args: Parameters<Service["ResetWhatsAppChatSettings"]>) => call("ResetWhatsAppChatSettings", ...args);
export const getWhatsAppChatTasks = (chatID: string) => call("GetWhatsAppChatTasks", chatID).then(list);
export const addWhatsAppChatTask = (...args: Parameters<Service["AddWhatsAppChatTask"]>) => call("AddWhatsAppChatTask", ...args);
export const deleteWhatsAppChatTask = (chatID: string, taskID: string, daily: boolean) => call("DeleteWhatsAppChatTask", chatID, taskID, daily);
export const sendWhatsAppMessage =(chatID: string, text: string, replyToMessageID = "") => call("SendWhatsAppMessage", chatID, text, replyToMessageID);
export const deleteWhatsAppMessage = (chatID: string, messageID: string) => call("DeleteWhatsAppMessage", chatID, messageID);
export const kickWhatsAppGroupMember = (chatID: string, memberID: string) => call("KickWhatsAppGroupMember", chatID, memberID);
