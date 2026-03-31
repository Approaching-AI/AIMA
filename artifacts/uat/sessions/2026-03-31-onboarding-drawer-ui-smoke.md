# Onboarding Drawer UI Smoke

**日期**：2026-03-31  
**目标**：验证新手 onboarding 入口的真实交互是否成立，重点覆盖 drawer 打开/关闭、键盘焦点约束、命令回填，以及移动端从非聊天态返回聊天输入区。  
**环境**：`dev-mac` / 本地 `go run ./cmd/aima serve --addr 127.0.0.1:6188 --allow-insecure-no-auth`  
**构建**：`feat/onboarding-drawer-spec` 当前未推送本地分支

## 验证范围

- 头部新增弱化 `Onboarding` 入口
- 右侧 drawer 能正常打开与关闭
- `Quick Start / Commands / Troubleshooting` 三层内容可切换
- 点击命令按钮只回填输入框，不自动发送
- drawer 打开后不会再把焦点泄漏到背后聊天输入框
- 移动端从 `Dashboard` 态进入 onboarding 后，点击命令会回到聊天输入区

## 实际验证

### 1. 桌面态入口与分层内容

- 打开 `/ui/` 后，`AIMA Agent` 头部出现次级 `Onboarding` 按钮，视觉弱于 `Connect AIMA-Service`
- 点击后右侧 drawer 正常展开
- `Quick Start` 默认打开
- 切到 `Commands` 后，看到 `Inspect / Inventory / CLI examples` 三组命令
- 切到 `Troubleshooting` 后，能看到故障兜底说明

### 2. 命令回填

- 在 drawer 中点击 `status`
- drawer 自动关闭
- 聊天输入框被填入 `status`
- 没有自动发送

### 3. 键盘 modal 行为

- drawer 打开后，初始焦点落到 drawer 内部关闭按钮
- `Tab / Shift+Tab` 只在 drawer 内循环，不会跳到背后聊天输入框或头部其他按钮
- `Ctrl/Cmd+K` 在 drawer 打开时不会再把焦点抢到背后输入框
- `Escape` 会关闭 drawer，并把焦点还给 `Onboarding` 入口按钮

### 4. 移动端返回聊天

- 缩到移动端视口后，先切到 `Dashboard`
- 再打开 onboarding，并点击 `status`
- drawer 关闭后，底部不再停留在原来的 `Dashboard` 激活态
- 聊天输入区重新可见，输入框中为 `status`

## 结论

**结论：PASS**

这轮 smoke 覆盖了 onboarding drawer 最容易出问题的真实交互路径。当前实现满足“次级入口 + 右侧说明抽屉 + 分层内容 + 安全命令回填 + 移动端回到聊天”的目标。
