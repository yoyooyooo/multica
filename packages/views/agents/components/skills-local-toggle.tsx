"use client";

import type { AgentSkillsLocal } from "@multica/core/types";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../i18n";

// SkillsLocalToggle renders the per-agent "merge host-machine skills" switch
// shared between the Create dialog (uncontrolled-by-network, only emits the
// new value) and the edit tab (which immediately persists). The component
// stays headless about save behavior — the parent decides what to do on
// change. Section labels follow the create dialog `skills_section` keys so
// both surfaces share identical copy.
export function SkillsLocalToggle({
  value,
  onChange,
  disabled,
  hintScope = "create",
}: {
  value: AgentSkillsLocal;
  onChange: (next: AgentSkillsLocal) => void;
  disabled?: boolean;
  /**
   * Picks the i18n namespace for the secondary hint copy. The create dialog
   * lives under `create_dialog.skills_section.local_*` while the edit tab
   * uses `tab_body.skills.local_*` — wording is intentionally identical
   * today but kept in two places so each surface can evolve independently.
   */
  hintScope?: "create" | "tab";
}) {
  const { t } = useT("agents");
  const checked = value === "merge";

  const label =
    hintScope === "create"
      ? t(($) => $.create_dialog.skills_section.local_label)
      : t(($) => $.tab_body.skills.local_label);
  const hint = checked
    ? hintScope === "create"
      ? t(($) => $.create_dialog.skills_section.local_hint_on)
      : t(($) => $.tab_body.skills.local_hint_on)
    : hintScope === "create"
      ? t(($) => $.create_dialog.skills_section.local_hint_off)
      : t(($) => $.tab_body.skills.local_hint_off);

  return (
    <div className="rounded-md border border-border bg-muted/30 px-3 py-2.5">
      <div className="flex items-start justify-between gap-3">
        <Label
          htmlFor="skills-local-toggle"
          className="cursor-pointer text-sm font-medium leading-tight"
        >
          {label}
        </Label>
        <Switch
          id="skills-local-toggle"
          checked={checked}
          onCheckedChange={(next: boolean) => onChange(next ? "merge" : "ignore")}
          disabled={disabled}
        />
      </div>
      <p className="mt-1.5 text-xs leading-relaxed text-muted-foreground">
        {hint}
      </p>
    </div>
  );
}
