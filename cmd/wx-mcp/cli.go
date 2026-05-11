package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func maybeRunCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help":
		printCLIUsage()
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
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("stats", flags)
		return true
	case "unread":
		runToolCLI("unread", parseKVFlags(args[1:]))
		return true
	case "new-messages", "new_messages":
		runToolCLI("new_messages", parseKVFlags(args[1:]))
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
	default:
		return false
	}
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
	case "new_messages":
		result, err = srv.toolNewMessages(flags)
	case "export_messages":
		result, err = srv.toolExportMessages(flags)
	case "search":
		result, err = srv.toolSearch(flags)
	case "messages":
		result, err = srv.toolMessages(flags)
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
	default:
		err = fmt.Errorf("unknown cli tool %q", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(result)
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
			if key == "force" {
				out[key] = true
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
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

func firstPositional(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

func printCLIUsage() {
	fmt.Fprint(os.Stderr, `wx-mcp - MCP server plus cache companion CLI

MCP mode:
  wx-mcp

Cache CLI:
  wx-mcp cache status
  wx-mcp cache refresh [--force]
  wx-mcp cache rebuild

Query/export CLI:
  wx-mcp sessions [--limit 20] [--type-filter private,group]
  wx-mcp resolve-chat "张三"
  wx-mcp history "张三" [--limit 50] [--after 2026-05-11]
  wx-mcp media "张三" [--local-id 123] [--type image|video|file]
  wx-mcp search "关键词" [--in "某群"] [--after 2026-01-01] [--type text]
  wx-mcp contacts [--keyword 李]
  wx-mcp members "某群"
  wx-mcp unread [--limit 50]
  wx-mcp new-messages [--after 2026-05-11] [--talker wxid_x] [--limit 100]
  wx-mcp stats ["张三"] [--limit 10]
  wx-mcp favorites [--limit 20]
  wx-mcp red-packets [--limit 20]
  wx-mcp transfers [--limit 20]
  wx-mcp sns-feed [--limit 20]
  wx-mcp sns-search "关键词"
  wx-mcp sns-notifications [--include-read]
  wx-mcp export "张三" --path /tmp/messages.jsonl [--format jsonl|markdown|html]
`)
}
