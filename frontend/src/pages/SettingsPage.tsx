import { useCallback, useEffect, useState } from "react";
import { StatusBadge } from "../components/StatusBadge";
import { applyAgentSettings, getAgentRuntimeStatus, getSettings, resetWhatsAppChatSettings, saveSettings, type AgentRuntimeStatusDTO, type SettingsValuesDTO, type SettingsViewDTO } from "../services/backend";

const secretKeep = () => ({ action: "keep", value: "" });
const settingsCategories = [
  { id: "general", label: "General", description: "Give your assistant a name, instructions, and access to the right conversations." },
  { id: "chats", label: "Chat defaults", description: "Choose how your assistant responds in new chats." },
  { id: "provider", label: "AI provider", description: "Connect the model that powers your assistant." },
  { id: "performance", label: "Performance", description: "Fine-tune response limits, history, and message processing." },
  { id: "advanced", label: "Advanced", description: "Connection policies, diagnostics, and launch configuration." },
] as const;
type SettingsCategory = typeof settingsCategories[number]["id"];
type ChatResetCategory = "moderation" | "triggers" | "instructions" | "all";

const agentFieldLabels: Record<string, string> = {
  ASSISTANT_NAME: "Assistant name",
  WAZZAP_BASE_PROMPT: "Base prompt",
  WAZZAP_WHATSAPP_ENABLED: "WhatsApp mode",
  WAZZAP_OWNER_JID: "Owner JID",
  WAZZAP_CHAT_ALLOWLIST: "Chat allowlist",
  WAZZAP_LLM_ENDPOINT: "LLM API base URL",
  WAZZAP_LLM_API_KEY: "LLM API key",
  WAZZAP_LLM_MODEL: "LLM model",
};

function readinessMessage(field: string, message: string): string {
  if (field === "WAZZAP_BASE_PROMPT" && message.includes("required")) return "Enter a base prompt before starting the Agent.";
  return message;
}

