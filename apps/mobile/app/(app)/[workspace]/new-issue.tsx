/**
 * New issue creation modal.
 *
 * Modes (mirrors web's `useCreateModeStore`):
 *  - Manual: title + description + status/priority/assignee chips.
 *            Fully wired to `apiClient.issues.create` via useCreateIssue().
 *            Description supports inline `@mention` (members + agents).
 *  - Agent:  natural-language prompt + agent picker (placeholder; Phase 3
 *            wires the real picker + apiClient.quickCreateIssue).
 *
 * Manual-mode state lives at the top level (title / description / status /
 * priority / assignee / mention markers / selection / mentioning). The
 * `ManualPanel` is a controlled child — submit button lives in the Stack
 * header so it needs to read `canSubmit` and call `onSubmit` from this
 * scope.
 *
 * Mention pattern mirrors `comment-composer.tsx` exactly:
 *   1. `onChangeText` + `onSelectionChange` recompute `tokenAtCursor` to
 *      detect an active `@query` at the caret.
 *   2. `MentionSuggestionBar` renders above the chip row when there's an
 *      active mention; picking calls `insertMention` and pushes to markers.
 *   3. On submit, `serializeMentions(description, markers)` produces the
 *      canonical `[@name](mention://type/id)` markdown — same wire format
 *      as web's Tiptap mention extension. Server's util.ParseMentions and
 *      mobile's mention-chip both parse it back.
 *
 * Defaults (status="todo", priority="none") mirror web's ManualCreatePanel —
 * "Behavioral parity" rule (apps/mobile/CLAUDE.md).
 */
import { useCallback, useMemo, useState } from "react";
import {
  Alert,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  TextInput,
  View,
  type NativeSyntheticEvent,
  type TextInputSelectionChangeEventData,
} from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { Stack, router } from "expo-router";
import type { IssuePriority, IssueStatus } from "@multica/core/types";
import { SubmitIssueButton } from "@/components/issue/submit-issue-button";
import { CreateFormAttributeRow } from "@/components/issue/create-form-attribute-row";
import {
  CreateModeToggle,
  type CreateMode,
} from "@/components/issue/create-mode-toggle";
import type { AssigneeValue } from "@/components/issue/pickers/assignee-picker-sheet";
import { MentionSuggestionBar } from "@/components/issue/mention-suggestion-bar";
import { Text } from "@/components/ui/text";
import { useCreateIssue } from "@/data/mutations/issues";
import {
  insertMention,
  serializeMentions,
  tokenAtCursor,
  type MentionMarker,
} from "@/lib/mention-serialize";

interface MentioningState {
  start: number;
  query: string;
}

export default function NewIssueModal() {
  const [mode, setMode] = useState<CreateMode>("manual");

  // Manual mode fields
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState<IssueStatus>("todo");
  const [priority, setPriority] = useState<IssuePriority>("none");
  const [assignee, setAssignee] = useState<AssigneeValue>(null);

  // Mention state (description only — web doesn't allow mentions in title)
  const [markers, setMarkers] = useState<MentionMarker[]>([]);
  const [selection, setSelection] = useState<{ start: number; end: number }>({
    start: 0,
    end: 0,
  });
  const [mentioning, setMentioning] = useState<MentioningState | null>(null);

  // Agent mode fields (Phase 3 wires the picker)
  const [prompt, setPrompt] = useState("");
  const [agentId] = useState<string | null>(null);

  const createIssue = useCreateIssue();
  const isSubmitting = createIssue.isPending;

  const canSubmit =
    !isSubmitting &&
    (mode === "manual"
      ? title.trim().length > 0
      : prompt.trim().length > 0);

  const recomputeMentioning = useCallback(
    (text: string, cursor: number) => {
      const token = tokenAtCursor(text, cursor);
      if (token) {
        setMentioning({ start: token.start, query: token.query });
      } else if (mentioning) {
        setMentioning(null);
      }
    },
    [mentioning],
  );

  const onDescriptionChange = useCallback(
    (next: string) => {
      setDescription(next);
      recomputeMentioning(next, selection.end);
    },
    [recomputeMentioning, selection.end],
  );

  const onDescriptionSelectionChange = useCallback(
    (e: NativeSyntheticEvent<TextInputSelectionChangeEventData>) => {
      const sel = e.nativeEvent.selection;
      setSelection(sel);
      recomputeMentioning(description, sel.end);
    },
    [recomputeMentioning, description],
  );

  const onSelectMention = useCallback(
    (mention: MentionMarker) => {
      if (!mentioning) return;
      const { newText, newSelection, marker } = insertMention(
        description,
        { start: mentioning.start, queryLength: mentioning.query.length },
        mention,
      );
      setDescription(newText);
      setSelection(newSelection);
      setMarkers((prev) => [...prev, marker]);
      setMentioning(null);
    },
    [mentioning, description],
  );

  const onSubmit = useCallback(async () => {
    if (mode === "manual") {
      const trimmedTitle = title.trim();
      if (trimmedTitle.length === 0) return;
      const finalDescription = serializeMentions(description, markers).trim();
      try {
        await createIssue.mutateAsync({
          title: trimmedTitle,
          description: finalDescription || undefined,
          status,
          priority,
          ...(assignee
            ? { assignee_type: assignee.type, assignee_id: assignee.id }
            : {}),
        });
        router.back();
      } catch (err) {
        Alert.alert(
          "Failed to create issue",
          err instanceof Error ? err.message : "Unknown error",
        );
      }
    } else {
      // Agent mode — Phase 3 swaps this for apiClient.quickCreateIssue.
      if (prompt.trim().length === 0) return;
      console.log("[new-issue] submit (agent)", {
        prompt: prompt.trim(),
        agent_id: agentId,
      });
      router.back();
    }
  }, [
    mode,
    title,
    description,
    markers,
    status,
    priority,
    assignee,
    prompt,
    agentId,
    createIssue,
  ]);

  const headerRight = useMemo(() => {
    function HeaderRight() {
      return (
        <SubmitIssueButton
          disabled={!canSubmit}
          loading={isSubmitting}
          onPress={onSubmit}
        />
      );
    }
    return HeaderRight;
  }, [canSubmit, isSubmitting, onSubmit]);

  const headerTitle = useMemo(() => {
    function HeaderTitle() {
      return <CreateModeToggle mode={mode} onChange={setMode} />;
    }
    return HeaderTitle;
  }, [mode]);

  return (
    <>
      <Stack.Screen options={{ headerRight, headerTitle }} />
      <KeyboardAvoidingView
        className="flex-1 bg-background"
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        {mode === "manual" ? (
          <ManualPanel
            title={title}
            onTitleChange={setTitle}
            description={description}
            onDescriptionChange={onDescriptionChange}
            descriptionSelection={selection}
            onDescriptionSelectionChange={onDescriptionSelectionChange}
            status={status}
            onStatusChange={setStatus}
            priority={priority}
            onPriorityChange={setPriority}
            assignee={assignee}
            onAssigneeChange={setAssignee}
            mentioning={mentioning}
            onSelectMention={onSelectMention}
            submitting={isSubmitting}
          />
        ) : (
          <AgentPanel prompt={prompt} onPromptChange={setPrompt} />
        )}
      </KeyboardAvoidingView>
    </>
  );
}

