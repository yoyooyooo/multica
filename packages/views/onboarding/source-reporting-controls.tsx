"use client";

import { useId } from "react";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

export function SourceReportingControls({
  domainConsent,
  onDomainConsentChange,
  className,
}: {
  domainConsent: boolean;
  onDomainConsentChange: (enabled: boolean) => void;
  className?: string;
}) {
  const { t } = useT("onboarding");
  const labelId = useId();
  const descriptionId = useId();

  return (
    <section
      className={cn(
        "rounded-lg border bg-muted/30 p-4",
        className,
      )}
      aria-labelledby={`${labelId}-title`}
    >
      <div>
        <h2 id={`${labelId}-title`} className="text-sm font-medium text-foreground">
          {t(($) => $.source_reporting.summary.title)}
        </h2>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {t(($) => $.source_reporting.summary.description)}
        </p>
      </div>

      <div className="mt-4 flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div id={labelId} className="text-sm font-medium text-foreground">
            {t(($) => $.source_reporting.domain.label)}
          </div>
          <p
            id={descriptionId}
            className="mt-1 text-xs leading-relaxed text-muted-foreground"
          >
            {t(($) => $.source_reporting.domain.description)}
          </p>
        </div>
        <Switch
          checked={domainConsent}
          onCheckedChange={onDomainConsentChange}
          aria-labelledby={labelId}
          aria-describedby={descriptionId}
          className="mt-0.5"
        />
      </div>
    </section>
  );
}
