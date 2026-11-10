# --- Dependencies ---
FROM node:22-alpine AS deps
WORKDIR /app

COPY pnpm-lock.yaml pnpm-workspace.yaml package.json turbo.json .npmrc ./
COPY apps/web/package.json apps/web/
COPY apps/web/source.config.ts apps/web/source.config.ts
COPY packages/core/package.json packages/core/
COPY packages/ui/package.json packages/ui/
COPY packages/views/package.json packages/views/
COPY packages/tsconfig/package.json packages/tsconfig/
COPY packages/eslint-config/package.json packages/eslint-config/

RUN corepack enable && \
    PNPM_VERSION="$(node -p 'require("./package.json").packageManager')" && \
    corepack prepare "$PNPM_VERSION" --activate
RUN pnpm install --frozen-lockfile

# --- Build ---
FROM node:22-alpine AS builder
WORKDIR /app

COPY package.json ./
RUN corepack enable && \
    PNPM_VERSION="$(node -p 'require("./package.json").packageManager')" && \
    corepack prepare "$PNPM_VERSION" --activate
COPY --from=deps /app ./
COPY package.json turbo.json pnpm-workspace.yaml ./
COPY apps/web/ apps/web/
COPY packages/ packages/
RUN pnpm install --frozen-lockfile --offline

ARG NEXT_PUBLIC_APP_VERSION=dev
ENV NEXT_PUBLIC_APP_VERSION=$NEXT_PUBLIC_APP_VERSION
ENV STANDALONE=true
RUN pnpm --filter @multica/web build && \
    test -d /app/apps/web/.next/standalone && \
    test -d /app/apps/web/.next/static && \
    mkdir -p /font-licenses && \
    cp apps/web/node_modules/@fontsource-variable/inter/LICENSE /font-licenses/Inter-OFL-1.1.txt && \
    cp apps/web/node_modules/@fontsource-variable/geist-mono/LICENSE /font-licenses/Geist-Mono-OFL-1.1.txt && \
    cp apps/web/node_modules/@fontsource-variable/source-serif-4/LICENSE /font-licenses/Source-Serif-4-OFL-1.1.txt && \
    cp apps/web/node_modules/@fontsource/instrument-serif/LICENSE /font-licenses/Instrument-Serif-OFL-1.1.txt

# --- Runtime ---
FROM node:22-alpine AS runner
WORKDIR /app

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
LABEL org.opencontainers.image.title="Multica Fork Web" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"

ENV NODE_ENV=production
ENV REMOTE_API_URL=http://backend:8080
RUN addgroup --system --gid 1001 nodejs && adduser --system --uid 1001 nextjs

COPY --from=builder --chown=nextjs:nodejs /app/apps/web/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/apps/web/.next/static ./apps/web/.next/static
COPY --from=builder --chown=nextjs:nodejs /app/apps/web/public ./apps/web/public
COPY --from=builder --chown=nextjs:nodejs /font-licenses /usr/share/licenses/multica-fonts
COPY --chown=nextjs:nodejs LICENSE NOTICE ./

USER nextjs
EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
CMD ["node", "apps/web/server.js"]
