# wechat-cli

本机微信数据 CLI。给强 agent 用，也给人直接用。

macOS / Windows · 本地解密 · 一行安装 · 稳定 JSON · 聊天记录 / 搜索 / 图片 / 文件 / 语音转写 / 朋友圈 / 转账红包

`wechat-cli` 读取你电脑上的 WeChat / 微信 4.x 本地数据库，把消息、联系人、群聊、媒体、朋友圈、收藏、转账、红包等数据输出成结构化 JSON。数据默认留在本机，不上传到云端。

它不是微信机器人，不控制屏幕，不发消息，不自动点赞评论，也不是公众号或小程序工具。

## 安装

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.sh | zsh
```

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.ps1 | iex"
```

默认安装的是 CLI，不注册 MCP，不装后台 watcher。安装完成后命令会放到用户 PATH 上：

- macOS: `~/.local/bin/wechat-cli`
- Windows: `%LOCALAPPDATA%\Microsoft\WindowsApps\wechat-cli.cmd`，如该目录不存在则使用 `%USERPROFILE%\.local\bin\wechat-cli.cmd`

首次安装前请确认：

- macOS arm64 + WeChat 4.x，或 Windows amd64 + Windows WeChat / Weixin 4.x
- 微信已登录，并至少打开过一个聊天
- macOS 首次 key 初始化可能要求输入一次 Mac admin 密码；密码只输入到本机隐藏提示，不要发给 agent 或网页
- macOS 15+ 建议安装后把 `~/.local/share/wechat-cli/wechat-cli` 和 `~/.local/share/wechat-cli/wxkey` 加到 Full Disk Access，减少系统隐私弹窗

## 快速开始

```bash
wechat-cli sessions
wechat-cli resolve-chat "张三"
wechat-cli timeline "某个群" --limit 20
wechat-cli history "张三" --view agent --limit 50
wechat-cli search "关键词" --in "某个群"
wechat-cli media "某个群" --type image --limit 10
```

所有命令默认输出 JSON。常用命令是薄封装，完整能力都可以通过通用调用访问：

```bash
wechat-cli tools
wechat-cli call chat_timeline --chat "某个群" --limit 20
wechat-cli call-json messages '{"chat":"张三","limit":50,"view":"agent"}'
printf '{"keyword":"会议","limit":20}' | wechat-cli call-json search
```

`freshness` 是返回数据的新鲜度/诊断信息：例如是否触发过 metadata 自动刷新、分页是否还有下一页、结果是否可能受缺 key 或 cache 滞后影响。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `sessions` | 最近会话、未读数、最后消息摘要 |
| `resolve-chat` | 把昵称、备注、群名解析成稳定 talker |
| `timeline` | 普通读聊天的首选入口，返回 `query` / `freshness` / `messages` |
| `history` | 更底层的消息读取，支持时间、类型、sender、分页等过滤 |
| `search` | 走微信本地 FTS 的跨会话全文搜索 |
| `media` | 按消息定位图片、视频、文件等本机可读资源 |
| `members` | 群成员、群名片、好友关系 |
| `sns-feed` / `sns-search` / `sns-notifications` | 朋友圈时间线、搜索、点赞评论通知 |
| `transfers` / `red-packets` | 转账和红包记录 |
| `favorites` | 微信收藏 |
| `export` | 单个会话导出到 jsonl / markdown / html |
| `schema` / `sql` | 只读数据库结构和 SQL 诊断 |
| `cache status` / `cache refresh` | metadata cache 诊断与刷新 |

典型消息行：

```json
{
  "id": {"local_id": 123, "server_id_str": "9876543210", "talker": "xxx@chatroom"},
  "time_iso": "2026-05-26T13:00:00+08:00",
  "sender": "张三",
  "sender_wxid": "wxid_xxx",
  "is_from_me": false,
  "kind": "image",
  "text": "[图片]",
  "images": [{"path": "/Users/me/.wechat-cli/media-cache/xxx.jpg"}],
  "warnings": []
}
```

默认输出只给 agent 可用的信息：可读图片/视频/文件路径、链接、引用、转账红包、位置、语音转写等。raw XML、CDN/aeskey、不可读 `.dat`、候选路径和解码细节默认隐藏；维护者需要时再传 `include_debug=true` 或 `fields=full`。

## MCP 兼容

默认形态是 CLI。MCP 只保留为兼容入口：

```bash
wechat-cli serve-mcp
```

安装时需要 MCP 注册才加参数：

```bash
./install.sh --all --yes --mcp
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Mcp
```

## 数据与隐私

- `wechat-cli` 只读打开微信本地数据库。
- 聊天正文默认 live read，不做全量正文 cache。
- 联系人和会话 metadata cache 位于 `~/.wechat-cli/cache/`，用于名称解析和会话排序。
- key map 位于 `~/.config/wxcli/config.json`。不要把它、微信 DB、聊天导出、截图或日志贴到公开 issue。
- `wechat-cli` 不发送消息、不自动转发、不点赞评论、不修改微信数据。

## 排障

| 现象 | 处理 |
| --- | --- |
| 找不到会话 | 先用 `wechat-cli resolve-chat "名字"` 看候选，必要时在微信里打开对应聊天后重试 |
| 提示缺 key | 确认微信已登录并打开过聊天；macOS agent 可跑 `wxkey doctor` / `wxkey setup` |
| macOS 频繁弹隐私授权 | 给 `wechat-cli` 和 `wxkey` 加 Full Disk Access |
| 图片只有 warning 没 path | 微信本地只有 `.dat` 且 image key 仍不可用；打开原图或对应聊天后重试 |
| Windows 初始化失败 | 确认 Windows 微信登录、`WECHAT_CLI_DB_ROOT` 指向直接包含 `db_storage` 的账号目录 |

更详细的 agent 操作说明见 [AGENTS.md](AGENTS.md)，模型发现摘要见 [llms.txt](llms.txt)。

## 开发

```bash
go test ./...
go build -trimpath -o wechat-cli ./cmd/wx-mcp
```

macOS release 包：

```bash
WECHAT_CLI_WCDB_DYLIB=/path/to/libWCDB.dylib ./scripts/package.sh 1.6.1
```

Windows release 包由 GitHub Actions 的 `Windows Release Package` workflow 构建。

## 相关项目

- [wxkey](https://github.com/r266-tech/wxkey): macOS WeChat key bootstrap companion，release 包内已包含，普通用户通常不需要单独安装。
- [jackwener/wx-cli](https://github.com/jackwener/wx-cli): 面向终端/脚本的 WeChat data CLI，命令体验值得参考。
- [joeseesun/wechat-radar](https://github.com/joeseesun/wechat-radar): 基于微信数据的本地情报看板。
- [ylytdeng/wechat-decrypt](https://github.com/ylytdeng/wechat-decrypt): 微信数据库解密与导出工具集。

## License

See [LICENSE](LICENSE).

---

<!-- babata-star-callout-v2 -->
## If this saved you time

Starring the repo helps prioritize which integrations stay maintained. This project is part of [babata](https://github.com/r266-tech).
