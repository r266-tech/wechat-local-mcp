package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func maybeRunCLI(args []string) bool {
	if len(args) == 0 {
		printCLIUsageTo(os.Stdout)
		return true
	}
	switch args[0] {
	case "-h", "--help", "help":
		printCLIUsageTo(os.Stdout)
		return true
	case "serve-mcp", "mcp-server", "mcp", "serve":
		runMCPServer()
		return true
	case "tools", "list-tools", "list_tools":
		runToolsCLI()
		return true
	case "call":
		runGenericToolCLI(args[1:])
		return true
	case "call-json", "call_json":
		runToolJSONCLI(args[1:])
		return true
	case "cache":
		runCacheCLI(args[1:])
		return true
	case "resolve-chat", "resolve_chat":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" {
			flags["query"] = q
		}
		runToolCLI("resolve_chat", flags)
		return true
	case "sessions":
		runToolCLI("sessions", parseKVFlags(args[1:]))
		return true
	case "contacts":
		runToolCLI("contacts", parseKVFlags(args[1:]))
		return true
	case "history", "messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("messages", flags)
		return true
	case "timeline", "chat-timeline", "chat_timeline", "conversation-view", "conversation_view":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("chat_timeline", flags)
		return true
	case "media", "media-resources", "media_resources", "attachments":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("media_resources", flags)
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
		runToolCLI("search", flags)
		return true
	case "members", "group-members", "group_members":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("group_members", flags)
		return true
	case "stats":
		runToolCLI("stats", parseKVFlags(args[1:]))
		return true
	case "unread":
		runToolCLI("unread", parseKVFlags(args[1:]))
		return true
	case "export", "export-messages", "export_messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("export_messages", flags)
		return true
	case "favorites":
		runToolCLI("favorites", parseKVFlags(args[1:]))
		return true
	case "red-packets", "red_packets":
		runToolCLI("red_packets", parseKVFlags(args[1:]))
		return true
	case "transfers":
		runToolCLI("transfers", parseKVFlags(args[1:]))
		return true
	case "sns", "sns-feed", "sns_feed":
		runToolCLI("sns_feed", parseKVFlags(args[1:]))
		return true
	case "sns-search", "sns_search":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		runToolCLI("sns_search", flags)
		return true
	case "sns-notifications", "sns_notifications":
		runToolCLI("sns_notifications", parseKVFlags(args[1:]))
		return true
	case "sql":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" && flags["query"] == nil {
			flags["query"] = q
		}
		runToolCLI("sql", flags)
		return true
	case "schema":
		runToolCLI("schema", parseKVFlags(args[1:]))
		return true
	case "announcements", "chatroom-announcements", "chatroom_announcements":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil {
			flags["chatroom_id"] = chat
		}
		runToolCLI("chatroom_announcements", flags)
		return true
	case "forward-history", "forward_history":
		runToolCLI("forward_history", parseKVFlags(args[1:]))
		return true
	default:
		return false
	}
}

func runToolsCLI() {
	writeJSONCLI(listedToolDefs())
}

func runGenericToolCLI(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s call <tool> [--key value ...]\n", appName)
		os.Exit(2)
	}
	runToolCLI(args[0], parseKVFlags(args[1:]))
}

func runToolJSONCLI(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s call-json <tool> '<json args>'\n", appName)
		os.Exit(2)
	}
	raw := ""
	if len(args) > 1 {
		raw = args[1]
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		raw = string(data)
	}
	flags := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &flags); err != nil {
			fmt.Fprintf(os.Stderr, "invalid json args: %v\n", err)
			os.Exit(1)
		}
	}
	runToolCLI(args[0], flags)
}

func runCacheCLI(args []string) {
	if len(args) == 0 {
		printCLIUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "status":
		runToolCLI("cache_status", parseKVFlags(args[1:]))
	case "refresh":
		runToolCLI("cache_refresh", parseKVFlags(args[1:]))
	case "rebuild":
		runToolCLI("cache_rebuild", parseKVFlags(args[1:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown cache command %q\n", args[0])
		os.Exit(2)
	}
}

func runToolCLI(name string, flags map[string]any) {
	if err := validateToolArgs(name, flags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	writeJSONCLI(result)
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

func writeJSONCLI(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
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
	printCLIUsageTo(os.Stderr)
}

func printCLIUsageTo(w io.Writer) {
	fmt.Fprintf(w, `%s - local WeChat data CLI

General:
  %s --help
  %s tools
  %s call <tool> [--key value ...]
  %s call-json <tool> '{"key":"value"}'
  %s serve-mcp                         # optional legacy MCP stdio adapter

Cache CLI:
  %s cache status
  %s cache refresh [--force] [--background]
  %s cache rebuild

Query/export CLI:
  %s sessions [--limit 20] [--type-filter private,group]
  %s resolve-chat "张三"
  %s history "张三" [--limit 50] [--after 2026-05-11] [--view agent] [--include-media-paths=false]
  %s timeline "张三" [--limit 10] [--display-order asc]
  %s media "张三" [--local-id 123] [--type image|video|file]
  %s search "关键词" [--in "某群"] [--after 2026-01-01] [--type text]
  %s contacts [--keyword 李]
  %s members "某群"
  %s unread [--limit 50]
  %s stats
  %s favorites [--limit 20]
  %s red-packets [--limit 20]
  %s transfers [--limit 20]
  %s sns-feed [--limit 20]
  %s sns-search "关键词"
  %s sns-notifications [--include-read]
  %s schema [--subdir session] [--file session.db]
  %s sql "select count(*) from Session"
  %s announcements [--limit 20]
  %s forward-history [--limit 20]
  %s export "张三" --path /tmp/messages.jsonl [--format jsonl|markdown|html]
`, appName,
		appName, appName, appName, appName, appName,
		appName, appName, appName,
		appName, appName, appName, appName, appName, appName, appName, appName,
		appName, appName, appName, appName, appName, appName, appName, appName,
		appName, appName, appName, appName, appName)
}
