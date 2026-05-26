# wx-mcp

**让 Claude Code、Codex、Cursor 等 AI agent 读取本机微信 4.x 数据的 MCP Server。**

macOS / Windows · 本地解密 · 一行安装 · 聊天记录 / 搜索 / 图片 / 文件 / 语音转写 / 朋友圈 / 转账红包

`wx-mcp` 读取你电脑上的 WeChat / 微信 4.x 本地数据库，通过 MCP tools 把消息、联系人、群聊、媒体、朋友圈、收藏、转账、红包等数据交给 agent 使用。数据默认留在本机，不上传到云端。

它不是微信机器人，不控制屏幕，不发消息，不自动点赞评论，也不是公众号或小程序工具。

## 为什么是 MCP，而不只是 CLI

`wx-cli` 这类项目很适合人类在终端里查微信数据；`wx-mcp` 的主目标是让 agent 稳定、低噪声地调用本机微信数据。

| 形态 | 适合谁 | 优点 | 局限 |
| --- | --- | --- | --- |
| CLI | 人类、shell 脚本 | 安装后马上敲命令，调试直观 | agent 要解析 stdout，分页、媒体、warning、freshness 容易靠约定漂移 |
| MCP | AI agent、MCP 客户端 | 有工具 schema、结构化 JSON、分页/freshness/warnings、可直接返回本机图片/文件路径 | 需要 MCP 客户端注册 |

所以 `wx-mcp` 的产品默认是 MCP，但并没有放弃 CLI。安装后同一个二进制也能这样用：

```bash
wx-mcp sessions
wx-mcp timeline "某个群" --limit 20
wx-mcp history "张三" --view agent --limit 50
wx-mcp search "关键词" --in "某个群"
wx-mcp media "某个群" --type image --limit 10
```

CLI 用来给人快速验证和脚本化；MCP 用来给 agent 做长期、可靠、结构化的数据访问。

## 特性

- **一行安装**: release zip 内含二进制、WCDB 动态库、安装器和 MCP manifest。
- **本地优先**: 直接读取本机微信数据库；聊天正文不进 wx-mcp 长期缓存。
- **agent-ready 输出**: 消息默认带 `query` / `freshness` / `messages`，支持稳定分页。
- **可读媒体路径**: 图片、视频、文件默认只返回 agent 能直接读取的本机 `path`；不可读 `.dat` 不冒充图片。
- **图片 key 自动刷新**: 微信 V4 图片缺 `image_key` 时自动尝试 `wxkey image-key` 并重试解码。
- **语音转写**: 本地语音可优先走本机 ASR，默认返回 `voice.transcript`。
- **metadata cache + live messages**: 联系人/会话用轻量 cache 做解析，聊天正文按需 live read。
- **macOS 无需关闭 SIP**: 首次 key bootstrap 不要求关闭 SIP；用户只在本机隐藏提示里输入一次 Mac admin 密码。
- **Windows 支持**: Windows WeChat / Weixin 登录后，安装器会前台验证 key scan 和 metadata refresh。

## 安装

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.sh | zsh
```

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.ps1 | iex"
```

安装器会下载最新 release，解压后安装到用户目录并注册常见 MCP 客户端。

首次安装前请确认：

- macOS arm64 + WeChat 4.x，或 Windows amd64 + Windows WeChat / Weixin 4.x
- 微信已登录，并至少打开过一个聊天
- macOS 首次 key 初始化可能会要求输入一次 Mac admin 密码；密码只输入到本机隐藏提示，不要发给 agent 或任何网页
- macOS 15+ 建议安装后把 `~/.local/share/wx-mcp/wx-mcp` 和 `~/.local/share/wx-mcp/wxkey` 加到 Full Disk Access，减少系统隐私弹窗

源码安装只建议开发者使用；普通用户和 agent 应优先使用 release 安装。

## 快速验证

安装完成后，重开 MCP 客户端，或直接用 CLI 验证：

```bash
wx-mcp sessions
wx-mcp timeline "文件传输助手" --limit 10
wx-mcp search "会议" --limit 20
```

能看到会话或消息，说明 key、WCDB 动态库和 metadata cache 都已工作。

## 常用 MCP tools

