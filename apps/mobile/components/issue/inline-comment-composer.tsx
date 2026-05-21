/**
 * Inline comment composer that sits at the bottom of the issue detail
 * screen. Replaces the previous modal-route composer (`new-comment.tsx`)
 * — a phone keyboard already gives the composer dedicated real estate,
 * so the extra route + draft store + edit/reply modes were overhead with
 * no UX win.
 *
 * Layout:
 *   - `KeyboardStickyView` keeps the whole strip pinned just above the
 *     keyboard when focused, drops it back to the safe-area bottom when
 *     blurred. Matches iMessage / Slack iOS / Things composer pattern.
 *   - `<MentionSuggestionBar>` sits above the input row. It returns null
 *     when no `@<query>` token is active so it doesn't reserve space.
 *   - One `<TextInput multiline>`, no toolbar, no attach buttons.
 *
 * Mention pipeline reuses `useMentionInput` + `MentionSuggestionBar`
 * verbatim — the same hook/component the legacy modal used, and the
 * same one that `new-issue.tsx` and `chat-composer.tsx` use.
 */
import { useCallback } from "react";
import { Pressable, TextInput, View } from "react-native";
import { KeyboardStickyView } from "react-native-keyboard-controller";
import { Ionicons } from "@expo/vector-icons";
import { useMentionInput } from "@/lib/use-mention-input";
import { MentionSuggestionBar } from "@/components/issue/mention-suggestion-bar";
import { useCreateComment } from "@/data/mutations/issues";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";

export function InlineCommentComposer({ issueId }: { issueId: string }) {
  const mention = useMentionInput();
  const createComment = useCreateComment(issueId);
  const { colorScheme } = useColorScheme();
  const theme = THEME[colorScheme];

  const canSend =
    mention.text.trim().length > 0 && !createComment.isPending;

  const onSend = useCallback(async () => {
    if (!canSend) return;
    const snap = mention.snapshot();
    const content = mention.serialize().trim();
    // Optimistic clear so the user can keep typing the next message
    // immediately. Mutation already does optimistic insert into the
    // timeline cache, so the row appears as we clear.
    mention.reset();
    try {
      await createComment.mutateAsync({ content });
    } catch {
      // Failure path: restore the user's text so nothing is lost. The
      // mutation's own onError already rolls back the optimistic timeline
      // insert + surfaces the failed-comment row.
      mention.restore(snap);
    }
  }, [canSend, mention, createComment]);

  return (
    <KeyboardStickyView offset={{ closed: 0, opened: 0 }}>
      {/* Suggestion bar renders null when no active `@<query>` — costs
       *  zero space in the common case. */}
      <MentionSuggestionBar {...mention.suggestionBar} />

      <View className="flex-row items-end gap-2 px-3 py-2 border-t border-border bg-background">
        <TextInput
          value={mention.text}
          onChangeText={mention.handlers.onChangeText}
          selection={mention.selection}
          onSelectionChange={mention.handlers.onSelectionChange}
          placeholder="Add a comment…"
          placeholderTextColor={theme.mutedForeground}
          multiline
          className="flex-1 px-3 py-2 rounded-2xl bg-secondary text-base text-foreground"
          style={{ maxHeight: 120, textAlignVertical: "top" }}
        />
        <Pressable
          onPress={onSend}
          disabled={!canSend}
          hitSlop={6}
          className="h-9 w-9 items-center justify-center"
          accessibilityRole="button"
          accessibilityLabel="Send comment"
          accessibilityState={{ disabled: !canSend }}
        >
          <Ionicons
            name="arrow-up-circle"
            size={30}
            color={canSend ? theme.primary : theme.mutedForeground}
          />
        </Pressable>
      </View>
    </KeyboardStickyView>
  );
}
