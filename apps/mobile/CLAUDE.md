# Mobile App Rules (apps/mobile/)

For cross-app sharing rules, see the root `CLAUDE.md` *Sharing Principles* section. This file documents the locked tech-stack baseline and the few mobile-specific rules — so AI doesn't suggest outdated alternatives.

## What mobile may import from `packages/`

- `import type` from `@multica/core/types/*` (zero runtime coupling)
- Pure functions from `@multica/core/`

Everything else, mobile writes its own.

## Behavioral parity with web/desktop

Mobile is allowed to differ in **UI and interaction** — it's a phone, not a port. It is NOT allowed to differ in **product semantics**. Users should not get a different mental model of "what's there" depending on which client they open.

Concrete rules:

- **Counts and visibility must agree.** If web shows the user N comments on an issue under a given filter, mobile must show the same N (subject to identical pagination/coalescing rules). If mobile silently re-implements timeline grouping with different coalescing windows, mobile is wrong.
- **Permissions and access checks must agree.** "Can comment", "can change status", "can archive inbox item" — mobile decides via the same logic web does (mirrored from packages/core, not re-derived from feel).
- **State enums and transitions must agree.** Issue status set, priority set, inbox item types, comment types — mobile renders all of them (with a sensible fallback for unknown values, per "API Response Compatibility" in the root CLAUDE.md). Mobile does NOT silently drop categories.
- **Data identity must agree.** Same `id`, same `slug`, same canonical fields. Mobile does not invent its own ids or normalize differently.

**Concrete UX divergence is fine** when it preserves semantics:

- ✅ Web shows comment thread as a recursive tree; mobile shows a flat list (because phone screens). Same comments, different layout.
- ✅ Web has a sidebar workspace switcher; mobile puts it in Settings. Same switching semantics.
- ✅ Web shows inbox item read-state with a filled background; mobile uses a leading dot. Same boolean.
- ❌ Web counts both replies and parent comments in the comment count; mobile counts only top-level. **Not allowed** — same N rule.
- ❌ Web treats `status="cancelled"` as visible; mobile silently hides it. **Not allowed** — same enums rule.

When UI requires a divergence, write down at the divergence point what the rule is mirroring (point at the source function in packages/core or packages/views) and why mobile renders it differently. Future readers should be able to tell, in 30 seconds, that the mobile divergence is intentional and which web-side function is the source of truth.

### ⚠️ Incident (2026-05-09): inbox dedup missing — counts disagreed

**Symptom**: Web sidebar showed "Inbox 1" while mobile rendered 3+ unread dots on the same workspace, same user, same moment.

**Root cause**: Backend `GET /api/inbox` returns raw rows that include:
1. archived items, and
2. multiple inbox notifications per issue (a comment, a status change, and an assignment on the same issue each create one row).

Web/desktop run those raw rows through `deduplicateInboxItems` (`packages/core/inbox/queries.ts`) before rendering and before counting unread:
1. filter `archived = true` out
2. group by `issue_id`, keep the newest in each group
3. sort by `created_at` desc

Mobile's first cut rendered the raw list directly. So a single issue with 3 notifications showed as 3 rows with 3 unread dots, while web showed 1.

**Fix**: mirror `deduplicateInboxItems` into `apps/mobile/lib/inbox-display.ts`, run mobile's inbox tab through it before rendering and before any counting.

**Lesson — encode this into your reflexes when adding any new mobile screen that consumes a list endpoint**:

> Before rendering an API list response, grep `packages/core/<domain>/queries.ts` and `packages/views/<domain>/components/*.tsx` for any preprocessing — `dedupe*`, `coalesce*`, `filter*`, `*-display.ts`, `useMemo(() => transform(raw))`. Mirror everything that runs between `useQuery` and the JSX in web/desktop. **Do not assume the backend returns "what should be displayed"** — it usually returns the raw cache shape, and the client is responsible for shaping it.

This pattern repeats: timeline coalescing (`buildTimelineGroups`), inbox dedup, comment thread flattening, etc. Each one is a behavioral parity hazard if mobile skips it.

## Tech-stack baseline

Start minimal. Add to this list when actually adopted — do NOT pre-list libraries.

