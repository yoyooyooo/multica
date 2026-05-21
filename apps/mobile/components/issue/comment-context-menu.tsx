/**
 * Native iOS context menu for a comment bubble. Wraps children with
 * `ContextMenuView` from `react-native-ios-context-menu`, which hands the
 * long-press gesture to UIKit's `UIContextMenuInteraction` — system blur,
 * snapshot scale, and the menu chrome all run on UIKit, not on JS.
 *
 * Why this replaces the old `comment/[commentId]/actions.tsx` formSheet
 * route: a JS `Pressable.onLongPress` racing against the underlying
 * `UITextView` selection magnifier produced ghost gestures, a non-iOS
 * "tap → route → sheet" cadence, and a two-long-press path to copy text.
 * Letting UIKit own the long-press eliminates the race by construction
 * (gesture recognizer arbitration happens inside UIKit, not via JS prop
 * propagation through nested native views), and matches the iMessage /
 * Telegram / Slack iOS standard: one long-press → menu → one tap.
 *
 * The "Select text" path is preserved verbatim — the menu has a dedicated
 * item that flips `useCommentSelectStore.selectingId` to this comment's id.
 * `CommentBody` reads that store and, when matched, (a) drops this
 * ContextMenuView wrapper so UIKit no longer owns the long-press, and
 * (b) sets `selectable={true}` on the Markdown body so the next long-press
 * fires the native UIKit selection magnifier (handles, Copy/Look Up
 * callout). Same iOS 26 iMessage "Select" behaviour, but now triggered
 * from a UIContextMenu item instead of a route — no sheet, no flicker.
 *
 * Reactions: the auxiliary preview (experimental in the lib) renders the
 * 5 quick emojis + a "+" button above the bubble snapshot — iMessage
 * Tapback parity. Tapping any emoji toggles the reaction and dismisses
 * the menu; the "+" pushes the existing `emoji-picker` formSheet route
 * for the full set.
 */
import { useCallback, useMemo, type ReactNode } from "react";
import { Alert, Pressable, View } from "react-native";
import { router } from "expo-router";
import {
  ContextMenuView,
  type MenuConfig,
  type MenuElementConfig,
} from "react-native-ios-context-menu";
import * as Clipboard from "expo-clipboard";
import * as Haptics from "expo-haptics";
import type { Reaction, TimelineEntry } from "@multica/core/types";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useCommentSelectStore } from "@/data/comment-select-store";
import {
  useDeleteComment,
  useResolveComment,
  useToggleCommentReaction,
} from "@/data/mutations/issues";
import { useActorLookup } from "@/data/use-actor-name";
import { QUICK_EMOJIS } from "@/lib/quick-emojis";
import { Text } from "@/components/ui/text";

// Quick-react row size matches the prior action-sheet row (first 5 of 8).
// Aligned with web's QUICK_EMOJIS slice in the comment toolbar so muscle
// memory carries across clients.
const QUICK_ROW_SIZE = 5;

// Action key registry — kept here as constants so the switch in
// `onPressMenuItem` and the menu config can never drift.
const KEY = {
  REPLY: "reply",
  EDIT: "edit",
  COPY_TEXT: "copy-text",
  SELECT_TEXT: "select-text",
  COPY_LINK: "copy-link",
  RESOLVE: "resolve",
  NEW_ISSUE: "new-issue",
  NEW_SUB_ISSUE: "new-sub-issue",
  MORE_REACTIONS: "more-reactions",
  DELETE: "delete",
} as const;

interface Props {
  entry: TimelineEntry;
  issueId: string;
  /** Human-readable issue identifier (e.g. `MUL-123`). Used to build the
   *  shareable web URL for "Copy link to comment". Optional — the menu
   *  item is hidden when missing. */
  issueIdentifier: string | undefined;
  children: ReactNode;
}

