// Package wxparse extracts structured info from wechat XML payloads stored
// in transfer / red-packet / favorite tables. Standalone parsers — no dep on
// the message_content enrich pipeline.
package wxparse

import (
	"encoding/xml"
	"strings"
)

// StripMsgPrefix trims the "wxid_xxx:\n" sender prefix WeChat prepends to
// group message content so xml.Unmarshal sees a clean XML document.
func StripMsgPrefix(raw string) string {
	if idx := strings.Index(raw, "<"); idx > 0 {
		return raw[idx:]
	}
	return raw
}

// xmlTransferMsg parses wechat transfer (subtype=2000) messages. Amount is in
// wcpayinfo.feedesc ("￥5.00"); appmsg.des is human-readable summary
// ("收到转账5.00元"); pay_memo is sender's note attached to the transfer.
type xmlTransferMsg struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		Des       string `xml:"des"`
		WcPayInfo struct {
			FeeDesc    string `xml:"feedesc"`
			PaySubType int    `xml:"paysubtype"`
			PayMemo    string `xml:"pay_memo"`
		} `xml:"wcpayinfo"`
	} `xml:"appmsg"`
}

// TransferInfo extracts (amount, description, memo) from a transfer message
// XML. Returns a non-nil error on parse failure so callers can distinguish
// "no transfer payload in this message" (all fields empty, err nil) from
// "parser drifted on this WeChat schema" (err set).
func TransferInfo(content string) (amount, des, memo string, err error) {
	var t xmlTransferMsg
	if err = xml.Unmarshal([]byte(StripMsgPrefix(content)), &t); err != nil {
		return
	}
	return t.AppMsg.WcPayInfo.FeeDesc, t.AppMsg.Des, t.AppMsg.WcPayInfo.PayMemo, nil
}

// xmlRedPacketMsg parses wechat red-packet (subtype=2001) messages.
// sendertitle is sender-side wishing text; nativeurl carries the deep link;
// scenetext distinguishes 1v1 / group / luck-draw scenarios.
type xmlRedPacketMsg struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		WcPayInfo struct {
			SenderTitle   string `xml:"sendertitle"`
			ReceiverTitle string `xml:"receivertitle"`
			SceneText     string `xml:"scenetext"`
			TemplateID    string `xml:"templateid"`
			InnerType     int    `xml:"innertype"`
			NativeURL     string `xml:"nativeurl"`
		} `xml:"wcpayinfo"`
	} `xml:"appmsg"`
}

// RedPacketInfo extracts (wishing, sceneText) from a red-packet XML.
// Non-nil err = parser drift; empty fields with nil err = legitimately absent.
func RedPacketInfo(content string) (wishing, sceneText string, err error) {
	var r xmlRedPacketMsg
	if err = xml.Unmarshal([]byte(StripMsgPrefix(content)), &r); err != nil {
		return
	}
	return r.AppMsg.WcPayInfo.SenderTitle, r.AppMsg.WcPayInfo.SceneText, nil
}

// xmlFavItem covers the most common favorite XML shapes (link / note / data).
// title/desc/url fields fall through whichever sub-item element is populated.
type xmlFavItem struct {
	XMLName    xml.Name `xml:"favitem"`
	WebURLItem struct {
		PageTitle string `xml:"pagetitle"`
		PageDesc  string `xml:"pagedesc"`
		CleanURL  string `xml:"clean_url"`
	} `xml:"weburlitem"`
	NoteItem struct {
		Title       string `xml:"title"`
		Description string `xml:"description"`
	} `xml:"noteitem"`
	DataItem struct {
		DataTitle string `xml:"datatitle"`
		DataDesc  string `xml:"datadesc"`
	} `xml:"dataitem"`
}

// FavoriteInfo extracts (title, description, url) from a favorite XML.
// Picks whichever inner item shape is populated. Non-nil err = parser drift.
func FavoriteInfo(content string) (title, desc, url string, err error) {
	var f xmlFavItem
	if err = xml.Unmarshal([]byte(content), &f); err != nil {
		return
	}
	switch {
	case f.WebURLItem.PageTitle != "":
		return f.WebURLItem.PageTitle, f.WebURLItem.PageDesc, f.WebURLItem.CleanURL, nil
	case f.NoteItem.Title != "":
		return f.NoteItem.Title, f.NoteItem.Description, "", nil
	case f.DataItem.DataTitle != "":
		return f.DataItem.DataTitle, f.DataItem.DataDesc, "", nil
	}
	return
}

// xmlForwardMsg parses forward_chat (base_kind=49, subtype=19) messages.
// recorditem wraps its recordinfo payload in CDATA; ,chardata reads it as
// plain text (Go's xml parser auto-unwraps CDATA into character data), which
// we then unmarshal as a separate XML document.
// datatype attr: 1=text, 2=image, 3=voice, 4=video, 5=link, 8=file, 17=nested forward_chat.
type xmlForwardMsg struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		Title      string `xml:"title"`
		Des        string `xml:"des"`
		RecordItem string `xml:"recorditem"`
	} `xml:"appmsg"`
}

// xmlForwardRecordInfo is the inner XML inside <recorditem> CDATA.
type xmlForwardRecordInfo struct {
	XMLName   xml.Name         `xml:"recordinfo"`
	Title     string           `xml:"title"`
	Desc      string           `xml:"desc"`
	DataItems []xmlForwardItem `xml:"datalist>dataitem"`
}