- **Expo SDK 55**
- **React Native 0.82**
- **React 19.1** — whatever Expo SDK 55 ships. Pinned in `apps/mobile/package.json` directly, NOT via root `catalog:`.
- **TypeScript** strict
- **Expo Router 55** (file-based routing — version aligns with Expo SDK)
- **NativeWind 4** + **Tailwind 3.4** — NativeWind 5 is unstable; stay on v4. (Note: web/desktop use Tailwind v4 — versions intentionally differ.)
- **react-native-reusables (RNR)** — the shadcn equivalent for React Native. Uses NativeWind + RN-Primitives + CVA. Component API mirrors shadcn.
- **TanStack Query 5** — mobile owns its `QueryClient` with `AppState` focus listener + `NetInfo` online listener.
- **Zustand** — mobile-local state only.
- **expo-secure-store** — auth token persistence.

When upgrading any of these, update this list.

## Visual tokens (separate from web)

Mobile maintains its own design tokens in `apps/mobile/tailwind.config.js`. You MAY reference `packages/ui/styles/tokens.css` (web/desktop tokens) as inspiration, but **do not import or symlink the file**. Tokens are transcribed by hand and may diverge for mobile (touch-friendly spacing, no hover states, native typography).

Tailwind version mismatch (mobile v3.4 vs web v4) makes file sharing impractical anyway — this isolation is intentional.

## Build & release

- **Main CI** (`.github/workflows/ci.yml`) excludes mobile via `--filter='!@multica/mobile'`. Mobile failures do NOT block web/desktop PRs.
- **Mobile verify** (`.github/workflows/mobile-verify.yml`): triggered on `apps/mobile/**` or `packages/core/types/**` changes — runs typecheck/lint/test only, no IPA build.
- **Mobile release** (`.github/workflows/mobile-release.yml`): triggered by `mobile-v*.*.*` tag → `eas build` + `eas submit`.
- **OTA** — EAS Update for JS-only fixes that don't change the runtime version. Manual / on-demand push to preview/production channels.

Mobile release cadence is decoupled from main `v*.*.*` tags (server / CLI / desktop).

## Realtime / WebSocket strategy

Mobile uses the same WS server protocol as web/desktop, but mounts subscriptions differently. The rules below exist because mobile-specific constraints (cellular data cost, AppState lifecycle, per-screen unmount cleanup, smaller cache surface) make a direct port of web's pattern wrong.

### Three-layer stack

```
Layer 1  ws-client.ts                — single socket, no React. Exponential
                                       backoff with full jitter. Three-state
                                       lifecycle (idle / active / paused) so
                                       the provider can pause on background
                                       and resume on foreground without
                                       racing the auto-reconnect timer.
Layer 2  realtime-provider.tsx       — owns the WSClient. Mounts/unmounts on
                                       auth + workspace + AppState + NetInfo
                                       changes. Exposes useWSClient().
Layer 3  use-<feature>-realtime.ts   — per-feature subscriptions. Translate
                                       events → cache mutations.
```

Layer 3 is what changes per feature; layers 1 and 2 are infrastructure and shouldn't be edited when adding event coverage.

### Mount strategy: list-level global, per-record per-screen

Mobile **does NOT use a single centralized `useRealtimeSync` hook** like `packages/core/realtime/use-realtime-sync.ts`. That pattern is fine on web (one tab = one mount, lives forever) but on mobile it gets in the way: most events care about a single record (one issue's comments, one chat session's messages), and the hook needs to know which record without prop-drilling.

Two mount tiers:

- **Listing-level (always-on for the workspace session)** — mount inside the `<RealtimeSubscriptions />` component in `app/(app)/[workspace]/_layout.tsx`. These don't take parameters; they patch caches keyed only on `wsId`. Examples: `useInboxRealtime`, `useMyIssuesRealtime`. Both run from the moment the user enters a workspace until they leave it, regardless of which tab is foregrounded.

- **Per-record (mounted with id, cleans up on unmount)** — mount inside the screen that owns the record, parameterized by the id from the route. Example: `useIssueRealtime(id, () => router.back())` in `issue/[id].tsx`. The hook filters every event by `payload.issue_id === id` and only patches the current issue's caches. When the user navigates away the `useEffect` cleanup unsubscribes all listeners, so a backgrounded screen doesn't keep mutating caches it no longer owns.

Don't mount a per-record hook globally to "just be safe" — every filter call on every event then runs N times where N is the number of issues a user has ever opened in this session.

### Patch over invalidate (cellular-data rule)

