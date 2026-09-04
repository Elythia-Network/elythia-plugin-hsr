package hsr

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

// 相手のインスタンスのプロキシ URL を、こちらのプロキシ URL に貼り替えること。
//
// **そのまま出すと 2 つ壊れる。** CSP (img-src 'self') で表示できないうえ、
// 閲覧者の接続先が相手のサーバーに漏れる。
func TestRewriteAssetURL(t *testing.T) {
	got := rewriteAssetURL("https://other.example/api/plugin/hsr/asset/SpriteOutput/AvatarRoundIcon/1415.png")
	if want := assetRoutePrefix + "SpriteOutput/AvatarRoundIcon/1415.png"; got != want {
		t.Errorf("got %q want %q", got, want)
	}

	// 素の URL は触らない。
	if got := rewriteAssetURL("https://example.test/files/x.png"); got != "https://example.test/files/x.png" {
		t.Errorf("関係ない URL を書き換えた: %q", got)
	}

	// 想定外の形は落とす。相手が渡した文字列をそのまま URL にしない。
	for _, bad := range []string{
		"https://other.example/api/plugin/hsr/asset/../../etc/passwd.png",
		"https://other.example/api/plugin/hsr/asset/",
		"https://other.example/api/plugin/hsr/asset/a b.png",
		"https://other.example/api/plugin/hsr/asset/x.svg",
	} {
		if got := rewriteAssetURL(bad); got != "" {
			t.Errorf("不正なパスを通した: %q -> %q", bad, got)
		}
	}
}

// 入れ子になった応答の中の URL もすべて貼り替えること。
func TestRewriteAssetHosts_Nested(t *testing.T) {
	raw := `{
		"profileIcon": "https://other.example/api/plugin/hsr/asset/A.png",
		"characters": [
			{"icon": "https://other.example/api/plugin/hsr/asset/B.png",
			 "lightCone": {"icon": "https://other.example/api/plugin/hsr/asset/C.png"},
			 "relics": [{"icon": "https://other.example/api/plugin/hsr/asset/D.png"}]}
		]
	}`
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	rewriteAssetHosts(v)

	out, _ := json.Marshal(v)
	s := string(out)
	if strings.Contains(s, "other.example") {
		t.Errorf("相手のホストが残っている: %s", s)
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if !strings.Contains(s, assetRoutePrefix+name+".png") {
			t.Errorf("%s が貼り替わっていない: %s", name, s)
		}
	}
}

// fakeAPI records which caller the plugin used.
type fakeAPI struct {
	calls []string
	resp  json.RawMessage
	err   error
}

func (a *fakeAPI) Anonymous() plugin.Caller { return &fakeCaller{api: a, who: "anonymous"} }
func (a *fakeAPI) AsUser(userID string) plugin.Caller {
	return &fakeCaller{api: a, who: "asUser:" + userID}
}

type fakeCaller struct {
	api *fakeAPI
	who string
}

func (c *fakeCaller) Call(_ context.Context, endpoint string, _ any) (json.RawMessage, error) {
	c.api.calls = append(c.api.calls, c.who+" "+endpoint)
	return c.api.resp, c.api.err
}

// fakeCtx is the minimum plugin.Context remoteAcct needs.
type fakeCtx struct {
	plugin.Context
	api plugin.API
}

func (c *fakeCtx) API() plugin.API { return c.api }

// **閲覧者として引く。** 匿名で引くと ugcVisibilityForVisitor='local' (既定) の
// インスタンスでリモート利用者が NO_SUCH_USER になり、問い合わせ自体が出せない
// (mk-go #2106)。原神版で実際にこれを踏んだ。
func TestRemoteAcct_CallsAsViewer(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"other.example"}`)}
	ctx := &fakeCtx{api: api}

	host, username, err := remoteAcct(context.Background(), ctx, "viewer1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if host != "other.example" || username != "alice" {
		t.Fatalf("host/username: %q / %q", host, username)
	}
	if len(api.calls) != 1 || api.calls[0] != "asUser:viewer1 users/show" {
		t.Errorf("閲覧者として呼んでいない: %v", api.calls)
	}
}

// 未ログインの閲覧者では匿名で引く。引けないのは設定どおりの挙動なので、
// 無理に権限を上げない。
func TestRemoteAcct_AnonymousWhenNoViewer(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"other.example"}`)}
	ctx := &fakeCtx{api: api}

	if _, _, err := remoteAcct(context.Background(), ctx, "", "u1"); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 1 || api.calls[0] != "anonymous users/show" {
		t.Errorf("匿名で呼んでいない: %v", api.calls)
	}
}

