import { Events } from "@wailsio/runtime";
import { ApplyAgentSettings, BeginWhatsAppPairing, CancelWhatsAppBroadcastSchedule, CancelWhatsAppPairing, DeleteWhatsAppMessage, GetAgentRuntimeStatus, GetAppInfo, GetLogs, GetSettings, GetSettingsSchema, GetWhatsAppBroadcastGroups, GetWhatsAppBroadcastSchedules, GetWhatsAppChatSettings, GetWhatsAppConversations, GetWhatsAppGroupMembers, GetWhatsAppMessages, GetWhatsAppSessionStatus, GetWhatsAppUsage, KickWhatsAppGroupMember, LogoutWhatsAppSession, NormalizeWhatsAppBroadcastPayload, Ping, ReconnectWhatsAppSession, ResetWhatsAppChatSettings, ResumeWhatsAppSession, SaveSettings, SaveWhatsAppChatSettings, ScheduleWhatsAppBroadcast, SendWhatsAppBroadcast, SendWhatsAppMessage, StartAgent, StopAgent, StopWhatsAppSession, ValidateSettings } from "../../bindings/github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/wails/appservice";
import type { AgentRuntimeStatusDTO, ApplyAgentSettingsRequestDTO, AppInfo, BeginWhatsAppPairingRequestDTO, FieldDescriptorDTO, LogEntryDTO, PingEvent, ResetWhatsAppChatSettingsRequestDTO, ResetWhatsAppChatSettingsResultDTO, SaveSettingsRequestDTO, SaveSettingsResultDTO, SaveWhatsAppChatSettingsRequestDTO, ScheduleWhatsAppBroadcastRequestDTO, SendWhatsAppBroadcastRequestDTO, SettingsPatchDTO, SettingsValuesDTO, SettingsViewDTO, ValidationResultDTO, WhatsAppBroadcastGroupDTO, WhatsAppBroadcastGroupResultDTO, WhatsAppBroadcastScheduleDTO, WhatsAppChatSettingsDTO, WhatsAppConversationDTO, WhatsAppGroupMemberDTO, WhatsAppGroupMembersDTO, WhatsAppMentionDTO, WhatsAppMessageDTO, WhatsAppQuoteDTO, WhatsAppSessionEventDTO, WhatsAppSessionOperationDTO, WhatsAppSessionStatusDTO, WhatsAppUsageDTO, WhatsAppDailyUsageDTO, WhatsAppGroupUsageDTO } from "../../bindings/github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/wails/models";
export type { AgentRuntimeStatusDTO, ApplyAgentSettingsRequestDTO, AppInfo, BeginWhatsAppPairingRequestDTO, FieldDescriptorDTO, LogEntryDTO, PingEvent, ResetWhatsAppChatSettingsRequestDTO, ResetWhatsAppChatSettingsResultDTO, SaveSettingsRequestDTO, SaveSettingsResultDTO, SaveWhatsAppChatSettingsRequestDTO, ScheduleWhatsAppBroadcastRequestDTO, SendWhatsAppBroadcastRequestDTO, SettingsPatchDTO, SettingsValuesDTO, SettingsViewDTO, ValidationResultDTO, WhatsAppBroadcastGroupDTO, WhatsAppBroadcastGroupResultDTO, WhatsAppBroadcastScheduleDTO, WhatsAppChatSettingsDTO, WhatsAppConversationDTO, WhatsAppGroupMemberDTO, WhatsAppGroupMembersDTO, WhatsAppMentionDTO, WhatsAppMessageDTO, WhatsAppQuoteDTO, WhatsAppSessionEventDTO, WhatsAppSessionOperationDTO, WhatsAppSessionStatusDTO, WhatsAppUsageDTO, WhatsAppDailyUsageDTO, WhatsAppGroupUsageDTO };

type EventEnvelope = { data?: unknown };
export type EventSource = {
  On: (name: string, callback: (event: EventEnvelope) => void) => () => void;
};

const webMode = import.meta.env.MODE === "web";
const pingListeners = new Set<(event: PingEvent) => void>();

async function webCall<T>(method: string, args: unknown[] = []): Promise<T> {
  const response = await fetch("/api/call", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ method, args }),
  });
  const payload = await response.json().catch(() => ({})) as { result?: T; error?: string };
  if (!response.ok) throw new Error(payload.error || `Request failed (${response.status}).`);
  return payload.result as T;
}

