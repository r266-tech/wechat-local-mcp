package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

type cliOptions struct {
	Pretty bool
}

type cliSuccessEnvelope struct {
	OK      bool   `json:"ok"`
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
	Data    any    `json:"data"`
}

type cliErrorEnvelope struct {
	OK    bool     `json:"ok"`
	Error cliError `json:"error"`
}

type cliError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
}

type cliCommandSpec struct {
	Command     string   `json:"command"`
	Aliases     []string `json:"aliases,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Usage       string   `json:"usage"`
	Positional  string   `json:"positional,omitempty"`
	Description string   `json:"description,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

var cliCommandSpecs = []cliCommandSpec{
	{Command: "tools", Aliases: []string{"list-tools", "list_tools"}, Usage: appName + " tools", Description: "List all tool schemas.", Examples: []string{appName + " tools"}},
	{Command: "call", Usage: appName + " call <tool> [--key value ...]", Description: "Call a tool with key/value CLI arguments.", Examples: []string{appName + " call chat_timeline --chat wxmcp测试群 --limit 20"}},
	{Command: "call-json", Aliases: []string{"call_json"}, Usage: appName + " call-json <tool> '<json args>'", Description: "Call a tool with a JSON argument object from argv or stdin.", Examples: []string{appName + " call-json messages '{\"chat\":\"wxmcp测试群\",\"limit\":20,\"view\":\"agent\"}'"}},
	{Command: "tool-schema", Aliases: []string{"describe", "describe-tool", "tool_schema"}, Usage: appName + " tool-schema <command-or-tool>", Description: "Return one command/tool schema.", Examples: []string{appName + " tool-schema timeline"}},
	{Command: "serve-mcp", Aliases: []string{"mcp-server", "mcp", "serve"}, Usage: appName + " serve-mcp", Description: "Run the optional legacy MCP stdio adapter.", Examples: []string{appName + " serve-mcp"}},
	{Command: "cache", Usage: appName + " cache <status|refresh|rebuild>", Description: "Metadata cache subcommands.", Examples: []string{appName + " cache status"}},
	{Command: "cache status", Tool: "cache_status", Usage: appName + " cache status", Examples: []string{appName + " cache status"}},
	{Command: "cache refresh", Tool: "cache_refresh", Usage: appName + " cache refresh [--force] [--background]", Examples: []string{appName + " cache refresh --force"}},
	{Command: "cache rebuild", Tool: "cache_rebuild", Usage: appName + " cache rebuild", Examples: []string{appName + " cache rebuild"}},
	{Command: "sessions", Tool: "sessions", Usage: appName + " sessions [--limit 20] [--type-filter private,group]", Examples: []string{appName + " sessions --limit 20", appName + " sessions --type-filter group --keyword 测试"}},
	{Command: "resolve-chat", Aliases: []string{"resolve_chat"}, Tool: "resolve_chat", Usage: appName + " resolve-chat <chat> [--type-filter private]", Positional: "query", Examples: []string{appName + " resolve-chat wxmcp测试群 --type-filter group"}},
	{Command: "contacts", Tool: "contacts", Usage: appName + " contacts [--keyword 李]", Examples: []string{appName + " contacts --keyword 李 --limit 20"}},
	{Command: "history", Aliases: []string{"messages"}, Tool: "messages", Usage: appName + " history <chat> [--limit 50] [--after 2026-05-11] [--view agent]", Positional: "chat", Examples: []string{appName + " history wxmcp测试群 --view agent --limit 50"}},
	{Command: "timeline", Aliases: []string{"chat-timeline", "chat_timeline", "conversation-view", "conversation_view"}, Tool: "chat_timeline", Usage: appName + " timeline <chat> [--limit 10] [--display-order asc]", Positional: "chat", Examples: []string{appName + " timeline wxmcp测试群 --limit 20", appName + " timeline wxmcp测试群 --limit 20 --offset 20"}},
	{Command: "media", Aliases: []string{"media-resources", "media_resources", "attachments"}, Tool: "media_resources", Usage: appName + " media <chat> [--local-id 123] [--type image|video|file]", Positional: "chat", Examples: []string{appName + " media wxmcp测试群 --local-id 10", appName + " media wxmcp测试群 --type image --limit 20"}},
	{Command: "search", Tool: "search", Usage: appName + " search <keyword> [--in 某群] [--after 2026-01-01] [--type text]", Positional: "keyword", Examples: []string{appName + " search 测试 --in wxmcp测试群 --limit 10"}},
	{Command: "members", Aliases: []string{"group-members", "group_members"}, Tool: "group_members", Usage: appName + " members <group>", Positional: "chat", Examples: []string{appName + " members wxmcp测试群 --limit 50"}},
	{Command: "unread", Tool: "unread", Usage: appName + " unread [--limit 50]", Examples: []string{appName + " unread --limit 50"}},
	{Command: "stats", Tool: "stats", Usage: appName + " stats", Examples: []string{appName + " stats"}},
	{Command: "favorites", Tool: "favorites", Usage: appName + " favorites [--limit 20]", Examples: []string{appName + " favorites --limit 20"}},
	{Command: "red-packets", Aliases: []string{"red_packets"}, Tool: "red_packets", Usage: appName + " red-packets [--limit 20]", Examples: []string{appName + " red-packets --limit 20"}},
	{Command: "transfers", Tool: "transfers", Usage: appName + " transfers [--limit 20]", Examples: []string{appName + " transfers --limit 20"}},
	{Command: "sns-feed", Aliases: []string{"sns", "sns_feed"}, Tool: "sns_feed", Usage: appName + " sns-feed [--limit 20]", Examples: []string{appName + " sns-feed --limit 20"}},
	{Command: "sns-search", Aliases: []string{"sns_search"}, Tool: "sns_search", Usage: appName + " sns-search <keyword>", Positional: "keyword", Examples: []string{appName + " sns-search 关键词 --limit 20"}},
	{Command: "sns-notifications", Aliases: []string{"sns_notifications"}, Tool: "sns_notifications", Usage: appName + " sns-notifications [--include-read]", Examples: []string{appName + " sns-notifications --include-read"}},
	{Command: "schema", Tool: "schema", Usage: appName + " schema [--subdir session] [--file session.db]", Examples: []string{appName + " schema --subdir session --file session.db"}},
	{Command: "sql", Tool: "sql", Usage: appName + " sql <query>", Positional: "query", Examples: []string{appName + " sql 'select count(*) as n from Session' --subdir session --file session.db"}},
	{Command: "announcements", Aliases: []string{"chatroom-announcements", "chatroom_announcements"}, Tool: "chatroom_announcements", Usage: appName + " announcements [chatroom-id]", Positional: "chatroom_id", Examples: []string{appName + " announcements wxmcp测试群 --limit 20"}},
	{Command: "forward-history", Aliases: []string{"forward_history"}, Tool: "forward_history", Usage: appName + " forward-history [--limit 20]", Examples: []string{appName + " forward-history --limit 20"}},
	{Command: "export", Aliases: []string{"export-messages", "export_messages"}, Tool: "export_messages", Usage: appName + " export <chat> --path /tmp/messages.jsonl [--format jsonl|markdown|html] [--view agent|raw]", Positional: "chat", Examples: []string{appName + " export wxmcp测试群 --path /tmp/wxmcp.jsonl --format jsonl", appName + " export wxmcp测试群 --path /tmp/wxmcp.raw.jsonl --view raw"}},
}

func maybeRunCLI(args []string) bool {
	opts, args, err := parseGlobalCLIOptions(args)
	if err != nil {
		exitCLIError(opts, 2, "invalid_global_argument", err.Error(), "", "")
	}
	if len(args) == 0 {
		runCLIHelp("", opts)
		return true
	}
	if hasHelpFlag(args[1:]) {
		runCLIHelp(helpTargetForCommand(args), opts)
		return true
	}
	switch args[0] {
	case "-h", "--help", "help":
		runCLIHelp(strings.Join(args[1:], " "), opts)
		return true
	case "serve-mcp", "mcp-server", "mcp", "serve":
		runMCPServer()
		return true
	case "tools", "list-tools", "list_tools":
		runToolsCLI(opts)
		return true
	case "call":
		runGenericToolCLI(args[1:], opts)
		return true
	case "call-json", "call_json":
		runToolJSONCLI(args[1:], opts)
		return true
	case "tool-schema", "tool_schema", "describe", "describe-tool":
		runToolSchemaCLI(args[1:], opts)
		return true
	case "cache":
		runCacheCLI(args[1:], opts)
		return true
	case "resolve-chat", "resolve_chat":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" {
			flags["query"] = q
		}
		runToolCLI("resolve_chat", flags, opts, args[0])
		return true
	case "sessions":
		runToolCLI("sessions", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "contacts":
		runToolCLI("contacts", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "history", "messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("messages", flags, opts, args[0])
		return true
	case "timeline", "chat-timeline", "chat_timeline", "conversation-view", "conversation_view":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("chat_timeline", flags, opts, args[0])
		return true
	case "media", "media-resources", "media_resources", "attachments":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("media_resources", flags, opts, args[0])
		return true
	case "search":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		if v, ok := flags["in"]; ok && flags["chat"] == nil {
			flags["chat"] = v
			delete(flags, "in")
		}
		runToolCLI("search", flags, opts, args[0])
		return true
	case "members", "group-members", "group_members":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("group_members", flags, opts, args[0])
		return true
	case "stats":
		runToolCLI("stats", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "unread":
		runToolCLI("unread", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "export", "export-messages", "export_messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("export_messages", flags, opts, args[0])
		return true
	case "favorites":
		runToolCLI("favorites", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "red-packets", "red_packets":
		runToolCLI("red_packets", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "transfers":
		runToolCLI("transfers", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sns", "sns-feed", "sns_feed":
		runToolCLI("sns_feed", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sns-search", "sns_search":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		runToolCLI("sns_search", flags, opts, args[0])
		return true
	case "sns-notifications", "sns_notifications":
		runToolCLI("sns_notifications", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sql":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" && flags["query"] == nil {
			flags["query"] = q
		}
		runToolCLI("sql", flags, opts, args[0])
		return true
	case "schema":
		runToolCLI("schema", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "announcements", "chatroom-announcements", "chatroom_announcements":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil {
			flags["chatroom_id"] = chat
		}
		runToolCLI("chatroom_announcements", flags, opts, args[0])
		return true
	case "forward-history", "forward_history":
		runToolCLI("forward_history", parseKVFlags(args[1:]), opts, args[0])
		return true
	default:
		exitCLIError(opts, 2, "unknown_command", fmt.Sprintf("unknown command %q", args[0]), "", args[0])
		return true
	}
}

func runToolsCLI(opts cliOptions) {
	data := map[string]any{
		"query": map[string]any{
			"tool":     "tools",
			"command":  "tools",
			"returned": len(toolDefs),
		},
		"tools": listedToolDefs(),
	}
	writeCLISuccess("tools", "tools", data, opts)
}

func runGenericToolCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" call <tool> [--key value ...]", "", "call")
	}
	runToolCLI(args[0], parseKVFlags(args[1:]), opts, "call")
}

func runToolJSONCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" call-json <tool> '<json args>'", "", "call-json")
	}
	raw := ""
	if len(args) > 1 {
		raw = args[1]
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			exitCLIError(opts, 1, "stdin_read_error", err.Error(), args[0], "call-json")
		}
		raw = string(data)
	}
	flags := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &flags); err != nil {
			exitCLIError(opts, 1, "invalid_json", "invalid json args: "+err.Error(), args[0], "call-json")
		}
	}
	runToolCLI(args[0], flags, opts, "call-json")
}

func runToolSchemaCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" tool-schema <command-or-tool>", "tool_schema", "tool-schema")
		return
	}
	target := strings.Join(args, " ")
	if _, _, ok := cliHelpForTarget(target); !ok {
		exitCLIError(opts, 2, "unknown_help_target", fmt.Sprintf("unknown command or tool %q", target), "tool_schema", "tool-schema")
	}
	writeCLIToolSchema(target, opts)
}

func runCacheCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		runCLIHelp("cache", opts)
		return
	}
	if hasHelpFlag(args[1:]) {
		runCLIHelp("cache "+args[0], opts)
		return
	}
	switch args[0] {
	case "status":
		runToolCLI("cache_status", parseKVFlags(args[1:]), opts, "cache status")
	case "refresh":
		runToolCLI("cache_refresh", parseKVFlags(args[1:]), opts, "cache refresh")
	case "rebuild":
		runToolCLI("cache_rebuild", parseKVFlags(args[1:]), opts, "cache rebuild")
	default:
		exitCLIError(opts, 2, "unknown_command", fmt.Sprintf("unknown cache command %q", args[0]), "", "cache")
	}
}

func runToolCLI(name string, flags map[string]any, opts cliOptions, command string) {
	if err := validateToolArgs(name, flags); err != nil {
		exitCLIError(opts, 1, cliErrorCode(err), err.Error(), name, command)
	}
	srv := &server{}
	var result any
	var err error
	switch name {
	case "resolve_chat":
		result, err = srv.toolResolveChat(flags)
	case "sessions":
		result, err = srv.toolSessions(flags)
	case "contacts":
		result, err = srv.toolContacts(flags)
	case "cache_status":
		result, err = srv.toolCacheStatus(flags)
	case "cache_refresh":
		result, err = srv.toolCacheRefresh(flags)
	case "cache_rebuild":
		result, err = srv.toolCacheRebuild(flags)
	case "stats":
		result, err = srv.toolStats(flags)
	case "unread":
		result, err = srv.toolUnread(flags)
	case "export_messages":
		result, err = srv.toolExportMessages(flags)
	case "search":
		result, err = srv.toolSearch(flags)
	case "messages":
		result, err = srv.toolMessages(flags)
	case "chat_timeline":
		result, err = srv.toolChatTimeline(flags)
	case "media_resources":
		result, err = srv.toolMediaResources(flags)
	case "group_members":
		result, err = srv.toolGroupMembers(flags)
	case "favorites":
		result, err = srv.toolFavorites(flags)
	case "red_packets":
		result, err = srv.toolRedPackets(flags)
	case "transfers":
		result, err = srv.toolTransfers(flags)
	case "sns_feed":
		result, err = srv.toolSnsFeed(flags)
	case "sns_search":
		result, err = srv.toolSnsSearch(flags)
	case "sns_notifications":
		result, err = srv.toolSnsNotifications(flags)
	case "sns":
		result, err = srv.toolSns(flags)
	case "sql":
		result, err = srv.toolSQL(flags)
	case "schema":
		result, err = srv.toolSchema(flags)
	case "chatroom_announcements":
		result, err = srv.toolChatroomAnnouncements(flags)
	case "forward_history":
		result, err = srv.toolForwardHistory(flags)
	default:
		err = fmt.Errorf("unknown cli tool %q", name)
	}
	if err != nil {
		exitCLIError(opts, 1, "tool_error", err.Error(), name, command)
	}
	writeCLISuccess(name, command, cliAgentDataEnvelope(name, command, flags, result), opts)
}