When a WS payload contains the full updated object, **patch** the cache (`setQueryData` / `setQueriesData`). Only fall back to **invalidate** when:

1. The payload is just an id (we don't know the full new shape — e.g., `issue:created` with no scope context).
2. The cache shape doesn't match what we can patch (e.g., multi-key scope-filtered lists where we'd have to predict membership).
3. The event is rare enough that the extra refetch isn't a real cost (e.g., `issue:deleted` on a list that was about to invalidate anyway).
4. After a reconnect, where we may have missed events while disconnected.

Web is fine to invalidate generously because most users are on broadband; mobile users on cellular pay for each refetch. A `setQueryData` is free; an `invalidateQueries` is a network roundtrip per affected query key.

### Mobile-owned updaters (don't import `packages/core/issues/ws-updaters.ts`)

Mobile has its own `apps/mobile/data/realtime/issue-ws-updaters.ts` even though web has a near-identical file. **Do not import web's updaters into mobile.** Two reasons:

1. **Key-factory binding.** Web's updaters reference `issueKeys` from `packages/core/issues/queries.ts` — a different runtime instance from mobile's `apps/mobile/data/queries/issue-keys.ts`. TanStack Query compares keys structurally so it *appears* to work, but binding cache mutation to a foreign key factory invites silent drift the moment either side adjusts its key shape (renames a segment, adds a discriminator).
2. **Cache-shape divergence.** Mobile has simpler caches: flat `Issue[]` for my-issues (web has status-bucketed); no children subtree (web does); no label-byIssue cache (web does). Web's updaters carry conditional dead-code for paths mobile doesn't have, and mobile would silently no-op on web shapes that don't exist locally.

When the same logic needs to exist on both sides, copy the design — not the import. Document the mirror at the top of the mobile file (see `issue-ws-updaters.ts` for the pattern).

### Event-always-wins (optimistic conflict policy)

Mutations like `useUpdateIssue` apply an optimistic patch to the detail cache, then the server processes the request and broadcasts `issue:updated`. If a separate WS event (from another client / another user / an agent) arrives between the optimistic patch and the mutation response, the WS handler overwrites the optimistic state with the server's authoritative state. Brief UI flicker is acceptable; correctness wins.

**Do not** add timestamp-comparison logic to "protect" the optimistic state — the server is the truth and the user benefits from seeing real changes immediately. If a specific event proves problematic in practice, add the gate at that point, not by default.

### Reconnect handling

Each hook registers a single `ws.onReconnect(cb)` that invalidates **only the queries it owns**:

| Hook | Invalidates on reconnect |
|---|---|
| `useInboxRealtime` | `["inbox", wsId]` |
| `useMyIssuesRealtime` | `issueKeys.myAll(wsId)` |
| `useIssueRealtime(id)` | `issueKeys.detail(wsId, id)` + `issueKeys.timeline(wsId, id)` |

No global "invalidate everything on reconnect" sweep. The fanout would be every screen the user has ever visited in this session refetching simultaneously — wasteful on cellular and prone to rate-limiting the server in low-signal areas where reconnects happen frequently.

### Adding new event coverage — recipe

1. **Read the payload.** Find the event in `@multica/core/types/events.ts`. Note the fields; decide if patch is possible (full object) or invalidate is required (just an id).
2. **Mirror, don't import.** If web has an updater for this event in `packages/core/<feature>/ws-updaters.ts`, copy the design into `apps/mobile/data/realtime/<feature>-ws-updaters.ts`. Adapt to mobile's actual cache shapes — don't carry web's bucket/children/childProgress dead-code if mobile doesn't have those caches.
3. **Subscribe in a hook.** Either extend an existing `use-<feature>-realtime.ts` or create a new one. Filter by id at the top of each handler so per-record hooks ignore unrelated events.
4. **Mount it.** Listing-level → add to `<RealtimeSubscriptions />` in workspace `_layout.tsx`. Per-record → add to the owning screen's body, parameterized by the route id.
5. **Add reconnect invalidate.** Single `ws.onReconnect()` call scoped to the hook's own keys.
6. **Verify cross-client.** Open the affected screen on mobile, change the same record from a second client (web or another device), confirm mobile updates within ~500ms without pull-to-refresh.

