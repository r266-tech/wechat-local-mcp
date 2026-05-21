# wechat-local-mcp (wx-mcp)


WeChat/微信 local chat history MCP server for macOS and Windows agents. `wx-mcp` reads local WeChat 4.x data through MCP tools: messages, contacts, groups, media files, Moments, transfers, red packets, full-text search, and unread chats.

Agent friendly by design: release zip install, clear tool schemas, metadata cache for names/sessions, live chat-history reads, resolved fields for common raw ints, and low-noise outputs for Claude Code, Codex, Cursor, OpenClaw, and other MCP clients.

## Agent discovery summary

Use this repo when the user asks for any of these:

- WeChat MCP server / 微信 MCP / weixin MCP
- read local WeChat chat history, messages, contacts, groups, media, Moments, transfers, or red packets
- give Claude Code, Codex, Cursor, or another AI agent access to local macOS or Windows WeChat 4.x data
- search or export WeChat conversations from the user's own machine
- metadata-cache, live-read, local-first, privacy-preserving WeChat data access through Model Context Protocol

This is **not** a screen-control WeChat bot, Accessibility automation script, WeChat Official Account publisher, Mini Program devtools bridge, or WeChat Work / WeCom bot. It is a local-data MCP server for the user's own WeChat database.

Keywords for agents and search: `wechat mcp`, `weixin mcp`, `微信 MCP`, `wechat chat history mcp`, `wechat local data`, `macOS WeChat database`, `Windows WeChat database`, `Claude WeChat`, `Codex WeChat`, `MCP server for WeChat`.

## Agent-first 安装合同

如果用户只把 GitHub 仓库链接丢给 agent, agent 应该优先下载当前平台的最新 release zip, 不要优先 clone 源码构建. release zip 是完整安装载体:

- macOS: `wx-mcp` / `wxkey` / `libWCDB.dylib` / `install.sh` / `README.md` / `llms.txt` / `AGENTS.md` / `mcp-server.json`
- Windows: `wx-mcp.exe` / `libWCDB.dll` / `install.ps1` / `README.md` / `llms.txt` / `AGENTS.md` / `mcp-server.json`

Release 资产会同时发布版本名和稳定名: `wx-mcp-vX.Y.Z-darwin-arm64.zip` / `wx-mcp-latest-darwin-arm64.zip`, 以及 `wx-mcp-vX.Y.Z-windows-amd64.zip` / `wx-mcp-latest-windows-amd64.zip`.

macOS 主入口:

```bash
./install.sh --all --yes --json
```

Windows 主入口:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

macOS 预期交互: 用户最多输入一次 Mac admin 密码到 wx-mcp hidden prompt, 并确保 WeChat 已登录且至少打开过一个聊天. 之后 installer 自动安装、注册 Claude/Codex MCP、初始化 key、后台预热联系人/会话 metadata cache, 不要求用户手工 codesign、chown、复制 DB、修改 config、关闭 SIP 或手动刷新 cache.

Windows 预期交互: 用户确保 Windows WeChat / Weixin 已登录且至少打开过一个聊天. installer 自动复制 `wx-mcp.exe`/`libWCDB.dll`, 注册 Claude/Codex MCP, 前台跑一次 metadata `cache refresh --force` 来验证进程内 key scan 和 cache 构建成功; 成功才返回 `status=ready`.

## 运行前提

- macOS arm64 + WeChat 4.x, 或 Windows amd64 + Windows WeChat/Weixin 4.x
- **macOS 运行时解密不要求关闭 SIP** — wx-mcp 读库时只加载 `libWCDB.dylib` 并用 `sqlite3_key_v2` 打开加密 DB; 只要 `~/.config/wxcli/config.json` 已有 schema-2 per-DB key map, SIP 开着也能跑.
- **macOS 只保留一种取 key 路径: `./wxkey bootstrap`, 不关闭 SIP** — bootstrap 会检查 WeChat 签名, 必要时退出 WeChat 并为 wx-mcp 创建 ad-hoc signed shadow WeChat 副本, 让用户输入一次 Mac admin 密码并存入 macOS Keychain, 再用 `sudo -S + task_for_pid + mach_vm_read` 扫微信进程内存拿 WCDB key. 后续缺 key / key 过期时自动复用 Keychain 里的 sudo 密码刷新.
- Windows 取 key 不使用 `wxkey`; wx-mcp 直接扫描当前用户登录的 `Weixin.exe` / `WeChat.exe`, 验证后写 schema-2 key map.
- 微信 / WeChat 4.x 开着且登录过, 至少打开过一个会话 (让 DB 加载进内存, key 才会出现在 heap 里)
- key 拿到后写 `~/.config/wxcli/config.json`, 之后微信可关
- WCDB 动态库不进源码仓库; macOS release zip 提供 `libWCDB.dylib`, Windows release zip 提供 `libWCDB.dll`.