func cliAgentDataEnvelope(tool, command string, args map[string]any, result any) any {
	if _, ok := result.(map[string]any); ok {
		return result
	}
	listKey := cliResultListKey(tool)
	if listKey == "" {
		return result
	}
	rows, ok := cliResultRows(result)
	if !ok {
		return result
	}
	return compactMap(map[string]any{
		"query": cliResultQueryMeta(tool, command, args, rows),
		listKey: cliResultRowsForTool(tool, rows),
	})
}

func cliResultRows(result any) ([]map[string]any, bool) {
	switch rows := result.(type) {
	case []map[string]any:
		return rows, true
	case []wcdb.Row:
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any(row))
		}
		return out, true
	default:
		return nil, false
	}
}

func cliResultRowsForTool(tool string, rows []map[string]any) []map[string]any {
	if tool != "search" {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, cliSearchMessageRow(row))
	}
	return out
}

func cliSearchMessageRow(row map[string]any) map[string]any {
	createTime := int64MapValue(row, "create_time")
	out := compactMap(map[string]any{
		"id": compactMap(map[string]any{
			"local_id": row["local_id"],
			"talker":   row["talker"],
		}),
		"time":        formatUnixLocal(createTime),
		"time_iso":    cliFormatUnixISO(createTime),
		"create_time": row["create_time"],
		"sender":      firstNonEmpty(stringMapValue(row, "sender_display_name"), stringMapValue(row, "sender_wxid")),
		"sender_wxid": row["sender_wxid"],
		"chat": compactMap(map[string]any{
			"talker":       row["talker"],
			"display_name": row["talker_display_name"],
			"chat_type":    row["chat_type"],
		}),
		"kind":  row["kind_name"],
		"text":  firstNonEmpty(stringMapValue(row, "content_summary"), stringMapValue(row, "content")),
		"match": stringMapValue(row, "content"),
	})
	return out
}

