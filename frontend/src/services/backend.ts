import { AppService } from "../../bindings/github.com/Chomosuke9/DiscordAgent-Go/internal/ui";
import type { AgentRuntimeStatusDTO, AppInfo, DiscordSessionOperationDTO, DiscordSessionStatusDTO } from "../../bindings/github.com/Chomosuke9/DiscordAgent-Go/internal/ui/models";
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
export const getDiscordSessionStatus = (): Promise<DiscordSessionStatusDTO> => call("GetDiscordSessionStatus");
export const beginDiscordLink = (...args: Parameters<Service["BeginDiscordLink"]>): Promise<DiscordSessionOperationDTO> => call("BeginDiscordLink", ...args);
export const resumeDiscordSession = () => call("ResumeDiscordSession");
export const stopDiscordSession = () => call("StopDiscordSession");
export const cancelDiscordLink = (operationID: string) => call("CancelDiscordLink", operationID);
export const reconnectDiscordSession = () => call("ReconnectDiscordSession");
export const unlinkDiscordBot = () => call("UnlinkDiscordBot");
export const getDiscordConversations = () => call("GetDiscordConversations").then(list);
export const getDiscordUsage = (periodDays = 7) => call("GetDiscordUsage", periodDays);
export const getDiscordMessages = (chatID: string) => call("GetDiscordMessages", chatID).then(list);
export const getDiscordGroupMembers = (chatID: string) => call("GetDiscordGroupMembers", chatID);
export const getDiscordBroadcastGroups = () => call("GetDiscordBroadcastGroups").then(list);
export const normalizeDiscordBroadcastPayload = (payload: string) => call("NormalizeDiscordBroadcastPayload", payload);
export const getDiscordBroadcastSchedules = () => call("GetDiscordBroadcastSchedules").then(list);
export const scheduleDiscordBroadcast = (...args: Parameters<Service["ScheduleDiscordBroadcast"]>) => call("ScheduleDiscordBroadcast", ...args);
export const cancelDiscordBroadcastSchedule = (id: string) => call("CancelDiscordBroadcastSchedule", id);
export const sendDiscordBroadcast = (...args: Parameters<Service["SendDiscordBroadcast"]>) => call("SendDiscordBroadcast", ...args).then(list);
export const getDiscordChatSettings = (chatID: string) => call("GetDiscordChatSettings", chatID);
export const saveDiscordChatSettings = (...args: Parameters<Service["SaveDiscordChatSettings"]>) => call("SaveDiscordChatSettings", ...args);
export const resetDiscordChatSettings = (...args: Parameters<Service["ResetDiscordChatSettings"]>) => call("ResetDiscordChatSettings", ...args);
export const getDiscordChatTasks = (chatID: string) => call("GetDiscordChatTasks", chatID).then(list);
export const addDiscordChatTask = (...args: Parameters<Service["AddDiscordChatTask"]>) => call("AddDiscordChatTask", ...args);
export const deleteDiscordChatTask = (chatID: string, taskID: string, daily: boolean) => call("DeleteDiscordChatTask", chatID, taskID, daily);
export const sendDiscordMessage =(chatID: string, text: string, replyToMessageID = "") => call("SendDiscordMessage", chatID, text, replyToMessageID);
export const deleteDiscordMessage = (chatID: string, messageID: string) => call("DeleteDiscordMessage", chatID, messageID);
export const kickDiscordGroupMember = (chatID: string, memberID: string) => call("KickDiscordGroupMember", chatID, memberID);
