// cmd/mockwx —— 微信/企微回调 mock 服务（W8，2026-09-12）
// 用途：无真实凭证也能端到端测通道（smoke_channel.sh）。
// 能力：
//
//	GET  /cgi-bin/gettoken?...            → 伪造 access_token
//	POST /cgi-bin/message/send?token=     → 记录出站消息到 -out JSONL，回 {errcode:0}
//	GET  /cgi-bin/__token?...             → 供公众号 token 路径
//	GET  /__gen_callback?...              → 生成"已签名+加密"的回调体，供脚本 POST 到真实服务的 channel 回调端点
//	GET  /__health                        → 存活
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"ai-scrm/pkg/wxcrypt"
)

type sentMsg struct {
	Time      string      `json:"time"`
	ToUser    string      `json:"to_user"`
	MsgType   string      `json:"msg_type"`
	Content   string      `json:"content"`
	AgentID   interface{} `json:"agentid"`
	HaveToken bool        `json:"has_token"`
}

// main 启动当前命令入口。
func main() {
	listen := flag.String("listen", "127.0.0.1:9099", "mock 监听地址")
	out := flag.String("out", "mockwx_outbox.jsonl", "出站消息 JSONL 落盘路径")
	flag.Parse()

	var mu sync.Mutex
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Fatalf("打开出站文件失败: %v", err)
	}
	defer f.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/__health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	// gettoken（企微 & 公众号共用返回结构）
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 0, "access_token": "MOCK_TOKEN_" + fmt.Sprint(time.Now().Unix()), "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "MOCK_MP_TOKEN", "expires_in": 7200})
	})
	// message/send（企微自建应用）+ kf/send_msg（客服）
	sendHandler := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec := sentMsg{Time: time.Now().Format(time.RFC3339), MsgType: strOf(body["msgtype"]), HaveToken: r.URL.Query().Get("access_token") != ""}
		rec.ToUser = strOf(body["touser"])
		rec.AgentID = body["agentid"]
		if t, ok := body["text"].(map[string]interface{}); ok {
			rec.Content = strOf(t["content"])
		} else if t, ok := body["markdown"].(map[string]interface{}); ok {
			rec.Content = strOf(t["content"])
		}
		mu.Lock()
		b, _ := json.Marshal(rec)
		f.Write(append(b, '\n'))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 0, "msgid": "MOCK_MSG"})
	}
	mux.HandleFunc("/cgi-bin/message/send", sendHandler)
	mux.HandleFunc("/cgi-bin/kf/send_msg", sendHandler)
	mux.HandleFunc("/cgi-bin/message/custom/send", sendHandler) // 公众号客服消息
	// get_jsapi_ticket（企微侧边栏 JS-SDK 签名，W7）
	mux.HandleFunc("/cgi-bin/get_jsapi_ticket", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 0, "ticket": "MOCK_JS_TICKET", "expires_in": 7200})
	})

	// 生成"已签名+加密"回调体：token/aeskey/corpid/content/fromuser
	mux.HandleFunc("/__gen_echostr", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c, err := wxcrypt.New(q.Get("token"), q.Get("aeskey"), q.Get("corpid"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		plain := q.Get("echo")
		if plain == "" {
			plain = "mock_echo_token_12345"
		}
		enc, err := c.Encrypt(plain)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ts := fmt.Sprint(time.Now().Unix())
		nonce := "1372623124"
		sig := c.Signature(ts, nonce, enc)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"echostr": enc, "msg_signature": sig, "timestamp": ts, "nonce": nonce, "plaintext": plain})
	})

	// 生成"已签名+加密"回调体：token/aeskey/corpid/content/fromuser
	mux.HandleFunc("/__gen_callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		token, aeskey, corpid := q.Get("token"), q.Get("aeskey"), q.Get("corpid")
		content, fromUser := q.Get("content"), q.Get("from")
		if fromUser == "" {
			fromUser = "wm_external_userid_001"
		}
		c, err := wxcrypt.New(token, aeskey, corpid)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		plain := fmt.Sprintf("<xml><ToUserName><![CDATA[%s]]></ToUserName><FromUserName><![CDATA[%s]]></FromUserName><CreateTime>%d</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[%s]]></Content><AgentID>1000002</AgentID></xml>", corpid, fromUser, time.Now().Unix(), content)
		enc, err := c.Encrypt(plain)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ts := fmt.Sprint(time.Now().Unix())
		nonce := "1772739091"
		sig := c.Signature(ts, nonce, enc)
		body := fmt.Sprintf("<xml><ToUserName><![CDATA[%s]]></ToUserName><Encrypt><![CDATA[%s]]></Encrypt><AgentID><![CDATA[1000002]]></AgentID><MsgSignature><![CDATA[%s]]></MsgSignature><TimeStamp>%s</TimeStamp><Nonce><![CDATA[%s]]></Nonce></xml>", corpid, enc, sig, ts, nonce)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"body": body, "msg_signature": sig, "timestamp": ts, "nonce": nonce})
	})

	log.Printf("[mockwx] listening on %s, outbox=%s", *listen, *out)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// strOf 将 JSON 字段安全转换为字符串。
func strOf(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
