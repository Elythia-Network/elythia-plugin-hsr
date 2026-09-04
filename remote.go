package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/peercache"
)

/*
 * リモート利用者の戦績を、相手のインスタンスから取り寄せる。
 *
 * 経路は mk-go 同士に閉じた peer channel (mk-go #2537)。ActivityPub には
 * 出ないので、相手も同じプラグインを入れた mk-go である必要がある。
 *
 * **問い合わせは非同期。** 送ってすぐ答えは返らないので、初回は「まだ無い」
 * として返し、届いた分を次から出す。
 */

// peerRequest is what we ask another instance.
//
// **username だけを送る。** 誰が見に来たかは送らない (相手に渡す必要が無い)。
type peerRequest struct {
	Username string `json:"username"`
}

// peerResponse is what the other instance answers.
type peerResponse struct {
	Linked  bool            `json:"linked"`
	Profile json.RawMessage `json:"profile,omitempty"`
}

// remoteTTL / remoteNegativeTTL は peercache に渡す寿命。
const (
	remoteTTL         = 30 * time.Minute
	remoteNegativeTTL = 10 * time.Minute
)

// newRemoteCache builds the view-time cache shared by the peer callback and
// the profile route.
//
// **型は plugin/peercache が持つ (mk-go #2820)。** 非同期取り寄せ + TTL +
// 空振りの記憶 + 初回は空、という形は 3 プラグインに手で書かれていた。
func newRemoteCache(ctx plugin.Context, db *sql.DB) (*peercache.Cache, error) {
	return peercache.New(peercache.Options{
		Context:     ctx,
		DB:          db,
		Request:     func(key string) any { return peerRequest{Username: key} },
		TTL:         remoteTTL,
		NegativeTTL: remoteNegativeTTL,
	})
}

// registerPeer wires both directions of the plugin channel.
func registerPeer(ctx plugin.Context, peer plugin.Peer, db *sql.DB, client *enkaClient) error {
	cache, err := newRemoteCache(ctx, db)
	if err != nil {
		return err
	}

	// 相手から「この利用者の戦績をくれ」と聞かれたとき。
	//
	// from は署名で確定したホストなので、名乗りとして扱ってよい。ただし
	// **中身は信用しない** — username は相手が自由に書ける。
	peer.Handle(func(c context.Context, from string, payload json.RawMessage) (any, error) {
		var req peerRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("リクエストを読めません: %w", err)
		}
		if req.Username == "" {
			return nil, errors.New("username が空です")
		}

		userID, err := localUserIDByUsername(c, ctx, req.Username)
		if err != nil {
			return nil, err
		}
		if userID == "" {
			return peerResponse{Linked: false}, nil
		}

		profile, err := buildProfile(c, db, client, userID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return peerResponse{Linked: false}, nil
		}
		body, err := json.Marshal(profile)
		if err != nil {
			return nil, err
		}
		return peerResponse{Linked: true, Profile: body}, nil
	})

	// 問い合わせの答えが返ってきたとき。
	peer.OnReply(func(c context.Context, _, id string, reply json.RawMessage) error {
		var res peerResponse
		if err := json.Unmarshal(reply, &res); err != nil {
			return fmt.Errorf("応答を読めません: %w", err)
		}
		return cache.Store(c, id, res.Profile, res.Linked && len(res.Profile) > 0)
	})
	return nil
}

