package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r266-tech/wechat-local-mcp/internal/wcdb"
)

type chatCandidate struct {
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	ChatType      string `json:"chat_type"`
	LastTimestamp int64  `json:"last_timestamp,omitempty"`
	SortTimestamp int64  `json:"sort_timestamp,omitempty"`
	Source        string `json:"source"`
	Match         string `json:"match"`
}

func agentChatType(username, contactType string, isVerified bool) string {
	switch {
	case username == "brandsessionholder" || username == "brandservicesessionholder" || username == "@placeholder_foldgroup":
		return "folded"
	case strings.HasSuffix(username, "@chatroom"):
		return "group"
	case strings.HasSuffix(username, "@weclaw"):
		return "bot"
	case isVerified || contactType == "official_account" || strings.HasPrefix(username, "gh_") || strings.HasPrefix(username, "biz_") || strings.HasPrefix(username, "@"):
		return "official_account"
	case contactType == "corp_im":
		return "corp_im"
	case contactType == "stranger":
		return "stranger"
	default:
		return "private"
	}
}

func normalizeChatType(t string) string {
	switch strings.TrimSpace(strings.ToLower(t)) {
	case "", "all":
		return "all"
	case "friend", "friends":
		return "private"
	case "official":
		return "official_account"
	case "clawbot":
		return "bot"
	default:
		return strings.TrimSpace(strings.ToLower(t))
	}
}

func chatTypeAllowed(chatType, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" || normalizeChatType(filter) == "all" {
		return true
	}
	for _, part := range strings.Split(filter, ",") {
		if normalizeChatType(part) == "all" || normalizeChatType(part) == chatType {
			return true
		}
	}
	return false
}

func looksLikeRawChatID(s string) bool {
	return strings.Contains(s, "@") ||
		strings.HasPrefix(s, "wxid_") ||
		strings.HasPrefix(s, "gh_") ||
		strings.HasPrefix(s, "biz_") ||
		s == "brandsessionholder" ||
		s == "brandservicesessionholder"
}

func compactMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

func candidateMatch(query string, r wcdb.Row) string {
	q := compactMatch(query)
	fields := []string{"username", "display_name", "nick_name", "remark", "alias"}
	for _, f := range fields {
		v := rowString(r, f)
		if v == "" {
			continue
		}
		if compactMatch(v) == q {
			return f + "_exact"
		}
	}
	for _, f := range fields {
		v := rowString(r, f)
		if v == "" {
			continue
		}
		if strings.Contains(compactMatch(v), q) {
			return f + "_fuzzy"
		}
	}
	return "matched"
}