// xmlForwardItem is one dataitem within a recordinfo datalist. RawInner
// captures the entire element content so nested forward payloads (datatype=17)
// can be re-scanned for an inner <recordinfo> without committing to a specific
// wrapper tag (wechat versions differ: sometimes <recordxml>, sometimes inline
// CDATA, sometimes raw nested <recordinfo>).
type xmlForwardItem struct {
	DataType         int    `xml:"datatype,attr"`
	DataID           string `xml:"dataid,attr"`
	SourceName       string `xml:"sourcename"`
	SourceTime       string `xml:"sourcetime"`
	DataTitle        string `xml:"datatitle"`
	DataDesc         string `xml:"datadesc"`
	DataFmt          string `xml:"datafmt"`
	FullMD5          string `xml:"fullmd5"`
	DataSize         int64  `xml:"datasize"`
	SrcMsgLocalID    int64  `xml:"srcMsgLocalid"`
	SrcMsgCreateTime int64  `xml:"srcMsgCreateTime"`
	RawInner         string `xml:",innerxml"`
}

// ForwardItem is a JSON-serializable view of one forwarded sub-message.
// Only populated fields are emitted (omitempty) so text items don't carry
// file-specific keys. NestedItems is set for datatype=17 (合并转发 nested).
type ForwardItem struct {
	DataType         int           `json:"datatype"`
	SourceName       string        `json:"sourcename,omitempty"`
	SourceTime       string        `json:"sourcetime,omitempty"`
	DataTitle        string        `json:"datatitle,omitempty"`
	DataDesc         string        `json:"datadesc,omitempty"`
	DataFmt          string        `json:"datafmt,omitempty"`
	FullMD5          string        `json:"fullmd5,omitempty"`
	DataSize         int64         `json:"datasize,omitempty"`
	SrcMsgLocalID    int64         `json:"src_msg_localid,omitempty"`
	SrcMsgCreateTime int64         `json:"src_msg_create_time,omitempty"`
	NestedItems      []ForwardItem `json:"nested_items,omitempty"`
	// ParseError surfaces nested-forward (datatype=17) parse failure on this
	// item without losing the rest of the outer forward. Outer-XML parser
	// drift propagates as ForwardItems' returned error instead.
	ParseError string `json:"parse_error,omitempty"`
}

// ForwardItems extracts structured sub-messages from a forward_chat (subtype=19)
// XML. depth bounds nested-forward recursion (pass ≥1 to include nested; 0
// keeps datatype=17 items but without NestedItems). Non-nil err = parser drift.
// Empty slice with nil err = legitimately not a forward / empty datalist.
// Binary/media payloads (cdndataurl / aeskey) are intentionally dropped —
// encrypted CDN pointers unusable without the WeChat client.
func ForwardItems(content string, depth int) ([]ForwardItem, error) {
	var m xmlForwardMsg
	if err := xml.Unmarshal([]byte(StripMsgPrefix(content)), &m); err != nil {
		return nil, err
	}
	inner := strings.TrimSpace(m.AppMsg.RecordItem)
	if inner == "" {
		return nil, nil
	}
	return parseRecordInfo(inner, depth)
}

// parseRecordInfo unmarshals a <recordinfo> XML document into a flat slice of
// ForwardItem, recursing into nested forwards (datatype=17) up to depth.
// Non-nil err = this <recordinfo> XML failed to parse (parser drift on the
// document itself). Nested-forward parse failures don't fail outer; they're
// recorded as ParseError on the offending item so the rest of the outer list
// survives.
func parseRecordInfo(recordXML string, depth int) ([]ForwardItem, error) {
	var ri xmlForwardRecordInfo
	if err := xml.Unmarshal([]byte(recordXML), &ri); err != nil {
		return nil, err
	}
	if len(ri.DataItems) == 0 {
		return nil, nil
	}
	out := make([]ForwardItem, 0, len(ri.DataItems))
	for _, it := range ri.DataItems {
		fi := ForwardItem{
			DataType:         it.DataType,
			SourceName:       it.SourceName,
			SourceTime:       it.SourceTime,
			DataTitle:        it.DataTitle,
			DataDesc:         it.DataDesc,
			DataFmt:          it.DataFmt,
			FullMD5:          it.FullMD5,
			DataSize:         it.DataSize,
			SrcMsgLocalID:    it.SrcMsgLocalID,
			SrcMsgCreateTime: it.SrcMsgCreateTime,
		}
		if it.DataType == 17 && depth > 0 {
			if nested := extractNestedRecordInfo(it.RawInner); nested != "" {
				items, err := parseRecordInfo(nested, depth-1)
				if err != nil {
					fi.ParseError = err.Error()
				} else {
					fi.NestedItems = items
				}
			}
		}
		out = append(out, fi)
	}
	return out, nil
}

// extractNestedRecordInfo scans a dataitem's inner XML for the first
// <recordinfo>...</recordinfo> block, regardless of wrapping tag or CDATA.
// Returns the extracted block or empty string if none found.
func extractNestedRecordInfo(inner string) string {
	start := strings.Index(inner, "<recordinfo>")
	if start < 0 {
		return ""
	}
	end := strings.Index(inner[start:], "</recordinfo>")
	if end < 0 {
		return ""
	}
	return inner[start : start+end+len("</recordinfo>")]
}