## 安装

macOS 入口:

```bash
./install.sh --all --yes --json
```

macOS 入口面向"把 GitHub 链接或 zip 丢给 agent"的场景: 安装/构建 binary, 复制 `libWCDB.dylib`, 注册 Claude/Codex MCP, 跑 `wxkey bootstrap`, 后台预热 metadata cache, 并按需安装 launchd watcher. 所有结果都以 JSON 输出; agent 主要看 `status` / `blocked_by` / `next_action` / `errors[]` / `log`.

Windows 入口:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

Windows 版需要 `wx-mcp.exe` 旁边有 `libWCDB.dll` 或 `WCDB.dll`. 如果微信数据不在默认位置, 设置 `WX_MCP_DB_ROOT` 到直接包含 `db_storage` 的账号目录. Windows 微信保持登录时, wx-mcp 会扫描 `Weixin.exe` / `WeChat.exe` 进程内存里的 SQLCipher raw key, 验证后写入 schema-2 key map. 默认安装会前台验证一次 metadata cache refresh; 如需只启动后台预热, 额外传 `-BackgroundRefresh`. 详细说明见 `docs/WINDOWS_USER_GUIDE.md`.

> **首次安装只需要用户输一次 Mac admin 密码.** Agent 可以直接跑 `./install.sh --all --yes --json`; `wxkey bootstrap` 会弹出 wx-mcp 的隐藏密码输入框, 验证 sudo 后把密码存入用户 macOS Keychain. 之后所有运行 (metadata cache refresh / wx-mcp 启动 / DB 解密 / 缺 key 自动补扫) 都复用这份 Keychain 凭据, 不要求用户进终端输入命令、再次告知密码, 也不要求关闭 SIP.

> **避免 TCC 反复弹 "wx-mcp 想访问其他 App 的数据" (macOS 15+).** 装完后, 进**系统设置 → 隐私与安全性 → 完全磁盘访问权限**, 点 `+` 把 `~/.local/share/wx-mcp/wx-mcp` 和 `~/.local/share/wx-mcp/wxkey` 加进去. 加完之后所有访问微信容器的请求都默默通过, 不再弹窗. (`--all` 默认**不**装 launchd watcher; 如果你确实需要后台 5 分钟一次自动刷新 metadata cache, 加 `--watcher` 显式开, 但前提是先给上面两个 binary 加 Full Disk Access, 否则 watcher 每次跑都会触发弹窗.)

源码 clone 场景只适合开发者或没有 release zip 的应急安装; 普通 agent 安装应优先 release zip, 因为源码仓库不包含 `libWCDB.dylib` / `libWCDB.dll`.

```bash
git clone https://github.com/r266-tech/wechat-local-mcp.git
cd wechat-local-mcp
WX_MCP_WCDB_DYLIB=/path/to/libWCDB.dylib ./install.sh --all --yes --json
```

release zip 场景会直接复制包内 binary 和 WCDB 动态库. macOS 源码 clone 场景会优先 `go build`; 如果本地没有 wxkey 源码或二进制, installer 会用 Go 从 `github.com/r266-tech/wxkey/cmd/wxkey@latest` 安装 companion CLI. Windows 源码 clone 场景需要本机 Go 和 `libWCDB.dll`.

release zip 用户更新时, agent 应先按当前平台下载最新稳定名 zip, 解压到新目录, 再从新目录运行 update:

```bash
# macOS
./install.sh --update --yes --json
```

