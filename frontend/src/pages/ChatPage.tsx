import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  deleteWhatsAppMessage,
  getWhatsAppChatSettings,
  getWhatsAppConversations,
  getWhatsAppGroupMembers,
  getWhatsAppMessages,
  kickWhatsAppGroupMember,
  saveWhatsAppChatSettings,
  sendWhatsAppMessage,
  type WhatsAppChatSettingsDTO,
  type WhatsAppConversationDTO,
  type WhatsAppGroupMemberDTO,
  type WhatsAppMessageDTO,
} from "../services/backend";

function conversationKind(kind: string): string {
  if (kind === "group") return "Group";
  if (kind === "status") return "Status";
  return "Direct";
}

function messageTime(value: string, includeDate = false): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString("en-US", includeDate
    ? { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }
    : { hour: "2-digit", minute: "2-digit" });
}

function deliveryLabel(delivery: string): string {
  switch (delivery) {
    case "sent": return "Sent";
    case "pending": return "Pending";
    case "failed": return "Failed to send";
    case "unknown": return "Unknown status";
    default: return "";
  }
}

function actionErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  return fallback;
}

export function ChatPage() {
  const [conversations, setConversations] = useState<WhatsAppConversationDTO[]>([]);
  const [selectedChatID, setSelectedChatID] = useState("");
  const [messages, setMessages] = useState<WhatsAppMessageDTO[]>([]);
  const [loadingConversations, setLoadingConversations] = useState(true);
  const [loadingMessages, setLoadingMessages] = useState(false);
  const [conversationError, setConversationError] = useState("");
  const [messagesError, setMessagesError] = useState("");
  const [actionError, setActionError] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [members, setMembers] = useState<WhatsAppGroupMemberDTO[]>([]);
  const [botIsGroupAdmin, setBotIsGroupAdmin] = useState(false);
  const [groupAdminChecked, setGroupAdminChecked] = useState(false);
  const [loadingMembers, setLoadingMembers] = useState(false);
  const [membersError, setMembersError] = useState("");
  const [memberRefresh, setMemberRefresh] = useState(0);
  const [busyMember, setBusyMember] = useState("");
  const [busyMessage, setBusyMessage] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [chatSettings, setChatSettings] = useState<WhatsAppChatSettingsDTO | null>(null);
  const [moderationLevel, setModerationLevel] = useState(0);
  const [promptOverrideMode, setPromptOverrideMode] = useState("append");
  const [promptOverrideText, setPromptOverrideText] = useState("");
  const [triggerMention, setTriggerMention] = useState(true);
  const [triggerName, setTriggerName] = useState(false);
  const [triggerReply, setTriggerReply] = useState(true);
  const [triggerNameRegex, setTriggerNameRegex] = useState(false);
  const [triggerNamePattern, setTriggerNamePattern] = useState("");
  const [loadingSettings, setLoadingSettings] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const [settingsSaved, setSettingsSaved] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const stickToLatest = useRef(true);
  const forceLatestOnLoad = useRef(true);
  const membersChatID = useRef("");
  const selectedConversation = conversations.find((item) => item.id === selectedChatID) ?? null;

  useEffect(() => {
    let mounted = true;
    let requestInFlight = false;
    const refresh = async () => {
      if (requestInFlight) return;
      requestInFlight = true;
      try {
        const result = await getWhatsAppConversations();
        if (!mounted) return;
        setConversations(result);
        setSelectedChatID((current) => current && result.some((item) => item.id === current)
          ? current
          : result[0]?.id ?? "");
        setConversationError("");
      } catch {
        if (mounted) setConversationError("The conversation list is temporarily unavailable. The app will retry automatically.");
      } finally {
        requestInFlight = false;
        if (mounted) setLoadingConversations(false);
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => { mounted = false; window.clearInterval(timer); };
  }, []);

  useEffect(() => {
    let mounted = true;
    if (!selectedChatID) {
      setMessages([]);
      setLoadingMessages(false);
      setMessagesError("");
      return () => { mounted = false; };
    }
    setMessages([]);
    setLoadingMessages(true);
    setMessagesError("");
    setDraft("");
    setActionError("");
    stickToLatest.current = true;
    forceLatestOnLoad.current = true;
    let requestInFlight = false;
    const refresh = async () => {
      if (requestInFlight) return;
      requestInFlight = true;
      try {
        const result = await getWhatsAppMessages(selectedChatID);
        if (mounted) {
          setMessages(result);
          setMessagesError("");
        }
      } catch {
        if (mounted) setMessagesError("Messages in this conversation are temporarily unavailable. The app will retry automatically.");
      } finally {
        requestInFlight = false;
        if (mounted) setLoadingMessages(false);
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => { mounted = false; window.clearInterval(timer); };
  }, [selectedChatID]);

  useEffect(() => {
    const transcript = transcriptRef.current;
    if (!transcript || (!forceLatestOnLoad.current && !stickToLatest.current)) return;
    transcript.scrollTop = transcript.scrollHeight;
    forceLatestOnLoad.current = false;
  }, [messages, loadingMessages]);

  useEffect(() => {
    let mounted = true;
    if (membersChatID.current !== selectedChatID) {
      membersChatID.current = selectedChatID;
      setMembers([]);
      setBotIsGroupAdmin(false);
      setGroupAdminChecked(false);
      setMembersError("");
    }
    if (!selectedChatID || selectedConversation?.kind !== "group") {
      setMembers([]);
      setBotIsGroupAdmin(false);
      setGroupAdminChecked(false);
      setMembersError("");
      setLoadingMembers(false);
      return () => { mounted = false; };
    }
    setBotIsGroupAdmin(false);
    setLoadingMembers(true);
    setMembersError("");
    void getWhatsAppGroupMembers(selectedChatID)
      .then((result) => {
        if (!mounted) return;
        setMembers(result.members ?? []);
        setBotIsGroupAdmin(result.botIsAdmin);
        setGroupAdminChecked(true);
      })
      .catch((error) => {
        if (!mounted) return;
        setBotIsGroupAdmin(false);
        setGroupAdminChecked(true);
        setMembersError(actionErrorMessage(error, "Could not load group members. Make sure the Agent is running, then refresh."));
      })
      .finally(() => { if (mounted) setLoadingMembers(false); });
    return () => { mounted = false; };
  }, [selectedChatID, selectedConversation?.kind, memberRefresh]);

  useEffect(() => {
    let mounted = true;
    if (!settingsOpen || !selectedChatID) {
      setChatSettings(null);
      setSettingsError("");
      setSettingsSaved(false);
      setLoadingSettings(false);
      return () => { mounted = false; };
    }
    setLoadingSettings(true);
    setSettingsError("");
    setSettingsSaved(false);
    void getWhatsAppChatSettings(selectedChatID)
      .then((settings) => {
        if (!mounted) return;
        setChatSettings(settings);
        setModerationLevel(settings.moderationLevel);
        setPromptOverrideMode(settings.promptOverrideMode || "append");
        setPromptOverrideText(settings.promptOverrideText);
        setTriggerMention(settings.triggerMention);
        setTriggerName(settings.triggerName);
        setTriggerReply(settings.triggerReply);
        setTriggerNameRegex(settings.triggerNameRegex);
        setTriggerNamePattern(settings.triggerNamePattern);
      })
      .catch((error) => {
        if (mounted) setSettingsError(actionErrorMessage(error, "Could not load chat settings."));
      })
      .finally(() => { if (mounted) setLoadingSettings(false); });
    return () => { mounted = false; };
  }, [settingsOpen, selectedChatID]);

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedConversation || !draft.trim() || sending) return;
    setSending(true);
    setActionError("");
    try {
      const message = await sendWhatsAppMessage(selectedChatID, draft);
      stickToLatest.current = true;
      forceLatestOnLoad.current = true;
      setMessages((current) => current.some((item) => item.id === message.id)
        ? current
        : [...current, message].slice(-100));
      setDraft("");
    } catch (error) {
      setActionError(actionErrorMessage(error, "Could not send the message. Check the Agent and WhatsApp status."));
    } finally {
      setSending(false);
    }
  }

  async function deleteMessage(message: WhatsAppMessageDTO) {
    setBusyMessage(message.id);
    setActionError("");
    try {
      await deleteWhatsAppMessage(selectedChatID, message.id);
      setMessages((current) => current.map((item) => item.id === message.id
        ? { ...item, deleted: true, content: "This message was deleted on WhatsApp." }
        : item));
    } catch (error) {
      setActionError(actionErrorMessage(error, "Could not delete the message. Make sure it is still available and the Agent is running."));
    } finally {
      setBusyMessage("");
    }
  }

  async function kickMember(member: WhatsAppGroupMemberDTO) {
    if (!window.confirm(`Remove ${member.name} from this WhatsApp group?`)) return;
    setBusyMember(member.id);
    setActionError("");
    try {
      await kickWhatsAppGroupMember(selectedChatID, member.id);
      setMembers((current) => current.filter((item) => item.id !== member.id));
    } catch (error) {
      setMembersError(actionErrorMessage(error, "Could not remove the member. Check the connection and the WhatsApp account's admin permissions."));
    } finally {
      setBusyMember("");
    }
  }

  async function saveChatSettings(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!chatSettings || savingSettings) return;
    setSavingSettings(true);
    setSettingsError("");
    setSettingsSaved(false);
    try {
      const updated = await saveWhatsAppChatSettings({
        chatID: selectedChatID,
        expectedVersion: chatSettings.version,
        moderationLevel,
        promptOverrideMode: promptOverrideText.trim() ? promptOverrideMode : "",
        promptOverrideText,
        triggerMention,
        triggerName,
        triggerReply,
        triggerNameRegex,
        triggerNamePattern,
      });
      setChatSettings(updated);
      setSettingsSaved(true);
    } catch (error) {
      setSettingsError(actionErrorMessage(error, "Could not save chat settings."));
    } finally {
      setSavingSettings(false);
    }
  }

  const canSend = selectedConversation?.kind === "group" || selectedConversation?.kind === "direct";

  return <div className="page chat-page">

    {(conversationError || messagesError || actionError) && <p className="error-text inbox-error" role="status">{actionError || conversationError || messagesError}</p>}

    <div className="bot-inbox card">
      {loadingConversations && conversations.length === 0 ? <div className="inbox-empty"><span className="inbox-spinner" />Loading conversations…</div>
        : conversations.length === 0 ? <div className="inbox-empty">
          <span className="inbox-empty-icon" aria-hidden="true">◌</span>
          <strong>{conversationError ? "Could not load conversations" : "No saved messages yet"}</strong>
          <p>{conversationError ? "The app will retry automatically." : "Messages processed by the Agent will appear here. This history only shows messages saved by the app."}</p>
        </div> : <>
          <nav className="bot-chat-list" aria-label="Conversation list">
            {conversations.map((conversation) => <button
              key={conversation.id}
              className={conversation.id === selectedChatID ? "bot-chat-item selected" : "bot-chat-item"}
              onClick={() => setSelectedChatID(conversation.id)}
              aria-current={conversation.id === selectedChatID ? "true" : undefined}
            >
              <span className="bot-chat-avatar" aria-hidden="true">{conversation.name.trim().slice(0, 1).toUpperCase() || "?"}</span>
              <span className="bot-chat-summary">
                <span className="bot-chat-title"><strong>{conversation.name}</strong><time>{messageTime(conversation.lastMessageAt, true)}</time></span>
              <span className="bot-chat-preview"><span>{conversation.lastFromBot ? "Bot: " : ""}{conversation.lastMessage}</span><small>{conversationKind(conversation.kind)}</small></span>
              </span>
            </button>)}
          </nav>

          <section className="bot-transcript" aria-label={selectedConversation ? `Messages with ${selectedConversation.name}` : "Conversation messages"}>
            {selectedConversation ? <header className="transcript-header">
              <span className="bot-chat-avatar" aria-hidden="true">{selectedConversation.name.trim().slice(0, 1).toUpperCase() || "?"}</span>
              <span className="transcript-title"><strong>{selectedConversation.name}</strong><small>{conversationKind(selectedConversation.kind)} · {selectedConversation.messageCount} saved messages</small></span>
              <button type="button" className="chat-settings-trigger" aria-label="Open chat settings" aria-expanded={settingsOpen}
                onClick={() => setSettingsOpen(true)} title="Chat settings">⚙</button>
            </header> : null}
            <div className="transcript-messages" aria-live="polite" ref={transcriptRef}
              onScroll={(event) => {
                const element = event.currentTarget;
                stickToLatest.current = element.scrollHeight - element.scrollTop - element.clientHeight <= 48;
              }}>
              {loadingMessages && messages.length === 0 ? <div className="inbox-empty">Loading messages…</div>
                : messagesError && messages.length === 0 ? <div className="inbox-empty">Could not load messages. The app will retry automatically.</div>
                  : messages.length === 0 ? <div className="inbox-empty">There are no messages in the Agent history for this conversation.</div>
                    : messages.map((message) => <article key={message.id} className={message.role === "assistant" ? "bot-message from-bot" : "bot-message from-contact"}>
                      <strong className="message-sender">{message.sender}</strong>
                      <p>{message.content}</p>
                      <footer>
                        <time>{messageTime(message.createdAt)}</time>
                        {message.role === "assistant" && message.delivery && <span>{deliveryLabel(message.delivery)}</span>}
                        {!message.deleted && ((message.role === "assistant" && message.delivery === "sent") || (message.role !== "assistant" && selectedConversation?.kind === "group" && groupAdminChecked && botIsGroupAdmin)) && <button
                          type="button" className="message-delete" disabled={busyMessage === message.id}
                          onClick={() => void deleteMessage(message)}
                        >{busyMessage === message.id ? "Deleting…" : "Delete"}</button>}
                      </footer>
                    </article>)}
            </div>

            {settingsOpen && <>
              <button type="button" className="chat-settings-backdrop" aria-label="Close chat settings" onClick={() => setSettingsOpen(false)} />
              <aside className="chat-settings-drawer" aria-label="Chat settings">
                <header className="chat-settings-header">
                  <span><small>CHAT SETTINGS</small><strong>{selectedConversation?.name}</strong></span>
                  <button type="button" className="chat-settings-close" aria-label="Close" onClick={() => setSettingsOpen(false)}>×</button>
                </header>
                {loadingSettings ? <p className="member-hint">Loading settings…</p> : <>
                  {settingsError && <p className="error-text settings-feedback" role="status">{settingsError}</p>}
                  {settingsSaved && <p className="settings-success" role="status">Settings saved.</p>}
                  {chatSettings && <form className="chat-settings-form" onSubmit={(event) => void saveChatSettings(event)}>
                    <section className="chat-settings-section">
                      <h2>Agent triggers</h2>
                      <p>In groups, the Agent responds when one of the enabled triggers matches. Direct chats can still receive replies without a trigger.</p>
                      <div className="trigger-options">
                        <label><input type="checkbox" checked={triggerMention} onChange={(event) => { setTriggerMention(event.target.checked); setSettingsSaved(false); }} /> Mention the bot account</label>
                        <label><input type="checkbox" checked={triggerName} onChange={(event) => { setTriggerName(event.target.checked); setSettingsSaved(false); }} /> Agent name</label>
                        <label><input type="checkbox" checked={triggerReply} onChange={(event) => { setTriggerReply(event.target.checked); setSettingsSaved(false); }} /> Reply to a bot message</label>
                      </div>
                      {triggerName && <>
                        <label className="trigger-regex-toggle"><input type="checkbox" checked={triggerNameRegex} onChange={(event) => { setTriggerNameRegex(event.target.checked); setSettingsSaved(false); }} /> Use a custom regex for the name trigger</label>
                        {triggerNameRegex ? <label className="settings-field">Regex pattern
                          <input type="text" value={triggerNamePattern} maxLength={512} required
                            onChange={(event) => { setTriggerNamePattern(event.target.value); setSettingsSaved(false); }}
                            placeholder="Contoh: (?i)\bvivy\b" />
                          <small>Uses Go regex syntax. Add (?i) to make matching case-insensitive.</small>
                        </label> : <p className="member-hint">The trigger uses the Agent name from the main settings.</p>}
                      </>}
                    </section>
                    <section className="chat-settings-section">
                      <h2>Agent permissions</h2>
                      <p>Choose which moderation actions the Agent can use in this conversation.</p>
                      <label className="settings-field">Moderation level
                        <select value={moderationLevel} onChange={(event) => { setModerationLevel(Number(event.target.value)); setSettingsSaved(false); }}>
                          <option value={0}>Disabled — no moderation</option>
                          <option value={1}>Level 1 — delete messages</option>
                          <option value={2}>Level 2 — delete and mute</option>
                          <option value={3}>Level 3 — delete, mute, and kick</option>
                        </select>
                      </label>
                    </section>
                    <section className="chat-settings-section">
                      <h2>Custom instructions</h2>
                      <p>Add instructions for the Agent in this conversation. Leave blank to use only the main instructions.</p>
                      <label className="settings-field">Apply mode
                        <select value={promptOverrideMode} onChange={(event) => { setPromptOverrideMode(event.target.value); setSettingsSaved(false); }}>
                          <option value="append">Append to main instructions</option>
                          <option value="replace">Replace main instructions for this conversation</option>
                        </select>
                      </label>
                      <label className="settings-field">Instructions
                        <textarea value={promptOverrideText} maxLength={16000} rows={5}
                          onChange={(event) => { setPromptOverrideText(event.target.value); setSettingsSaved(false); }}
                          placeholder="Example: Keep replies concise and use English." />
                      </label>
                    </section>
                    {selectedConversation?.kind === "group" && <section className="chat-settings-section group-settings-section">
                      <header><span><h2>Group members</h2><p>{members.length} members{botIsGroupAdmin ? " · bot account is an admin" : " · bot account is not an admin"}</p></span>
                        <button type="button" className="secondary-button" disabled={loadingMembers} onClick={() => setMemberRefresh((value) => value + 1)}>
                          {loadingMembers ? "Loading…" : "Refresh"}
                        </button>
                      </header>
                      {membersError && <p className="error-text">{membersError}</p>}
                      {loadingMembers && members.length === 0 ? <p className="member-hint">Loading WhatsApp group members…</p>
                        : members.length === 0 ? <p className="member-hint">No members to display.</p>
                          : <ul>{members.map((member) => <li key={member.id}>
                            <span className="member-avatar" aria-hidden="true">{member.name.trim().slice(0, 1).toUpperCase() || "?"}</span>
                            <span className="member-name">{member.name}<small>{member.isSuperAdmin ? "Group owner" : member.isAdmin ? "Admin" : "Member"}</small></span>
                            {member.canKick && <button type="button" className="member-kick" disabled={busyMember === member.id}
                              onClick={() => void kickMember(member)}>{busyMember === member.id ? "Removing…" : "Remove"}</button>}
                          </li>)}</ul>}
                      {!botIsGroupAdmin && <p className="member-hint">The bot account must be a group admin to delete members' messages or remove members.</p>}
                    </section>}
                    <footer className="chat-settings-footer">
                      <button type="button" className="secondary-button" onClick={() => setSettingsOpen(false)}>Close</button>
                      <button type="submit" disabled={savingSettings || !chatSettings}>{savingSettings ? "Saving…" : "Save settings"}</button>
                    </footer>
                  </form>}
                </>}
              </aside>
            </>}

            {canSend && <form className="chat-composer" onSubmit={(event) => void sendMessage(event)}>
              <textarea aria-label="Write a WhatsApp message" value={draft} onChange={(event) => setDraft(event.target.value)}
                placeholder="Write a message as the Agent…" rows={2} maxLength={12000} disabled={sending} />
              <button type="submit" disabled={sending || !draft.trim()}>{sending ? "Sending…" : "Send"}</button>
            </form>}
            <p className="transcript-note">This history shows messages saved by the Agent. Sending messages and moderation require an active Agent and WhatsApp connection.{selectedConversation?.kind === "group" && !groupAdminChecked ? " Checking the bot account's admin permissions…" : selectedConversation?.kind === "group" && membersError ? " Could not check admin permissions. Open Chat settings for details." : ""}</p>
          </section>
        </>}
    </div>
  </div>;
}
