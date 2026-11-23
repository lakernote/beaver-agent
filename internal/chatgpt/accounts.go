// Package chatgpt implements Sign in with ChatGPT for the Beaver provider.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	APIBaseURL = "https://api.openai.com/v1"
	planScope  = "chatgpt.tokens.use.direct"
)

var (
	ErrAccountMissing = errors.New("没有这份 ChatGPT 授权，请在模型配置里连接账号")
	ErrSignInRequired = errors.New("ChatGPT 授权已失效，请在模型配置里重新登录")
	ErrPlanConsent    = errors.New("此账号尚未授权使用 ChatGPT 订阅，请重新授权并允许使用套餐")
)

// Account 只包含可在模型配置页面显示的信息。
type Account struct {
	ID             string `json:"id"`
	Email          string `json:"email,omitempty"`
	Plan           string `json:"plan,omitempty"`
	SignedIn       bool   `json:"signedIn"`
	PlanAuthorized bool   `json:"planAuthorized"`
}

type credential struct {
	ClientID     string    `json:"client_id"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	Scopes       string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func (value credential) account() Account {
	active := value.AccessToken != "" && (value.RefreshToken != "" || time.Now().Before(value.ExpiresAt))
	return Account{ID: accountID(value.ClientID), Email: value.Email, SignedIn: active,
		PlanAuthorized: active && hasScope(value.Scopes, planScope) && hasScope(value.Scopes, "resource.invoke")}
}

func (value *credential) clearTokens() {
	*value = credential{ClientID: value.ClientID, Subject: value.Subject, Email: value.Email}
}

type credentialFile struct {
	HostID   string                `json:"host_id"`
	Accounts map[string]credential `json:"accounts"`
}

type Client struct {
	path  string
	mu    sync.Mutex
	locks sync.Map
	http  *http.Client
	keys  oidc.KeySet
}

func New(home string) *Client {
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: rejectRedirect}
	return &Client{path: filepath.Join(home, "chatgpt.json"), http: client,
		keys: oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), issuer+"/.well-known/jwks.json")}
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func accountID(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:16])
}

func (client *Client) read() (credentialFile, error) {
	value := credentialFile{Accounts: make(map[string]credential)}
	info, err := os.Lstat(client.path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2*1024*1024 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return value, errors.New("ChatGPT 凭据文件无效或权限过宽")
	}
	data, err := os.ReadFile(client.path)
	if err != nil {
		return value, err
	}
	if json.Unmarshal(data, &value) != nil || value.Accounts == nil {
		return value, errors.New("无法读取 ChatGPT 凭据")
	}
	return value, nil
}

// write 先写同目录的 0600 文件，再一次性替换旧记录。刷新令牌轮换时，
// access token、refresh token、范围和到期时间一起提交。
func (client *Client) write(value credentialFile) error {
	file, err := os.CreateTemp(filepath.Dir(client.path), ".chatgpt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), client.path)
}

func (client *Client) hostID() (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	file, err := client.read()
	if err != nil || file.HostID != "" {
		return file.HostID, err
	}
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	file.HostID = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	return file.HostID, client.write(file)
}

func (client *Client) load(id string) (credential, error) {
	file, err := client.read()
	if err != nil {
		return credential{}, err
	}
	value, ok := file.Accounts[id]
	if !ok || accountID(value.ClientID) != id || value.Subject == "" {
		return credential{}, ErrAccountMissing
	}
	return value, nil
}

func (client *Client) save(value credential) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	file, err := client.read()
	if err != nil {
		return err
	}
	file.Accounts[accountID(value.ClientID)] = value
	return client.write(file)
}

func (client *Client) Accounts() ([]Account, error) {
	file, err := client.read()
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(file.Accounts))
	for _, value := range file.Accounts {
		accounts = append(accounts, value.account())
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Email == accounts[j].Email {
			return accounts[i].ID < accounts[j].ID
		}
		return accounts[i].Email < accounts[j].Email
	})
	return accounts, nil
}

func (client *Client) Account(id string) (Account, error) {
	value, err := client.load(id)
	if err != nil {
		return Account{}, err
	}
	return value.account(), nil
}

func (client *Client) lock(id string) func() {
	lock, _ := client.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := lock.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func hasScope(scopes, required string) bool {
	for _, scope := range strings.Fields(scopes) {
		if scope == required {
			return true
		}
	}
	return false
}
