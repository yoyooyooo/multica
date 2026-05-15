/**
 * New project modal. Mirrors `new-issue.tsx` shape — vertical form, header
 * Cancel / Create buttons. Title is required; everything else has a default
 * (status=planned, priority=none, no lead, no description, no icon).
 *
 * Lead is intentionally NOT exposed in the create form. Web does the same:
 * lead assignment is a follow-up action because most users create the
 * project from a "I need to track this stream of work" intent and figure
 * out who's leading it later. The picker lives on the detail screen.
 *
 * On success: dismiss modal → navigate to the new project's detail page so
 * the user can immediately add a lead / attach issues / configure properties.
 */
import { useCallback, useState } from "react";
import {
  Alert,
  InteractionManager,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { Stack, router } from "expo-router";
import type { ProjectPriority, ProjectStatus } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { AutosizeTextArea } from "@/components/ui/autosize-textarea";
import {
  MIN_BODY_INPUT_HEIGHT_PX,
  MOBILE_PLACEHOLDER_COLOR,
} from "@/components/ui/input-tokens";
import { ProjectStatusIcon } from "@/components/ui/project-status-icon";
import { ProjectPriorityIcon } from "@/components/ui/project-priority-icon";
import { ProjectStatusPickerSheet } from "@/components/project/pickers/project-status-picker-sheet";
import { ProjectPriorityPickerSheet } from "@/components/project/pickers/project-priority-picker-sheet";
import {
  projectPriorityLabel,
  projectStatusLabel,
} from "@/lib/project-status";
import { useCreateProject } from "@/data/mutations/projects";
import { useWorkspaceStore } from "@/data/workspace-store";

export default function NewProject() {
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const create = useCreateProject();

  const [title, setTitle] = useState("");
  const [icon, setIcon] = useState("");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState<ProjectStatus>("planned");
  const [priority, setPriority] = useState<ProjectPriority>("none");

  const [statusOpen, setStatusOpen] = useState(false);
  const [priorityOpen, setPriorityOpen] = useState(false);

  const dirty =
    title.length > 0 ||
    icon.length > 0 ||
    description.length > 0 ||
    status !== "planned" ||
    priority !== "none";

  const canCreate = title.trim().length > 0 && !create.isPending;

  const onCancel = useCallback(() => {
    if (!dirty) {
      router.back();
      return;
    }
    Alert.alert(
      "Discard project?",
      "Your draft will be lost.",
      [
        { text: "Keep editing", style: "cancel" },
        {
          text: "Discard",
          style: "destructive",
          onPress: () => router.back(),
        },
      ],
    );
  }, [dirty]);

  const onCreate = useCallback(() => {
    if (!canCreate) return;
    create.mutate(
      {
        title: title.trim(),
        description: description.trim() || undefined,
        icon: icon.trim() || undefined,
        status,
        priority,
      },
      {
        onSuccess: (project) => {
          router.back();
          // Wait for the modal dismiss animation to finish before pushing
          // the detail screen. `InteractionManager` resolves once iOS
          // says all in-flight animations / interactions are done — more
          // robust than a hard-coded `setTimeout(150)` if iOS timing
          // changes or the device is under load.
          InteractionManager.runAfterInteractions(() => {
            if (wsSlug) router.push(`/${wsSlug}/project/${project.id}`);
          });
        },
        onError: (err) => {
          Alert.alert(
            "Failed to create project",
            err instanceof Error ? err.message : "Unknown error",
          );
        },
      },
    );
  }, [canCreate, create, title, description, icon, status, priority, wsSlug]);

  const headerLeft = useCallback(() => {
    return (
      <Pressable onPress={onCancel} className="px-1 py-1">
        <Text className="text-base text-brand">Cancel</Text>
      </Pressable>
    );
  }, [onCancel]);

  const headerRight = useCallback(() => {
    return (
      <Pressable
        onPress={onCreate}
        disabled={!canCreate}
        className={canCreate ? "px-1 py-1" : "px-1 py-1 opacity-40"}
      >
        <Text className="text-base text-brand font-semibold">
          {create.isPending ? "Creating…" : "Create"}
        </Text>
      </Pressable>
    );
  }, [canCreate, onCreate, create.isPending]);

  return (
    <>
      <Stack.Screen options={{ headerLeft, headerRight }} />
      <KeyboardAvoidingView
        className="flex-1 bg-background"
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <ScrollView
          className="flex-1"
          contentContainerClassName="px-4 pt-4 pb-6 gap-4"
          keyboardShouldPersistTaps="handled"
        >
          <Field label="Icon (emoji)">
            <TextInput
              value={icon}
              onChangeText={(v) => setIcon(v.slice(0, 4))}
              placeholder="📦"
              placeholderTextColor={MOBILE_PLACEHOLDER_COLOR}
              className="text-2xl text-foreground bg-secondary/50 rounded-md px-3 py-2 self-start min-w-[60px] text-center"
              maxLength={4}
            />
          </Field>

          <Field label="Title">
            <TextInput
              value={title}
              onChangeText={setTitle}
              placeholder="Project title"
              placeholderTextColor={MOBILE_PLACEHOLDER_COLOR}
              className="text-base text-foreground bg-secondary/50 rounded-md px-3 py-2"
              autoFocus
              returnKeyType="next"
            />
          </Field>

          <Field label="Description">
            <AutosizeTextArea
              value={description}
              onChangeText={setDescription}
              placeholder="What is this project about?"
              className="bg-secondary/50 rounded-md px-3 py-2"
              minHeight={MIN_BODY_INPUT_HEIGHT_PX}
            />
          </Field>

          <View className="flex-row gap-2">
            <View className="flex-1">
              <Field label="Status">
                <Pressable
                  onPress={() => setStatusOpen(true)}
                  className="flex-row items-center gap-2 bg-secondary/50 rounded-md px-3 py-2.5"
                >
                  <ProjectStatusIcon status={status} size={16} />
                  <Text className="text-sm text-foreground flex-1">
                    {projectStatusLabel(status)}
                  </Text>
                </Pressable>
              </Field>
            </View>
            <View className="flex-1">
              <Field label="Priority">
                <Pressable
                  onPress={() => setPriorityOpen(true)}
                  className="flex-row items-center gap-2 bg-secondary/50 rounded-md px-3 py-2.5"
                >
                  <ProjectPriorityIcon priority={priority} size={16} />
                  <Text className="text-sm text-foreground flex-1">
                    {projectPriorityLabel(priority)}
                  </Text>
                </Pressable>
              </Field>
            </View>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>

      <ProjectStatusPickerSheet
        visible={statusOpen}
        value={status}
        onChange={setStatus}
        onClose={() => setStatusOpen(false)}
      />
      <ProjectPriorityPickerSheet
        visible={priorityOpen}
        value={priority}
        onChange={setPriority}
        onClose={() => setPriorityOpen(false)}
      />
    </>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <View className="gap-1.5">
      <Text className="text-xs uppercase tracking-wider text-muted-foreground">
        {label}
      </Text>
      {children}
    </View>
  );
}