export function CommentContextMenu({
  entry,
  issueId,
  issueIdentifier,
  children,
}: Props) {
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const userId = useAuthStore((s) => s.user?.id);
  const { getName } = useActorLookup();
  const toggleReaction = useToggleCommentReaction(issueId);
  const deleteComment = useDeleteComment(issueId);
  const resolveComment = useResolveComment(issueId);

  // Same permission gating the old actions route applied. Server enforces
  // the same rules; this just hides items the user can't act on.
  const isOwn =
    entry.actor_type === "member" && entry.actor_id === userId;
  const isRoot = !entry.parent_id;
  const resolved = !!entry.resolved_at;
  const hasContent = !!entry.content;
  const webUrl = process.env.EXPO_PUBLIC_WEB_URL;
  const canCopyLink = !!(webUrl && wsSlug && issueIdentifier);

  const reactions = useMemo<Reaction[]>(
    () => (entry.reactions ?? []) as Reaction[],
    [entry.reactions],
  );

  const onReact = useCallback(
    (emoji: string) => {
      const existing = reactions.find(
        (r) =>
          r.emoji === emoji &&
          r.actor_type === "member" &&
          r.actor_id === userId,
      );
      toggleReaction.mutate({ commentId: entry.id, emoji, existing });
    },
    [reactions, userId, toggleReaction, entry.id],
  );

  const actorName = getName(
    entry.actor_type as "member" | "agent" | null | undefined,
    entry.actor_id,
  );

  // UIMenu nests submenus visually as inline sections when
  // `menuOptions: ['displayInline']` is set — same effect as iMessage's
  // grouped sections (primary actions / thread state / extract / destructive).
  // We build the menu top-down so the visual order matches the order of
  // declarations here.
  const menuConfig = useMemo<MenuConfig>(() => {
    const primary: MenuElementConfig[] = [];
    primary.push({
      actionKey: KEY.REPLY,
      actionTitle: "Reply",
      icon: { iconType: "SYSTEM", iconValue: "arrowshape.turn.up.left" },
    });
    if (isOwn) {
      primary.push({
        actionKey: KEY.EDIT,
        actionTitle: "Edit",
        icon: { iconType: "SYSTEM", iconValue: "pencil" },
      });
    }
    if (hasContent) {
      primary.push({
        actionKey: KEY.COPY_TEXT,
        actionTitle: "Copy",
        icon: { iconType: "SYSTEM", iconValue: "doc.on.doc" },
      });
      primary.push({
        actionKey: KEY.SELECT_TEXT,
        actionTitle: "Select Text",
        icon: { iconType: "SYSTEM", iconValue: "selection.pin.in.out" },
      });
    }
    if (canCopyLink) {
      primary.push({
        actionKey: KEY.COPY_LINK,
        actionTitle: "Copy Link",
        icon: { iconType: "SYSTEM", iconValue: "link" },
      });
    }

    const items: MenuElementConfig[] = [
      {
        menuTitle: "",
        menuOptions: ["displayInline"],
        menuItems: primary,
      },
    ];

    if (isRoot) {
      items.push({
        menuTitle: "",
        menuOptions: ["displayInline"],
        menuItems: [
          {
            actionKey: KEY.RESOLVE,
            actionTitle: resolved ? "Unresolve Thread" : "Resolve Thread",
            icon: {
              iconType: "SYSTEM",
              iconValue: resolved
                ? "arrow.uturn.backward"
                : "checkmark.circle",
            },
          },
        ],
      });
    }

    items.push({
      menuTitle: "",
      menuOptions: ["displayInline"],
      menuItems: [
        {
          actionKey: KEY.NEW_ISSUE,
          actionTitle: "New Issue From Comment",
          icon: {
            iconType: "SYSTEM",
            iconValue: "square.and.pencil",
          },
        },
        {
          actionKey: KEY.NEW_SUB_ISSUE,
          actionTitle: "New Sub-Issue From Comment",
          icon: {
            iconType: "SYSTEM",
            iconValue: "square.on.square",
          },
        },
      ],
    });

    items.push({
      menuTitle: "",
      menuOptions: ["displayInline"],
      menuItems: [
        {
          actionKey: KEY.MORE_REACTIONS,
          actionTitle: "More Reactions…",
          icon: { iconType: "SYSTEM", iconValue: "face.smiling" },
        },
      ],
    });

    if (isOwn) {
      items.push({
        menuTitle: "",
        menuOptions: ["displayInline"],
        menuItems: [
          {
            actionKey: KEY.DELETE,
            actionTitle: "Delete",
            menuAttributes: ["destructive"],
            icon: { iconType: "SYSTEM", iconValue: "trash" },
          },
        ],
      });
    }

    return {
      // UIMenu omits an empty title from its chrome — keeps the menu visually
      // tight against the bubble snapshot, like iMessage.
      menuTitle: "",
      menuItems: items,
    };
  }, [isOwn, hasContent, canCopyLink, isRoot, resolved]);

  const handlePressMenuItem = useCallback(
    ({ nativeEvent }: { nativeEvent: { actionKey: string } }) => {
      switch (nativeEvent.actionKey) {
        case KEY.REPLY:
          // Reply / Edit will be reconnected to the inline composer in a
          // follow-up — the modal composer they used to push is gone.
          Alert.alert("Reply", "Threaded replies are coming soon.");
          return;

        case KEY.EDIT:
          Alert.alert("Edit", "Editing your comment is coming soon.");
          return;

        case KEY.COPY_TEXT:
          if (entry.content) {
            void Clipboard.setStringAsync(entry.content);
            void Haptics.notificationAsync(
              Haptics.NotificationFeedbackType.Success,
            );
          }
          return;

        case KEY.SELECT_TEXT:
          // Flip the store synchronously — CommentBody re-renders without
          // its ContextMenuView wrapper and with `selectable={true}` on the
          // Markdown so the next long-press fires UIKit's native selection
          // magnifier. No race: the menu has already dismissed itself by
          // the time `onPressMenuItem` fires.
          useCommentSelectStore.getState().setSelecting(entry.id);
          return;

        case KEY.COPY_LINK: {
          if (!canCopyLink) return;
          const url = `${webUrl}/${wsSlug}/issue/${issueIdentifier}#comment-${entry.id}`;
          void Clipboard.setStringAsync(url);
          void Haptics.notificationAsync(
            Haptics.NotificationFeedbackType.Success,
          );
          return;
        }

        case KEY.RESOLVE:
          resolveComment.mutate({
            commentId: entry.id,
            resolved: !entry.resolved_at,
          });
          return;

        case KEY.NEW_ISSUE:
          if (!wsSlug) return;
          router.push({
            pathname: "/[workspace]/new-issue",
            params: {
              workspace: wsSlug,
              seed_content: entry.content ?? "",
              seed_actor: actorName,
              seed_source_issue: issueIdentifier ?? "",
            },
          });
          return;

        case KEY.NEW_SUB_ISSUE:
          if (!wsSlug) return;
          router.push({
            pathname: "/[workspace]/new-issue",
            params: {
              workspace: wsSlug,
              seed_content: entry.content ?? "",
              seed_actor: actorName,
              seed_source_issue: issueIdentifier ?? "",
              parent_id: issueId,
            },
          });
          return;

        case KEY.MORE_REACTIONS:
          if (!wsSlug) return;
          router.push({
            pathname:
              "/[workspace]/issue/[id]/comment/[commentId]/emoji-picker",
            params: {
              workspace: wsSlug,
              id: issueId,
              commentId: entry.id,
            },
          });
          return;

        case KEY.DELETE:
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
          return;
      }
    },
    [
      wsSlug,
      issueId,
      issueIdentifier,
      entry.id,
      entry.content,
      entry.resolved_at,
      actorName,
      canCopyLink,
      webUrl,
      resolveComment,
      deleteComment,
    ],
  );

  return (
    <ContextMenuView
      menuConfig={menuConfig}
      onPressMenuItem={handlePressMenuItem}
      isAuxiliaryPreviewEnabled
      auxiliaryPreviewConfig={{
        // Anchor reactions row above the bubble snapshot, leading-aligned —
        // matches iMessage Tapback's position for incoming bubbles. (UIKit
        // flips it for outgoing if needed, but our card layout keeps all
        // bubbles left-aligned, so leading is correct universally.)
        horizontalAlignment: "targetLeading",
        // Fade the row in alongside the menu's own spring entrance —
        // anything else feels late.
        transitionConfigEntrance: {
          mode: "syncedToMenuEntranceTransition",
          shouldAnimateSize: true,
        },
        transitionExitPreset: { mode: "fade" },
      }}
      renderAuxiliaryPreview={() => (
        <View className="flex-row items-center gap-2 bg-card rounded-full px-2 py-1.5 self-start">
          {QUICK_EMOJIS.slice(0, QUICK_ROW_SIZE).map((emoji) => (
            <Pressable
              key={emoji}
              onPress={() => onReact(emoji)}
              hitSlop={4}
              className="size-9 rounded-full items-center justify-center active:bg-secondary"
            >
              <Text className="text-xl">{emoji}</Text>
            </Pressable>
          ))}
        </View>
      )}
    >
      {children}
    </ContextMenuView>
  );
}