export function SettingsPage() {
  const [category, setCategory] = useState<SettingsCategory>("general");
  const [snapshot, setSnapshot] = useState<SettingsViewDTO | null>(null);
  const [draft, setDraft] = useState<SettingsValuesDTO | null>(null);
  const [primarySecret, setPrimarySecret] = useState("");
  const [fallbackSecret, setFallbackSecret] = useState("");
  const [langSmithSecret, setLangSmithSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [runtimeStatus, setRuntimeStatus] = useState<AgentRuntimeStatusDTO | null>(null);
  const [resetMessage, setResetMessage] = useState("");

  const refreshRuntime = useCallback(async () => {
    try { setRuntimeStatus(await getAgentRuntimeStatus()); } catch { /* settings remain usable if runtime status is temporarily unavailable */ }
  }, []);

  useEffect(() => {
    let active = true;
    getSettings().then((view) => {
      if (!active) return;
      setSnapshot(view);
      setDraft(view.values);
      setSaved(true);
    }).catch((reason: unknown) => {
      if (active) setError(reason instanceof Error ? reason.message : "Could not load settings.");
    });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    void refreshRuntime();
    const timer = window.setInterval(() => void refreshRuntime(), 2000);
    return () => window.clearInterval(timer);
  }, [refreshRuntime]);

  function update<K extends keyof SettingsValuesDTO>(key: K, value: SettingsValuesDTO[K]) {
    setDraft((current) => current ? { ...current, [key]: value } : current);
    setSaved(false);
  }

  function updateChatDefault<K extends keyof SettingsValuesDTO["chatDefaults"]>(key: K, value: SettingsValuesDTO["chatDefaults"][K]) {
    setDraft((current) => current ? { ...current, chatDefaults: { ...current.chatDefaults, [key]: value } } : current);
    setSaved(false);
  }

  async function submit() {
    if (!draft || !snapshot) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const result = await saveSettings({
        expectedRevision: snapshot.revision,
        patch: {
          draft,
          settings: draft,
          values: draft,
          secrets: {
            llmAPIKey: primarySecret ? { action: "replace", value: primarySecret } : secretKeep(),
            fallbackAPIKey: fallbackSecret ? { action: "replace", value: fallbackSecret } : secretKeep(),
            langSmithAPIKey: langSmithSecret ? { action: "replace", value: langSmithSecret } : secretKeep(),
          },
        },
      });
      setSnapshot(result.view);
      setDraft(result.view.values);
      setPrimarySecret("");
      setFallbackSecret("");
      setLangSmithSecret("");
      setSaved(true);
      await refreshRuntime();
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not save settings.");
    } finally {
      setBusy(false);
    }
  }

  async function applySavedSettings() {
    if (!snapshot || !saved) return;
    setBusy(true);
    setError(null);
    try {
      setRuntimeStatus(await applyAgentSettings({ expectedRevision: snapshot.revision }));
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not apply the saved settings.");
      await refreshRuntime();
    } finally {
      setBusy(false);
    }
  }

  async function resetSavedChatSettings(category: ChatResetCategory, label: string) {
    if (!snapshot || !saved || busy || runtimeStatus?.state !== "running" || runtimeStatus?.pendingChanges) return;
    const accepted = window.confirm("Reset " + label + " in every saved chat to the currently saved defaults? This overwrites chat-specific values.");
    if (!accepted) return;
    setBusy(true);
    setError(null);
    setResetMessage("");
    try {
      const result = await resetWhatsAppChatSettings({
        expectedSettingsRevision: snapshot.revision,
        category,
      });
      setResetMessage(result.changedChats === 0
        ? "No saved chat settings needed resetting for " + label + "."
        : "Reset " + label + " to the saved default in " + result.changedChats + (result.changedChats === 1 ? " chat." : " chats."));
      await refreshRuntime();
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : "Could not reset " + label.toLowerCase() + ".");
    } finally {
      setBusy(false);
    }
  }

  if (error && !draft) return <div className="page"><header className="page-header"><div><p className="eyebrow">SETTINGS</p><h1>Settings</h1><p className="lede">Your settings could not be loaded. Please try again.</p></div><StatusBadge tone="warn">Error</StatusBadge></header><section className="card error-text">{error}</section></div>;
  if (!draft || !snapshot) return <div className="page"><header className="page-header"><div><p className="eyebrow">SETTINGS</p><h1>Settings</h1><p className="lede">Loading saved settings…</p></div><StatusBadge>Loading</StatusBadge></header></div>;

  return <div className="page">
    <header className="page-header"><div><p className="eyebrow">SETTINGS</p><h1>Settings</h1><p className="lede">Make your assistant work the way you do.</p></div><StatusBadge tone={saved ? "good" : "warn"}>{saved ? "Saved" : "Unsaved changes"}</StatusBadge></header>
    <nav className="settings-categories" aria-label="Settings categories">{settingsCategories.map((item) => <button type="button" key={item.id} aria-pressed={category === item.id} className={category === item.id ? "selected" : ""} onClick={() => setCategory(item.id)}>{item.label}</button>)}</nav>
    <section className="card settings-form">
      <div className="settings-actions">
        <span className="settings-save-state"><strong>{saved ? "All changes saved" : "You have unsaved changes"}</strong><small>{runtimeStatus?.pendingChanges ? "Apply saved settings to restart the Agent with your changes." : runtimeStatus?.state === "running" ? "Your saved settings are active." : "Saved settings take effect when you start the Agent."}</small></span>
        <div className="settings-action-buttons">
          {runtimeStatus?.state === "running" && runtimeStatus.pendingChanges && <button className="button secondary" disabled={busy || !saved} onClick={() => void applySavedSettings()}>{busy ? "Working…" : "Apply to Agent"}</button>}
          <button className="button primary" disabled={busy || saved} onClick={() => void submit()}>{busy ? "Saving…" : saved ? "Saved" : "Save settings"}</button>
        </div>
      </div>
      {error && <p className="error-text" role="status">{error}</p>}
      {draft.agentEnabled && Boolean(snapshot.agentReadiness?.length) && <div className="readiness-note" role="status"><strong>Agent is not ready to start</strong><ul>{snapshot.agentReadiness?.map((issue) => <li key={`${issue.field}-${issue.code}`}><strong>{agentFieldLabels[issue.field] ?? issue.field}:</strong> {readinessMessage(issue.field, issue.message)}</li>)}</ul></div>}
      <section className="settings-category-panel" hidden={category !== "general"} aria-label="Assistant identity">
        <div className="settings-panel-heading"><h2>Assistant identity</h2><p className="muted">{settingsCategories.find((item) => item.id === "general")?.description}</p></div>
      <div className="settings-form-grid">
        <label><span>Assistant name</span><input value={draft.assistantName} onChange={(event) => update("assistantName", event.target.value)} /></label>
        <label><span>Owner WhatsApp ID</span><input value={draft.ownerJID} onChange={(event) => update("ownerJID", event.target.value)} /></label>
        <label className="wide"><span>Allowed chat IDs</span><input value={(draft.chatAllowlist ?? []).join(", ")} onChange={(event) => update("chatAllowlist", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))} /></label>
        <label className="wide"><span>Assistant instructions</span><textarea rows={4} value={draft.basePrompt} onChange={(event) => update("basePrompt", event.target.value)} /></label>
        <label><span>WhatsApp mode</span><select value={draft.whatsAppEnabled ? "enabled" : "disabled"} onChange={(event) => update("whatsAppEnabled", event.target.value === "enabled")}><option value="enabled">Enabled</option><option value="disabled">Disabled</option></select></label>
        <label><span>Agent mode</span><select value={draft.agentEnabled ? "enabled" : "disabled"} onChange={(event) => update("agentEnabled", event.target.value === "enabled")}><option value="enabled">Enabled</option><option value="disabled">Disabled</option></select></label>
        <label><span>Start on launch</span><select value={draft.startOnLaunch ? "enabled" : "disabled"} onChange={(event) => update("startOnLaunch", event.target.value === "enabled")}><option value="enabled">Enabled</option><option value="disabled">Disabled</option></select></label>
      </div>

      </section>
      <section className="settings-category-panel" hidden={category !== "chats"} aria-label="Chat defaults">
        <div className="settings-panel-heading"><h2>Chat defaults</h2><p className="muted">{settingsCategories.find((item) => item.id === "chats")?.description}</p></div>
      <p className="muted small">Used when a chat gets its settings for the first time. Existing chat settings keep their saved values. Save and Apply to use these defaults in the running Agent.</p>
      <details className="reset-chat-settings"><summary>Reset existing chats to these defaults</summary>
        <p className="muted small">Reset saved chat-specific settings across every chat, including older chats. These actions use the saved defaults above. {runtimeStatus?.state !== "running" ? "Start the Agent first." : runtimeStatus.pendingChanges ? "Apply saved settings to the Agent first." : "Save any pending edits before using these actions."}</p>
        <div className="reset-chat-settings-actions">
          <button type="button" className="button secondary" disabled={busy || !saved || runtimeStatus?.state !== "running" || runtimeStatus?.pendingChanges} onClick={() => void resetSavedChatSettings("moderation", "moderation")}>Reset moderation in all chats</button>
          <button type="button" className="button secondary" disabled={busy || !saved || runtimeStatus?.state !== "running" || runtimeStatus?.pendingChanges} onClick={() => void resetSavedChatSettings("triggers", "triggers")}>Reset triggers in all chats</button>
          <button type="button" className="button secondary" disabled={busy || !saved || runtimeStatus?.state !== "running" || runtimeStatus?.pendingChanges} onClick={() => void resetSavedChatSettings("instructions", "custom instructions")}>Reset custom instructions in all chats</button>
          <button type="button" className="button secondary" disabled={busy || !saved || runtimeStatus?.state !== "running" || runtimeStatus?.pendingChanges} onClick={() => void resetSavedChatSettings("all", "all chat settings")}>Reset all chat settings</button>
        </div>
        {resetMessage && <p className="success-text" role="status">{resetMessage}</p>}
      </details>
      <div className="settings-form-grid">
        <label><span>Moderation level</span><select value={draft.chatDefaults.moderationLevel} onChange={(event) => updateChatDefault("moderationLevel", Number(event.target.value))}>
          <option value={0}>Disabled — no moderation</option><option value={1}>Level 1 — delete messages</option><option value={2}>Level 2 — delete and mute</option><option value={3}>Level 3 — delete, mute, and kick</option>
        </select></label>
        <div className="wide"><span>Agent triggers</span><div className="trigger-options">
          <label><input type="checkbox" checked={draft.chatDefaults.triggerMention} onChange={(event) => updateChatDefault("triggerMention", event.target.checked)} /> Mention the bot account</label>
          <label><input type="checkbox" checked={draft.chatDefaults.triggerName} onChange={(event) => updateChatDefault("triggerName", event.target.checked)} /> Agent name</label>
          <label><input type="checkbox" checked={draft.chatDefaults.triggerReply} onChange={(event) => updateChatDefault("triggerReply", event.target.checked)} /> Reply to a bot message</label>
        </div></div>
        {draft.chatDefaults.triggerName && <label className="wide"><input type="checkbox" checked={draft.chatDefaults.triggerNameRegex} onChange={(event) => updateChatDefault("triggerNameRegex", event.target.checked)} /> Use a custom regex for the name trigger</label>}
        {draft.chatDefaults.triggerName && draft.chatDefaults.triggerNameRegex && <label className="wide"><span>Regex pattern</span><input value={draft.chatDefaults.triggerNamePattern} maxLength={512} onChange={(event) => updateChatDefault("triggerNamePattern", event.target.value)} /></label>}
        <label><span>Custom instructions mode</span><select value={draft.chatDefaults.promptMode} onChange={(event) => updateChatDefault("promptMode", event.target.value)}><option value="append">Append to main instructions</option><option value="replace">Replace main instructions</option></select></label>
        <label className="wide"><span>Default custom instructions</span><textarea rows={4} maxLength={16000} value={draft.chatDefaults.promptText} onChange={(event) => updateChatDefault("promptText", event.target.value)} placeholder="Leave blank to use only the main instructions" /></label>
      </div>

      </section>
      <section className="settings-category-panel" hidden={category !== "provider"} aria-label="Model & credentials">
        <div className="settings-panel-heading"><h2>Model & credentials</h2><p className="muted">{settingsCategories.find((item) => item.id === "provider")?.description}</p></div>
      <div className="settings-form-grid">
        <label><span>LLM API base URL</span><input value={draft.llmEndpoint} onChange={(event) => update("llmEndpoint", event.target.value)} /></label>
        <label><span>LLM model</span><input value={draft.llmModel} onChange={(event) => update("llmModel", event.target.value)} /></label>
        <label><span>Provider ID</span><input value={draft.llmProviderID} onChange={(event) => update("llmProviderID", event.target.value)} /></label>
        <label><span>Fallback API base URL</span><input value={draft.fallbackEndpoint} onChange={(event) => update("fallbackEndpoint", event.target.value)} /></label>
        <p className="muted small wide">Enter a base URL such as https://api.example.com/v1. The app adds /chat/completions automatically. A full chat-completions URL is also accepted.</p>
        <label><span>Primary API key {draft.llmAPIKeyConfigured ? "(saved)" : ""}</span><input type="password" placeholder="Leave blank to keep the current value" value={primarySecret} onChange={(event) => { setPrimarySecret(event.target.value); setSaved(false); }} /></label>
        <label><span>Fallback API key {draft.fallbackAPIKeyConfigured ? "(saved)" : ""}</span><input type="password" placeholder="Leave blank to keep the current value" value={fallbackSecret} onChange={(event) => { setFallbackSecret(event.target.value); setSaved(false); }} /></label>
        <label><span>LangSmith API key {draft.langSmithAPIKeyConfigured ? "(saved)" : ""}</span><input type="password" placeholder="Leave blank to keep the current value" value={langSmithSecret} onChange={(event) => { setLangSmithSecret(event.target.value); setSaved(false); }} /></label>
      </div>

      </section>
      <section className="settings-category-panel" hidden={category !== "performance"} aria-label="Response & processing">
        <div className="settings-panel-heading"><h2>Response & processing</h2><p className="muted">{settingsCategories.find((item) => item.id === "performance")?.description}</p></div>
      <div className="settings-form-grid">
        <label><span>LLM timeout</span><input value={draft.llmTimeout} onChange={(event) => update("llmTimeout", event.target.value)} /></label>
        <label><span>LLM concurrency</span><input type="number" min="0" value={draft.llmConcurrency} onChange={(event) => update("llmConcurrency", Number(event.target.value))} /></label>
        <label><span>Max output tokens</span><input type="number" min="0" value={draft.maxOutputTokens} onChange={(event) => update("maxOutputTokens", Number(event.target.value))} /></label>
        <label><span>Max response bytes</span><input type="number" min="0" value={draft.maxResponseBytes} onChange={(event) => update("maxResponseBytes", Number(event.target.value))} /></label>
        <label><span>History window</span><input type="number" min="0" value={draft.historyWindow} onChange={(event) => update("historyWindow", Number(event.target.value))} /></label>
        <label><span>Max context bytes</span><input type="number" min="0" value={draft.maxContextBytes} onChange={(event) => update("maxContextBytes", Number(event.target.value))} /></label>
        <label><span>History keep latest</span><input type="number" min="0" value={draft.historyKeepLatest} onChange={(event) => update("historyKeepLatest", Number(event.target.value))} /></label>
        <label><span>History max age</span><input value={draft.historyMaxAge} onChange={(event) => update("historyMaxAge", event.target.value)} /></label>
        <label><span>Inbound queue</span><input type="number" min="0" value={draft.inboundQueue} onChange={(event) => update("inboundQueue", Number(event.target.value))} /></label>
        <label><span>Inbound workers</span><input type="number" min="0" value={draft.inboundWorkers} onChange={(event) => update("inboundWorkers", Number(event.target.value))} /></label>
        <label><span>Command queue</span><input type="number" min="0" value={draft.commandQueue} onChange={(event) => update("commandQueue", Number(event.target.value))} /></label>
        <label><span>Command workers</span><input type="number" min="0" value={draft.commandWorkers} onChange={(event) => update("commandWorkers", Number(event.target.value))} /></label>
        <label><span>AI queue</span><input type="number" min="0" value={draft.aiQueue} onChange={(event) => update("aiQueue", Number(event.target.value))} /></label>
        <label><span>AI workers</span><input type="number" min="0" value={draft.aiWorkers} onChange={(event) => update("aiWorkers", Number(event.target.value))} /></label>
        <label><span>Message debounce</span><input value={draft.messageDebounce} onChange={(event) => update("messageDebounce", event.target.value)} /></label>
        <label><span>Message burst cap</span><input type="number" min="0" value={draft.messageBurstCap} onChange={(event) => update("messageBurstCap", Number(event.target.value))} /></label>
      </div>

      </section>
      <section className="settings-category-panel" hidden={category !== "advanced"} aria-label="Runtime & policy">
        <div className="settings-panel-heading"><h2>Runtime & policy</h2><p className="muted">{settingsCategories.find((item) => item.id === "advanced")?.description}</p></div>
      <div className="settings-form-grid">
        <label><span>Agent max live</span><input type="number" min="0" value={draft.agentMaxLive} onChange={(event) => update("agentMaxLive", Number(event.target.value))} /></label>
        <label><span>Agent idle TTL</span><input value={draft.agentIdleTTL} onChange={(event) => update("agentIdleTTL", event.target.value)} /></label>
        <label><span>Agent construction timeout</span><input value={draft.agentConstructionTimeout} onChange={(event) => update("agentConstructionTimeout", event.target.value)} /></label>
        <label><span>Connect timeout</span><input value={draft.connectTimeout} onChange={(event) => update("connectTimeout", event.target.value)} /></label>
        <label><span>Send timeout</span><input value={draft.sendTimeout} onChange={(event) => update("sendTimeout", event.target.value)} /></label>
        <label><span>Shutdown timeout</span><input value={draft.shutdownTimeout} onChange={(event) => update("shutdownTimeout", event.target.value)} /></label>
        <label><span>Policy ID</span><input value={draft.policyID} onChange={(event) => update("policyID", event.target.value)} /></label>
        <label><span>Policy revision</span><input type="number" min="0" value={draft.policyRevision} onChange={(event) => update("policyRevision", event.target.value)} /></label>
        <label><span>Log level</span><select value={draft.logLevel} onChange={(event) => update("logLevel", event.target.value)}><option value="debug">debug</option><option value="info">info</option><option value="warn">warn</option><option value="error">error</option></select></label>
        <label><span>Log format</span><select value={draft.logFormat} onChange={(event) => update("logFormat", event.target.value)}><option value="json">json</option><option value="text">text</option><option value="compact">compact</option></select></label>
      </div>

      <details className="advanced-settings"><summary>Launch & command-line options</summary>
      <p className="muted small">Command-line draft values do not change the running app’s data location or network address.</p>
      <div className="settings-form-grid">
        <label><span>Data directory (CLI draft)</span><input value={draft.dataDir} readOnly title="This setting is saved as a CLI draft; changing the data root from the GUI is not available yet." /></label>
        <label><span>Env file (CLI draft)</span><input value={draft.envFile} onChange={(event) => update("envFile", event.target.value)} /></label>
        <label><span>HTTP address (CLI draft)</span><input value={draft.httpAddress} onChange={(event) => update("httpAddress", event.target.value)} /></label>
        <label><span>Pairing output</span><select value={draft.pairingOutput} onChange={(event) => update("pairingOutput", event.target.value)}><option value="terminal">Terminal</option><option value="disabled">Disabled</option></select></label>
        <label><span>NO_COLOR</span><select value={draft.noColor ? "enabled" : "disabled"} onChange={(event) => update("noColor", event.target.value === "enabled")}><option value="enabled">Enabled</option><option value="disabled">Disabled</option></select></label>
        <label><span>FORCE_COLOR</span><select value={draft.forceColor ? "enabled" : "disabled"} onChange={(event) => update("forceColor", event.target.value === "enabled")}><option value="enabled">Enabled</option><option value="disabled">Disabled</option></select></label>
        <label><span>Tenant ID</span><input value={draft.tenantID || "Not created"} readOnly /></label>
        <label><span>Account ID</span><input value={draft.accountID || "Not created"} readOnly /></label>
      </div>
      </details>
      </section>
    </section>
  </div>;
}