func cliFormatUnixISO(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}

func cliResultListKey(tool string) string {
	switch tool {
	case "sessions", "unread":
		return "sessions"
	case "contacts":
		return "contacts"
	case "search":
		return "messages"
	case "messages":
		return "messages"
	case "media_resources":
		return "media"
	case "group_members":
		return "members"
	case "favorites":
		return "favorites"
	case "red_packets":
		return "red_packets"
	case "transfers":
		return "transfers"
	case "sns_feed", "sns_search":
		return "posts"
	case "sns_notifications":
		return "notifications"
	case "schema":
		return "tables"
	case "sql":
		return "rows"
	case "chatroom_announcements":
		return "announcements"
	case "forward_history":
		return "forwards"
	default:
		return ""
	}
}

func cliResultQueryMeta(tool, command string, args map[string]any, rows []map[string]any) map[string]any {
	limit := getInt(args, "limit", 0)
	offset := getInt(args, "offset", 0)
	meta := compactMap(map[string]any{
		"tool":        tool,
		"command":     command,
		"chat":        firstNonEmpty(getStr(args, "chat"), getStr(args, "talker"), getStr(args, "chatroom_id")),
		"keyword":     getStr(args, "keyword"),
		"type":        firstNonEmpty(getStr(args, "type"), getStr(args, "kind_name"), getStr(args, "type_filter"), getStr(args, "filter")),
		"sender":      getStr(args, "sender"),
		"after":       getStr(args, "after"),
		"before":      getStr(args, "before"),
		"limit":       limit,
		"offset":      offset,
		"returned":    len(rows),
		"has_more":    false,
		"next_offset": 0,
	})
	if limit > 0 {
		meta["has_more"] = len(rows) >= limit
		if len(rows) >= limit {
			meta["next_offset"] = offset + len(rows)
		}
	} else {
		delete(meta, "limit")
		delete(meta, "offset")
		delete(meta, "next_offset")
	}
	return meta
}