func resolveChatCandidates(db *wcdb.DB, query, typeFilter string, limit int) ([]chatCandidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query/chat is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	like := "%" + query + "%"
	likeNoSpace := "%" + strings.ReplaceAll(query, " ", "") + "%"
	byUser := map[string]chatCandidate{}
	addRows := func(rows []wcdb.Row, source string) {
		for _, r := range rows {
			u := rowString(r, "username")
			if u == "" {
				continue
			}
			ct := agentChatType(u, rowString(r, "contact_type"), rowInt64(r, "is_verified") != 0)
			if !chatTypeAllowed(ct, typeFilter) {
				continue
			}
			c := chatCandidate{
				Username:      u,
				DisplayName:   rowString(r, "display_name"),
				ChatType:      ct,
				LastTimestamp: rowInt64(r, "last_timestamp"),
				SortTimestamp: rowInt64(r, "sort_timestamp"),
				Source:        source,
				Match:         candidateMatch(query, r),
			}
			if c.DisplayName == "" {
				c.DisplayName = u
			}
			if old, ok := byUser[u]; !ok || rankCandidate(c) > rankCandidate(old) ||
				(rankCandidate(c) == rankCandidate(old) && c.SortTimestamp > old.SortTimestamp) {
				byUser[u] = c
			}
		}
	}
	sessionRows, err := db.Query(`SELECT s.username, s.display_name, s.last_timestamp, s.sort_timestamp,
		c.nick_name, c.remark, c.alias, c.type AS contact_type, c.is_verified
		FROM sessions_unified s LEFT JOIN contacts_unified c ON c.username = s.username
		WHERE s.username = ? OR s.display_name = ?
			OR s.username LIKE ? COLLATE NOCASE OR s.display_name LIKE ? COLLATE NOCASE
			OR REPLACE(s.display_name, ' ', '') LIKE ? COLLATE NOCASE
		ORDER BY s.sort_timestamp DESC LIMIT 100`, query, query, like, like, likeNoSpace)
	if err != nil {
		return nil, err
	}
	addRows(sessionRows, "session")
	contactRows, err := db.Query(`SELECT c.username, c.display_name, c.nick_name, c.remark, c.alias,
		c.type AS contact_type, c.is_verified,
		COALESCE(s.last_timestamp, 0) AS last_timestamp,
		COALESCE(s.sort_timestamp, 0) AS sort_timestamp
		FROM contacts_unified c LEFT JOIN sessions_unified s ON s.username = c.username
		WHERE c.username = ? OR c.display_name = ? OR c.nick_name = ? OR c.remark = ? OR c.alias = ?
			OR c.username LIKE ? COLLATE NOCASE
			OR c.display_name LIKE ? COLLATE NOCASE OR REPLACE(c.display_name, ' ', '') LIKE ? COLLATE NOCASE
			OR c.nick_name LIKE ? COLLATE NOCASE OR REPLACE(c.nick_name, ' ', '') LIKE ? COLLATE NOCASE
			OR c.remark LIKE ? COLLATE NOCASE OR REPLACE(c.remark, ' ', '') LIKE ? COLLATE NOCASE
			OR c.alias LIKE ? COLLATE NOCASE
		ORDER BY sort_timestamp DESC LIMIT 100`,
		query, query, query, query, query,
		like, like, likeNoSpace, like, likeNoSpace, like, likeNoSpace, like)
	if err != nil {
		return nil, err
	}
	addRows(contactRows, "contact")

	out := make([]chatCandidate, 0, len(byUser))
	for _, c := range byUser {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rankCandidate(out[i]), rankCandidate(out[j])
		if ri != rj {
			return ri > rj
		}
		if out[i].SortTimestamp != out[j].SortTimestamp {
			return out[i].SortTimestamp > out[j].SortTimestamp
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func rankCandidate(c chatCandidate) int {
	score := 0
	if c.Source == "session" {
		score += 10
	}
	if strings.Contains(c.Match, "_exact") {
		score += 100
	}
	if strings.Contains(c.Match, "alias") {
		score += 5
	}
	return score
}

func resolveTalkerForCache(db *wcdb.DB, a map[string]any, required bool) (string, error) {
	raw := getStr(a, "talker")
	if raw == "" {
		raw = getStr(a, "chat")
	}
	if raw == "" {
		raw = getStr(a, "chatroom_id")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", fmt.Errorf("talker or chat is required")
		}
		return "", nil
	}
	if looksLikeRawChatID(raw) {
		return raw, nil
	}
	cands, err := resolveChatCandidates(db, raw, getStr(a, "type_filter"), 5)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("chat %q not found; call resolve_chat first to inspect candidates", raw)
	}
	if strings.Contains(cands[0].Match, "_exact") {
		return cands[0].Username, nil
	}
	return cands[0].Username, nil
}

func decorateSessionRows(rows []wcdb.Row, typeFilter string) []wcdb.Row {
	out := rows[:0]
	for _, r := range rows {
		u := rowString(r, "username")
		ct := agentChatType(u, rowString(r, "contact_type"), rowInt64(r, "is_verified") != 0)
		r["chat_type"] = ct
		delete(r, "contact_type")
		delete(r, "is_verified")
		if chatTypeAllowed(ct, typeFilter) {
			out = append(out, r)
		}
	}
	return out
}

func decorateMessageRows(rows []wcdb.Row) {
	for _, r := range rows {
		ct := agentChatType(rowString(r, "talker"), rowString(r, "talker_contact_type"), rowInt64(r, "talker_is_verified") != 0)
		r["chat_type"] = ct
		delete(r, "talker_contact_type")
		delete(r, "talker_is_verified")
	}
}

func (s *server) toolResolveChat(a map[string]any) (any, error) {
	query := getStr(a, "query")
	if query == "" {
		query = getStr(a, "chat")
	}
	if query == "" {
		query = getStr(a, "keyword")
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	limit := getInt(a, "limit", 10)
	cands, err := resolveChatCandidates(db, query, getStr(a, "type_filter"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"query":      query,
		"candidates": cands,
	}, nil
}