// localUserIDByUsername resolves a local username to its user id.
//
// **mk-go の API を通す。** DB を直接見ると、凍結や可視性の判断を自分で
// 実装することになる。
//
// ここは匿名でよい。`ugcVisibilityForVisitor` のゲートが効くのは**リモート**
// 利用者を引くときだけで (mk-go #2106)、自分のところの利用者は匿名でも引ける。
func localUserIDByUsername(c context.Context, ctx plugin.Context, username string) (string, error) {
	api := ctx.API()
	if api == nil {
		// 本番では必ず配線されている。テストで渡していない場合に落とさない
		// ための保険で、「分からない」として扱う。
		return "", nil
	}
	raw, err := api.Anonymous().Call(c, "users/show", map[string]any{"username": username})
	if err != nil {
		var ae *plugin.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return "", nil
		}
		return "", err
	}
	var user struct {
		ID   string  `json:"id"`
		Host *string `json:"host"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		return "", err
	}
	// **自分のところの利用者だけ答える。** 手元にキャッシュしている他所の
	// 利用者を又貸しすると、情報の出どころが分からなくなる。
	if user.Host != nil && *user.Host != "" {
		return "", nil
	}
	return user.ID, nil
}

// remoteLookup answers for a user that is not ours.
func remoteLookup(c context.Context, ctx plugin.Context, db *sql.DB, viewerID, userID string) (any, error) {
	host, username, err := remoteAcct(c, ctx, viewerID, userID)
	if err != nil || host == "" {
		return map[string]any{"linked": false}, nil
	}

	cache, err := newRemoteCache(ctx, db)
	if err != nil {
		return nil, err
	}
	// 初回は空で返り、取り寄せは裏で走る。期限切れでも古いものは返る。
	cached, err := cache.Lookup(c, host, username)
	if err != nil {
		return nil, err
	}
	if len(cached) == 0 {
		return map[string]any{"linked": false}, nil
	}

	var profile map[string]any
	if err := json.Unmarshal(cached, &profile); err != nil {
		return map[string]any{"linked": false}, nil
	}
	// 相手が返した URL は**相手のインスタンスの**プロキシを指す。そのまま出すと
	// CSP で表示できないうえ、閲覧者の接続先が相手に漏れる。
	rewriteAssetHosts(profile)
	return profile, nil
}

// remoteAcct resolves a user id to its host and username.
//
// **閲覧者として引く。** 匿名で引くと、`ugcVisibilityForVisitor` が `local`
// (既定) のインスタンスではリモート利用者が NO_SUCH_USER になり、問い合わせ
// 自体を出せない (mk-go #2106 のゲート)。未ログインの閲覧者では引けないままだが、
// それは「未ログインにリモートの情報を見せない」という設定どおりの挙動。
func remoteAcct(c context.Context, ctx plugin.Context, viewerID, userID string) (host, username string, err error) {
	api := ctx.API()
	if api == nil {
		return "", "", nil
	}
	caller := api.Anonymous()
	if viewerID != "" {
		caller = api.AsUser(viewerID)
	}
	raw, err := caller.Call(c, "users/show", map[string]any{"userId": userID})
	if err != nil {
		return "", "", err
	}
	var user struct {
		Username string  `json:"username"`
		Host     *string `json:"host"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		return "", "", err
	}
	if user.Host == nil || *user.Host == "" {
		return "", "", nil
	}
	return *user.Host, user.Username, nil
}

// rewriteAssetHosts points image URLs at our own proxy.
//
// 相手が返すのは相手のインスタンスの `/api/plugin/hsr/asset/<path>`。
// **パスだけを取り出して自分の URL に組み直す** (相手の URL をそのまま出すと
// CSP で弾かれるうえ、閲覧者の接続先が相手に漏れる)。
func rewriteAssetHosts(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok {
				t[k] = rewriteAssetURL(s)
				continue
			}
			rewriteAssetHosts(val)
		}
	case []any:
		for _, val := range t {
			rewriteAssetHosts(val)
		}
	}
}

// assetRoutePrefix is where this plugin serves proxied images.
const assetRoutePrefix = "/api/plugin/hsr/asset/"

func rewriteAssetURL(s string) string {
	i := strings.Index(s, assetRoutePrefix)
	if i < 0 {
		return s
	}
	path := s[i+len(assetRoutePrefix):]
	if !validAssetPath(path) {
		// 想定外の形は落とす。相手が渡した文字列をそのまま URL にしない。
		return ""
	}
	return assetRoutePrefix + path
}