func cliToolNames() []string {
	return []string{
		"resolve_chat",
		"sessions",
		"contacts",
		"cache_status",
		"cache_refresh",
		"cache_rebuild",
		"stats",
		"unread",
		"export_messages",
		"search",
		"messages",
		"chat_timeline",
		"media_resources",
		"group_members",
		"favorites",
		"red_packets",
		"transfers",
		"sns",
		"sns_feed",
		"sns_search",
		"sns_notifications",
		"sql",
		"schema",
		"chatroom_announcements",
		"forward_history",
	}
}

func writeJSONCLI(v any, opts cliOptions) {
	enc := json.NewEncoder(os.Stdout)
	if opts.Pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
}

func writeCLISuccess(tool, command string, data any, opts cliOptions) {
	writeJSONCLI(cliSuccessEnvelope{
		OK:      true,
		Tool:    tool,
		Command: command,
		Data:    data,
	}, opts)
}

func exitCLIError(opts cliOptions, code int, errCode, message, tool, command string) {
	writeJSONCLI(cliErrorEnvelope{
		OK: false,
		Error: cliError{
			Code:    errCode,
			Message: message,
			Tool:    tool,
			Command: command,
		},
	}, opts)
	os.Exit(code)
}

func cliErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "missing required argument"):
		return "missing_required_argument"
	case strings.HasPrefix(msg, "unknown argument"):
		return "unknown_argument"
	case strings.HasPrefix(msg, "invalid argument"):
		return "invalid_argument"
	default:
		return "invalid_argument"
	}
}

func parseGlobalCLIOptions(args []string) (cliOptions, []string, error) {
	opts := cliOptions{}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasValue, ok := splitGlobalFlag(a)
		if !ok {
			out = append(out, a)
			continue
		}
		if !hasValue && i+1 < len(args) && isBoolLiteral(args[i+1]) {
			val = args[i+1]
			hasValue = true
			i++
		}
		switch key {
		case "json":
			if hasValue {
				if _, err := parseBoolValue(key, val); err != nil {
					return opts, nil, err
				}
			}
		case "pretty":
			if !hasValue {
				opts.Pretty = true
				continue
			}
			b, err := parseBoolValue(key, val)
			if err != nil {
				return opts, nil, err
			}
			opts.Pretty = b
		case "compact":
			if !hasValue {
				opts.Pretty = false
				continue
			}
			b, err := parseBoolValue(key, val)
			if err != nil {
				return opts, nil, err
			}
			opts.Pretty = !b
		default:
			out = append(out, a)
		}
	}
	return opts, out, nil
}

