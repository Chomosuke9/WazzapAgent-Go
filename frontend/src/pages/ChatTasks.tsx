import { useEffect, useState } from "react";
import {
  addDiscordChatTask,
  deleteDiscordChatTask,
  getDiscordChatTasks,
  type DiscordChatTaskDTO,
} from "../services/backend";

export function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  return fallback;
}

// datetime-local wants "YYYY-MM-DDTHH:MM" in the browser's time zone.
function localInputValue(date: Date): string {
  const shifted = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return shifted.toISOString().slice(0, 16);
}

// A daily task runs at the bot's clock time, which is the time part of
// nextRun (RFC 3339 with the bot's UTC offset).
function taskWhen(task: DiscordChatTaskDTO): string {
  if (task.daily) return `Every day at ${task.nextRun.slice(11, 16)}`;
  const date = new Date(task.nextRun);
  if (Number.isNaN(date.getTime())) return task.nextRun;
  return date.toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

// ChatTasks lists, adds and deletes the chat's reminders and daily tasks. The
// bot runs them; people only ask it, in chat or here.
export function ChatTasks({ chatID }: { chatID: string }) {
  const [tasks, setTasks] = useState<DiscordChatTaskDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busyTask, setBusyTask] = useState("");
  const [adding, setAdding] = useState(false);
  const [daily, setDaily] = useState(false);
  const [runAt, setRunAt] = useState(() => localInputValue(new Date(Date.now() + 60 * 60_000)));
  const [clock, setClock] = useState("07:00");
  const [prompt, setPrompt] = useState("");

  useEffect(() => {
    let mounted = true;
    setLoading(true);
    setError("");
    void getDiscordChatTasks(chatID)
      .then((result) => { if (mounted) setTasks(result); })
      .catch((reason) => { if (mounted) setError(errorMessage(reason, "Could not load tasks. Make sure the Agent is running.")); })
      .finally(() => { if (mounted) setLoading(false); });
    return () => { mounted = false; };
  }, [chatID]);

  async function addTask() {
    if (!prompt.trim() || adding) return;
    const at = new Date(runAt);
    if (!daily && Number.isNaN(at.getTime())) {
      setError("Choose when the task should run.");
      return;
    }
    setAdding(true);
    setError("");
    try {
      const task = await addDiscordChatTask({ chatID, prompt, daily, runAt: daily ? "" : at.toISOString(), time: daily ? clock : "" });
      setTasks((current) => [...current, task].sort((left, right) => new Date(left.nextRun).getTime() - new Date(right.nextRun).getTime()));
      setPrompt("");
    } catch (reason) {
      setError(errorMessage(reason, "Could not add the task."));
    } finally {
      setAdding(false);
    }
  }

  async function deleteTask(task: DiscordChatTaskDTO) {
    setBusyTask(task.id);
    setError("");
    try {
      await deleteDiscordChatTask(chatID, task.id, task.daily);
      setTasks((current) => current.filter((item) => item.id !== task.id || item.daily !== task.daily));
    } catch (reason) {
      setError(errorMessage(reason, "Could not delete the task."));
    } finally {
      setBusyTask("");
    }
  }

  return <section className="chat-settings-section chat-tasks-section">
    <h2>Tasks</h2>
    <p>The Agent carries these out in this chat: once within the next 24 hours, or every day at a time in the bot's time zone. People in the chat ask the Agent in plain words to add or delete one.</p>
    {error && <p className="error-text">{error}</p>}
    {loading ? <p className="member-hint">Loading tasks…</p>
      : tasks.length === 0 ? <p className="member-hint">No tasks in this chat.</p>
        : <ul className="chat-task-list">{tasks.map((task) => <li key={`${task.daily ? "d" : "o"}-${task.id}`}>
          <span className="chat-task-text"><small>{taskWhen(task)} · ID {task.id}</small>{task.prompt}</span>
          <button type="button" className="member-kick" disabled={busyTask === task.id}
            onClick={() => void deleteTask(task)}>{busyTask === task.id ? "Deleting…" : "Delete"}</button>
        </li>)}</ul>}
    <div className="chat-task-add">
      <div className="chat-task-when">
        <label className="settings-field">Repeat
          <select value={daily ? "daily" : "once"} onChange={(event) => setDaily(event.target.value === "daily")}>
            <option value="once">Once</option>
            <option value="daily">Every day</option>
          </select>
        </label>
        {daily
          ? <label className="settings-field">Time
            <input type="time" value={clock} onChange={(event) => setClock(event.target.value)} />
          </label>
          : <label className="settings-field">Run at
            <input type="datetime-local" value={runAt} onChange={(event) => setRunAt(event.target.value)} />
          </label>}
      </div>
      <label className="settings-field">Task
        <textarea rows={2} maxLength={4000} value={prompt} onChange={(event) => setPrompt(event.target.value)}
          placeholder="Example: remind everyone to submit the weekly report" />
      </label>
      <button type="button" className="secondary-button" disabled={adding || !prompt.trim()} onClick={() => void addTask()}>
        {adding ? "Adding…" : "Add task"}
      </button>
    </div>
  </section>;
}
