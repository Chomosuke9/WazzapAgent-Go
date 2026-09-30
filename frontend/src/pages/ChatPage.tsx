import { useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import {
  deleteDiscordMessage,
  getDiscordConversations,
  getDiscordGroupMembers,
  getDiscordMessages,
  kickDiscordGroupMember,
  sendDiscordMessage,
  type DiscordConversationDTO,
  type DiscordGroupMemberDTO,
  type DiscordMentionDTO,
  type DiscordMessageDTO,
  type DiscordQuoteDTO,
} from "../services/backend";
import { ChatSettings } from "./ChatSettings";
import { ChatTasks } from "./ChatTasks";

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

function conversationTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const today = new Date();
  return date.toDateString() === today.toDateString()
    ? date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : date.toLocaleDateString(undefined, { day: "numeric", month: "short", ...(date.getFullYear() !== today.getFullYear() ? { year: "numeric" as const } : {}) });
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

type MentionCandidate = {
  start: number;
  end: number;
  name: string;
  senderRef: string;
  bot: boolean;
};

function displayMentionName(value: string, fallback: string): string {
  return value.replace(/[\r\n@]/g, " ").replace(/\s+/g, " ").trim() || fallback;
}

function mentionInsertionText(name: string, senderRef: string): string {
  const safeName = displayMentionName(name, "Contact").replace(/[()]/g, " ").replace(/\s+/g, " ").trim();
  return `@${safeName || "Contact"} (${senderRef})`;
}

function renderMessageText(
  text: string,
  mentions: DiscordMentionDTO[] = [],
  onMentionClick?: (name: string, senderRef: string) => void,
): ReactNode[] {
  if (!text) return [];
  const candidates: MentionCandidate[] = [];
  const addCandidate = (candidate: MentionCandidate) => {
    if (candidate.start < 0 || candidate.end > text.length || candidate.start >= candidate.end) return;
    candidates.push(candidate);
  };

  for (const mention of mentions) {
    if (!mention.token) continue;
    let fromIndex = 0;
    while (fromIndex < text.length) {
      const start = text.indexOf(mention.token, fromIndex);
      if (start < 0) break;
      const end = start + mention.token.length;
      const before = start > 0 ? text[start - 1] : "";
      const after = end < text.length ? text[end] : "";
      if (!/[\p{L}\p{N}_]/u.test(before) && !/[\p{L}\p{N}_]/u.test(after)) {
        addCandidate({
          start, end,
          name: displayMentionName(mention.displayName, mention.bot ? "Bot" : mention.token.slice(1)),
          senderRef: mention.senderRef,
          bot: mention.bot,
        });
      }
      fromIndex = end;
    }
  }

  const canonicalMention = /@([^@()\r\n]+?)\s*\(([0-9a-z]{6})\)/g;
  for (const match of text.matchAll(canonicalMention)) {
    const name = displayMentionName(match[1] ?? "", "Contact");
    const senderRef = match[2] ?? "";
    const start = match.index ?? -1;
    addCandidate({ start, end: start + match[0].length, name, senderRef, bot: false });
  }

  candidates.sort((left, right) => left.start - right.start || right.end - left.end);
  const selected: MentionCandidate[] = [];
  let end = 0;
  for (const candidate of candidates) {
    if (candidate.start < end) continue;
    selected.push(candidate);
    end = candidate.end;
  }
  if (selected.length === 0) return [text];

  const result: ReactNode[] = [];
  let cursor = 0;
  selected.forEach((candidate, index) => {
    if (candidate.start > cursor) result.push(text.slice(cursor, candidate.start));
    const label = `@${candidate.name}`;
    result.push(candidate.senderRef && onMentionClick && !candidate.bot
      ? <button
          type="button"
          className="message-mention"
          key={`mention-${candidate.start}-${index}`}
          onClick={() => onMentionClick(candidate.name, candidate.senderRef)}
          title={`Add ${label} as a mention`}
        >{label}</button>
      : <span className="message-mention" key={`mention-${candidate.start}-${index}`}>{label}</span>);
    cursor = candidate.end;
  });
  if (cursor < text.length) result.push(text.slice(cursor));
  return result;
}

function replyRoleLabel(quote: DiscordQuoteDTO): string {
  return quote.role === "assistant" ? "You" : quote.sender || "Contact";
}

export function ChatPage() {
  const [conversations, setConversations] = useState<DiscordConversationDTO[]>([]);
  const [chatQuery, setChatQuery] = useState("");
  const [selectedChatID, setSelectedChatID] = useState("");
  const [mobileConversationOpen, setMobileConversationOpen] = useState(false);
  const [messages, setMessages] = useState<DiscordMessageDTO[]>([]);
  const [loadingConversations, setLoadingConversations] = useState(true);
  const [loadingMessages, setLoadingMessages] = useState(false);
  const [conversationError, setConversationError] = useState("");
  const [messagesError, setMessagesError] = useState("");
  const [actionError, setActionError] = useState("");
  const [draft, setDraft] = useState("");
  const [replyTarget, setReplyTarget] = useState<DiscordMessageDTO | null>(null);
  const [sending, setSending] = useState(false);
  const [members, setMembers] = useState<DiscordGroupMemberDTO[]>([]);
  const [botIsGroupAdmin, setBotIsGroupAdmin] = useState(false);
  const [groupAdminChecked, setGroupAdminChecked] = useState(false);
  const [loadingMembers, setLoadingMembers] = useState(false);
  const [membersError, setMembersError] = useState("");
  const [memberRefresh, setMemberRefresh] = useState(0);
  const [busyMember, setBusyMember] = useState("");
  const [busyMessage, setBusyMessage] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const stickToLatest = useRef(true);
  const forceLatestOnLoad = useRef(true);
  const membersChatID = useRef("");
  const messageInputRef = useRef<HTMLTextAreaElement>(null);
  const pendingCaretPosition = useRef<number | null>(null);
  const selectedConversation = conversations.find((item) => item.id === selectedChatID) ?? null;
  const visibleConversations = conversations.filter((item) => item.name.toLocaleLowerCase().includes(chatQuery.trim().toLocaleLowerCase()));

  useEffect(() => {
    const caret = pendingCaretPosition.current;
    if (caret === null) return;
    pendingCaretPosition.current = null;
    const input = messageInputRef.current;
    input?.focus();
    input?.setSelectionRange(caret, caret);
  }, [draft]);

  useEffect(() => {
    let mounted = true;
    let requestInFlight = false;
    const refresh = async () => {
      if (requestInFlight || document.hidden) return;
      requestInFlight = true;
      try {
        const result = await getDiscordConversations();
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
    const onVisibilityChange = () => { if (!document.hidden) void refresh(); };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => { mounted = false; window.clearInterval(timer); document.removeEventListener("visibilitychange", onVisibilityChange); };
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
    setReplyTarget(null);
    setActionError("");
    stickToLatest.current = true;
    forceLatestOnLoad.current = true;
    let requestInFlight = false;
    const refresh = async () => {
      if (requestInFlight || document.hidden) return;
      requestInFlight = true;
      try {
        const result = await getDiscordMessages(selectedChatID);
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
    const onVisibilityChange = () => { if (!document.hidden) void refresh(); };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => { mounted = false; window.clearInterval(timer); document.removeEventListener("visibilitychange", onVisibilityChange); };
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
    void getDiscordGroupMembers(selectedChatID)
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

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedConversation || !draft.trim() || sending) return;
    setSending(true);
    setActionError("");
    try {
      const message = await sendDiscordMessage(selectedChatID, draft, replyTarget?.id ?? "");
      stickToLatest.current = true;
      forceLatestOnLoad.current = true;
      setMessages((current) => current.some((item) => item.id === message.id)
        ? current
        : [...current, message].slice(-100));
      setDraft("");
      setReplyTarget(null);
    } catch (error) {
      setActionError(actionErrorMessage(error, "Could not send the message. Check the Agent and WhatsApp status."));
    } finally {
      setSending(false);
    }
  }

  function insertMention(name: string, senderRef: string) {
    if (!senderRef) return;
    const input = messageInputRef.current;
    const start = input?.selectionStart ?? draft.length;
    const end = input?.selectionEnd ?? draft.length;
    const prefix = start > 0 && !/\s/.test(draft[start - 1] ?? "") ? " " : "";
    const suffix = end < draft.length && /\s/.test(draft[end] ?? "") ? "" : " ";
    const token = mentionInsertionText(name, senderRef);
    const insertion = `${prefix}${token}${suffix}`;
    setDraft(`${draft.slice(0, start)}${insertion}${draft.slice(end)}`);
    pendingCaretPosition.current = start + insertion.length;
  }

  async function deleteMessage(message: DiscordMessageDTO) {
    setBusyMessage(message.id);
    setActionError("");
    try {
      await deleteDiscordMessage(selectedChatID, message.id);
      setMessages((current) => current.map((item) => item.id === message.id
        ? { ...item, deleted: true, content: "This message was deleted on WhatsApp." }
        : item));
    } catch (error) {
      setActionError(actionErrorMessage(error, "Could not delete the message. Make sure it is still available and the Agent is running."));
    } finally {
      setBusyMessage("");
    }
  }

  async function kickMember(member: DiscordGroupMemberDTO) {
    if (!window.confirm(`Remove ${member.name} from this WhatsApp group?`)) return;
    setBusyMember(member.id);
    setActionError("");
    try {
      await kickDiscordGroupMember(selectedChatID, member.id);
      setMembers((current) => current.filter((item) => item.id !== member.id));
    } catch (error) {
      setMembersError(actionErrorMessage(error, "Could not remove the member. Check the connection and the WhatsApp account's admin permissions."));
    } finally {
      setBusyMember("");
    }
  }

  const canSend = selectedConversation?.kind === "group" || selectedConversation?.kind === "direct";

  return <div className={`page chat-page${mobileConversationOpen ? " mobile-chat-open" : ""}`}>

    {(conversationError || messagesError || actionError) && <p className="error-text inbox-error" role="status">{actionError || conversationError || messagesError}</p>}

    <div className="bot-inbox card">
      {loadingConversations && conversations.length === 0 ? <div className="inbox-empty"><span className="inbox-spinner" />Loading conversations…</div>
        : conversations.length === 0 ? <div className="inbox-empty">
          <span className="inbox-empty-icon" aria-hidden="true">◌</span>
          <strong>{conversationError ? "Could not load conversations" : "Your conversations start here"}</strong>
          <p>{conversationError ? "The app will retry automatically." : "Start your assistant from Overview. Conversations will appear here as it receives messages."}</p>
        </div> : <>
          <nav className="bot-chat-list" aria-label="Conversation list">
            <div className="chat-list-toolbar">
              <div><strong>Conversations</strong><span>{conversations.length}</span></div>
              <label className="chat-list-search"><span className="visually-hidden">Search conversations</span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><circle cx="10.8" cy="10.8" r="6.8" /><path d="m16 16 5 5" /></svg><input value={chatQuery} onChange={(event) => setChatQuery(event.target.value)} placeholder="Search conversations" /></label>
            </div>
            {visibleConversations.length === 0 && <p className="chat-list-no-results">No matching conversations.</p>}
            {visibleConversations.map((conversation) => <button
              key={conversation.id}
              className={conversation.id === selectedChatID ? "bot-chat-item selected" : "bot-chat-item"}
              onClick={() => { setSelectedChatID(conversation.id); setMobileConversationOpen(true); }}
              aria-current={conversation.id === selectedChatID ? "true" : undefined}
            >
              <span className="bot-chat-avatar" aria-hidden="true">{conversation.name.trim().slice(0, 1).toUpperCase() || "?"}</span>
              <span className="bot-chat-summary">
                <span className="bot-chat-title"><strong>{conversation.name}</strong><time title={messageTime(conversation.lastMessageAt, true)}>{conversationTime(conversation.lastMessageAt)}</time></span>
              <span className="bot-chat-preview"><span>{conversation.lastFromBot ? "You: " : ""}{renderMessageText(conversation.lastMessage, conversation.lastMessageMentions ?? [])}</span><small>{conversationKind(conversation.kind)}</small></span>
              </span>
            </button>)}
          </nav>

          <section className="bot-transcript" aria-label={selectedConversation ? `Messages with ${selectedConversation.name}` : "Conversation messages"}>
            {selectedConversation ? <header className="transcript-header">
              <button type="button" className="mobile-chat-back" aria-label="Back to conversations"
                onClick={() => { setSettingsOpen(false); setMobileConversationOpen(false); }} title="Back to conversations">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m15 18-6-6 6-6" /></svg>
              </button>
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
                    : messages.map((message) => <article id={`chat-message-${message.id}`} key={message.id} className={message.role === "assistant" ? "bot-message from-bot" : "bot-message from-contact"}>
                      {message.role !== "assistant" && <div className="message-sender-line">
                        {message.senderRef
                          ? <button type="button" className="message-sender message-sender-action"
                              onClick={() => insertMention(message.sender, message.senderRef)}
                              title={`Add @${message.sender} as a mention`}>{message.sender}</button>
                          : <strong className="message-sender">{message.sender}</strong>}
                        {message.isSuperAdmin
                          ? <span className="message-role-badge">Group owner</span>
                          : message.isAdmin ? <span className="message-role-badge">Admin</span> : null}
                      </div>}
                      <div className="message-body" title="Double-click to reply" onDoubleClick={() => {
                        if (!message.deleted && (message.role !== "assistant" || message.delivery === "sent")) setReplyTarget(message);
                      }}>
                        {message.quote && <div className="message-quote" aria-label={`Reply to ${replyRoleLabel(message.quote)}`}>
                          <div className="message-quote-heading">
                            <strong>{replyRoleLabel(message.quote)}</strong>
                            {message.quote.isSuperAdmin
                              ? <span className="message-role-badge">Group owner</span>
                              : message.quote.isAdmin ? <span className="message-role-badge">Admin</span> : null}
                          </div>
                          <span>{renderMessageText(message.quote.content, message.quote.mentions ?? [])}</span>
                        </div>}
                        <p className="message-content">{renderMessageText(message.content, message.mentions ?? [], insertMention)}</p>
                      </div>
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
                <div className="chat-settings-form">
                  <ChatSettings key={selectedChatID} chatID={selectedChatID} isGroup={selectedConversation?.kind === "group"} />
                  <ChatTasks key={`tasks-${selectedChatID}`} chatID={selectedChatID} />
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
                </div>
              </aside>
            </>}

            {canSend && <form className="chat-composer" onSubmit={(event) => void sendMessage(event)}>
              {replyTarget && <div className="chat-reply-context">
                <span><strong>Replying to {replyTarget.role === "assistant" ? "You" : replyTarget.sender}</strong>
                  <small>{renderMessageText(replyTarget.content, replyTarget.mentions ?? [])}</small></span>
                <button type="button" className="reply-clear" aria-label="Cancel reply" onClick={() => setReplyTarget(null)}>×</button>
              </div>}
              <textarea ref={messageInputRef} aria-label="Write a WhatsApp message" value={draft} onChange={(event) => setDraft(event.target.value)}
                placeholder="Write a message…" rows={2} maxLength={12000} disabled={sending} />
              <button type="submit" disabled={sending || !draft.trim()}>{sending ? "Sending…" : "Send"}</button>
            </form>}
            <p className="transcript-note">Double-click a message to reply. Select a name to mention someone.{selectedConversation?.kind === "group" && !groupAdminChecked ? " Checking the bot account's admin permissions…" : selectedConversation?.kind === "group" && membersError ? " Could not check admin permissions. Open Chat settings for details." : ""}</p>
          </section>
        </>}
    </div>
  </div>;
}