| Tool | 用途 |
| --- | --- |
| `sessions` | 最近会话、未读数、最后消息摘要 |
| `resolve_chat` | 把昵称、备注、群名解析成稳定 talker |
| `chat_timeline` | 普通读聊天的首选入口，返回 `query` / `freshness` / `messages` |
| `messages` | 更底层的消息读取，支持时间、类型、sender、分页等过滤 |
| `search` | 走微信本地 FTS 的跨会话全文搜索 |
| `media_resources` | 按消息定位图片、视频、文件等本机可读资源 |
| `group_members` | 群成员、群名片、好友关系 |
| `sns_feed` / `sns_search` / `sns_notifications` | 朋友圈时间线、搜索、点赞评论通知 |
| `transfers` / `red_packets` | 转账和红包记录 |
| `favorites` | 微信收藏 |
| `export_messages` | 单个会话导出到 jsonl / markdown / html |
| `schema` / `sql` | 只读数据库结构和 SQL 诊断 |
| `cache_status` / `cache_refresh` | metadata cache 诊断与刷新 |

典型 agent 消息行长这样：

```json
{
  "id": {"local_id": 123, "server_id_str": "9876543210", "talker": "xxx@chatroom"},
  "time_iso": "2026-05-26T13:00:00+08:00",
  "sender": "张三",
  "sender_wxid": "wxid_xxx",
  "is_from_me": false,
  "kind": "image",
  "text": "[图片]",
  "images": [{"path": "/Users/me/.wx-mcp/media-cache/xxx.jpg"}],
  "warnings": []
}
```

调试字段、raw XML、CDN/aeskey、不可读 `.dat`、候选路径等默认隐藏；维护者需要时再传 `include_debug=true` 或 `fields=full`。

## 数据与隐私

- `wx-mcp` 只读打开微信本地数据库。
- 聊天正文默认 live read，不做全量正文 cache。
- 联系人和会话 metadata cache 位于 `~/.wx-mcp/cache/`，用于名称解析和会话排序。
- key map 位于 `~/.config/wxcli/config.json`。不要把它、微信 DB、聊天导出、截图或日志贴到公开 issue。
- `wx-mcp` 不发送消息、不自动转发、不点赞评论、不修改微信数据。

## 排障

| 现象 | 处理 |
| --- | --- |
| 找不到会话 | 先用 `resolve_chat` 看候选，必要时在微信里打开对应聊天后重试 |
| 提示缺 key | 确认微信已登录并打开过聊天；macOS agent 可跑 `wxkey doctor` / `wxkey setup` |
| macOS 频繁弹隐私授权 | 给 `wx-mcp` 和 `wxkey` 加 Full Disk Access |
| 图片只有 warning 没 path | 微信本地只有 `.dat` 且 image key 仍不可用；打开原图或对应聊天后重试 |
| Windows 初始化失败 | 确认 Windows 微信登录、`WX_MCP_DB_ROOT` 指向直接包含 `db_storage` 的账号目录 |

更详细的 agent 操作说明见 [AGENTS.md](AGENTS.md)，模型发现摘要见 [llms.txt](llms.txt)。

## 开发

```bash
go test ./...
go build -trimpath -o wx-mcp ./cmd/wx-mcp
```

macOS release 包：

```bash
WX_MCP_WCDB_DYLIB=/path/to/libWCDB.dylib ./scripts/package.sh 1.5.4
```

Windows release 包由 GitHub Actions 的 `Windows Release Package` workflow 构建。

## 相关项目

- [wxkey](https://github.com/r266-tech/wxkey): macOS WeChat key bootstrap companion，release 包内已包含，普通用户通常不需要单独安装。
- [jackwener/wx-cli](https://github.com/jackwener/wx-cli): 面向终端/脚本的 WeChat data CLI，README 结构和命令体验值得参考。
- [joeseesun/wechat-radar](https://github.com/joeseesun/wechat-radar): 基于微信数据的本地情报看板。
- [ylytdeng/wechat-decrypt](https://github.com/ylytdeng/wechat-decrypt): 微信数据库解密与导出工具集。

## License

See [LICENSE](LICENSE).

---

<!-- babata-star-callout-v2 -->
## If this saved you time

Starring the repo helps prioritize which integrations stay maintained. This project is part of [babata](https://github.com/r266-tech).
