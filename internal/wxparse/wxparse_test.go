package wxparse

import "testing"

func TestStripMsgPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"wxid_abc:\n<msg>x</msg>", "<msg>x</msg>"},
		{"<msg>x</msg>", "<msg>x</msg>"},
		{"hello", "hello"}, // no '<' → returned as-is
		{"", ""},
	}
	for _, c := range cases {
		got := StripMsgPrefix(c.in)
		if got != c.want {
			t.Errorf("StripMsgPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

const transferSample = `wxid_abc:
<?xml version="1.0"?>
<msg>
	<appmsg appid="" sdkver="">
		<title><![CDATA[微信转账]]></title>
		<des><![CDATA[收到转账5.00元。如需收钱，请点此升级至最新版本]]></des>
		<type>2000</type>
		<wcpayinfo>
			<paysubtype>3</paysubtype>
			<feedesc><![CDATA[￥5.00]]></feedesc>
			<pay_memo><![CDATA[周末聚餐 AA]]></pay_memo>
		</wcpayinfo>
	</appmsg>
</msg>`

func TestTransferInfo(t *testing.T) {
	amount, des, memo, err := TransferInfo(transferSample)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if amount != "￥5.00" {
		t.Errorf("amount = %q, want ￥5.00", amount)
	}
	if des != "收到转账5.00元。如需收钱，请点此升级至最新版本" {
		t.Errorf("des = %q", des)
	}
	if memo != "周末聚餐 AA" {
		t.Errorf("memo = %q, want '周末聚餐 AA'", memo)
	}
}

func TestTransferInfo_Malformed(t *testing.T) {
	amount, des, memo, err := TransferInfo("not xml at all")
	if err == nil {
		t.Error("malformed input should return non-nil err")
	}
	if amount != "" || des != "" || memo != "" {
		t.Errorf("malformed should return empty, got (%q,%q,%q)", amount, des, memo)
	}
}

const redPacketSample = `<msg>
	<appmsg appid="" sdkver="">
		<wcpayinfo>
			<sendertitle>恭喜发财，大吉大利</sendertitle>
			<scenetext>微信红包</scenetext>
			<nativeurl>wxpay://c2cbizmessagehandler/hongbao/receivehongbao?msgtype=1</nativeurl>
		</wcpayinfo>
	</appmsg>
</msg>`

func TestRedPacketInfo(t *testing.T) {
	wishing, sceneText, err := RedPacketInfo(redPacketSample)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if wishing != "恭喜发财，大吉大利" {
		t.Errorf("wishing = %q", wishing)
	}
	if sceneText != "微信红包" {
		t.Errorf("sceneText = %q", sceneText)
	}
}

const favLinkSample = `<favitem type="5">
	<source sourcetype="3" sourceid="__biz=...">
		<fromusr>wxid_abc</fromusr>
		<link>https://mp.weixin.qq.com/s/test</link>
	</source>
	<weburlitem>
		<pagetitle>Hermes Agent深度解析</pagetitle>
		<pagedesc>开源 AI Agent 框架</pagedesc>
		<clean_url>https://mp.weixin.qq.com/s/test</clean_url>
	</weburlitem>
</favitem>`

const favNoteSample = `<favitem type="1">
	<noteitem>
		<title>会议纪要</title>
		<description>讨论 OKR 进度</description>
	</noteitem>
</favitem>`

func TestFavoriteInfo(t *testing.T) {
	title, desc, url, err := FavoriteInfo(favLinkSample)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if title != "Hermes Agent深度解析" {
		t.Errorf("link title = %q", title)
	}
	if desc != "开源 AI Agent 框架" {
		t.Errorf("link desc = %q", desc)
	}
	if url != "https://mp.weixin.qq.com/s/test" {
		t.Errorf("link url = %q", url)
	}

	title, desc, url, err = FavoriteInfo(favNoteSample)
	if err != nil {
		t.Fatalf("note unexpected err: %v", err)
	}
	if title != "会议纪要" || desc != "讨论 OKR 进度" || url != "" {
		t.Errorf("note = (%q,%q,%q), want (会议纪要,讨论 OKR 进度,)", title, desc, url)
	}
}

func TestFavoriteInfo_Malformed(t *testing.T) {
	title, desc, url, err := FavoriteInfo("garbage")
	if err == nil {
		t.Error("malformed input should return non-nil err")
	}
	if title != "" || desc != "" || url != "" {
		t.Errorf("malformed should return empty, got (%q,%q,%q)", title, desc, url)
	}
}

const forwardSample = `<msg><appmsg appid="" sdkver="0"><title>群聊的聊天记录</title><des>V: [文件] wx-mcp.zip
V: 谁在用cc</des><type>19</type><recorditem><![CDATA[<recordinfo><fromscene>0</fromscene><favcreatetime>1776405641</favcreatetime><title>群聊的聊天记录</title><desc>V: [文件] wx-mcp.zip</desc><datalist count="3"><dataitem datatype="8" dataid="aaa"><datafmt>zip</datafmt><sourcename>V</sourcename><sourcetime>2026-04-17 13:59</sourcetime><datatitle>wx-mcp.zip</datatitle><fullmd5>d205fc3df103b57f137242314a05edef</fullmd5><datasize>4385180</datasize><srcMsgLocalid>5928</srcMsgLocalid><srcMsgCreateTime>1776405582</srcMsgCreateTime></dataitem><dataitem datatype="1" dataid="bbb"><sourcename>V</sourcename><sourcetime>2026-04-17 13:59</sourcetime><datadesc>谁在用cc, 可以试用下这个mcp</datadesc><srcMsgLocalid>5929</srcMsgLocalid><srcMsgCreateTime>1776405582</srcMsgCreateTime></dataitem><dataitem datatype="1" dataid="ccc"><sourcename>V</sourcename><sourcetime>2026-04-17 14:00</sourcetime><datadesc>初始化, 第一次用的时候会慢</datadesc><srcMsgLocalid>5931</srcMsgLocalid><srcMsgCreateTime>1776405624</srcMsgCreateTime></dataitem></datalist></recordinfo>]]></recorditem></appmsg></msg>`

func TestForwardItems(t *testing.T) {
	items, err := ForwardItems(forwardSample, 3)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// File item
	if items[0].DataType != 8 {
		t.Errorf("items[0].DataType = %d, want 8", items[0].DataType)
	}
	if items[0].DataTitle != "wx-mcp.zip" {
		t.Errorf("items[0].DataTitle = %q", items[0].DataTitle)
	}
	if items[0].DataFmt != "zip" {
		t.Errorf("items[0].DataFmt = %q", items[0].DataFmt)
	}
	if items[0].FullMD5 != "d205fc3df103b57f137242314a05edef" {
		t.Errorf("items[0].FullMD5 = %q", items[0].FullMD5)
	}
	if items[0].DataSize != 4385180 {
		t.Errorf("items[0].DataSize = %d", items[0].DataSize)
	}
	if items[0].SrcMsgLocalID != 5928 {
		t.Errorf("items[0].SrcMsgLocalID = %d", items[0].SrcMsgLocalID)
	}

	// Text items
	if items[1].DataType != 1 || items[1].DataDesc != "谁在用cc, 可以试用下这个mcp" {
		t.Errorf("items[1] = %+v", items[1])
	}
	if items[2].DataType != 1 || items[2].DataDesc != "初始化, 第一次用的时候会慢" {
		t.Errorf("items[2] = %+v", items[2])
	}

	// Common fields
	if items[0].SourceName != "V" {
		t.Errorf("items[0].SourceName = %q", items[0].SourceName)
	}
	if items[2].SourceTime != "2026-04-17 14:00" {
		t.Errorf("items[2].SourceTime = %q", items[2].SourceTime)
	}
}

const forwardRichSample = `<msg><appmsg><type>19</type><recorditem><![CDATA[<recordinfo><datalist count="2"><dataitem datatype="5" dataid="link1"><streamweburl>https://mp.weixin.qq.com/s/test</streamweburl><link>https://mp.weixin.qq.com/s/test</link><sourcename>V</sourcename><sourcetime>2026-05-22 16:56</sourcetime><fromnewmsgid>6241033583148630707</fromnewmsgid><weburlitem><thumburl>https://example.test/thumb.jpg</thumburl><title>一文搞懂如何在Codex中使用goals</title><appmsgshareitem><srcusername>gh_7e5d9d010744</srcusername><srcdisplayname>AI寒武纪</srcdisplayname></appmsgshareitem></weburlitem></dataitem><dataitem datatype="1" dataid="quote1"><datadesc>不错</datadesc><sourcename>白日做梦</sourcename><refermsgitem><type>48</type><svrid>1841310816813489186</svrid><displayname>V</displayname><content>&lt;msg&gt;&lt;location x="27.452047" y="114.178642" label="山顶" poiname="金顶"/&gt;&lt;/msg&gt;</content><referdesc>金顶</referdesc></refermsgitem></dataitem></datalist></recordinfo>]]></recorditem></appmsg></msg>`

func TestForwardItems_RichLinkAndQuote(t *testing.T) {
	items, err := ForwardItems(forwardRichSample, 3)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
	if items[0].Link == nil || items[0].Link.URL != "https://mp.weixin.qq.com/s/test" ||
		items[0].Link.Title != "一文搞懂如何在Codex中使用goals" ||
		items[0].Link.SourceDisplayName != "AI寒武纪" {
		t.Fatalf("link item = %+v", items[0])
	}
	if items[0].FromNewMsgID != "6241033583148630707" {
		t.Fatalf("fromnewmsgid = %q", items[0].FromNewMsgID)
	}
	if items[1].ReferMsg == nil || items[1].ReferMsg.Type != 48 ||
		items[1].ReferMsg.SvrID != "1841310816813489186" ||
		items[1].ReferMsg.ReferDesc != "金顶" ||
		items[1].ReferMsg.Content == "" {
		t.Fatalf("refermsg item = %+v", items[1].ReferMsg)
	}
}

func TestForwardItems_Malformed(t *testing.T) {
	items, err := ForwardItems("not xml", 3)
	if err == nil {
		t.Error("malformed input should return non-nil err")
	}
	if items != nil {
		t.Errorf("malformed should return nil items, got %+v", items)
	}
	// Non-forward app msg parses fine but has no recorditem → nil items, nil err.
	items, err = ForwardItems(transferSample, 3)
	if err != nil {
		t.Errorf("non-forward should not error, got %v", err)
	}
	if items != nil {
		t.Errorf("non-forward should return nil, got %+v", items)
	}
}

// Nested forward: datatype=17 with inner recordinfo (wrapped in <recordxml>).
// Inner forward contains 2 items.
const nestedForwardSample = `<msg><appmsg appid="" sdkver="0"><title>群聊的聊天记录</title><type>19</type><recorditem><![CDATA[<recordinfo><title>outer</title><datalist count="2"><dataitem datatype="1" dataid="a1"><sourcename>Alice</sourcename><datadesc>hi from outer</datadesc></dataitem><dataitem datatype="17" dataid="a2"><sourcename>Alice</sourcename><datatitle>Bob的聊天记录</datatitle><datadesc>Bob: text1\nBob: text2</datadesc><recordxml><recordinfo><title>inner</title><datalist count="2"><dataitem datatype="1" dataid="b1"><sourcename>Bob</sourcename><datadesc>text1</datadesc></dataitem><dataitem datatype="1" dataid="b2"><sourcename>Bob</sourcename><datadesc>text2</datadesc></dataitem></datalist></recordinfo></recordxml></dataitem></datalist></recordinfo>]]></recorditem></appmsg></msg>`

func TestForwardItems_Nested(t *testing.T) {
	items, err := ForwardItems(nestedForwardSample, 3)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("outer len = %d, want 2", len(items))
	}
	if items[0].DataType != 1 || items[0].DataDesc != "hi from outer" {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[1].DataType != 17 {
		t.Errorf("items[1].DataType = %d, want 17", items[1].DataType)
	}
	if len(items[1].NestedItems) != 2 {
		t.Fatalf("nested len = %d, want 2", len(items[1].NestedItems))
	}
	if items[1].NestedItems[0].DataDesc != "text1" {
		t.Errorf("nested[0].DataDesc = %q", items[1].NestedItems[0].DataDesc)
	}
	if items[1].NestedItems[1].DataDesc != "text2" {
		t.Errorf("nested[1].DataDesc = %q", items[1].NestedItems[1].DataDesc)
	}

	// depth=0 keeps datatype=17 entry but drops NestedItems
	shallow, err := ForwardItems(nestedForwardSample, 0)
	if err != nil {
		t.Fatalf("shallow unexpected err: %v", err)
	}
	if len(shallow) != 2 || shallow[1].NestedItems != nil {
		t.Errorf("depth=0 should keep entry without nested; got %+v", shallow[1])
	}
}

// Outer XML valid, inner <recordinfo> has type-mismatched field (datasize
// expects int64, "garbage" doesn't parse). Outer items should survive; the
// datatype=17 entry should carry ParseError so agents can see drift without
// losing the rest of the forward.
const nestedForwardMalformedInner = `<msg><appmsg appid="" sdkver="0"><title>群聊的聊天记录</title><type>19</type><recorditem><![CDATA[<recordinfo><title>outer</title><datalist count="2"><dataitem datatype="1" dataid="a1"><sourcename>Alice</sourcename><datadesc>survives</datadesc></dataitem><dataitem datatype="17" dataid="a2"><sourcename>Alice</sourcename><datatitle>Bob的聊天记录</datatitle><recordxml><recordinfo><datalist count="1"><dataitem datatype="1"><datasize>not-an-int</datasize></dataitem></datalist></recordinfo></recordxml></dataitem></datalist></recordinfo>]]></recorditem></appmsg></msg>`

func TestForwardItems_NestedMalformed(t *testing.T) {
	items, err := ForwardItems(nestedForwardMalformedInner, 3)
	if err != nil {
		t.Fatalf("outer should parse fine, got err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("outer len = %d, want 2 (outer survives despite inner drift)", len(items))
	}
	if items[0].DataDesc != "survives" {
		t.Errorf("items[0].DataDesc = %q, want \"survives\"", items[0].DataDesc)
	}
	if items[1].ParseError == "" {
		t.Error("items[1].ParseError empty — nested parse failure was silently swallowed")
	}
	if items[1].NestedItems != nil {
		t.Errorf("items[1].NestedItems should be nil on parse error, got %+v", items[1].NestedItems)
	}
}
