// 通道适配器单测（W4/W5/W7）：httptest 模拟企微/公众号端点，验证 token 换取、发送、签名、入站解密。
package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-scrm/internal/model"
	"ai-scrm/pkg/wxcrypt"
)

const (
	tkAESKey = "jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C"
	tkToken  = "test_callback_token"
)

// mockServer 起一个模拟企微/公众号端点的服务器，返回其 URL。
func mockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "access_token": "TK", "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "access_token": "TK", "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/message/send", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "msgid": "app1"})
	})
	mux.HandleFunc("/cgi-bin/kf/send_msg", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "msgid": "kf1"})
	})
	mux.HandleFunc("/cgi-bin/message/custom/send", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "msgid": "mp1"})
	})
	mux.HandleFunc("/cgi-bin/get_jsapi_ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "ticket": "JSTICKET", "expires_in": 7200})
	})
	return httptest.NewServer(mux)
}

func TestWecomKfSendText(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	cred := &Credential{ChannelID: 9101, CorpID: "wx_kf", Secret: "s", BaseURL: srv.URL}
	res := wecomKfAdapter{}.SendText(context.Background(), cred, "wm_ext_1", "你好呀", "text")
	if res.Err != nil || !res.Sent || res.MsgID != "kf1" {
		t.Fatalf("kf 发送失败: %+v", res)
	}
}

func TestWechatMPSendText(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	cred := &Credential{ChannelID: 9102, Type: model.ChannelTypeWechatMP, AppID: "wx_mp", Secret: "s", BaseURL: srv.URL}
	res := wechatMPAdapter{}.SendText(context.Background(), cred, "openid_1", "你好公众号", "text")
	if res.Err != nil || !res.Sent || res.MsgID != "mp1" {
		t.Fatalf("mp 发送失败: %+v", res)
	}
}

func TestWechatMPInboundDecrypt(t *testing.T) {
	// 安全模式：构造加密信封，走适配器 DecryptInbound 应还原出 openid 与正文
	appid := "wx_mp_receive"
	c, err := wxcrypt.New(tkToken, tkAESKey, appid)
	if err != nil {
		t.Fatal(err)
	}
	plain := `<xml><ToUserName><![CDATA[` + appid + `]]></ToUserName><FromUserName><![CDATA[openid_abc]]></FromUserName><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[在吗]]></Content></xml>`
	enc, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	ts, nonce := "1409659589", "1372623124"
	sig := c.Signature(ts, nonce, enc)
	body := []byte(`<xml><Encrypt><![CDATA[` + enc + `]]></Encrypt><MsgSignature><![CDATA[` + sig + `]]></MsgSignature><TimeStamp>` + ts + `</TimeStamp><Nonce><![CDATA[` + nonce + `]]></Nonce></xml>`)
	cred := &Credential{ChannelID: 9103, Type: model.ChannelTypeWechatMP, AppID: appid, Token: tkToken, Encoding: tkAESKey}
	in, err := wechatMPAdapter{}.DecryptInbound(cred, ts, nonce, sig, body)
	if err != nil {
		t.Fatalf("mp 入站解密失败: %v", err)
	}
	if in.ExternalID != "openid_abc" || in.Content != "在吗" {
		t.Fatalf("mp 入站解析错: %+v", in)
	}
}

func TestBuildJSConfig(t *testing.T) {
	srv := mockServer(t)
	defer srv.Close()
	cred := &Credential{ChannelID: 9104, CorpID: "wx_corp", Secret: "s", BaseURL: srv.URL}
	res, err := BuildJSConfig(context.Background(), cred, "https://example.com/page?a=1")
	if err != nil {
		t.Fatalf("jsconfig 失败: %v", err)
	}
	if res.CorpID != "wx_corp" || len(res.Signature) != 40 {
		t.Fatalf("jsconfig 返回值异常: %+v", res)
	}
	// 签名应等于对已知 ticket/nonce/ts/url 的 sha1（用同一函数自证，至少覆盖到分支）
	if res.Signature != SignJSConfig("JSTICKET", res.NonceStr, res.Timestamp, "https://example.com/page?a=1") {
		t.Fatalf("签名与本地重算不一致")
	}
}

func TestAdapterRegistryTypes(t *testing.T) {
	for _, typ := range []string{model.ChannelTypeWecomApp, model.ChannelTypeWecomKf, model.ChannelTypeWechatMP} {
		if _, ok := registry[typ]; !ok {
			t.Fatalf("适配器未注册: %s", typ)
		}
	}
}
