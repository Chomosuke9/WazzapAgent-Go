import { describe, expect, it, vi } from "vitest";
import { normalisePingEvent, normaliseWhatsAppSessionEvent, subscribeToPing, subscribeToWhatsAppSession, type EventSource } from "./backend";

describe("backend event adapter", () => {
  it("accepts only the safe app:ping payload", () => {
    expect(normalisePingEvent({ data: { message: "Pong", sequence: 3 } })).toEqual({ message: "Pong", sequence: 3 });
    expect(normalisePingEvent({ data: { message: "Pong", sequence: "3" } })).toBeNull();
  });

  it("returns the runtime unsubscribe function", () => {
    let callback: ((event: { data: unknown }) => void) | undefined;
    const unsubscribe = vi.fn();
    const source: EventSource = { On: vi.fn((_name, handler) => { callback = handler; return unsubscribe; }) };
    const received: number[] = [];
    const stop = subscribeToPing(source, (event) => received.push(event.sequence));
    callback?.({ data: { message: "Pong", sequence: 1 } });
    stop();
    expect(received).toEqual([1]);
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("accepts only a structured WhatsApp session event", () => {
    const event = { operationID: "op-1", status: { bindingState: "paired", runtimeState: "connected", sessionPresent: true } };
    expect(normaliseWhatsAppSessionEvent({ data: event })).toEqual(event);
    expect(normaliseWhatsAppSessionEvent({ data: { operationID: 5, status: {} } })).toBeNull();
  });

  it("unsubscribes from WhatsApp session updates", () => {
    let callback: ((event: { data: unknown }) => void) | undefined;
    const unsubscribe = vi.fn();
    const source: EventSource = { On: vi.fn((_name, handler) => { callback = handler; return unsubscribe; }) };
    const received: string[] = [];
    const stop = subscribeToWhatsAppSession(source, (event) => received.push(event.operationID));
    callback?.({ data: { operationID: "op-2", status: { bindingState: "unpaired", runtimeState: "pairing", sessionPresent: false } } });
    stop();
    expect(received).toEqual(["op-2"]);
    expect(unsubscribe).toHaveBeenCalledOnce();
  });
});
