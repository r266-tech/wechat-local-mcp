package main

// arg helpers
type props = map[string]any

func strProp(desc string) any  { return map[string]any{"type": "string", "description": desc} }
func intProp(desc string) any  { return map[string]any{"type": "integer", "description": desc} }
func boolProp(desc string) any { return map[string]any{"type": "boolean", "description": desc} }
func enumStrProp(desc string, values ...string) any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func jsonSchema(properties props, required []string) any {
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func listedToolDefs() []toolDef {
	out := make([]toolDef, len(toolDefs))
	copy(out, toolDefs)
	for i := range out {
		out[i].Annotations = toolAnnotations(out[i].Name)
	}
	return out
}

func toolAnnotations(name string) map[string]any {
	switch name {
	case "cache_refresh":
		return map[string]any{
			"readOnlyHint":    false,
			"destructiveHint": false,
			"idempotentHint":  true,
			"openWorldHint":   false,
		}
	case "cache_rebuild":
		return map[string]any{
			"readOnlyHint":    false,
			"destructiveHint": true,
			"idempotentHint":  true,
			"openWorldHint":   false,
		}
	case "export_messages":
		return map[string]any{
			"readOnlyHint":    false,
			"destructiveHint": false,
			"idempotentHint":  false,
			"openWorldHint":   false,
		}
	default:
		return map[string]any{
			"readOnlyHint":    true,
			"destructiveHint": false,
			"idempotentHint":  true,
			"openWorldHint":   false,
		}
	}
}

var toolDefs = []toolDef{
	{
		Name: "sessions",
		Description: "聊天会话列表, 按 sort_timestamp DESC. " +
			"字段: username / display_name / chat_type (private/group/official_account/folded/bot/...) / unread_count / summary (末条预览) / " +
			"sort_timestamp (含置顶调整, 用于排序) / last_timestamp (最新消息实际时间, 多数情况两者相等) / " +
			"last_sender_wxid / last_sender_display_name / " +
			"last_msg_type (base_kind raw int) / last_msg_sub_type (subtype raw int) / " +
			"last_msg_kind_name (resolved: text/image/voice/card/video/sticker/location/voip/system, " +
			"app 子类 link/file/music/solitaire/quote/transfer/red_packet/miniprogram/forward_chat/announcement/pat/channel_video). " +
			"type_filter 支持 all/private(friend)/group/official_account(official)/folded/bot, 可逗号分隔. keyword 匹配 username / summary / " +
			"display_name / nick_name / remark / alias (大小写无关, 空格无关).",
		InputSchema: jsonSchema(props{
			"limit":       intProp("返回条数 (默认 50)"),
			"type_filter": strProp("all (默认) / private / group / official_account / folded / bot, 可逗号分隔"),
			"keyword":     strProp("模糊搜索"),
		}, nil),
	},
	{
		Name: "resolve_chat",
		Description: "把昵称/备注/alias/群名/微信号解析成 wx-mcp 可用的 username/talker. " +
			"当 agent 只知道人名或群名时先调这个; 返回 candidates 按精确匹配和最近会话排序.",
		InputSchema: jsonSchema(props{
			"query":       strProp("要解析的人名/群名/微信号"),
			"chat":        strProp("query 的别名"),
			"keyword":     strProp("query 的别名"),
			"type_filter": strProp("可选: private / group / official_account / folded / bot, 可逗号分隔"),
			"limit":       intProp("候选数量 (默认 10)"),
		}, nil),
	},
	{
		Name: "contacts",
		Description: "搜索微信联系人或群. 不传 keyword 则列出全部. " +
			"字段: username / display_name (remark > nick_name > username) / nick_name / " +
			"remark (omitempty) / alias (omitempty, 微信号) / description (omitempty, 个性签名/群简介) / " +
			"type (friend/group/official_account/corp_im/clawbot/stranger/other, 由 username 规则推导) / chat_type / " +
			"is_verified (bool, 公众号/服务号/认证账号).",
		InputSchema: jsonSchema(props{
			"keyword":      strProp("模糊搜索 (匹配 wxid/昵称/备注/alias/拼音首字母)"),
			"limit":        intProp("返回条数 (默认 50)"),
			"groups_only":  boolProp("仅返回群"),
			"friends_only": boolProp("仅返回好友 (排除群和公众号)"),
		}, nil),
	},
	{
		Name: "messages",
		Description: "会话消息, 默认直接读取实时微信消息 DB, 不缓存聊天正文. talker 可传 wxid/xxx@chatroom; chat 可传昵称/备注/群名让 wx-mcp 用 metadata cache 自动解析. " +
			"view=agent 返回给 agent 直接消费的 query/freshness/messages envelope; query 含 returned/limit/offset/has_more/next_offset, 用于可靠分页爬全量. messages[] 是低噪声 timeline 行: id(local_id/server_id_str/talker) / time / create_time(unix秒) / time_iso / sender / sender_wxid / is_from_me / kind / text / warnings, " +
			"并为非文本消息提供 display-ready 结构: images / videos / files / link / music / miniprogram / forward_chat / quote / transfer / red_packet / location / card / voice / video / sticker / solitaire / announcement / pat. " +
			"默认遵循微信 UI 可见语义: 图片/视频/文件给 agent 可直接读取的本机 path, 语音默认优先用 faster-whisper large-v3 返回本地 ASR transcript, raw SILK、不可读 .dat、CDN/aeskey、协议码和 raw XML 下沉到 debug/full/media_resources; 引用消息会扁平到 quote 并复用原消息可见 payload; 合并转发 item 使用 source_id 统一关联原消息, 媒体无法解析时给明确 warnings; 链接直接给 title/url/source/thumb_url. " +
			"fields=lite (默认) 返回: local_id / server_id / server_id_str / create_time / create_time_human / " +
			"talker / talker_display_name / chat_type / sender_wxid / sender_display_name / is_from_me / base_kind / kind_name / content_summary " +
			"/ id / display / display-ready 非文本结构 / warnings (群聊已剥 'wxid:\\n' 前缀). " +
			"正常 agent 查询不需要 fields=full; 默认隐藏 media_resources/media_read_hints/CDN/aeskey/.dat 解码细节. 维护者诊断时才传 include_debug=true/debug=true 或 fields=full. 可传 include_media_paths=false 跳过媒体路径补齐. " +
			"若消息 XML 或引用消息(refermsg)里的真实图片 md5 能匹配本机 temp 里的 PNG/JPG 副本, media_read_hints 会优先给 direct_readable_local_paths 供 agent 直接读图; 引用图片带 source=message_refermsg / message_role=referenced_message. " +
			"图片 .dat 会 best-effort 解码到 ~/.wx-mcp/media-cache 并返回 decoded_media_local_paths / decoded_local_paths; 微信 V4 图片缺 image_key 或 image_key 失效时会先自动跑 wxkey image-key 刷新并重试, 仍失败才在 agent view 给 concise warning, debug/full 返回 decode_status=needs_image_key 和刷新诊断. " +
			"fields=full 是调试兼容接口, 额外返回: subtype / message_content (raw 文本/XML) / " +
			"message_content_parsed (图/表情/app/语音 XML 结构化, 引用递归 depth=5). " +
			"forward_chat (subtype=19) 的 parsed 额外含 forward_items[] (每条: datatype/sourcename/sourcetime/datatitle/datadesc/datafmt/fullmd5/datasize/src_msg_localid); " +
			"datatype 1=text/2=image/3=voice/4=video/5=link/6=location/8=file/17=nested-forward/18=miniprogram (文本走 datadesc, 文件走 datatitle+fullmd5; 嵌套走 nested_items[] 递归 depth=5; agent view 直接递归输出 items). " +
			"base_kind: 1=text/3=image/34=voice/42=card/43=video/47=sticker/48=location/49=app/50=voip/10000=system. " +
			"kind_name 在 base_kind=49 时按 subtype 细化: 5=link/6=file/19=forward_chat/33,36=miniprogram/" +
			"53=solitaire/57=quote/87=announcement/2000=transfer/2001=red_packet/62=pat/51=channel_video/3,76=music. " +
			"after/before 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"talker":              strProp("会话对象 (wxid 或 xxx@chatroom)"),
			"chat":                strProp("会话显示名/备注/alias/群名; talker 为空时自动解析"),
			"limit":               intProp("返回条数 (默认 50)"),
			"offset":              intProp("跳过条数 (默认 0)"),
			"after":               strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before":              strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
			"keyword":             strProp("消息内容关键词"),
			"type":                strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":           strProp("可选: 同 type"),
			"base_kind":           intProp("可选: base_kind raw int"),
			"sender":              strProp("可选: sender wxid 或昵称"),
			"view":                enumStrProp("返回视图: default 保持原 fields 输出; agent 返回低噪声扁平 timeline", "default", "agent"),
			"order":               enumStrProp("查询顺序: desc 最近消息优先 (默认) / asc 最早消息优先", "desc", "asc"),
			"display_order":       enumStrProp("输出展示顺序: query 保持查询顺序 (默认) / desc / asc; 用 order=desc + display_order=asc 展示最近 N 条的聊天顺序", "query", "desc", "asc"),
			"fields":              enumStrProp("lite (默认) / full", "lite", "full"),
			"include_media_paths": boolProp("是否补齐图片/视频/文件本机资源路径和 display-ready media refs (默认 true; 传 false 可关闭)"),
			"include_debug":       boolProp("是否在 lite/agent 输出中包含调试媒体字段或 debug 节点 (默认 false)"),
			"debug":               boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name:        "chat_timeline",
		Description: "面向 agent 展示/总结的高层聊天时间线工具, 是普通查消息的首选入口. 自动解析 chat, live 读取最近消息, 默认 order=desc + display_order=asc 展示最近窗口的聊天顺序. 返回对象包含 query / freshness / messages; query 含 returned/limit/offset/has_more/next_offset 便于可靠分页爬全量; messages 是低噪声 agent 行, 每条有稳定 id、time/create_time/time_iso、sender_wxid/is_from_me、display-ready 非文本结构和轻量 warnings, 默认隐藏调试噪音.",
		InputSchema: jsonSchema(props{
			"talker":              strProp("会话对象 (wxid 或 xxx@chatroom)"),
			"chat":                strProp("会话显示名/备注/alias/群名; talker 为空时自动解析"),
			"limit":               intProp("返回条数 (默认 50)"),
			"offset":              intProp("跳过条数 (默认 0)"),
			"after":               strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before":              strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
			"keyword":             strProp("消息内容关键词"),
			"type":                strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":           strProp("可选: 同 type"),
			"base_kind":           intProp("可选: base_kind raw int"),
			"sender":              strProp("可选: sender wxid 或昵称"),
			"order":               enumStrProp("查询顺序: desc 最近消息优先 (默认) / asc 最早消息优先", "desc", "asc"),
			"display_order":       enumStrProp("输出展示顺序: asc 默认聊天顺序 / desc / query", "query", "desc", "asc"),
			"include_images":      boolProp("是否补齐图片/文件路径 (默认 true; false 时等价 include_media_paths=false)"),
			"include_media_paths": boolProp("是否补齐图片/视频/文件本机资源路径和 display-ready media refs (默认 true)"),
			"include_debug":       boolProp("是否附带 debug 节点 (默认 false)"),
			"debug":               boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name: "media_resources",
		Description: "消息附件/媒体资源定位. 读取 message_resource.db, 按 chat/talker/local_id/server_id/time/sender/type 过滤, " +
			"默认返回 agent-ready 资源: images/videos/files[].path 和 resources[].path 只会是可直接读取的本机图片/视频/文件路径, resources 默认不暴露 resource_id/status/raw family/variant 等维护字段. " +
			"不可读 .dat、重复候选 paths、local_path_details、raw type/variant_code、解码细节和候选路径默认隐藏; 维护者诊断时传 include_debug=true/debug=true 才返回. " +
			"对图片会补查消息 XML 的真实图片 md5, 若本机 temp 存在同 md5 PNG/JPG 副本则优先返回真实 path. 图片 .dat 会 best-effort 解码到 ~/.wx-mcp/media-cache; 微信 V4 图片缺 image_key 或 image_key 失效时会自动跑 wxkey image-key 刷新并重试, 仍失败才给 concise warning, 不把 .dat 当图片路径给 agent. wx-mcp 不做图片识别. " +
			"适合 agent 在 messages/search 拿到 local_id 或 server_id 后继续定位图片、视频、文件和转发记录里的资源. " +
			"after/before 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"talker":                strProp("可选: 限定 wxid 或 xxx@chatroom"),
			"chat":                  strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"local_id":              intProp("可选: message local_id"),
			"server_id":             intProp("可选: message server_id"),
			"server_id_str":         strProp("可选: message server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"message_server_id":     intProp("可选: server_id 的别名, 兼容 red_packets/transfers 输出"),
			"message_server_id_str": strProp("可选: message_server_id 字符串形式"),
			"after":                 strProp("可选: 起始时间"),
			"before":                strProp("可选: 截止时间"),
			"sender":                strProp("可选: sender wxid 或昵称"),
			"type":                  strProp("可选: kind_name, 如 image/video/file/forward_chat/miniprogram"),
			"kind_name":             strProp("可选: 同 type"),
			"base_kind":             intProp("可选: base_kind raw int"),
			"resource_family":       strProp("可选: image / video / file / cover / unknown"),
			"resource_type_raw":     intProp("可选: MessageResourceDetail.type raw int"),
			"include_local_paths":   boolProp("是否返回已存在本地文件路径 (默认 true)"),
			"include_debug":         boolProp("是否返回 .dat/local_path_details/raw type/解码细节等调试信息 (默认 false)"),
			"debug":                 boolProp("include_debug 的别名"),
			"limit":                 intProp("返回消息条数 (默认 50)"),
			"offset":                intProp("跳过消息条数 (默认 0)"),
		}, nil),
	},
	{
		Name: "group_members",
		Description: "群成员. 字段: username / display_name / nick_name / " +
			"remark (omitempty) / alias (omitempty) / is_owner (bool) / is_friend (bool). " +
			"stats=true 附 msg_count (扫消息表较慢).",
		InputSchema: jsonSchema(props{
			"chatroom_id": strProp("群 ID (xxx@chatroom)"),
			"chat":        strProp("群名/备注; chatroom_id 为空时自动解析"),
			"stats":       boolProp("附带每人发言条数 (扫消息表, 较慢)"),
			"limit":       intProp("返回条数 (默认 100)"),
			"offset":      intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name: "sns",
		Description: "朋友圈 timeline. 返回字段: tid / username / nickname / avatar_url / " +
			"create_time / content / type / private / liked_by_me / " +
			"media (type/sub_type/url/thumb/url_key/thumb_key/md5/width/height/total_size/video_md5/video_duration) / location (name/lat/lon) / " +
			"likes ([username, nickname]) / " +
			"comments ([username, nickname, content, create_time, reply_to, reply_to_nick]). " +
			"时间过滤针对 XML 里的 createTime (非 SQL tid), 先按 tid DESC 粗拉再解析过滤.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name:        "sns_feed",
		Description: "朋友圈时间线, 等价于 sns 但语义更明确. 支持 user/keyword/after/before/limit/offset.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name:        "sns_search",
		Description: "朋友圈正文全文搜索. 返回字段同 sns_feed, keyword 必填.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, []string{"keyword"}),
	},
	{
		Name:        "sns_notifications",
		Description: "朋友圈互动通知: 点赞/评论. 默认仅未读; include_read=true 返回已读+未读.",
		InputSchema: jsonSchema(props{
			"include_read": boolProp("包含已读通知"),
			"after":        strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":       strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":        intProp("返回条数 (默认 50)"),
		}, nil),
	},
	{
		Name: "search",
		Description: "跨会话消息全文搜索, 默认直接读取微信 message_fts.db 和 Msg_<hash> 分片, 不缓存聊天正文. metadata cache 只用于 chat/sender 名称解析. " +
			"字段: content (群聊已剥 'wxid:\\n' 前缀) / local_id / talker / talker_display_name / chat_type / " +
			"create_time / sender_wxid / sender_display_name / base_kind / kind_name. " +
			"sender + base_kind/kind_name 来自 join 回所有包含 Msg_<hash>(talker) 的 message shard. " +
			"search_mode=fts/like/auto 保留兼容; 三种模式都使用微信 live FTS, 不做全局 LIKE 扫描.",
		InputSchema: jsonSchema(props{
			"keyword":     strProp("搜索关键词"),
			"talker":      strProp("可选: 限定 wxid 或 xxx@chatroom"),
			"chat":        strProp("可选: 限定昵称/备注/群名, 自动解析为 talker"),
			"after":       strProp("可选: 起始时间"),
			"before":      strProp("可选: 截止时间"),
			"type":        strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":   strProp("可选: 同 type"),
			"base_kind":   intProp("可选: base_kind raw int"),
			"sender":      strProp("可选: sender wxid 或昵称"),
			"search_mode": enumStrProp("兼容参数: fts (默认) / like / auto; 当前都走微信 live FTS", "fts", "like", "auto"),
			"limit":       intProp("返回条数 (默认 20)"),
		}, []string{"keyword"}),
	},
	{
		Name: "sql",
		Description: "本地 WCDB SQL. OS 级 readonly (SQLITE_OPEN_READONLY 打开), DDL/DML 会 rc≠0 直接报错 — " +
			"SELECT/WITH 默认外层限流; PRAGMA/EXPLAIN 允许直接执行. " +
			"db 位置由 subdir/file 定位. 用 schema tool 列出有哪些 db 和表.",
		InputSchema: jsonSchema(props{
			"query":  strProp("SQL 语句"),
			"subdir": strProp("db_storage 下的子目录 (默认 session)"),
			"file":   strProp("数据库文件名 (默认 session.db)"),
			"limit":  intProp("SELECT/WITH 外层最大返回行数 (默认 200, 最大 1000)"),
		}, []string{"query"}),
	},
	{
		Name: "transfers",
		Description: "微信转账记录. 字段: transfer_id / transcation_id / session_username / session_display_name / " +
			"payer_wxid / payer_display_name / receiver_wxid / receiver_display_name / pay_sub_type (raw int) / " +
			"begin_transfer_time / invalid_time / last_modified_time / message_server_id / " +
			"amount (从 messages join 出, 如 '￥5.00') / description (人类可读, 如 '收到转账5.00元') / memo (转账留言, omitempty). " +
			"amount/description/memo 通过 message_server_id 从所有匹配 Msg_<hash>(session_username) 的 shard 拉 XML 提取. " +
			"after/before 按 begin_transfer_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "red_packets",
		Description: "微信红包记录. 字段: send_id / sender_wxid / sender_display_name / " +
			"session_username / session_display_name / native_url (微信红包深链) / message_server_id / " +
			"wishing (祝福语 如 '恭喜发财大吉大利', 从 join XML 提取) / scene_text (如 '微信红包', omitempty). " +
			"红包金额随机, 仅领取后可见, 不在本地数据中. " +
			"不传 after/before 时按 rowid DESC (近似收到顺序); 传时间过滤时 live join 对应 Msg_<hash> 取 create_time.",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"talker": strProp("可选: 限定会话对象"),
			"chat":   strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"sender": strProp("可选: sender wxid 或昵称"),
			"after":  strProp("可选: 起始时间"),
			"before": strProp("可选: 截止时间"),
		}, nil),
	},
	{
		Name: "favorites",
		Description: "微信收藏. 字段: server_id / favorite_type (text/image/voice/video/link/location/file/" +
			"chat_history/miniprogram/unknown) / update_time / source_id (内部复合 ID) / " +
			"from_wxid / from_display_name / source_chat_username (omitempty) / source_chat_display_name / " +
			"content (XML 原文) / title (从 XML 提取, omitempty) / description (omitempty) / url (omitempty). " +
			"after/before 按 update_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "chatroom_announcements",
		Description: "群公告. 字段: chatroom_id / chatroom_display_name / announcement / " +
			"editor_wxid / editor_display_name / publish_time. " +
			"不传 chatroom_id 按 publish_time DESC 列所有群公告. " +
			"after/before 按 publish_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"chatroom_id": strProp("群 ID (xxx@chatroom), 不传则返回所有群公告 (按发布时间倒序)"),
			"limit":       intProp("返回条数 (默认 20)"),
			"after":       strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before":      strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "forward_history",
		Description: "最近转发目标列表 (你最近转发到了哪些会话, 用于快捷转发 UI). " +
			"非'被转发的消息历史' — 不存消息内容. 字段: username / display_name / forward_time. " +
			"after/before 按 forward_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "schema",
		Description: "WCDB 数据库结构. 不传参数列出所有 subdir 下 db 的表名 (分片的 message_*.db 折叠为一条 + shard_count). " +
			"传 subdir+file 返回该 db 每张表的 CREATE TABLE DDL.",
		InputSchema: jsonSchema(props{
			"subdir": strProp("db_storage 下子目录"),
			"file":   strProp("数据库文件名"),
		}, nil),
	},
	{
		Name:        "cache_status",
		Description: "查看 wx-mcp metadata snapshot cache 与统一 index.sqlite 诊断信息. 默认只缓存联系人/会话用于名称解析, 不缓存聊天正文, 不触发 wxkey setup; 不再输出 fresh=true 这种易误解的全局新鲜度结论.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name: "cache_refresh",
		Description: "刷新 metadata snapshot cache 并重建统一 index.sqlite. 默认只 snapshot contact/contact.db 和 session/session.db; 聊天正文现查. " +
			"background=true 立即返回并在后台刷新, 避免 MCP 调用超时.",
		InputSchema: jsonSchema(props{
			"force":      boolProp("强制重建所有 plaintext snapshots"),
			"background": boolProp("后台刷新并立即返回"),
		}, nil),
	},
	{
		Name:        "cache_rebuild",
		Description: "删除当前 wx-mcp cache 目录后完整重建 metadata snapshot cache + index.sqlite.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name:        "unread",
		Description: "未读会话列表. metadata cache-backed; 字段同 sessions, 仅返回 unread_count > 0. type_filter/filter 支持 private,group 等逗号分隔.",
		InputSchema: jsonSchema(props{
			"limit":       intProp("返回条数 (默认 50)"),
			"type_filter": strProp("all/private/group/official_account/folded/bot, 可逗号分隔"),
			"filter":      strProp("type_filter 的别名, 兼容 wx-cli 风格"),
		}, nil),
	},
	{
		Name:        "stats",
		Description: "metadata cache 状态统计. wx-mcp 不缓存聊天正文, 因此只返回 sessions/contacts 计数和提示.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name: "export_messages",
		Description: "导出单个 chat/talker 的消息到本地文件, 直接读取实时消息 DB; 不支持全局无关键词导出. " +
			"format=jsonl/markdown/html, 支持 after/before/keyword/limit 过滤.",
		InputSchema: jsonSchema(props{
			"path":      strProp("输出文件绝对路径"),
			"format":    enumStrProp("jsonl (默认) / markdown / html", "jsonl", "markdown", "html"),
			"talker":    strProp("可选: 限定会话对象"),
			"chat":      strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"after":     strProp("可选: 起始时间"),
			"before":    strProp("可选: 截止时间"),
			"keyword":   strProp("可选: 内容关键词"),
			"type":      strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name": strProp("可选: 同 type"),
			"base_kind": intProp("可选: base_kind raw int"),
			"sender":    strProp("可选: sender wxid 或昵称"),
			"limit":     intProp("最大导出条数 (默认 10000)"),
			"offset":    intProp("跳过条数 (默认 0)"),
		}, []string{"path"}),
	},
}
