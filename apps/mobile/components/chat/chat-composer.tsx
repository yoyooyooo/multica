/**
 * Bottom chat input — floating-card style (Linear / iMessage / Slack
 * idiom). One multiline TextInput on top, an inline toolbar row inside
 * the same card with `@` on the left and Send / Stop on the right.
 *
 * Differences vs. `comment-composer.tsx` (the sibling for issue comments):
 *   1. No MarkdownToolbar — chat is natural-language conversation, not
 *      structured discussion. No list / checkbox / code / quote buttons.
 *   2. No `replyingTo` chip — chat is a flat conversation.
 *   3. Send swaps to Stop while `sending===true`, giving one mid-row
 *      affordance to interrupt the agent. Crossfaded via reanimated.
 *   4. v1 cuts file upload — only `@` mention is wired here.
 *
 * Draft persistence is delegated to the caller (see chat.tsx's
 * useChatDraftsStore). Composer is stateless w.r.t. session id.
 *
 * Keyboard avoidance + bottom safe-area are handled by the parent
 * (KeyboardAvoidingView + SafeAreaView edges={["top","bottom"]} in
 * app/(app)/[workspace]/(tabs)/chat.tsx) — this component just lays out
 * the card and trusts the parent.
 */
import { Pressable, View } from "react-native";
import { useEffect, useState } from "react";
import * as Haptics from "expo-haptics";
import { Image } from "expo-image";
import Animated, { FadeIn, FadeOut } from "react-native-reanimated";
import { AutosizeTextArea } from "@/components/ui/autosize-textarea";
import { useMentionInput } from "@/lib/use-mention-input";
import { MentionSuggestionBar } from "@/components/issue/mention-suggestion-bar";
import { cn } from "@/lib/utils";

interface Props {
  /** Current draft text (controlled). Empty string = no draft. */
  value: string;
  /** Fired on every keystroke. The caller writes to the drafts store. */
  onChangeText: (next: string) => void;
  /** Send the serialised markdown content. Caller resets the input by
   *  setting `value=""` after a successful send. */
  onSend: (content: string) => Promise<void> | void;
  /** Cancel the in-flight agent task. Only callable while `sending===true`. */
  onStop: () => void;
  /** True while an agent task is running for the active session. The
   *  composer still accepts typing (user can queue the next message) but
   *  swaps the Send button for a Stop button. */
  sending: boolean;
  /** Hard-disable typing + send. Used when there's no usable agent in the
   *  workspace or the session is archived (legacy). */
  disabled?: boolean;
  /** When `disabled` is true, replaces the placeholder with the reason. */
  disabledReason?: string;
}

const IS_IOS = process.env.EXPO_OS === "ios";

export function ChatComposer({
  value,
  onChangeText,
  onSend,
  onStop,
  sending,
  disabled = false,
  disabledReason,
}: Props) {
  const mention = useMentionInput();
  const [focused, setFocused] = useState(false);

  // Drive the mention hook from the controlled `value`. When the parent
  // resets (post-send) or rehydrates a saved draft (post session-switch),
  // sync the internal text. Push-only — onChangeText is the upward
  // signal — to avoid an infinite ping-pong loop.
  useEffect(() => {
    if (mention.text !== value) {
      mention.reset();
      if (value) {
        mention.insertAtCursor(value);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- text managed by mention
  }, [value]);

  const trimmed = mention.text.trim();
  const canSend = !disabled && !sending && trimmed.length > 0;

  const placeholder = disabled
    ? disabledReason ?? "Chat unavailable"
    : sending
      ? "Agent is working…"
      : "Message…";

  async function handleSend() {
    if (!canSend) return;
    const content = mention.serialize().trim();
    if (!content) return;
    if (IS_IOS) {
      void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
    }
    // Optimistic clear — the parent's draft store mirrors `value` and will
    // see "" on the next onChangeText; the visual reset is immediate.
    onChangeText("");
    mention.reset();
    try {
      await onSend(content);
    } catch {
      // Restore the text so the user doesn't lose what they typed. Push
      // through onChangeText so the drafts store gets it too.
      onChangeText(content);
    }
  }

  function handleStop() {
    if (IS_IOS) {
      void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Medium);
    }
    onStop();
  }

  return (
    <View className="px-3 pb-2">
      <MentionSuggestionBar {...mention.suggestionBar} mode="chat" />
      <View
        className={cn(
          "rounded-3xl border bg-secondary",
          focused ? "border-primary/30" : "border-border",
          disabled && "opacity-60",
        )}
        style={{ borderCurve: "continuous" }}
      >
        <AutosizeTextArea
          value={mention.text}
          onChangeText={(next) => {
            mention.handlers.onChangeText(next);
            onChangeText(next);
          }}
          selection={mention.selection}
          onSelectionChange={mention.handlers.onSelectionChange}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          placeholder={placeholder}
          className="px-4 pt-3 pb-1"
          editable={!disabled}
        />
        <View className="flex-row items-center px-2 pb-2 pt-1">
          <Pressable
            onPress={mention.handlers.onAtButtonPress}
            disabled={disabled || sending}
            className="h-8 w-8 items-center justify-center rounded-full active:opacity-60"
            hitSlop={6}
            accessibilityRole="button"
            accessibilityLabel="Mention"
          >
            <Image
              source="sf:at"
              tintColor="#71717a"
              style={{ width: 18, height: 18 }}
            />
          </Pressable>
          <View className="flex-1" />
          {sending ? (
            <Animated.View
              key="stop"
              entering={FadeIn.duration(120)}
              exiting={FadeOut.duration(120)}
            >
              <Pressable
                onPress={handleStop}
                className="h-8 w-8 items-center justify-center rounded-full bg-foreground active:opacity-80"
                hitSlop={8}
                accessibilityRole="button"
                accessibilityLabel="Stop agent"
              >
                <View className="h-3 w-3 rounded-sm bg-background" />
              </Pressable>
            </Animated.View>
          ) : (
            <Animated.View
              key="send"
              entering={FadeIn.duration(120)}
              exiting={FadeOut.duration(120)}
            >
              <Pressable
                onPress={handleSend}
                disabled={!canSend}
                className={cn(
                  "h-8 w-8 items-center justify-center rounded-full",
                  canSend
                    ? "bg-primary active:opacity-80"
                    : "bg-background",
                )}
                hitSlop={8}
                accessibilityRole="button"
                accessibilityLabel="Send"
                accessibilityState={{ disabled: !canSend }}
              >
                <Image
                  source="sf:arrow.up"
                  tintColor={canSend ? "#ffffff" : "#a1a1aa"}
                  style={{ width: 16, height: 16 }}
                />
              </Pressable>
            </Animated.View>
          )}
        </View>
      </View>
    </View>
  );
}