function call<T>(method: string, native: () => Promise<T>, ...args: unknown[]): Promise<T> {
  return webMode ? webCall<T>(method, args) : native();
}

export const getAppInfo = (): Promise<AppInfo> => call("GetAppInfo", GetAppInfo);
export const ping = async (): Promise<void> => {
  if (!webMode) return Ping();
  const event = await webCall<PingEvent>("Ping");
  pingListeners.forEach((listener) => listener(event));
};
export const getSettings = (): Promise<SettingsViewDTO> => call("GetSettings", GetSettings);
export const getSettingsSchema = (): Promise<FieldDescriptorDTO[] | null> => call("GetSettingsSchema", GetSettingsSchema);
export const validateSettings = (request: SettingsPatchDTO): Promise<ValidationResultDTO> => call("ValidateSettings", () => ValidateSettings(request), request);
export const saveSettings = (request: SaveSettingsRequestDTO): Promise<SaveSettingsResultDTO> => call("SaveSettings", () => SaveSettings(request), request);
export const getAgentRuntimeStatus = (): Promise<AgentRuntimeStatusDTO> => call("GetAgentRuntimeStatus", GetAgentRuntimeStatus);
export const getLogs = (): Promise<LogEntryDTO[]> => call<LogEntryDTO[] | null>("GetLogs", GetLogs).then((entries) => entries ?? []);
export const startAgent = (): Promise<AgentRuntimeStatusDTO> => call("StartAgent", StartAgent);
export const stopAgent = (): Promise<AgentRuntimeStatusDTO> => call("StopAgent", StopAgent);
export const applyAgentSettings = (request: ApplyAgentSettingsRequestDTO): Promise<AgentRuntimeStatusDTO> => call("ApplyAgentSettings", () => ApplyAgentSettings(request), request);
export const getWhatsAppSessionStatus = (): Promise<WhatsAppSessionStatusDTO> => call("GetWhatsAppSessionStatus", GetWhatsAppSessionStatus);
export const beginWhatsAppPairing = (request: BeginWhatsAppPairingRequestDTO): Promise<WhatsAppSessionOperationDTO> => call("BeginWhatsAppPairing", () => BeginWhatsAppPairing(request), request);
export const resumeWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => call("ResumeWhatsAppSession", ResumeWhatsAppSession);
export const stopWhatsAppSession = (): Promise<WhatsAppSessionStatusDTO> => call("StopWhatsAppSession", StopWhatsAppSession);
export const cancelWhatsAppPairing = (operationID: string): Promise<WhatsAppSessionStatusDTO> => call("CancelWhatsAppPairing", () => CancelWhatsAppPairing(operationID), operationID);
export const reconnectWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => call("ReconnectWhatsAppSession", ReconnectWhatsAppSession);
export const logoutWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => call("LogoutWhatsAppSession", LogoutWhatsAppSession);
export const getWhatsAppConversations = (): Promise<WhatsAppConversationDTO[]> => call<WhatsAppConversationDTO[] | null>("GetWhatsAppConversations", GetWhatsAppConversations).then((items) => items ?? []);
export const getWhatsAppUsage = (periodDays = 7): Promise<WhatsAppUsageDTO> => call("GetWhatsAppUsage", () => GetWhatsAppUsage(periodDays), periodDays);
export const getWhatsAppMessages = (chatID: string): Promise<WhatsAppMessageDTO[]> => call<WhatsAppMessageDTO[] | null>("GetWhatsAppMessages", () => GetWhatsAppMessages(chatID), chatID).then((items) => items ?? []);
export const getWhatsAppGroupMembers = (chatID: string): Promise<WhatsAppGroupMembersDTO> => call("GetWhatsAppGroupMembers", () => GetWhatsAppGroupMembers(chatID), chatID);
export const getWhatsAppBroadcastGroups = (): Promise<WhatsAppBroadcastGroupDTO[]> => call<WhatsAppBroadcastGroupDTO[] | null>("GetWhatsAppBroadcastGroups", GetWhatsAppBroadcastGroups).then((groups) => groups ?? []);
export const normalizeWhatsAppBroadcastPayload = (payload: string): Promise<string> => call("NormalizeWhatsAppBroadcastPayload", () => NormalizeWhatsAppBroadcastPayload(payload), payload);
export const getWhatsAppBroadcastSchedules = (): Promise<WhatsAppBroadcastScheduleDTO[]> => call<WhatsAppBroadcastScheduleDTO[] | null>("GetWhatsAppBroadcastSchedules", GetWhatsAppBroadcastSchedules).then((items) => items ?? []);
export const scheduleWhatsAppBroadcast = (request: ScheduleWhatsAppBroadcastRequestDTO): Promise<WhatsAppBroadcastScheduleDTO> => call("ScheduleWhatsAppBroadcast", () => ScheduleWhatsAppBroadcast(request), request);
export const cancelWhatsAppBroadcastSchedule = (id: string): Promise<void> => call("CancelWhatsAppBroadcastSchedule", () => CancelWhatsAppBroadcastSchedule(id), id);
export const sendWhatsAppBroadcast = (request: SendWhatsAppBroadcastRequestDTO): Promise<WhatsAppBroadcastGroupResultDTO[]> => call<WhatsAppBroadcastGroupResultDTO[] | null>("SendWhatsAppBroadcast", () => SendWhatsAppBroadcast(request), request).then((results) => results ?? []);
export const getWhatsAppChatSettings = (chatID: string): Promise<WhatsAppChatSettingsDTO> => call("GetWhatsAppChatSettings", () => GetWhatsAppChatSettings(chatID), chatID);
export const saveWhatsAppChatSettings = (request: SaveWhatsAppChatSettingsRequestDTO): Promise<WhatsAppChatSettingsDTO> => call("SaveWhatsAppChatSettings", () => SaveWhatsAppChatSettings(request), request);
export const resetWhatsAppChatSettings = (request: ResetWhatsAppChatSettingsRequestDTO): Promise<ResetWhatsAppChatSettingsResultDTO> => call("ResetWhatsAppChatSettings", () => ResetWhatsAppChatSettings(request), request);
export const sendWhatsAppMessage = (chatID: string, text: string, replyToMessageID = ""): Promise<WhatsAppMessageDTO> => call("SendWhatsAppMessage", () => SendWhatsAppMessage(chatID, text, replyToMessageID), chatID, text, replyToMessageID);
export const deleteWhatsAppMessage = (chatID: string, messageID: string): Promise<void> => call("DeleteWhatsAppMessage", () => DeleteWhatsAppMessage(chatID, messageID), chatID, messageID);
export const kickWhatsAppGroupMember = (chatID: string, memberID: string): Promise<void> => call("KickWhatsAppGroupMember", () => KickWhatsAppGroupMember(chatID, memberID), chatID, memberID);