```powershell
# Windows
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

`--update` 在非 git 目录不会联网拉 GitHub; 它只会把当前 release 包里的文件重装进用户目录. 如果明确要重跑完整初始化, 再用 `--all`.

已有 git checkout 的更新入口:

```bash
./install.sh --update --yes --json
```

`--update` 会先 `git pull --ff-only`, 再重装 binary. 默认不重新 bootstrap、不刷新 cache、不重注册 MCP、不动 watcher; 需要时显式加 `--refresh` / `--watcher` / `--bootstrap`, 或直接跑 `--all`.

安全分层:

```bash
./install.sh --doctor --json
./install.sh --dry-run --all --json
./install.sh --yes --json --mcp-client none        # 只安装文件, 不注册 MCP / bootstrap / watcher
./install.sh --clear-state --dry-run --json        # 预览清空 key/cache/log/Keychain 凭据
./install.sh --uninstall --yes --json
./install.sh --uninstall --purge-state --yes --json
```

`--uninstall` 只移除安装目录、watcher 和 MCP 注册, 默认保留 `~/.config/wxcli/config.json` / `~/.wx-mcp` 等用户状态, 避免误删已提取 key. 要回到"没装过 wx-mcp"的状态, 用 `--uninstall --purge-state`. 只想保留安装但重跑首次初始化, 用 `--clear-state`. 清状态只删除 `config.json`, 不删除 `~/.config/wxcli/lib` 里的 WCDB 动态库.

安装或更新新版时, installer 会删除旧 cache index 和 raw 里的非 contact/session snapshot, 确保从旧版升级后不会继续保留明文聊天正文 cache; 下一次 metadata 读取会自动重建轻量 index.

Windows 安全分层:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Doctor -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Yes -Json -NoMcp
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -ClearState -DryRun -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -Yes -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -PurgeState -Yes -Json
```

手动注册仍可用:

```bash
go build -o wx-mcp ./cmd/wx-mcp
claude mcp add --scope user wx-mcp "$PWD/wx-mcp"
```

## 验证 (推荐首次装完跑一次)

装完后 agent 会在 `install.sh --all` 里跑 `wxkey bootstrap`. 如需单独复验, 也由 agent 跑:

```bash
./wxkey bootstrap
```

bootstrap 会检查已有 config, 在需要时创建并签名 wx-mcp shadow WeChat 副本完成首次 key 初始化. 排障时再跑:

```bash
./wxkey doctor
```

doctor 默认会输出: SIP 状态 / WeChat 签名 / 微信进程 / 账号目录 / DB 数 / dylib / 已缓存 key 覆盖率. 需要内存 scan 是否通 / 当前 heap 拿到几个 key 时再显式跑 `wxkey doctor --scan`.
没有缓存 key 时, 微信没登录 / 签名未处理 / scan 失败会用中文报错指方向, 比 MCP 启动失败再排查省事.
如果 key 只有部分覆盖, agent 应自己跑轻量 `wxkey doctor` 找缺失 DB, 只让用户在 WeChat 里打开对应聊天/朋友圈/收藏, 然后 agent 再跑 `wxkey setup`; 不要把 doctor/setup 命令交给用户手动执行. `wxkey doctor` 默认只对比缓存 key map 和本地 DB salts, 不重新扫内存; 只有需要复验 task_for_pid/当前 heap 覆盖率时才跑 `wxkey doctor --scan`.

之后让 Claude/Codex 调任意 wx-mcp 工具 (如 sessions) 验证 E2E. 拿不到 key 时模型会照实告诉你错误.

## 开发 / 更新

```bash
go build -o wx-mcp ./cmd/wx-mcp
# MCP 下次启动生效 (或 claude mcp restart wx-mcp)

# 跑测试 (helpers + XML parsers, ~30 case 不依赖 db/dylib):
go test ./...
```

## 打分发包 (给朋友)

```bash
WX_MCP_WCDB_DYLIB=/path/to/libWCDB.dylib ./scripts/package.sh 1.5.1
# 产出 dist/wx-mcp-v1.5.1-darwin-arm64.zip + .sha256 (含 wx-mcp + wxkey + libWCDB.dylib + install.sh + docs)
```

