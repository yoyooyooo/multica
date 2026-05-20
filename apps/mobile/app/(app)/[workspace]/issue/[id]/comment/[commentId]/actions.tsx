/**
 * Per-comment action sheet — opened by long-pressing a comment body. Route
 * params identify the comment; the route reads the timeline cache to look
 * up the entry, decides per-row permissions (canEdit / canDelete / isRoot),
 * and dispatches mutations directly.
 *
 * Layout matches the iOS HIG "grouped inset list" — quick-emoji reaction
 * row + four grouped Cards:
 *   G1  Edit · Reply · Copy text · Copy link              (per-comment ops)
 *   G2  Resolve / Unresolve thread                        (root-only)
 *   G3  New issue from comment · New sub-issue            (extract flows)
 *   G4  Delete comment                                     (destructive)
 *
 * Self-contained: every action either fires a mutation + router.back(), or
 * (for navigation actions like Reply / New issue) `router.replace`s the
 * destination so the sheet doesn't stay on the back-stack behind the new
 * full-page modal.
 */
import { useCallback, useMemo } from "react";
import { Alert, Pressable, ScrollView, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { useLocalSearchParams, router } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import * as Clipboard from "expo-clipboard";
import * as Haptics from "expo-haptics";
import type { Reaction } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { Card } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import {
  issueDetailOptions,
  issueTimelineOptions,
} from "@/data/queries/issues";
import {
  useDeleteComment,
  useResolveComment,
  useToggleCommentReaction,
} from "@/data/mutations/issues";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useActorLookup } from "@/data/use-actor-name";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { QUICK_EMOJIS } from "@/lib/quick-emojis";

// Quick row shows the first 5 emojis + the overflow icon.
const QUICK_ROW_SIZE = 5;

function formatAbsoluteDate(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("en-US", {
    month: "long",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
    hour12: false,
  });
}

function summarizeContent(content: string | null | undefined): string {
  if (!content) return "(empty)";
  return content.replace(/\s+/g, " ").trim().slice(0, 80);
}