export function normalisePingEvent(value: unknown): PingEvent | null {
  const data = (value as EventEnvelope | null)?.data;
  if (!data || typeof data !== "object") return null;
  const candidate = data as Record<string, unknown>;
  if (typeof candidate.message !== "string" || typeof candidate.sequence !== "number") return null;
  return { message: candidate.message, sequence: candidate.sequence };
}

export function subscribeToPing(source: EventSource, onPing: (event: PingEvent) => void): () => void {
  return source.On("app:ping", (event) => {
    const parsed = normalisePingEvent(event);
    if (parsed) onPing(parsed);
  });
}

export const subscribeToBackendPing = (onPing: (event: PingEvent) => void): (() => void) =>
  webMode ? (pingListeners.add(onPing), () => { pingListeners.delete(onPing); }) : subscribeToPing(Events, onPing);

export function normaliseWhatsAppSessionEvent(value: unknown): WhatsAppSessionEventDTO | null {
  const data = (value as EventEnvelope | null)?.data;
  if (!data || typeof data !== "object") return null;
  const candidate = data as Record<string, unknown>;
  const status = candidate.status;
  if (typeof candidate.operationID !== "string" || !status || typeof status !== "object") return null;
  const valueStatus = status as Record<string, unknown>;
  if (typeof valueStatus.bindingState !== "string" || typeof valueStatus.runtimeState !== "string" || typeof valueStatus.sessionPresent !== "boolean") return null;
  return { operationID: candidate.operationID, status: valueStatus as unknown as WhatsAppSessionStatusDTO };
}

export function subscribeToWhatsAppSession(source: EventSource, onEvent: (event: WhatsAppSessionEventDTO) => void): () => void {
  return source.On("whatsapp:session", (event) => {
    const parsed = normaliseWhatsAppSessionEvent(event);
    if (parsed) onEvent(parsed);
  });
}

export const subscribeToBackendWhatsAppSession = (onEvent: (event: WhatsAppSessionEventDTO) => void): (() => void) =>
  webMode ? () => {} : subscribeToWhatsAppSession(Events, onEvent);
