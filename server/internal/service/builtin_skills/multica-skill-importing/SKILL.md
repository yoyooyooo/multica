---
name: multica-skill-importing
description: Use when a user provides a skill URL or asks to import/install a skill into Multica. Teaches the Multica workspace import API/CLI path, returned data, duplicate handling, and agent binding; never treats external local installers as the final Multica install.
user-invocable: false
allowed-tools: Bash(multica *)
---

# Importing skills into Multica

Use this skill when the user already provided a skill URL, slug, or clear intent to
import a specific skill into the current Multica workspace.

Do not use this skill to decide which skill the user needs. If the user only
describes a capability and no URL is known, load `multica-skill-discovery` first.

## The invariant

A skill is not installed for Multica until it exists in the Multica workspace
skill database. The supported final path is:

```bash
multica skill import --url <url> --output json
```

That CLI calls the workspace API:

```text
POST /api/skills/import
body: { "url": "<url>" }
```

Do not finish with `npx skills add`. That installs into an external/local skill
environment, not the Multica workspace DB, and it will not be managed or bound by
Multica.

## Supported URL sources

Use `multica skill import --url <url> --output json` for these source families:

```bash
multica skill import --url clawhub.ai/owner/skill --output json
multica skill import --url skills.sh/owner/repo/skill --output json
multica skill import --url github.com/owner/repo --output json
multica skill import --url github.com/owner/repo/tree/main/path/to/skill --output json
multica skill import --url github.com/owner/repo/blob/main/path/to/SKILL.md --output json
```

The backend also accepts a bare ClawHub slug in the same import path.

## Direct URL flow

1. If the user provided a URL, do not search first. Import it directly:

```bash
multica skill import --url <url> --output json
```

2. Treat a successful response as the source of truth. Report the relevant fields:

- `id`
- `name`
- `description`
- `config.origin`
- `files` / files count
- `created_at` / `updated_at`

3. If the user wants an agent to use the skill, bind the returned skill id:

```bash
multica agent skills set <agent-id> --skill-ids <skill-id>
```

Do not claim the skill is available to an agent until you have bound it or
verified that it is already bound.

## Duplicate imports

Duplicate imports return `409`. On current servers, the response includes the
existing workspace skill identity:

```json
{
  "error": "a skill with this name already exists",
  "existing_skill": {
    "id": "<skill-id>",
    "name": "<skill-name>"
  }
}
```

`multica skill import --url <url> --output json` prints that structured conflict
body and exits successfully for this duplicate case. Treat `existing_skill.id` and
`existing_skill.name` as the source of truth, then fetch details if needed:

```bash
multica skill get <skill-id> --output json
```

For legacy servers or old CLIs that only return a string like `a skill with this
name already exists`, recover by finding the existing workspace skill:

```bash
multica skill list --output json
multica skill get <skill-id> --output json
```

Then report that the skill already exists and include the existing `id` / `name`.
Do not retry in a loop and do not create a second skill with a different name just
to avoid the conflict.

## Incorrect → correct

Incorrect:

```bash
npx skills add https://skills.sh/owner/repo/skill
```

That bypasses Multica. The skill may exist locally, but Multica cannot manage it
as a workspace skill.

Correct:

```bash
multica skill import --url https://skills.sh/owner/repo/skill --output json
```

Correct follow-up when the skill must be available to an agent:

```bash
multica agent skills set <agent-id> --skill-ids <skill-id>
```

## Source of truth

- `server/internal/handler/skill.go` implements `ImportSkill` for `/api/skills/import`.
- `server/cmd/multica/cmd_skill.go` implements `multica skill import --url`.
- The import response is a workspace `SkillResponse`, so agents can read returned
  fields instead of guessing whether the import succeeded.