If a new event has no consumer on mobile (e.g., `subscriber:added` when mobile doesn't render subscriber lists yet), **don't subscribe**. Mounting a listener with no UI consumer adds CPU on every fire for zero user benefit.

## Lessons learned (encode into reflexes)

These are real mistakes that have been made building the mobile shell. Each one cost time to find. Treat as enforceable rules, not suggestions.

### 1. Install/upgrade any dependency: check `dist-tags` first

Do NOT hardcode version numbers from memory. Run `pnpm view <pkg> dist-tags` to see `latest / sdk-XX / canary` and decide which tag to lock. For Expo packages (`expo-*` / `react-native-*` that Expo aligns), use `pnpm exec expo install <pkg>` — it queries Expo's dependency manifest and picks the SDK-compatible version. `pnpm add <pkg>` will silently install the npm `latest`, which often outpaces the SDK and breaks at runtime. Past mistakes: hardcoded `expo@~54.0.0` (latest was already `55.x`); installed `lucide-react-native@0.468` without checking React 19 peer compatibility.

### 2. New source subdirectory: verify git tracking

Every time you create a new source subdirectory under `apps/mobile/` (e.g. `data/`, `lib/foo/`, `components/inbox/`):

1. Run `git check-ignore -v <dir>/<file>` immediately. The repo-root `.gitignore` has generic rules (`data/`, `build/`, `bin/`, `*.app`, `*.dmg`) that are intended for backend runtime/output dirs but will silently swallow mobile source.
2. If a rule matches, add `!<dir>/` and `!<dir>/**` to `apps/mobile/.gitignore` (subtree override beats parent rule).
3. After the commit lands, run `git ls-files <dir>` to confirm every file is tracked.

This rule exists because `apps/mobile/data/` was once committed-but-not-tracked — 14 source files (ApiClient, all queries, all stores) were missing from the git tree even though `git status` was clean. Local builds worked because Metro reads the filesystem; CI / clones would have died.

### 3. ApiClient capability list (4 must-haves)

Mobile's fetch wrapper (`apps/mobile/data/api.ts`) MUST implement all four. Missing any of them is a bug, not a deferred polish item.

1. **Zod `parseWithFallback` for response validation.** Strictly enforced by the root CLAUDE.md "API Response Compatibility" section and the "Type drift defense" section above. **Any new endpoint method that does `as T` on the response body is a bug.** Reuse schemas from `packages/core/api/schemas.ts` (pure Zod exports, on the mobile sharing whitelist); define mobile-side fallbacks for new endpoints in `apps/mobile/data/`.

2. **`onUnauthorized` 401 callback.** The `ApiClientOptions.onUnauthorized` hook fires on every 401 and must be wired in `app/_layout.tsx` to: clear auth token, clear workspace store, clear TanStack Query cache, navigate to `/login`. Without it a session that expired server-side puts every subsequent request into a 401 loop and the user sees opaque "API error: 401" toasts on every screen. Use a `signingOutRef` to make the callback idempotent — multiple in-flight requests will all 401 simultaneously when a session expires.

3. **`X-Request-ID` per request.** Generate a short random ID (`createRequestId()` in `apps/mobile/lib/request-id.ts`), send as `X-Request-ID` header. The same ID goes into client-side log lines so backend telemetry can be cross-referenced (server picks it up via the same header).

4. **Structured request logger.** Two log lines per request: `[api] → METHOD path` (start, with `rid`) and `[api] ← STATUS path` (end, with `rid` + `duration`). Use `console.error` for 5xx, `console.warn` for 404s, `console.log` for success. Without this, debugging mobile API issues means staring at the React Native Network panel; with it, the dev console is self-explanatory and prod telemetry already comes structured.

**What mobile correctly does NOT need (don't add these):** CSRF token (`X-CSRF-Token`), `credentials: "include"`, cookie reading. Mobile is Bearer-token auth, not cookie auth — the cookie attack surface that requires CSRF protection on web doesn't exist on mobile.

### 4. Visual alignment is baseline, not polish

When implementing a mobile screen / row / list:

1. Open the web/desktop equivalent source file (e.g. `packages/views/inbox/components/inbox-list-item.tsx`) and compare its JSX structure side-by-side with the mobile JSX you're about to write.
2. Run a screenshot of the web/desktop view next to a screenshot of the simulator.
3. The four items below are **baseline**, not polish for a later iteration:
   - **Tab bar must have icons** (Ionicons / SF Symbols / lucide-react-native) with focused/unfocused state switch.
   - **Each screen has a title at the top** (Stack large title, or a custom `ScreenHeader`).
   - **Row's right-side elements stack vertically into a column** when there are multiple (status above, time below). Pattern: nested flex-rows, each with its own right-aligned element. NOT a single horizontal flex-row with status and time competing for the same trailing slot.
   - **Secondary lines must use a type-aware label component** (mirror, e.g., `InboxDetailLabel`'s type switch). Rendering raw `item.body` directly leaks server-side markdown markers (`##`, `*`) and stale debug strings into the UI.

Skipping any of these in a "first cut" turns the v1 into something that prompts a "you didn't care about interaction at all" review — every time. Easier to do them up-front (15 min total) than to retrofit.

### 5. Every read query must pass `signal` to fetch; api.ts always has a hard timeout

**Symptom that triggered the rule (2026-05-11)**: Inbox screen sometimes returned to the foreground showing the FlatList pull-to-refresh spinner stuck indefinitely. List items were rendered underneath, but `isRefetching` never flipped back to `false`. Pull-to-refresh, navigating away, and re-opening the tab did not clear it.

**Root cause**: `apps/mobile/data/api.ts`'s `fetch()` had no timeout, no `AbortController`, and no caller-`signal` plumbing. iOS suspends backgrounded apps within ~30 seconds and can silently kill in-flight network tasks (facebook/react-native#35384 — "iOS fetch() POST fails if called too soon, with app running in background"; facebook/react-native#38711 — "JS Timers don't fire when app is launched in background"). When the app foregrounded, the suspended fetch's Promise neither resolved nor rejected. TanStack Query saw an existing query still in `fetching` state and did NOT start a new fetch on invalidate — it just waited on the dead Promise forever. `isRefetching` stayed `true`, the FlatList spinner stayed spinning.

**Rule, three parts (every one is required — partial fixes leave a footgun)**:

**1. `api.ts` `fetch()` MUST have a hard timeout** (currently 30s; the `FETCH_TIMEOUT_MS` constant). Without this, a single suspended request can wedge a query indefinitely. Use a manual `AbortController` + `setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS)` — **DO NOT** use `AbortSignal.timeout()`: Hermes throws `TypeError: AbortSignal.timeout is not a function` (facebook/react-native#42042). Same for `AbortSignal.any()` — Hermes does not implement it (livekit/livekit#4014). To combine the timeout signal with a caller-supplied signal, attach an `"abort"` event listener manually and forward to the inner controller.

**2. Every read-side `api.ts` method MUST accept `opts?: { signal?: AbortSignal }` and pass it to `fetch()`**. Mutations don't need this (TanStack Query doesn't pass a signal to `mutationFn`). The pattern:
```ts
async listInbox(opts?: { signal?: AbortSignal }): Promise<InboxItem[]> {
  return this.fetch<InboxItem[]>("/api/inbox", { signal: opts?.signal });
}
```
Adding a new query-bound method without `opts` is a bug — the next person who writes a `queryFn` will silently drop the signal.

**3. Every `queryFn` MUST forward the signal it receives from TanStack Query**. The official TanStack guide (tanstack.com/query/v5/docs/framework/react/guides/query-cancellation) states: "When a query becomes out-of-date or inactive, this `signal` will become aborted." The pattern:
```ts
queryOptions({
  queryKey: [...],
  queryFn: ({ signal }) => api.listInbox({ signal }),
});
```
Forgetting the destructure (writing `() => api.listInbox()`) defeats every benefit of (1) and (2): TQ can't cancel hung requests when the user navigates away, and on workspace switch every stale request lives until its 30s timeout.

**Verification**: After any change to `api.ts` or a new query addition, `grep -n "queryFn: () =>" apps/mobile/data/queries/` should return zero matches. Every `queryFn` should destructure `{ signal }`.

**Why the wiring already in `data/query-client.ts` (focusManager + AppState, onlineManager + NetInfo) is not enough on its own**: focusManager triggers a *refetch attempt* when the app comes back to the foreground, but if the prior fetch promise is hanging, TQ won't start a new request — it'll keep waiting on the dead one. Only timeout + signal cancellation actually unwedges the query. The three pieces work together: signal lets TQ proactively cancel on staleness, timeout is the safety net when nothing else fires, focusManager is the "user came back, let's recheck" trigger.
