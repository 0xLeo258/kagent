import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetChatClient, setChatClientFactory } from "../chat";
import type { ChatClient, ChatEvent } from "../chat/types";
import { useChat } from "./useChat";

afterEach(resetChatClient);

describe("read-only conversation access", () => {
  it("loads history but blocks sends, HITL replies, retry and cancellation", async () => {
    const sent = vi.fn();
    const cancel = vi.fn(async () => {});
    const transport: ChatClient = {
      protocolVersion: "test",
      history: async () => ({
        messages: [
          {
            id: "message-1",
            role: "agent",
            createdAt: "2026-09-07T09:00:00Z",
            parts: [{ kind: "text", text: "Stored result" }],
          },
        ],
        awaitingReply: {
          kind: "ask_user",
          taskId: "task-1",
          requestId: "question-1",
          questions: [
            { question: "Continue?", choices: ["Yes"], multiple: false },
          ],
        },
      }),
      send: () =>
        (async function* (): AsyncIterable<ChatEvent> {
          sent();
          yield { type: "status", state: "completed", taskId: "task-1" };
        })(),
      cancel,
    };
    setChatClientFactory(() => transport);
    const beforeSend = vi.fn(async () => {});
    const { result, rerender } = renderHook(
      ({ readOnly }) =>
        useChat({ id: "scheduled" }, beforeSend, {
          readOnly,
        }),
      { initialProps: { readOnly: true } },
    );
    await waitFor(() => expect(result.current.isLoadingHistory).toBe(false));
    expect(result.current.messages[0].parts).toEqual([
      { kind: "text", text: "Stored result" },
    ]);
    await act(async () => {
      await result.current.send("hello");
      await result.current.answerQuestion([["Yes"]]);
      await result.current.retry();
      await result.current.dismissQuestion();
      await result.current.cancel();
    });
    expect(sent).not.toHaveBeenCalled();
    expect(cancel).not.toHaveBeenCalled();
    expect(beforeSend).not.toHaveBeenCalled();
    rerender({ readOnly: false });
    await act(async () => {
      await result.current.answerQuestion([["Yes"]]);
    });
    expect(sent).toHaveBeenCalledTimes(1);
    expect(beforeSend).toHaveBeenCalledTimes(1);
  });
});