// ローカル利用者には host が無い。問い合わせ先が無いので何もしない。
func TestRemoteAcct_LocalUser(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":null}`)}
	ctx := &fakeCtx{api: api}

	host, _, err := remoteAcct(context.Background(), ctx, "viewer1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		t.Errorf("ローカル利用者に host を返した: %q", host)
	}
}

// API が配線されていなければ「分からない」として扱う (落とさない)。
func TestRemoteAcct_NoAPI(t *testing.T) {
	host, _, err := remoteAcct(context.Background(), &fakeCtx{}, "viewer1", "u1")
	if err != nil || host != "" {
		t.Errorf("host=%q err=%v", host, err)
	}
}

func TestLocalUserIDByUsername(t *testing.T) {
	// 自分のところの利用者。
	api := &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null}`)}
	got, err := localUserIDByUsername(context.Background(), &fakeCtx{api: api}, "alice")
	if err != nil || got != "u1" {
		t.Fatalf("id=%q err=%v", got, err)
	}
	if api.calls[0] != "anonymous users/show" {
		t.Errorf("自分のところの利用者は匿名で引ける: %v", api.calls)
	}

	// **他所の利用者は答えない。** 手元のキャッシュを又貸しすると出どころが
	// 分からなくなる。
	remote := &fakeAPI{resp: json.RawMessage(`{"id":"u2","host":"third.example"}`)}
	got, err = localUserIDByUsername(context.Background(), &fakeCtx{api: remote}, "bob")
	if err != nil || got != "" {
		t.Fatalf("又貸ししている: %q (err=%v)", got, err)
	}

	// 居ない利用者はエラーではなく空。
	missing := &fakeAPI{err: &plugin.APIError{Status: 404}}
	got, err = localUserIDByUsername(context.Background(), &fakeCtx{api: missing}, "nobody")
	if err != nil || got != "" {
		t.Fatalf("404 をエラーにしている: %q / %v", got, err)
	}

	if got, _ := localUserIDByUsername(context.Background(), &fakeCtx{}, "alice"); got != "" {
		t.Error("API 未配線で値を返している")
	}
}

// --- peer channel ---

// peerHarness wires the plugin with a fake peer and API.
func peerHarness(t *testing.T, api plugin.API) (*plugintest.Harness, plugintest.Handlers) {
	t.Helper()
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := plugintest.New(t).
		WithName("hsr").
		WithDB(testDB(t)).
		WithAPI(api).
		WithPeers("other.example").
		WithConfig(testConfig(t, srv.URL))
	// **Peer の登録は Definition.Peer 経由 (mk-go #2819 / #2820)。**
	h.Peer(Plugin)
	return h, h.Routes(Plugin)
}

// 相手から聞かれたら、自分のところの利用者の分だけ答える。
func TestPeer_AnswersForLocalUser(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null}`)}
	h, routes := peerHarness(t, api)

	if _, err := routes.Call(t, "POST /me/set", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`}); err != nil {
		t.Fatal(err)
	}

	res, err := h.DeliverPeer("other.example", peerRequest{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(peerResponse)
	if !got.Linked {
		t.Fatal("登録済みの利用者を linked=false で返している")
	}
	if !strings.Contains(string(got.Profile), "開拓者") {
		t.Errorf("戦績が入っていない: %s", got.Profile)
	}
}

// 登録していない利用者は linked=false。エラーにはしない。
func TestPeer_AnswersUnlinked(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"id":"u9","host":null}`)}
	h, _ := peerHarness(t, api)

	res, err := h.DeliverPeer("other.example", peerRequest{Username: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if res.(peerResponse).Linked {
		t.Error("登録していない利用者を linked=true で返している")
	}
}

// **中身は信用しない。** 相手は同じプラグインを持っているだけで善良とは限らない。
func TestPeer_RejectsBadRequest(t *testing.T) {
	h, _ := peerHarness(t, &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null}`)})

	if _, err := h.DeliverPeer("other.example", map[string]any{"username": ""}); err == nil {
		t.Error("空の username を通している")
	}
	if _, err := h.DeliverPeer("other.example", "not an object"); err == nil {
		t.Error("読めない payload を通している")
	}
}

// 問い合わせは非同期。初回は「まだ無い」を返しつつ、裏で送ること。
func TestRemoteLookup_AsksThenServesCache(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"other.example"}`)}
	h, routes := peerHarness(t, api)

	res, err := routes.Call(t, "POST /profile", plugintest.Request{UserID: "viewer1", Body: `{"userId":"u-remote"}`})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["linked"] != false {
		t.Errorf("初回から値を返している: %+v", res)
	}
	sends := h.PeerSends()
	if len(sends) != 1 || sends[0].Host != "other.example" {
		t.Fatalf("問い合わせを出していない: %+v", sends)
	}

	// 応答が届いたら次から出す。
	profile := map[string]any{
		"linked":      true,
		"nickname":    "開拓者",
		"profileIcon": "https://other.example" + assetRoutePrefix + "A.png",
	}
	body, _ := json.Marshal(profile)
	if err := h.DeliverPeerReply("other.example", sends[0].ID, peerResponse{Linked: true, Profile: body}); err != nil {
		t.Fatal(err)
	}

	res, err = routes.Call(t, "POST /profile", plugintest.Request{UserID: "viewer1", Body: `{"userId":"u-remote"}`})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["nickname"] != "開拓者" {
		t.Fatalf("取り寄せた分が出ていない: %+v", m)
	}
	// 相手のホストが残っていないこと。
	if icon, _ := m["profileIcon"].(string); icon != assetRoutePrefix+"A.png" {
		t.Errorf("アイコンが貼り替わっていない: %q", icon)
	}
}

