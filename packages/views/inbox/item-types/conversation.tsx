"use client";

import { MessageCircle } from "lucide-react";
import { useChatStore } from "@multica/core/chat";
import { useActorName } from "@multica/core/workspace/hooks";
import { ActorAvatar } from "../../common/actor-avatar";
import { useTimeAgo } from "../components/inbox-list-item";
import { registerInboxItemType, type InboxItemRowProps } from "./contract";

/**
 * Renderer for the `conversation` kind — an agent {@link ChatSession} surfaced
 * inline in the inbox feed. Selecting it opens the shared chat window (Multica
 * chat is a floating surface), so this kind has no detail pane of its own; the
 * row is the whole renderer.
 */
function ConversationRow({ entry, isSelected, onSelect }: InboxItemRowProps) {
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const setOpen = useChatStore((s) => s.setOpen);
  const { getActorName } = useActorName();
  const timeAgo = useTimeAgo();

  if (entry.kind !== "conversation") return null;
  const session = entry.conversation;

  return (
    <button
      type="button"
      onClick={() => {
        // Open the conversation in the shared chat window rather than the
        // inbox detail pane.
        setActiveSession(session.id);
        setOpen(true);
        onSelect();
      }}
      className={`group flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors ${
        isSelected ? "bg-accent" : "hover:bg-accent/50"
      }`}
    >
      <ActorAvatar actorType="agent" actorId={session.agent_id} size={28} enableHoverCard />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-1.5">
            {entry.unread && (
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-brand" />
            )}
            <span
              className={`truncate text-sm ${entry.unread ? "font-medium" : "text-muted-foreground"}`}
            >
              {session.title}
            </span>
          </div>
          <MessageCircle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        </div>
        <div className="mt-0.5 flex items-center justify-between gap-2">
          <p className="min-w-0 overflow-hidden text-ellipsis whitespace-nowrap text-xs text-muted-foreground">
            {getActorName("agent", session.agent_id)}
          </p>
          <span className="shrink-0 text-xs text-muted-foreground">
            {timeAgo(session.updated_at)}
          </span>
        </div>
      </div>
    </button>
  );
}

registerInboxItemType({
  kind: "conversation",
  Row: ConversationRow,
});
