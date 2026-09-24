import { Events } from "@wailsio/runtime";
import { ApplyAgentSettings, BeginWhatsAppPairing, CancelWhatsAppPairing, DeleteWhatsAppMessage, GetAgentRuntimeStatus, GetAppInfo, GetLogs, GetSettings, GetSettingsSchema, GetWhatsAppChatSettings, GetWhatsAppConversations, GetWhatsAppGroupMembers, GetWhatsAppMessages, GetWhatsAppSessionStatus, KickWhatsAppGroupMember, LogoutWhatsAppSession, Ping, ReconnectWhatsAppSession, ResumeWhatsAppSession, SaveSettings, SaveWhatsAppChatSettings, SendWhatsAppMessage, StartAgent, StopAgent, StopWhatsAppSession, ValidateSettings } from "../../bindings/github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/wails/appservice";
import type { AgentRuntimeStatusDTO, ApplyAgentSettingsRequestDTO, AppInfo, BeginWhatsAppPairingRequestDTO, FieldDescriptorDTO, LogEntryDTO, PingEvent, SaveSettingsRequestDTO, SaveSettingsResultDTO, SaveWhatsAppChatSettingsRequestDTO, SettingsPatchDTO, SettingsValuesDTO, SettingsViewDTO, ValidationResultDTO, WhatsAppChatSettingsDTO, WhatsAppConversationDTO, WhatsAppGroupMemberDTO, WhatsAppGroupMembersDTO, WhatsAppMentionDTO, WhatsAppMessageDTO, WhatsAppQuoteDTO, WhatsAppSessionEventDTO, WhatsAppSessionOperationDTO, WhatsAppSessionStatusDTO } from "../../bindings/github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/wails/models";
export type { AgentRuntimeStatusDTO, ApplyAgentSettingsRequestDTO, AppInfo, BeginWhatsAppPairingRequestDTO, FieldDescriptorDTO, LogEntryDTO, PingEvent, SaveSettingsRequestDTO, SaveSettingsResultDTO, SaveWhatsAppChatSettingsRequestDTO, SettingsPatchDTO, SettingsValuesDTO, SettingsViewDTO, ValidationResultDTO, WhatsAppChatSettingsDTO, WhatsAppConversationDTO, WhatsAppGroupMemberDTO, WhatsAppGroupMembersDTO, WhatsAppMentionDTO, WhatsAppMessageDTO, WhatsAppQuoteDTO, WhatsAppSessionEventDTO, WhatsAppSessionOperationDTO, WhatsAppSessionStatusDTO };

type EventEnvelope = { data?: unknown };
export type EventSource = {
  On: (name: string, callback: (event: EventEnvelope) => void) => () => void;
};

export const getAppInfo = (): Promise<AppInfo> => GetAppInfo();
export const ping = (): Promise<void> => Ping();
export const getSettings = (): Promise<SettingsViewDTO> => GetSettings();
export const getSettingsSchema = (): Promise<FieldDescriptorDTO[] | null> => GetSettingsSchema();
export const validateSettings = (request: SettingsPatchDTO): Promise<ValidationResultDTO> => ValidateSettings(request);
export const saveSettings = (request: SaveSettingsRequestDTO): Promise<SaveSettingsResultDTO> => SaveSettings(request);
export const getAgentRuntimeStatus = (): Promise<AgentRuntimeStatusDTO> => GetAgentRuntimeStatus();
export const getLogs = (): Promise<LogEntryDTO[]> => GetLogs().then((entries) => entries ?? []);
export const startAgent = (): Promise<AgentRuntimeStatusDTO> => StartAgent();
export const stopAgent = (): Promise<AgentRuntimeStatusDTO> => StopAgent();
export const applyAgentSettings = (request: ApplyAgentSettingsRequestDTO): Promise<AgentRuntimeStatusDTO> => ApplyAgentSettings(request);
export const getWhatsAppSessionStatus = (): Promise<WhatsAppSessionStatusDTO> => GetWhatsAppSessionStatus();
export const beginWhatsAppPairing = (request: BeginWhatsAppPairingRequestDTO): Promise<WhatsAppSessionOperationDTO> => BeginWhatsAppPairing(request);
export const resumeWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => ResumeWhatsAppSession();
export const stopWhatsAppSession = (): Promise<WhatsAppSessionStatusDTO> => StopWhatsAppSession();
export const cancelWhatsAppPairing = (operationID: string): Promise<WhatsAppSessionStatusDTO> => CancelWhatsAppPairing(operationID);
export const reconnectWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => ReconnectWhatsAppSession();
export const logoutWhatsAppSession = (): Promise<WhatsAppSessionOperationDTO> => LogoutWhatsAppSession();
export const getWhatsAppConversations = (): Promise<WhatsAppConversationDTO[]> => GetWhatsAppConversations().then((items) => items ?? []);
export const getWhatsAppMessages = (chatID: string): Promise<WhatsAppMessageDTO[]> => GetWhatsAppMessages(chatID).then((items) => items ?? []);
export const getWhatsAppGroupMembers = (chatID: string): Promise<WhatsAppGroupMembersDTO> => GetWhatsAppGroupMembers(chatID);
export const getWhatsAppChatSettings = (chatID: string): Promise<WhatsAppChatSettingsDTO> => GetWhatsAppChatSettings(chatID);
export const saveWhatsAppChatSettings = (request: SaveWhatsAppChatSettingsRequestDTO): Promise<WhatsAppChatSettingsDTO> => SaveWhatsAppChatSettings(request);
export const sendWhatsAppMessage = (chatID: string, text: string, replyToMessageID = ""): Promise<WhatsAppMessageDTO> => SendWhatsAppMessage(chatID, text, replyToMessageID);
export const deleteWhatsAppMessage = (chatID: string, messageID: string): Promise<void> => DeleteWhatsAppMessage(chatID, messageID);
export const kickWhatsAppGroupMember = (chatID: string, memberID: string): Promise<void> => KickWhatsAppGroupMember(chatID, memberID);

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
  subscribeToPing(Events, onPing);

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
  subscribeToWhatsAppSession(Events, onEvent);
