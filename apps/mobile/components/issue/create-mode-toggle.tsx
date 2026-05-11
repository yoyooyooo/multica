/**
 * Manual / Agent segmented control for the new-issue modal header. Same
 * dichotomy as web's `useCreateModeStore` (Manual = traditional form,
 * Agent = natural-language prompt dispatched to an agent).
 *
 * Rendered via `headerTitle` in the Stack.Screen options, replacing the
 * static "New Issue" title — iOS standard for two-mode screens (Mail
 * Inbox/VIP, Reminders lists, etc).
 *
 * Phase 1: mode state lives in local useState. Phase 3 will sync to a
 * mobile-local `useCreateModeStore` (mirroring web) so the choice
 * persists across modal opens.
 */
import { Pressable, View } from "react-native";
import { Text } from "@/components/ui/text";
import { cn } from "@/lib/utils";

export type CreateMode = "manual" | "agent";

interface Props {
  mode: CreateMode;
  onChange: (next: CreateMode) => void;
}

export function CreateModeToggle({ mode, onChange }: Props) {
  return (
    <View className="flex-row rounded-full bg-secondary p-0.5">
      <Segment
        label="Manual"
        active={mode === "manual"}
        onPress={() => onChange("manual")}
      />
      <Segment
        label="Agent"
        active={mode === "agent"}
        onPress={() => onChange("agent")}
      />
    </View>
  );
}

function Segment({
  label,
  active,
  onPress,
}: {
  label: string;
  active: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      hitSlop={4}
      className={cn(
        "px-3 py-1 rounded-full",
        active ? "bg-background shadow-sm" : "active:opacity-60",
      )}
    >
      <Text
        className={cn(
          "text-xs font-medium",
          active ? "text-foreground" : "text-muted-foreground",
        )}
      >
        {label}
      </Text>
    </Pressable>
  );
}
