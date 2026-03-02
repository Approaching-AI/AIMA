# /commit — 全量文档更新 + 提交 + 推送

当用户执行 `/commit` 时，按以下严格顺序操作：

## Phase 1: 文档审查与同步

> **核心原则：代码改了什么，文档就要跟着审查什么。**
> 不是机械更新固定文件，而是根据改动影响范围，审查所有可能失效的文档。

### 1.1 分析本次改动
- 运行 `git diff --stat` 和 `git diff` 查看所有改动
- 理解改动的范围、影响面、涉及了哪些子系统

### 1.2 全局文档审查
根据改动影响，逐项检查以下文档是否需要修订。**只改需要改的，不碰不相关的。**

#### 架构与设计文档
- `design/ARCHITECTURE.md` — 改动是否影响了系统架构、模块边界、数据流、接口定义？
  - 新增子系统/模块 → 补充架构图和模块说明
  - 改变了模块间交互方式 → 更新数据流描述
  - 新增/修改架构不变量 → 更新 §14 不变量列表
- `design/PRD.md` / `design/MRD.md` — 改动是否实现了新需求或改变了产品行为？

#### 项目开发指南
- `CLAUDE.md` — 改动是否影响了以下内容？
  - MCP 工具数量或列表 → 更新工具计数
  - 项目目录结构（新包、新目录）→ 更新 Project Structure
  - Go 约定或设计模式 → 补充新模式
  - CLI 命令行为变化（如默认值改变）→ 更新 Key Commands 或相关说明
  - 机器注册表 → 更新 Machine Registry
  - **不要更新**: 生产就绪审查章节、验证矩阵等历史快照

#### 跨 session 经验记忆
- `~/.claude/projects/C--Users-jguan-projects-aima/memory/MEMORY.md`
  - 新功能、重要 bug 修复、值得记住的经验 → 添加或更新条目
  - 保持简洁，总行数控制在 200 行以内
  - 内容太长 → 拆到独立的 `memory/<topic>.md` 并在 MEMORY.md 引用

### 1.3 总结经验到 skills（如有必要）
如果本次改动涉及以下情况，创建或更新 `.claude/skills/` 下的经验文档：
- 调试了一个非平凡的 bug → 记录根因、排查路径、修复方案
- 实现了新的架构模式或集成方式 → 记录设计决策、权衡、关键代码路径
- 在远程设备上踩了部署/兼容性坑 → 记录现象、环境、解决方案
- 经验具有复用价值（未来遇到类似场景可以直接参考）

**格式**: 按现有 skills 风格，简洁实用，重点是"下次再遇到怎么办"。
**命名**: `<topic>.md`，用小写短横线连接。
**不要**: 为简单的一行改动或纯文档修改创建 skill。

### 1.4 更新 skills/README.md（如有必要）
- 如果新增了 skill 文件 → 更新索引表
- 如果现有 skill 内容有重大变化 → 更新描述

## Phase 2: 提交

### 2.1 暂存文件
- `git add` 所有相关改动的文件（代码 + 文档）
- 不要 add `.env`、credentials、build 产物等敏感/临时文件

### 2.2 生成 commit message
- 分析所有暂存的改动（不仅是代码，包括文档更新）
- 用英文写 commit message，格式：`<type>: <concise summary>`
- type: feat / fix / refactor / docs / test / chore
- 如果改动跨多个 type，用主要改动的 type
- 末尾加 `Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>`

### 2.3 提交
- 用 HEREDOC 格式传 commit message

## Phase 3: 推送

### 3.1 推送当前分支
- `git push origin <current-branch>` （带 `-u` 如果是新分支）
- 如果推送失败（如远程有更新），先 `git pull --rebase` 再重试

### 3.2 创建 PR（worktree 分支自动触发）
检测当前是否在 worktree 中（路径包含 `.claude/worktrees/` 或分支名以 `worktree-` 开头）：
- **是 worktree** → 自动创建 PR 到 master：
  - `git log master..HEAD` 分析所有 commit
  - PR title: 简短概括（<70 字符）
  - PR body 格式：
    ```
    ## Summary
    - <1-3 bullet points summarizing all changes>

    ## Test plan
    - [ ] `go test ./...`
    - [ ] `go vet ./...`
    - [ ] <其他相关验证项>

    🤖 Generated with [Claude Code](https://claude.com/claude-code)
    ```
  - 使用 `gh pr create --title "..." --body "$(cat <<'EOF' ... EOF)"`
  - 如果 PR 已存在，跳过创建，显示现有 PR URL
- **不是 worktree** → 跳过 PR 创建

### 3.3 报告结果
- 显示 commit hash、分支名、推送状态
- 如果创建了 PR，显示 PR URL

## 注意事项

- 如果没有任何改动（`git status` 干净），告知用户并停止
- 如果用户在 `/commit` 后面带了参数（如 `/commit fix typo`），用参数作为 commit message hint
- 文档更新应该是增量的，不要重写未改变的部分
- 推送前确认当前分支不是 master/main（如果是，警告用户）
