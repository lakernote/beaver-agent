package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayUsesILinkAuthenticationAndMessageShape(t *testing.T) {
	requests := make(chan *http.Request, 2)
	bodies := make(chan []byte, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		requests <- request.Clone(context.Background())
		bodies <- data
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/getupdates") {
			_, _ = response.Write([]byte(`{"ret":0,"msgs":[],"get_updates_buf":"next"}`))
			return
		}
		_, _ = response.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()
	client := server.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test server only
	gateway := NewHTTPGateway(client)
	account := Account{ID: "bot", Token: "secret", BaseURL: server.URL}
	updates, err := gateway.GetUpdates(context.Background(), account, "cursor")
	if err != nil || updates.Buffer != "next" {
		t.Fatalf("getupdates 失败: %+v err=%v", updates, err)
	}
	request := <-requests
	body := <-bodies
	if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("AuthorizationType") != "ilink_bot_token" || request.Header.Get("iLink-App-Id") != "bot" {
		t.Fatalf("iLink 认证头不完整: %+v", request.Header)
	}
	if _, err := base64.StdEncoding.DecodeString(request.Header.Get("X-WECHAT-UIN")); err != nil {
		t.Fatalf("X-WECHAT-UIN 不是 Base64: %v", err)
	}
	var updateBody map[string]any
	if err := json.Unmarshal(body, &updateBody); err != nil || updateBody["get_updates_buf"] != "cursor" {
		t.Fatalf("getupdates 请求体错误: %s err=%v", body, err)
	}
	if err := gateway.SendText(context.Background(), account, "user", "context", "完成"); err != nil {
		t.Fatal(err)
	}
	request = <-requests
	body = <-bodies
	if !strings.HasSuffix(request.URL.Path, "/sendmessage") || !strings.Contains(string(body), `"context_token":"context"`) || !strings.Contains(string(body), `"text":"完成"`) {
		t.Fatalf("sendmessage 请求错误: %s %s", request.URL.Path, body)
	}
}

func TestResolveURLRejectsNonHTTPS(t *testing.T) {
	if _, err := resolveURL("http://example.com", "ilink/bot/getupdates"); err == nil {
		t.Fatal("微信凭据不得发送到非 HTTPS 地址")
	}
}

func TestGatewayTypingProtocolAndBusinessError(t *testing.T) {
	requests := make(chan string, 3)
	bodies := make(chan []byte, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- request.URL.Path
		bodies <- body
		response.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(request.URL.Path, "/getconfig"):
			_, _ = response.Write([]byte(`{"ret":0,"typing_ticket":"ticket-1"}`))
		case bytes.Contains(body, []byte(`"status":2`)):
			_, _ = response.Write([]byte(`{"ret":-2,"errmsg":"prepare failed"}`))
		default:
			_, _ = response.Write([]byte(`{"ret":0}`))
		}
	}))
	defer server.Close()
	gateway := NewHTTPGateway(server.Client())
	account := Account{ID: "bot", Token: "secret", BaseURL: server.URL}
	ticket, err := gateway.TypingTicket(context.Background(), account, "user", "context-1")
	if err != nil || ticket != "ticket-1" {
		t.Fatalf("ticket=%q err=%v", ticket, err)
	}
	if err := gateway.SendTyping(context.Background(), account, "user", ticket, true); err != nil {
		t.Fatal(err)
	}
	if err := gateway.SendTyping(context.Background(), account, "user", ticket, false); err == nil || !strings.Contains(err.Error(), "ret=-2") {
		t.Fatalf("business error = %v", err)
	}
	paths := []string{<-requests, <-requests, <-requests}
	values := [][]byte{<-bodies, <-bodies, <-bodies}
	if !strings.HasSuffix(paths[0], "/getconfig") || !bytes.Contains(values[0], []byte(`"context_token":"context-1"`)) {
		t.Fatalf("getconfig request: %s %s", paths[0], values[0])
	}
	if !strings.HasSuffix(paths[1], "/sendtyping") || !bytes.Contains(values[1], []byte(`"typing_ticket":"ticket-1"`)) || !bytes.Contains(values[1], []byte(`"status":1`)) {
		t.Fatalf("typing start request: %s %s", paths[1], values[1])
	}
	if !bytes.Contains(values[2], []byte(`"status":2`)) {
		t.Fatalf("typing stop request: %s %s", paths[2], values[2])
	}
}

func TestDecryptMediaSupportsWeixinKeyEncodings(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("weixin attachment")
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(ciphertext[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	encodings := []string{
		hex.EncodeToString(key),
		base64.StdEncoding.EncodeToString(key),
		base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key))),
	}
	for _, encoded := range encodings {
		decoded, err := decryptMedia(ciphertext, encoded)
		if err != nil || !bytes.Equal(decoded, plain) {
			t.Fatalf("key %q: decoded=%q err=%v", encoded, decoded, err)
		}
	}
}

func TestMediaDownloadURLOnlyAllowsTencentHTTPS(t *testing.T) {
	if value, err := mediaDownloadURL(CDNMedia{EncryptedQuery: "a+b/c="}); err != nil || !strings.Contains(value, "encrypted_query_param=a%2Bb%2Fc%3D") {
		t.Fatalf("fallback URL = %q, err=%v", value, err)
	}
	for _, value := range []string{"http://novac2c.cdn.weixin.qq.com/a", "https://example.com/a", "://broken"} {
		if _, err := mediaDownloadURL(CDNMedia{FullURL: value}); err == nil {
			t.Fatalf("untrusted URL accepted: %s", value)
		}
	}
	if _, err := mediaDownloadURL(CDNMedia{FullURL: "https://novac2c.cdn.weixin.qq.com/a"}); err != nil {
		t.Fatalf("Tencent CDN rejected: %v", err)
	}
}