// 相手が同じプラグインを持っていなければ黙って諦める (Misskey TS なら当然)。
func TestRemoteLookup_SkipsUnknownPeer(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"stranger.example"}`)}
	h, routes := peerHarness(t, api)

	res, err := routes.Call(t, "POST /profile", plugintest.Request{UserID: "viewer1", Body: `{"userId":"u-remote"}`})
	if err != nil {
		t.Fatalf("エラーにするべきではない: %v", err)
	}
	if res.(map[string]any)["linked"] != false {
		t.Errorf("値を返している: %+v", res)
	}
	if len(h.PeerSends()) != 0 {
		t.Error("持っていない相手に送っている")
	}
}

// どの問い合わせの答えか分からない応答は捨てる。取り込むと別人の戦績を出しかねない。
func TestPeer_DropsUncorrelatedReply(t *testing.T) {
	h, routes := peerHarness(t, &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"other.example"}`)})

	body, _ := json.Marshal(map[string]any{"linked": true, "nickname": "誰か"})
	if err := h.DeliverPeerReply("other.example", "unknown-id", peerResponse{Linked: true, Profile: body}); err != nil {
		t.Fatal(err)
	}

	res, err := routes.Call(t, "POST /profile", plugintest.Request{UserID: "viewer1", Body: `{"userId":"u-remote"}`})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["nickname"] == "誰か" {
		t.Error("相関の取れない応答を取り込んでいる")
	}
}

// 読めない応答でエラーにすること (握り潰すと壊れたまま気付けない)。
func TestPeer_RejectsBadReply(t *testing.T) {
	h, _ := peerHarness(t, &fakeAPI{resp: json.RawMessage(`{"username":"alice","host":"other.example"}`)})

	if err := h.DeliverPeerReply("other.example", "x", "not an object"); err == nil {
		t.Error("読めない応答を通している")
	}
}
