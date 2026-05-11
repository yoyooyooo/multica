/**
 * Bottom chip row + picker sheets for the new-issue form. Mirrors
 * `attribute-row.tsx`'s visual pattern but operates on form state
 * (controlled props + setters) instead of an `issue` object + mutation.
 *
 * Phase 1: status / priority / assignee. Phase 2 adds label, project,
 * due_date, parent as additional chips appended to the same row — the
 * horizontal ScrollView already handles overflow.
 *
 * Reuses (zero-modification):
 *  - StatusPickerSheet / PriorityPickerSheet / AssigneePickerSheet
 *  - AttributeChip
 *  - StatusIcon / PriorityIcon / ActorAvatar
 *
 * Chip "value present" rule: a chip is `filled` when the form value
 * differs from the default (todo / none / null). When at default it
 * renders `dimmed` with a placeholder label ("Priority", "Assignee").
 */
import { useState } from "react";
import { ScrollView, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import type { IssuePriority, IssueStatus } from "@multica/core/types";
import { AttributeChip } from "@/components/issue/attribute-chip";
import {
  AssigneePickerSheet,
  type AssigneeValue,
} from "@/components/issue/pickers/assignee-picker-sheet";
import { PriorityPickerSheet } from "@/components/issue/pickers/priority-picker-sheet";
import { StatusPickerSheet } from "@/components/issue/pickers/status-picker-sheet";
import { ActorAvatar } from "@/components/ui/actor-avatar";
import { PriorityIcon } from "@/components/ui/priority-icon";
import { StatusIcon } from "@/components/ui/status-icon";
import { useActorLookup } from "@/data/use-actor-name";
import { PRIORITY_LABEL, STATUS_LABEL } from "@/lib/issue-status";

interface Props {
  status: IssueStatus;
  onStatusChange: (next: IssueStatus) => void;
  priority: IssuePriority;
  onPriorityChange: (next: IssuePriority) => void;
  assignee: AssigneeValue;
  onAssigneeChange: (next: AssigneeValue) => void;
}

export function CreateFormAttributeRow({
  status,
  onStatusChange,
  priority,
  onPriorityChange,
  assignee,
  onAssigneeChange,
}: Props) {
  const [statusOpen, setStatusOpen] = useState(false);
  const [priorityOpen, setPriorityOpen] = useState(false);
  const [assigneeOpen, setAssigneeOpen] = useState(false);

  const { getName } = useActorLookup();
  const assigneeLabel = assignee
    ? getName(assignee.type, assignee.id)
    : "Assignee";
  const priorityLabel =
    priority === "none" ? "Priority" : PRIORITY_LABEL[priority];

  return (
    <View>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerClassName="flex-row gap-2 px-4 py-3"
      >
        <AttributeChip
          icon={<StatusIcon status={status} size={12} />}
          label={STATUS_LABEL[status]}
          variant="filled"
          onPress={() => setStatusOpen(true)}
        />
        <AttributeChip
          icon={<PriorityIcon priority={priority} />}
          label={priorityLabel}
          variant={priority === "none" ? "dimmed" : "filled"}
          onPress={() => setPriorityOpen(true)}
        />
        <AttributeChip
          icon={
            assignee ? (
              <ActorAvatar
                type={assignee.type}
                id={assignee.id}
                size={16}
              />
            ) : (
              <Ionicons
                name="person-circle-outline"
                size={16}
                color="#a1a1aa"
              />
            )
          }
          label={assigneeLabel}
          variant={assignee ? "filled" : "dimmed"}
          onPress={() => setAssigneeOpen(true)}
        />
      </ScrollView>

      <StatusPickerSheet
        visible={statusOpen}
        value={status}
        onChange={onStatusChange}
        onClose={() => setStatusOpen(false)}
      />
      <PriorityPickerSheet
        visible={priorityOpen}
        value={priority}
        onChange={onPriorityChange}
        onClose={() => setPriorityOpen(false)}
      />
      <AssigneePickerSheet
        visible={assigneeOpen}
        value={assignee}
        onChange={onAssigneeChange}
        onClose={() => setAssigneeOpen(false)}
      />
    </View>
  );
}