export default function CommentActionsRoute() {
  const { id, commentId, workspace: wsSlug } = useLocalSearchParams<{
    id: string;
    commentId: string;
    workspace: string;
  }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const userId = useAuthStore((s) => s.user?.id);
  const { getName } = useActorLookup();
  const { colorScheme } = useColorScheme();
  const fg = THEME[colorScheme].foreground;

  const { data: timeline = [] } = useQuery(issueTimelineOptions(wsId, id));
  const { data: issue } = useQuery(issueDetailOptions(wsId, id));
  const entry = useMemo(
    () => timeline.find((e) => e.id === commentId) ?? null,
    [timeline, commentId],
  );

  const toggleReaction = useToggleCommentReaction(id);
  const deleteComment = useDeleteComment(id);
  const resolveComment = useResolveComment(id);

  const quickEmojis = useMemo(
    () => QUICK_EMOJIS.slice(0, QUICK_ROW_SIZE),
    [],
  );

  const reactions = useMemo<Reaction[]>(
    () => (entry?.reactions ?? []) as Reaction[],
    [entry?.reactions],
  );

  const onReact = useCallback(
    (emoji: string) => {
      if (!entry) return;
      const existing = reactions.find(
        (r) =>
          r.emoji === emoji &&
          r.actor_type === "member" &&
          r.actor_id === userId,
      );
      toggleReaction.mutate({ commentId: entry.id, emoji, existing });
    },
    [entry, reactions, userId, toggleReaction],
  );

  if (!entry) {
    // Cache miss — the timeline cache should be warm because the user got
    // here by long-pressing a rendered comment, but if a WS event removed
    // the entry between push and render, fail closed and dismiss.
    router.back();
    return null;
  }

  const actorName = getName(
    entry.actor_type as "member" | "agent" | null | undefined,
    entry.actor_id,
  );

  // Permission gating mirrors the old comment-card.tsx logic: Edit/Delete
  // only on the user's own member comments; agent comments are never
  // user-editable. Resolve is root-only (server enforces).
  const isOwn =
    entry.actor_type === "member" && entry.actor_id === userId;
  const canEdit = isOwn;
  const canDelete = isOwn;
  const isRoot = !entry.parent_id;
  const resolved = !!entry.resolved_at;

  const wrap = (fn: () => void) => () => {
    router.back();
    // Microtask delay so the sheet dismiss animation starts before the next
    // navigation push / Alert / Clipboard call kicks in. Matches the
    // wrap-and-close convention from the previous CommentActionSheet.
    setTimeout(fn, 0);
  };

  const handleReply = () => {
    if (!wsSlug) return;
    router.push({
      pathname: "/[workspace]/issue/[id]/new-comment",
      params: {
        workspace: wsSlug,
        id,
        parent: entry.id,
        parentName: actorName,
      },
    });
  };

  const handleEdit = () => {
    if (!wsSlug) return;
    router.push({
      pathname: "/[workspace]/issue/[id]/new-comment",
      params: {
        workspace: wsSlug,
        id,
        edit: entry.id,
        initial: entry.content ?? "",
      },
    });
  };

  const handleCopy = async () => {
    if (entry.content) {
      await Clipboard.setStringAsync(entry.content);
      void Haptics.notificationAsync(
        Haptics.NotificationFeedbackType.Success,
      );
    }
  };

  const handleCopyLink = async () => {
    const webUrl = process.env.EXPO_PUBLIC_WEB_URL;
    if (!webUrl || !wsSlug || !issue?.identifier) return;
    const url = `${webUrl}/${wsSlug}/issue/${issue.identifier}#comment-${entry.id}`;
    await Clipboard.setStringAsync(url);
    void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Success);
  };

  const handleResolve = () => {
    resolveComment.mutate({
      commentId: entry.id,
      resolved: !entry.resolved_at,
    });
  };

  const handleNewIssue = () => {
    if (!wsSlug) return;
    router.push({
      pathname: "/[workspace]/new-issue",
      params: {
        workspace: wsSlug,
        seed_content: entry.content ?? "",
        seed_actor: actorName,
        seed_source_issue: issue?.identifier ?? "",
      },
    });
  };

  const handleNewSubIssue = () => {
    if (!wsSlug) return;
    router.push({
      pathname: "/[workspace]/new-issue",
      params: {
        workspace: wsSlug,
        seed_content: entry.content ?? "",
        seed_actor: actorName,
        seed_source_issue: issue?.identifier ?? "",
        parent_id: id,
      },
    });
  };

  const handleDelete = () => {
    Alert.alert(
      "Delete comment?",
      "This comment will be permanently deleted. Replies in the thread will also be removed. This cannot be undone.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Delete",
          style: "destructive",
          onPress: () => deleteComment.mutate(entry.id),
        },
      ],
    );
  };

  return (
    <View className="flex-1 bg-popover">
      <View className="px-4 pt-4 pb-2">
        <Text className="text-base font-semibold text-foreground">
          Comment
        </Text>
      </View>
      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerClassName="pb-4"
      >
        {/* Header — actor + content preview + absolute date */}
        <View className="px-3 pt-2">
          <Card className="p-0 rounded-2xl overflow-hidden">
            <View className="px-4 py-3 gap-1">
              <Text className="text-base text-foreground" numberOfLines={2}>
                <Text className="font-medium">{actorName}</Text>
                <Text className="text-muted-foreground">
                  : {summarizeContent(entry.content)}
                </Text>
              </Text>
              <Text className="text-xs text-muted-foreground">
                {formatAbsoluteDate(entry.created_at)}
              </Text>
            </View>
          </Card>
        </View>

        {/* Quick emoji row + overflow → full emoji picker formSheet route */}
        <View className="flex-row items-center px-4 py-3 gap-2">
          {quickEmojis.map((emoji) => (
            <Pressable
              key={emoji}
              onPress={wrap(() => onReact(emoji))}
              hitSlop={4}
              className="size-11 rounded-full bg-card border border-border items-center justify-center active:opacity-70"
            >
              <Text className="text-2xl">{emoji}</Text>
            </Pressable>
          ))}
          <Pressable
            onPress={wrap(() => {
              if (!wsSlug) return;
              router.push({
                pathname:
                  "/[workspace]/issue/[id]/comment/[commentId]/emoji-picker",
                params: { workspace: wsSlug, id, commentId: entry.id },
              });
            })}
            hitSlop={4}
            accessibilityLabel="More reactions"
            className="size-11 rounded-full bg-card border border-border items-center justify-center active:opacity-70"
          >
            <Ionicons name="add-outline" size={22} color={fg} />
          </Pressable>
        </View>

        {/* G1 — per-comment ops */}
        <View className="px-3">
          <Card className="p-0 rounded-2xl overflow-hidden">
            {canEdit ? (
              <>
                <ActionRow
                  icon="create-outline"
                  iconColor={fg}
                  label="Edit"
                  onPress={wrap(handleEdit)}
                />
                <Separator />
              </>
            ) : null}
            <ActionRow
              icon="arrow-undo-outline"
              iconColor={fg}
              label="Reply"
              onPress={wrap(handleReply)}
            />
            <Separator />
            <ActionRow
              icon="clipboard-outline"
              iconColor={fg}
              label="Copy text"
              onPress={wrap(handleCopy)}
            />
            <Separator />
            <ActionRow
              icon="link-outline"
              iconColor={fg}
              label="Copy link to comment"
              onPress={wrap(handleCopyLink)}
            />
          </Card>
        </View>

        {/* G2 — Resolve (root only) */}
        {isRoot ? (
          <View className="px-3 mt-3">
            <Card className="p-0 rounded-2xl overflow-hidden">
              <ActionRow
                icon={resolved ? "refresh-outline" : "checkmark-outline"}
                iconColor={fg}
                label={resolved ? "Unresolve thread" : "Resolve thread"}
                onPress={wrap(handleResolve)}
              />
            </Card>
          </View>
        ) : null}

        {/* G3 — extract flows */}
        <View className="px-3 mt-3">
          <Card className="p-0 rounded-2xl overflow-hidden">
            <ActionRow
              icon="open-outline"
              iconColor={fg}
              label="New issue from comment"
              onPress={wrap(handleNewIssue)}
            />
            <Separator />
            <ActionRow
              icon="duplicate-outline"
              iconColor={fg}
              label="New sub-issue from comment"
              onPress={wrap(handleNewSubIssue)}
            />
          </Card>
        </View>

        {/* G4 — destructive */}
        {canDelete ? (
          <View className="px-3 mt-3">
            <Card className="p-0 rounded-2xl overflow-hidden">
              <ActionRow
                icon="trash-outline"
                iconColor={THEME[colorScheme].destructive}
                label="Delete comment"
                labelClassName="text-destructive"
                onPress={wrap(handleDelete)}
              />
            </Card>
          </View>
        ) : null}
      </ScrollView>
    </View>
  );
}

function ActionRow({
  icon,
  iconColor,
  label,
  labelClassName,
  onPress,
}: {
  icon: React.ComponentProps<typeof Ionicons>["name"];
  iconColor: string;
  label: string;
  labelClassName?: string;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      className="flex-row items-center gap-3 px-4 py-3.5 active:bg-secondary"
    >
      <Text
        className={cn("flex-1 text-base text-foreground", labelClassName)}
      >
        {label}
      </Text>
      <Ionicons name={icon} size={20} color={iconColor} />
    </Pressable>
  );
}
