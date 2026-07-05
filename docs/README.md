# Multica Docs Index

这是本仓库的文档入口。本文档遵循“源码优先、当前/未来分离”的维护原则：

- **当前能力**：只描述当前源码和迁移中已经存在的行为。
- **未来计划**：必须明确标注为 Future / Roadmap，不和当前能力混写。
- **Fork-specific 能力**：如果只存在于本 fork，文档标题或正文需要说明适用范围，避免读者误以为已在 upstream 发布。

## 当前文档

- [External PR Integration](./external-pr-integration.md) — 本 fork 新增的 provider-neutral 外部 PR/MR/change 绑定能力；AGS 是第一个 provider。
- [Feature Flags](./feature-flags.md)
- [Custom Runtimes](./custom-runtimes.md)
- [Analytics](./analytics.md)
- [Product Overview](./product-overview.md)

## 规划 / 执行文档

- [Docs Outline](./docs-outline.md)
- [Docs Rewrite Plan](./docs-rewrite-plan.md)
- [Agent Quick Create Plan](./agent-quick-create-plan.md)
- [Onboarding Refactor Plan](./onboarding-refactor-plan.md)
- [Timezone Architecture RFC](./timezone-architecture-rfc.md)

## 维护约定

1. 修改 API、表结构、任务环境变量或状态流转时，同步更新对应文档。
2. 不把“想做”的能力写进当前能力章节；需要写时放进 `Future / Roadmap`。
3. 外部集成优先沉淀通用语义，具体 provider 差异尽量通过配置、环境变量或 provider profile 表达。