function ManualPanel({
  title,
  onTitleChange,
  description,
  onDescriptionChange,
  descriptionSelection,
  onDescriptionSelectionChange,
  status,
  onStatusChange,
  priority,
  onPriorityChange,
  assignee,
  onAssigneeChange,
  mentioning,
  onSelectMention,
  submitting,
}: {
  title: string;
  onTitleChange: (next: string) => void;
  description: string;
  onDescriptionChange: (next: string) => void;
  descriptionSelection: { start: number; end: number };
  onDescriptionSelectionChange: (
    e: NativeSyntheticEvent<TextInputSelectionChangeEventData>,
  ) => void;
  status: IssueStatus;
  onStatusChange: (next: IssueStatus) => void;
  priority: IssuePriority;
  onPriorityChange: (next: IssuePriority) => void;
  assignee: AssigneeValue;
  onAssigneeChange: (next: AssigneeValue) => void;
  mentioning: MentioningState | null;
  onSelectMention: (mention: MentionMarker) => void;
  submitting: boolean;
}) {
  return (
    <>
      <ScrollView
        className="flex-1"
        contentContainerClassName="px-4 pt-4 pb-2 gap-2"
        keyboardShouldPersistTaps="handled"
      >
        <TextInput
          value={title}
          onChangeText={onTitleChange}
          placeholder="Issue title"
          placeholderTextColor="#a1a1aa"
          className="text-2xl font-semibold text-foreground py-2"
          autoFocus
          returnKeyType="next"
          editable={!submitting}
        />
        <TextInput
          value={description}
          onChangeText={onDescriptionChange}
          selection={descriptionSelection}
          onSelectionChange={onDescriptionSelectionChange}
          placeholder="Description… (type @ to mention)"
          placeholderTextColor="#a1a1aa"
          className="text-base text-foreground py-2 min-h-[120px]"
          multiline
          textAlignVertical="top"
          editable={!submitting}
        />
      </ScrollView>

      <View className="border-t border-border bg-background">
        <MentionSuggestionBar
          visible={mentioning !== null}
          query={mentioning?.query ?? ""}
          onSelect={onSelectMention}
        />
        <CreateFormAttributeRow
          status={status}
          onStatusChange={onStatusChange}
          priority={priority}
          onPriorityChange={onPriorityChange}
          assignee={assignee}
          onAssigneeChange={onAssigneeChange}
        />
      </View>
    </>
  );
}

function AgentPanel({
  prompt,
  onPromptChange,
}: {
  prompt: string;
  onPromptChange: (next: string) => void;
}) {
  return (
    <>
      <ScrollView
        className="flex-1"
        contentContainerClassName="px-4 pt-4 pb-2"
        keyboardShouldPersistTaps="handled"
      >
        <TextInput
          value={prompt}
          onChangeText={onPromptChange}
          placeholder="Describe what you want done…"
          placeholderTextColor="#a1a1aa"
          className="text-base text-foreground py-2 min-h-[160px]"
          autoFocus
          multiline
          textAlignVertical="top"
        />
      </ScrollView>

      <View className="border-t border-border bg-background px-4 py-3">
        {/* Phase 3 will replace this with a real agent picker sheet. */}
        <Pressable
          onPress={() => {
            console.log("[new-issue] agent picker — Phase 3");
          }}
          className="flex-row items-center gap-2 px-3 py-2 rounded-full border border-dashed border-muted-foreground/30 self-start active:bg-secondary"
          hitSlop={4}
        >
          <Ionicons name="sparkles-outline" size={14} color="#a1a1aa" />
          <Text className="text-xs text-muted-foreground">
            Agent: Select
          </Text>
          <Ionicons name="chevron-down" size={12} color="#a1a1aa" />
        </Pressable>
      </View>
    </>
  );
}