func splitGlobalFlag(arg string) (key, val string, hasValue bool, ok bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", "", false, false
	}
	raw := strings.TrimPrefix(arg, "--")
	key, val, hasValue = strings.Cut(raw, "=")
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "json", "pretty", "compact":
		return key, val, hasValue, true
	default:
		return "", "", false, false
	}
}

func parseBoolValue(key, val string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value for --%s: %q", strings.ReplaceAll(key, "_", "-"), val)
	}
}

func parseKVFlags(args []string) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		a = strings.TrimPrefix(a, "--")
		key, val, hasEq := strings.Cut(a, "=")
		key = strings.ReplaceAll(key, "-", "_")
		if !hasEq {
			if isBoolCLIFlag(key) {
				if i+1 < len(args) && isBoolLiteral(args[i+1]) {
					val = args[i+1]
					i++
				} else {
					out[key] = true
					continue
				}
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				i++
			} else {
				out[key] = true
				continue
			}
		}
		if strings.HasSuffix(key, "_str") {
			out[key] = val
			continue
		}
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			out[key] = n
			continue
		}
		if val == "true" {
			out[key] = true
			continue
		}
		if val == "false" {
			out[key] = false
			continue
		}
		out[key] = val
	}
	return out
}

func isBoolCLIFlag(key string) bool {
	switch key {
	case "background", "debug", "force", "friends_only", "groups_only", "include_debug", "include_images", "include_local_paths", "include_media_paths", "include_read", "stats":
		return true
	default:
		return false
	}
}

func isBoolLiteral(s string) bool {
	return s == "true" || s == "false"
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func helpTargetForCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if args[0] == "cache" {
		for _, a := range args[1:] {
			if a != "-h" && a != "--help" {
				return "cache " + a
			}
		}
		return "cache"
	}
	return args[0]
}

func firstPositional(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			key := strings.TrimPrefix(a, "--")
			key, _, _ = strings.Cut(key, "=")
			key = strings.ReplaceAll(key, "-", "_")
			if !strings.Contains(a, "=") && !isBoolCLIFlag(key) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

func printCLIUsage() {
	runCLIHelp("", cliOptions{Pretty: true})
}

func printCLIUsageTo(w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cliHelpDocument(""))
}

func writeCLIHelp(target string, opts cliOptions) {
	writeCLISuccess("help", "help", cliHelpDocument(target), opts)
}

func writeCLIToolSchema(target string, opts cliOptions) {
	writeCLISuccess("tool_schema", "tool-schema", cliHelpDocument(target), opts)
}

func runCLIHelp(target string, opts cliOptions) {
	if strings.TrimSpace(target) != "" {
		if _, _, ok := cliHelpForTarget(target); !ok {
			exitCLIError(opts, 2, "unknown_help_target", fmt.Sprintf("unknown command or tool %q", target), "", target)
		}
	}
	writeCLIHelp(target, opts)
}

func cliHelpDocument(target string) any {
	target = strings.TrimSpace(target)
	if target != "" {
		spec, tool, ok := cliHelpForTarget(target)
		if !ok {
			return cliErrorEnvelope{OK: false, Error: cliError{Code: "unknown_help_target", Message: fmt.Sprintf("unknown command or tool %q", target), Command: target}}
		}
		doc := map[string]any{
			"name":         appName,
			"version":      appVersion,
			"command":      spec,
			"global_flags": cliGlobalFlags(),
		}
		if tool.Name != "" {
			tool.Annotations = toolAnnotations(tool.Name)
			doc["tool"] = tool
			doc["agent"] = agentHelpForTool(spec, tool)
		}
		return doc
	}
	return map[string]any{
		"name":    appName,
		"version": appVersion,
		"output_contract": map[string]any{
			"stdout":  "json",
			"success": "object with ok=true, tool, command, data",
			"error":   "object with ok=false and error.code/message",
			"default": "compact",
		},
		"global_flags": cliGlobalFlags(),
		"commands":     cliCommandSpecs,
	}
}

func cliGlobalFlags() []map[string]string {
	return []map[string]string{
		{"name": "--json", "description": "Accepted for compatibility; stdout is already JSON."},
		{"name": "--compact", "description": "Emit compact JSON. This is the default."},
		{"name": "--pretty", "description": "Emit indented JSON for inspection."},
		{"name": "--help", "description": "Return machine-readable help for the command without executing it."},
	}
}

