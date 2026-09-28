import { useEffect, useState } from "react";
import {
  getSettings,
  getWhatsAppChatSettings,
  saveWhatsAppChatSettings,
  type WhatsAppChatSettingsDTO,
} from "../services/backend";
import { errorMessage } from "./ChatTasks";

type Values = Omit<WhatsAppChatSettingsDTO, "version">;

const maxSmartRules = 10;

const moderationLevels = [
  { level: 0, title: "Off", detail: "The Agent does not moderate." },
  { level: 1, title: "Delete", detail: "The Agent may delete messages." },
  { level: 2, title: "Delete and mute", detail: "The Agent may delete messages and mute members." },
  { level: 3, title: "Delete, mute and kick", detail: "The Agent may also remove members." },
];

function ruleList(rules: string): string[] {
  return rules.split("\n").map((rule) => rule.trim()).filter(Boolean);
}

// ChatSettings shows when the Agent replies, its smart rules, its moderation
// level and the chat's custom instructions. Every change is saved at once,
// like the same change made with /trigger, /permission or /prompt in the chat.
export function ChatSettings({ chatID, isGroup }: { chatID: string; isGroup: boolean }) {
  const [settings, setSettings] = useState<WhatsAppChatSettingsDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<{ error: boolean; text: string } | null>(null);
  const [typeSafeKeyMissing, setTypeSafeKeyMissing] = useState(false);
  // Typed fields are drafts until their own button saves them.
  const [pattern, setPattern] = useState("");
  const [newRule, setNewRule] = useState("");
  const [instructions, setInstructions] = useState("");
  const [instructionsMode, setInstructionsMode] = useState("append");

  useEffect(() => {
    let mounted = true;
    setLoading(true);
    setNotice(null);
    void getWhatsAppChatSettings(chatID)
      .then((result) => {
        if (!mounted) return;
        setSettings(result);
        setPattern(result.triggerNamePattern);
        setNewRule("");
        setInstructions(result.promptOverrideText);
        setInstructionsMode(result.promptOverrideMode || "append");
      })
      .catch((error) => { if (mounted) setNotice({ error: true, text: errorMessage(error, "Could not load chat settings. Make sure the Agent is running.") }); })
      .finally(() => { if (mounted) setLoading(false); });
    void getSettings()
      .then((view) => { if (mounted) setTypeSafeKeyMissing(!view.values.typeSafeAPIKeyConfigured); })
      .catch(() => undefined);
    return () => { mounted = false; };
  }, [chatID]);

  async function save(patch: Partial<Values>, done: string): Promise<boolean> {
    if (!settings || saving) return false;
    const next = { ...settings, ...patch };
    setSaving(true);
    setNotice(null);
    try {
      const updated = await saveWhatsAppChatSettings({
        chatID,
        expectedVersion: settings.version,
        moderationLevel: next.moderationLevel,
        promptOverrideMode: next.promptOverrideText.trim() ? next.promptOverrideMode || "append" : "",
        promptOverrideText: next.promptOverrideText,
        triggerMention: next.triggerMention,
        triggerName: next.triggerName,
        triggerReply: next.triggerReply,
        triggerNameRegex: next.triggerNameRegex,
        triggerNamePattern: next.triggerNamePattern,
        triggerSmart: next.triggerSmart,
        triggerSmartRules: next.triggerSmartRules,
      });
      setSettings(updated);
      setNotice({ error: false, text: done });
      return true;
    } catch (error) {
      setNotice({ error: true, text: errorMessage(error, "Could not save the change.") });
      return false;
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <p className="member-hint chat-settings-loading">Loading settings…</p>;
  if (!settings) return notice && <p className="error-text settings-feedback" role="status">{notice.text}</p>;

  const rules = ruleList(settings.triggerSmartRules);
  const toggle = (field: "triggerMention" | "triggerReply" | "triggerName" | "triggerSmart", name: string) =>
    (event: { target: { checked: boolean } }) => void save({ [field]: event.target.checked }, `${name} is ${event.target.checked ? "on" : "off"}.`);
  const addRule = async () => {
    const rule = newRule.replace(/\s+/g, " ").trim();
    if (!rule) return;
    if (await save({ triggerSmart: true, triggerSmartRules: [...rules, rule].join("\n") }, "Rule added, and Smart is on.")) setNewRule("");
  };
  const instructionsChanged = instructions !== settings.promptOverrideText
    || (instructions.trim() !== "" && instructionsMode !== (settings.promptOverrideMode || "append"));

  return <>
    {notice && <p className={notice.error ? "error-text settings-feedback" : "settings-success"} role="status">{notice.text}</p>}

    {isGroup && <section className="chat-settings-section">
      <h2>When the Agent replies</h2>
      <p>The Agent answers when any checked item matches. Changes save right away. In the chat: /trigger</p>
      <div className="setting-choices">
        <label className="setting-choice"><input type="checkbox" checked={settings.triggerMention} disabled={saving} onChange={toggle("triggerMention", "Mention")} />
          <span><strong>Mention</strong><small>Someone @mentions the bot account.</small></span></label>
        <label className="setting-choice"><input type="checkbox" checked={settings.triggerReply} disabled={saving} onChange={toggle("triggerReply", "Reply")} />
          <span><strong>Reply</strong><small>Someone replies to one of the bot's messages.</small></span></label>
        <label className="setting-choice"><input type="checkbox" checked={settings.triggerName} disabled={saving} onChange={toggle("triggerName", "Name")} />
          <span><strong>Name</strong><small>{settings.triggerNameRegex ? `A message matches the regex ${settings.triggerNamePattern}.` : "Someone writes the Agent's name."}</small></span></label>
        {settings.triggerName && <div className="setting-detail">
          <label className="settings-field">Match the name with a regex (optional)
            <input type="text" value={pattern} maxLength={512} onChange={(event) => setPattern(event.target.value)} placeholder="Example: (?i)\bvivy\b" />
            <small>Go regex syntax. Add (?i) to ignore case.</small>
          </label>
          <div className="setting-actions">
            {settings.triggerNameRegex && <button type="button" className="secondary-button" disabled={saving}
              onClick={() => void save({ triggerNameRegex: false }, "The name trigger uses the Agent's name again.")}>Use the Agent's name</button>}
            <button type="button" className="secondary-button" disabled={saving || !pattern.trim() || (settings.triggerNameRegex && pattern === settings.triggerNamePattern)}
              onClick={() => void save({ triggerName: true, triggerNameRegex: true, triggerNamePattern: pattern.trim() }, "The name trigger uses your regex.")}>Save regex</button>
          </div>
        </div>}
        <label className="setting-choice"><input type="checkbox" checked={settings.triggerSmart} disabled={saving} onChange={toggle("triggerSmart", "Smart")} />
          <span><strong>Smart</strong><small>TypeSafe reads the other messages and wakes the Agent when one is meant for it, or matches a rule below.</small></span></label>
      </div>
      {settings.triggerSmart && typeSafeKeyMissing && <p className="setting-warning">Smart needs a TypeSafe API key. Add it in Settings under Model &amp; credentials; until then Smart never wakes the Agent.</p>}
    </section>}

    {isGroup && <section className="chat-settings-section">
      <h2>Smart rules</h2>
      <p>A message that matches a rule always wakes the Agent, and the Agent follows the rule. Write when it applies and what to do. In the chat: /trigger smart add</p>
      {!settings.triggerSmart && rules.length > 0 && <p className="setting-warning">Smart is off, so these rules are paused.</p>}
      {rules.length === 0 ? <p className="member-hint">No rules yet.</p>
        : <ol className="smart-rule-list">{rules.map((rule, index) => <li key={`${index}-${rule}`}>
          <span className="smart-rule-number">{index + 1}.</span>
          <span className="smart-rule-text">{rule}</span>
          <button type="button" className="member-kick" disabled={saving}
            onClick={() => void save({ triggerSmartRules: rules.filter((_, other) => other !== index).join("\n") }, `Rule ${index + 1} removed.`)}>Remove</button>
        </li>)}</ol>}
      {rules.length < maxSmartRules && <div className="smart-rule-add">
        <input type="text" value={newRule} maxLength={500} aria-label="New smart rule"
          onChange={(event) => setNewRule(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); void addRule(); } }}
          placeholder="Example: someone sends a scam link: delete it and warn them" />
        <button type="button" className="secondary-button" disabled={saving || !newRule.trim()} onClick={() => void addRule()}>Add rule</button>
      </div>}
    </section>}

    {isGroup && <section className="chat-settings-section">
      <h2>Moderation</h2>
      <p>What the Agent may do when a group admin asks or a smart rule says so. The bot account must be a group admin. In the chat: /permission</p>
      <div className="setting-choices">
        {moderationLevels.map((option) => <label key={option.level} className="setting-choice">
          <input type="radio" name="moderation-level" checked={settings.moderationLevel === option.level} disabled={saving}
            onChange={() => void save({ moderationLevel: option.level }, `Moderation is now level ${option.level}.`)} />
          <span><strong>{option.level} · {option.title}</strong><small>{option.detail}</small></span>
        </label>)}
      </div>
    </section>}

    <section className="chat-settings-section">
      <h2>Custom instructions</h2>
      <p>Extra instructions for this chat only. In the chat: /prompt</p>
      <div className="setting-choices setting-choices-inline">
        <label className="setting-choice"><input type="radio" name="instructions-mode" checked={instructionsMode === "append"} onChange={() => setInstructionsMode("append")} />
          <span><strong>Add to the main prompt</strong></span></label>
        <label className="setting-choice"><input type="radio" name="instructions-mode" checked={instructionsMode === "replace"} onChange={() => setInstructionsMode("replace")} />
          <span><strong>Use instead of the main prompt</strong></span></label>
      </div>
      <label className="settings-field">Instructions
        <textarea value={instructions} maxLength={16000} rows={5} onChange={(event) => setInstructions(event.target.value)}
          placeholder="Example: Keep replies concise and use English." />
      </label>
      <div className="setting-actions">
        {settings.promptOverrideText && <button type="button" className="secondary-button" disabled={saving}
          onClick={async () => { if (await save({ promptOverrideText: "", promptOverrideMode: "" }, "Custom instructions deleted.")) setInstructions(""); }}>Delete</button>}
        <button type="button" className="secondary-button setting-primary" disabled={saving || !instructionsChanged}
          onClick={() => void save({ promptOverrideText: instructions, promptOverrideMode: instructionsMode }, "Custom instructions saved.")}>Save instructions</button>
      </div>
    </section>
  </>;
}