Windows 包在 Windows 机器上打:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\package-windows.ps1 -Version 1.5.1 -WcdbLib C:\path\to\libWCDB.dll
# 产出 dist\wx-mcp-v1.5.1-windows-amd64.zip + .sha256 (含 wx-mcp.exe + libWCDB.dll + install.ps1 + docs)
```

朋友解压后:
1. macOS agent 先跑 `./install.sh --dry-run --all --json` 看计划; Windows agent 先跑 `.\install.ps1 -DryRun -All -Json`.
2. macOS agent 再跑 `./install.sh --all --yes --json`; Windows agent 再跑 `.\install.ps1 -All -Yes -Json`.
3. JSON 返回 `status=ready` 或 `status=warming_cache` 都表示安装主流程完成; `warming_cache` 表示 cache 正在后台预热. Windows 默认前台验证成功后返回 `ready`.
4. 让 Claude/Codex 调 `sessions` 拉数据, 通了即可.

前提: 如果目标机器没有现成 key, 首次 key scan 需要微信 4.x 登录态 + 至少开过一个会话. macOS 支持路径是 no-SIP `./wxkey bootstrap`: 用户只输一次 Mac admin 密码, agent 负责运行 bootstrap/doctor/setup 和后续重试, 后续自动复用 Keychain 凭据. Windows 支持路径是 `wx-mcp.exe cache refresh --force` 内置同用户进程扫描, 不运行 `wxkey`.

## Metadata cache + live messages

默认只构建联系人/会话 metadata snapshot cache + 统一 `index.sqlite`. 聊天正文不进入 wx-mcp cache; `messages` / `search` / 单会话 `export_messages` 在需要时直接读取微信本地 DB:

```bash
./wx-mcp cache status
./wx-mcp cache refresh      # 增量刷新联系人/会话 metadata
./wx-mcp cache rebuild      # 删除 cache 后完整重建
```

cache 位于 `~/.wx-mcp/cache/<wxid>/`:

```text
raw/          # contact/contact.db 和 session/session.db 的明文 snapshot
index.sqlite  # contacts_unified / sessions_unified
```

`sessions` / `resolve_chat` / `contacts` / `unread` 使用 metadata cache 做名称解析和会话排序. cache 不存在或已落后时, wx-mcp 会先自动 `cache refresh`, 但默认只刷新 contact/session, 不扫描全部聊天分片.
`messages` 通过 `chat` 或 `talker` 定位单会话后直读对应 `Msg_<hash>` 表; `search` 直读微信 `message_fts.db` 并按需 join 回消息分片补 sender/kind; 单会话 `export_messages` 也直读源 DB.
从旧版本升级后, 下一次 metadata refresh 会重建 index 并删除旧的非 contact/session snapshot 文件, 避免明文聊天记录继续留在 wx-mcp cache 里.
wx-mcp 不提供聊天正文 cache 模式; 跨聊天、跨年份检索走微信本地 `message_fts.db`, 单聊天历史和导出走 live message shard.

可选 watcher:

```bash
./install.sh --watcher --yes --json
```

watcher 是 launchd user agent (`com.r266.wx-mcp-cache-watcher`), 默认每 300 秒跑一次 `wx-mcp cache refresh`, 并用 `~/.wx-mcp/cache-refresh.lock` 防重入. 日志在 `~/Library/Logs/wx-mcp/`. 日常不需要 watcher: MCP 工具读 metadata cache 前会自动 freshness gate.

## Agent CLI

除了 MCP tools, `wx-mcp` 也提供 agent 友好的 CLI alias:

```bash
wx-mcp sessions --type-filter private,group
wx-mcp resolve-chat "张三"
wx-mcp history "张三" --limit 50
wx-mcp media "张三" --type image --limit 10
wx-mcp search "关键词" --in "某群" --after 2026-01-01 --type text
wx-mcp search "关键词" --after 2026-01-01      # 跨聊天/跨年份关键词搜索, 走微信 live FTS
wx-mcp members "某群"
wx-mcp stats
wx-mcp red-packets --limit 20
wx-mcp transfers --limit 20
wx-mcp sns-feed
wx-mcp sns-search "关键词"
wx-mcp sns-notifications --include-read
```

CLI 和 MCP 走同一套工具逻辑: 名称/会话走 metadata cache, 聊天正文默认 live read.

## Tools (24 个)

所有时间字段接 unix秒 或 `2006-01-02` (本地时区).

| Tool | 说明 |
|------|------|
| `sessions` | 会话列表 (按 sort_timestamp DESC). 字段: username / display_name / chat_type / unread_count / summary / sort_timestamp / last_timestamp / last_sender_wxid / last_sender_display_name / last_msg_type / last_msg_sub_type / last_msg_kind_name. 支持 type_filter (private/group/official_account/folded/bot, 可逗号分隔) + keyword 模糊搜索 |
| `resolve_chat` | 把昵称/备注/alias/群名解析成 username/talker. 返回 candidates, 供 agent 从自然语言目标进入精确工具调用 |
| `contacts` | 联系人/群搜索. 字段: username / display_name / nick_name / remark (omitempty) / alias (omitempty) / description (omitempty) / type / chat_type / is_verified |
| `messages` | 消息. talker 可传 wxid; chat 可传昵称/备注/群名自动解析. fields=lite (默认) 返回核心字段; fields=full 加 subtype + raw message_content + message_content_parsed (XML 结构化, 引用递归 depth=3). content_summary 已剥群聊 sender prefix |
| `media_resources` | 消息附件/媒体资源定位. 从 `message_resource.db` 返回 `server_id_str`、图片/视频/文件/封面资源的 raw type、variant_code、size、status、packed_strings(文件名/md5) 和已存在的本地 `local_paths`. 支持 chat/talker/local_id/server_id/server_id_str/type/resource_family 过滤 |
| `group_members` | 群成员. chatroom_id 可传群 ID; chat 可传群名自动解析. is_owner / is_friend 是 bool. stats=true 附 msg_count |
| `sns` | 朋友圈 + 点赞/评论. 字段: tid / username / nickname / avatar_url / create_time / content / type / private / liked_by_me / media (含 raw_type/sub_type/url_key/thumb_key/md5/width/height/total_size/video_md5/video_duration) / location / likes / comments |
| `sns_feed` | 朋友圈时间线, 语义化 alias, 字段同 sns |
| `sns_search` | 朋友圈正文搜索, keyword 必填, 字段同 sns |
| `sns_notifications` | 朋友圈点赞/评论通知. 默认未读; include_read=true 返回全部 |
| `search` | 跨会话全文搜索. 默认直读微信 `message_fts.db`, metadata cache 仅用于 chat/sender 名称解析. 支持 keyword + chat/talker/after/before/type/kind_name/base_kind/sender. `search_mode` 只为兼容旧调用保留, fts/like/auto 都使用 live FTS, 不做全局 LIKE 扫描. 字段含 chat_type / content / talker / sender / base_kind / kind_name / local_id / create_time |
| `sql` | 只读 SQL. `SELECT/WITH` 默认外层限流, `limit` 最大 1000; `PRAGMA/EXPLAIN` 可直接跑. OS 级 readonly (SQLITE_OPEN_READONLY) — DDL/DML 直接报错 |
| `transfers` | 转账. 字段: transfer_id / transcation_id / payer_wxid / receiver_wxid / session_username / pay_sub_type / begin_transfer_time / **amount** ("￥5.00") / **description** ("收到转账5.00元") / memo (omitempty). amount/description/memo 是 batch join messages.server_id 解 XML 提取 |
| `red_packets` | 红包. 字段: send_id / sender_wxid / session_username / native_url / message_server_id / **wishing** ("恭喜发财大吉大利") / scene_text. 支持 chat/talker/sender/after/before; 时间过滤 live join 对应 Msg_<hash> 取 create_time |
| `favorites` | 收藏. 字段: server_id / favorite_type (link/text/image/voice/video/file/chat_history/miniprogram/...) / from_wxid / source_chat_username (omitempty) / update_time / **title** / **description** / **url** (从 content XML 提取) / source_id / content (XML raw) |
| `chatroom_announcements` | 群公告. 字段: chatroom_id / chatroom_display_name / announcement / editor_wxid / editor_display_name / publish_time |
| `forward_history` | **最近转发目标列表** (用于快捷转发, 非"被转发的消息历史"). 字段: username / display_name / forward_time |
| `schema` | WCDB 数据库结构. 不传参列所有 db 子目录 + 表名; 传 subdir+file 返回每张表 DDL |
| `cache_status` | 查看 metadata snapshot cache 与统一 index.sqlite 诊断信息. 默认不缓存聊天正文, 不触发 wxkey setup, 不输出 `fresh=true` 这种全局新鲜度结论 |
| `cache_refresh` | 刷新 metadata snapshot cache 并重建 index.sqlite. 只处理 contact/session; background=true 立即返回并在后台刷新 |
| `cache_rebuild` | 删除当前 cache 后完整重建 metadata cache |
| `unread` | 未读会话列表, 字段同 sessions. 支持 filter/type_filter=private,group 等 |
| `stats` | 返回 metadata cache 的 sessions/contacts 状态; 不做全局聊天正文统计 |
| `export_messages` | 单会话 live export 到 jsonl / markdown / html 文件; 不支持全局无关键词导出 |

## 关键概念

### kind_name 解码

`local_type` 是 packed int64: `(subtype << 32) | base_kind`. messages tool 已拆出 `base_kind` / `subtype` / `kind_name`, lite mode 隐藏 raw `local_type`.

- `base_kind`: 1=text / 3=image / 34=voice / 42=card / 43=video / 47=sticker / 48=location / 49=app / 50=voip / 10000=system
- `kind_name` 在 `base_kind=49` 时按 subtype 细化: 3=music / 5=link / 6,8,24=file / 19=forward_chat / 33,36=miniprogram / 49=link / 51=channel_video / 57=quote / 62=pat / 87=announcement / 2000=transfer / 2001=red_packet
- 引用消息 (subtype=57) 时 `message_content_parsed.refermsg` 含完整 quote 上下文 + 可递归 decode 的 content_parsed (depth≤3)

### 跨表 join key

- `server_id` (messages) ⇄ `message_server_id` (transfers/red_packets/favorites): int64, 跨 re-import 稳定. transfers/red_packets 已自动 batch join 解 XML, 不需要 agent 自己再调 messages
- search 命中行通过 `(talker, local_id)` 路由回 `Msg_<hash>(talker)` 拿 sender + base_kind/kind_name

### 错误处理

主路径错误 fail-loud (db 打不开 / SQL 失败立即 error).
batch enrichment (transfers amount, search sender) 是 best-effort: 单 talker 路由失败时该字段缺失 (其他行不受影响, agent 看到字段不存在就知道没拿到).

## 架构

```
wx-mcp/
├── cmd/wx-mcp/
│   ├── main.go            MCP server + tool handlers + 复杂 enrich pipeline
│   ├── cache.go           metadata snapshot cache + index.sqlite + live export helpers
│   ├── agent.go           agent 入口: resolve_chat / chat_type / 自然语言目标解析
│   ├── cli.go             agent CLI aliases + cache/status/export/stats/unread
│   ├── main_test.go       parseTS / talkerHash / contentSummary 等测试
│   └── *_windows.go       Windows WCDB DLL / background refresh 适配
├── internal/
│   ├── wcdb/              WCDB dylib FFI (sqlite3_key_v2 解密)
│   ├── config/            ~/.config/wxcli/config.json 管理
│   ├── wxkind/            base_kind / app subtype / fav type / username 分类映射
│   └── wxparse/           transfer / red-packet / favorite XML 解析
├── scripts/package.sh     打 macOS 分发 zip + sha256
├── scripts/package-windows.ps1
├── install.sh             macOS agent-first installer / doctor / uninstall / clear-state / watcher
├── install.ps1            Windows installer / doctor / update / uninstall / clear-state
├── AGENTS.md              丢给 agent 的最短操作说明
├── mcp-server.json        生态/发现用 manifest
├── go.mod / go.sum
├── wx-mcp / wx-mcp.exe    编译产物 (.gitignore)
└── README.md
```

运行时加载同目录平台动态库: macOS `libWCDB.dylib`, Windows `libWCDB.dll` (分发包自带).

macOS 推荐首次 key 获取: agent 通过 `./install.sh --all --yes --json` 跑 `./wxkey bootstrap` →
必要时退出 WeChat 并 ad-hoc 重签 → 用户输一次 Mac admin 密码并存入 Keychain → sudo -S + task_for_pid + mach_vm_read 扫微信 heap →
SQLCipher 4 page-1 HMAC 验证 → 64 位 hex AES key → 存 `~/.config/wxcli/config.json`.

macOS 上 wx-mcp 检测到 config 缺 key 时仍会尝试自动 spawn 同目录的 `wxkey setup`, 但不会自动重签/重启 WeChat; 这类桌面副作用留给显式的 `./wxkey bootstrap`. Windows 上没有 `wxkey` companion; wx-mcp 直接扫描当前用户的 `Weixin.exe` / `WeChat.exe`, 验证 DB key 后写 schema-2 config.
wx-mcp 的运行时解密/读库本身不依赖 SIP: config 已有 key 时, 直接用 WCDB readonly 打开加密 DB.

分发 zip 结构:
```
wx-mcp-v1.5.1-darwin-arm64/
├── wx-mcp              (~10MB Go binary)
├── wxkey               (~3MB key 提取 CLI, 同目录被 wx-mcp spawn)
├── libWCDB.dylib       (~5MB Tencent WCDB, 随 binary 同目录加载)
├── install.sh           (agent-first install/doctor/uninstall/clear-state)
├── AGENTS.md
├── mcp-server.json
├── README.md
├── LICENSE
├── SECURITY.md
└── THIRD_PARTY_NOTICES.md
```

Windows 分发 zip 结构:
```
wx-mcp-v1.5.1-windows-amd64/
├── wx-mcp.exe
├── libWCDB.dll
├── install.ps1
├── AGENTS.md
├── mcp-server.json
├── README.md
├── docs/WINDOWS_USER_GUIDE.md
├── LICENSE
├── SECURITY.md
└── THIRD_PARTY_NOTICES.md
```

## Changelog

### Unreleased
- metadata refresh 期间如果 contact/session 源 DB 又变化, 普通查询静默使用最近一次完成的 metadata snapshot, 不再向用户输出容易误解为失败的 warning.
- `cache_status.metadata_stale_reason` 保留诊断信息, 但将 metadata 漂移原因改成人类可读文案.

### v1.5.1 (2026-05-20)
- 默认 cache 改为 metadata-only: 只缓存 contact/session 用于人名、群名和会话解析; 聊天正文由 `messages` / `search` / 单会话 `export_messages` 现查源 DB.
- installer 和 metadata refresh 都会检测/清理旧 index/raw 里的正文 cache 痕迹, 避免升级后继续保留明文聊天记录 cache.
- 移除旧的全量消息 cache 高级模式和 `new_messages` 工具; 不再提供全局无关键词正文遍历/统计.
- `red_packets` 的时间过滤改为 live join 对应 `Msg_<hash>` 取消息时间, 不再依赖消息 cache.
- macOS/Windows installer 增加清空状态入口: `--clear-state` / `-ClearState`, 以及带状态清理的卸载: `--uninstall --purge-state` / `-Uninstall -PurgeState`.
- live `messages` / search enrichment / 转账红包消息 join 会遍历所有包含同一 `Msg_<hash>` 表的 message shard, 避免同一会话跨分片时漏最新消息.
- `cache_status` 不再输出 `fresh=true/false`; 它只展示 metadata cache 诊断信息, stale 时返回 `metadata_stale_reason`.

### v1.4.5 (2026-05-11)
- **install.sh --update** 新增低副作用更新路径: git checkout 下先 `git pull --ff-only`, 再重装 wx-mcp / wxkey / libWCDB.
- `--update` 默认不 bootstrap、不刷新 cache、不重注册 MCP、不改 watcher; 朋友已有安装时可直接交给 agent 跑.
- `AGENTS.md` / `mcp-server.json` 增加 update 入口, 方便外部 agent 发现更新命令.

### v1.4.4 (2026-05-11)
- **media_resources** 新增消息附件/媒体资源定位工具, 直读 `message_resource.db`, 支持 chat/talker/local_id/server_id/server_id_str/type/resource_family 过滤.
- **media_resources** 解包 `packed_info` 里的 md5/文件名, 并按 WeChat 目录规则返回已存在的本地图片、视频缩略图/视频、文件路径.
- CLI 新增 `wx-mcp media` / `media-resources` / `attachments` alias; `AGENTS.md` 和 manifest 将 `media_resources` 标成 agent 主路径工具.

### v1.4.3 (2026-05-11)
- **MCP schema** 默认拒绝未知参数, fields/format/search_mode 加枚举约束, tools/list 增加 readOnly/destructive/idempotent hints.
- **red_packets** 支持 chat/talker/sender/after/before, 时间和 sender 过滤通过 cache join messages metadata.
- **sns media** 补 url/thumb key/token/enc_idx、md5、尺寸、video_md5、video_duration 等字段.
- **export_messages** 改为批量流式写文件, 避免一次性构造大字符串; 分发包额外产出 `.sha256`.

### v1.3.1 (2026-04-16)
- **messages** 支持公众号/服务号 — `findMsgDB` 以前只扫 `message_0..4.db`, 漏了 `biz_message_0..1.db` (公众号消息实际存那边), 导致所有 `gh_*` 拉不到消息. 现在 glob 扫 `(message|biz_message)_<n>.db` 全族, shard 数也不再 hardcode
- **favorites** 剥 raw `type_id` (= raw int 重复 `favorite_type`) — 违反"raw int 全 resolve"原则
- **sessions.last_sender_wxid** 剥订阅号合集 sender 前缀 — 以前返回 `_$_CUSTOM_USERNAME_PREFIX_$_<aggId>:<realId>`, 现在只保留 `<realId>` (通常是 `gh_xxx`)
- **messages** 对聚合 session (`brandsessionholder` / `brandservicesessionholder`) 给明确错误 "本身无消息表, 按具体 gh_<id> 查", 替换 cryptic "table not found"
- **schema** 按 prefix 分族列 db — 以前把 `biz_message_*` / `message_fts` 误折成 `message_0..4` 的 shard, 现在 `message`/`biz_message`/`message_fts`/`message_resource` 各占一条, `shard_count` 按族算

### v1.3.0 (2026-04-16)
- **messages.keyword** 修 zstd bug — 原本 SQL LIKE 在压缩字节上 match 失败, 现在拉宽 SQL 后在解压内容上 in-memory filter, 能命中 app 类消息 (转账/链接/小程序/...)
- **transfers** 加 amount / description / memo (batch join messages 解 XML); 字段 rename: payer_wxid / receiver_wxid / session_username
- **red_packets** drop 4 个语义不明 raw int (hb_status/hb_type/receive_status/scene_id), 加 wishing / scene_text / native_url
- **search** 补 sender_wxid / sender_display_name / base_kind / kind_name (join 回 Msg_<hash> 路由), drop FTS 内部 session_id, content 剥群聊 sender prefix
- **chatroom_announcements** 字段下划线后缀清理 (announcement_/editor_/publish_time_ → announcement/editor_wxid/publish_time)
- **favorites** 加 favorite_type resolve, 加 title / description / url (从 content XML 提取), drop local_id/update_seq/flag, rename fromusr → from_wxid
- **group_members** drop big_head_url, is_owner / is_friend → bool
- **schema** 修 P0 panic (全局调用 nil deref); 单 db 加载失败现在归并 error 字段而非 silent skip
- 模块化重构: kind/parse helpers 抽到 internal/wxkind + internal/wxparse, ~30 个 unit test 覆盖
- search / schema 的 silent error swallow → fail loud

### v1.2.0
- schema tool, cross-db keyword search, is_from_me, create_time_human, description sweep

### v1.1.0
- agent-friendly display_name across all 12 tools

### v1.0.0
- 初始 12 个 tool

---

<!-- babata-star-callout-v2 -->
## If this saved you time

Starring the repo helps me prioritize which integrations to keep maintained. This project is part of [babata](https://github.com/r266-tech) — a personal, macOS-native AI infrastructure stack.