func agentHelpForTool(spec cliCommandSpec, tool toolDef) map[string]any {
	out := map[string]any{
		"output": map[string]any{
			"success_envelope": map[string]string{
				"ok":      "true",
				"tool":    tool.Name,
				"command": spec.Command,
				"data":    "tool result payload",
			},
			"error_envelope": "ok=false with error.code, error.message, error.tool, error.command",
		},
	}
	if len(spec.Examples) > 0 {
		out["examples"] = spec.Examples
	}
	props := toolInputProperties(tool)
	var strategy []string
	if hasAnyProp(props, "chat", "talker", "chatroom_id") {
		strategy = append(strategy, "If a human chat name may be ambiguous, run resolve-chat first and pass the returned username as talker/chatroom_id.")
	}
	if hasAnyProp(props, "limit", "offset") {
		strategy = append(strategy, "For complete reads, loop while data.query.has_more is true when available; otherwise increment offset by the returned item count until fewer than limit rows return.")
	}
	if hasAnyProp(props, "after", "before") {
		strategy = append(strategy, "Time filters accept unix seconds, YYYY-MM-DD, YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD HH:MM:SS in local time.")
	}
	if hasAnyProp(props, "include_debug", "debug") {
		strategy = append(strategy, "Keep debug/include_debug false for normal use; retry with include_debug=true only to diagnose missing media or parser warnings.")
	}
	switch tool.Name {
	case "media_resources":
		strategy = append(strategy, "Use local_id/server_id from timeline/search rows to fetch direct image/video/file paths.")
		strategy = append(strategy, "Forwarded image items are resolved when their source server_id can be matched in message_resource.db and the local media file is cached or decodable.")
	case "export_messages":
		strategy = append(strategy, "Use export for large single-chat outputs instead of keeping all rows in model context.")
		strategy = append(strategy, "Default view=agent writes the same display-ready message shape as timeline; use view=raw only for low-level debugging.")
	case "chat_timeline":
		strategy = append(strategy, "Use timeline as the default chat-reading entrypoint for summarization and recent context.")
		strategy = append(strategy, "Default image refs expose one best readable local path: original/high-resolution when available, thumbnail only as fallback.")
		strategy = append(strategy, "For forwarded image items, success means forward_chat.items[].images[].path exists and forward_image_not_resolved is absent.")
	}
	if len(strategy) > 0 {
		out["strategy"] = strategy
	}
	recovery := []map[string]string{{
		"error":  "missing_required_argument / invalid_argument / unknown_argument",
		"action": "Call tool-schema for this command and retry with the documented properties.",
	}}
	if hasAnyProp(props, "chat", "talker", "chatroom_id") {
		recovery = append(recovery, map[string]string{"error": "chat not found or ambiguous", "action": "Run resolve-chat with type_filter, then retry with the returned username."})
	}
	if hasAnyProp(props, "include_debug", "debug") {
		recovery = append(recovery, map[string]string{"error": "missing media paths or parser warnings", "action": "Retry the same command with include_debug=true and inspect warnings."})
	}
	out["recovery"] = recovery
	return out
}

func toolInputProperties(tool toolDef) map[string]any {
	schema, _ := tool.InputSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	return props
}

func hasAnyProp(props map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := props[k]; ok {
			return true
		}
	}
	return false
}

func cliHelpForTarget(target string) (cliCommandSpec, toolDef, bool) {
	target = normalizeCLIName(target)
	for _, spec := range cliCommandSpecs {
		if normalizeCLIName(spec.Command) == target {
			return spec, toolDefByName(spec.Tool), true
		}
		for _, alias := range spec.Aliases {
			if normalizeCLIName(alias) == target {
				return spec, toolDefByName(spec.Tool), true
			}
		}
	}
	if td := toolDefByName(target); td.Name != "" {
		return cliCommandSpec{Command: target, Tool: td.Name, Usage: appName + " call " + td.Name + " [--key value ...]"}, td, true
	}
	return cliCommandSpec{}, toolDef{}, false
}

func toolDefByName(name string) toolDef {
	name = normalizeCLIName(name)
	for _, td := range toolDefs {
		if normalizeCLIName(td.Name) == name {
			return td
		}
	}
	return toolDef{}
}

func normalizeCLIName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
