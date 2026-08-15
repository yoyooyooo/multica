# Offline Google Fonts for web image builds

## Applicability

- Category: `operations`
- Accepted source target: `fork/v0.4.22`
- Donor: Mini deployment `20260815T151931Z-fork-v0.4.22-07427b29d-mini` offline-font mock that produced `multica-web:fork-v0.4.22-07427b29d-mini-r2`

This is a source/build contract. It does not by itself change the running Mini images.

## Problem

`apps/web` uses `next/font/google`. Next 16.2.6 fetches CSS and WOFF2 from Google during `next build`. In Docker that TLS path fails; the builder can exit without `.next/standalone`, and the runner stage then copies nothing useful.

## Current behavior

The fork vendors the four families used by `apps/web` and points Next at that mock:

- `apps/web/offline-fonts/responses.cjs`
- `Dockerfile.web` sets `NEXT_FONT_GOOGLE_MOCKED_RESPONSES`
- `@multica/web` `build` always loads the same mock

Missing mock entries fail closed (`Missing mocked response for URL`). A successful image build must still contain `.next/standalone` and `.next/static`.

`apps/docs` is outside `Dockerfile.web` and is not covered.

## Source and tests

- `apps/web/offline-fonts/`
- `Dockerfile.web`
- `apps/web/offline-fonts/responses.test.ts`

## Rollback and retirement

Rollback restores the previous `Dockerfile.web` and removes the vendored mock. Retire this delta only when official upstream image builds no longer fetch Google Fonts, or when `apps/web` stops using `next/font/google`.
