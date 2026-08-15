/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

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

// remoteTTL is how long a fetched remote profile is reused.
//
// 相手も Enka から取り直しているので、こちらが頻繁に聞いても新しくならない。
// **相手に負荷をかけない**ことを優先して長めにする。
const remoteTTL = 30 * time.Minute

// remoteNegativeTTL is how long "that user has no UID" is remembered.
const remoteNegativeTTL = 10 * time.Minute

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

// registerPeer wires both directions of the plugin channel.
func registerPeer(ctx plugin.Context, db *sql.DB, client *enkaClient) {
	peer := ctx.Peer()

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
	peer.OnReply(func(c context.Context, from, id string, reply json.RawMessage) error {
		var res peerResponse
		if err := json.Unmarshal(reply, &res); err != nil {
			return fmt.Errorf("応答を読めません: %w", err)
		}
		username, err := pendingUsername(c, db, id)
		if err != nil {
			return err
		}
		if username == "" {
			// どの問い合わせの答えか分からない。**捨てる。**
			// 相関が取れないものを取り込むと、別人の戦績を出しかねない。
			ctx.Logger().Warn("対応する問い合わせが無い応答を捨てました", "from", from, "id", id)
			return nil
		}
		return saveRemote(c, db, from, username, res)
	})
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

// rememberPending records which username a send id was for.
func rememberPending(c context.Context, db *sql.DB, id, host, username string) error {
	_, err := db.ExecContext(c, `
		INSERT INTO remote_pending (id, host, username, created_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (id) DO NOTHING
	`, id, host, username)
	return err
}

// pendingUsername returns the username a send id was for.
func pendingUsername(c context.Context, db *sql.DB, id string) (string, error) {
	var username string
	err := db.QueryRowContext(c, `SELECT username FROM remote_pending WHERE id = $1`, id).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// 一度使ったら消す。応答は 1 回しか来ない。
	_, _ = db.ExecContext(c, `DELETE FROM remote_pending WHERE id = $1`, id)
	return username, nil
}

// saveRemote stores what another instance answered.
func saveRemote(c context.Context, db *sql.DB, host, username string, res peerResponse) error {
	ttl := remoteTTL
	payload := res.Profile
	if !res.Linked || len(payload) == 0 {
		// 登録していない利用者。**空で覚える** — 開くたびに聞かないため。
		ttl = remoteNegativeTTL
		payload = json.RawMessage(`null`)
	}
	_, err := db.ExecContext(c, `
		INSERT INTO remote_snapshots (host, username, payload, fetched_at, expires_at)
		VALUES ($1, $2, $3, now(), now() + make_interval(secs => $4))
		ON CONFLICT (host, username) DO UPDATE SET
			payload = EXCLUDED.payload, fetched_at = EXCLUDED.fetched_at,
			expires_at = EXCLUDED.expires_at
	`, host, username, []byte(payload), int(ttl.Seconds()))
	return err
}

// remoteProfile returns the cached profile for a remote user, and whether it
// needs refreshing.
func remoteProfile(c context.Context, db *sql.DB, host, username string) (json.RawMessage, bool, error) {
	var payload []byte
	var expired bool
	err := db.QueryRowContext(c, `
		SELECT payload, expires_at <= now() FROM remote_snapshots
		WHERE host = $1 AND username = $2
	`, host, username).Scan(&payload, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if string(payload) == "null" {
		return nil, expired, nil
	}
	return payload, expired, nil
}

// remoteLookup answers for a user that is not ours.
//
// **その場では取りに行けない。** peer channel は非同期なので、初回は
// 「まだ無い」を返して問い合わせだけ出す。届いた分は次に開いたときに出る。
func remoteLookup(c context.Context, ctx plugin.Context, db *sql.DB, viewerID, userID string) (any, error) {
	host, username, err := remoteAcct(c, ctx, viewerID, userID)
	if err != nil || host == "" {
		// **エラーにしない。** そもそも関係のない利用者のプロフィールを
		// 開いただけかもしれない。表示側は linked:false で何も描かない。
		return map[string]any{"linked": false}, nil
	}

	cached, stale, err := remoteProfile(c, db, host, username)
	if err != nil {
		return nil, err
	}
	if stale {
		// 期限切れでも**古いものは返す**。取り直しは裏で進む。
		ask(c, ctx, db, host, username)
	}
	if len(cached) == 0 {
		return map[string]any{"linked": false}, nil
	}

	var profile map[string]any
	if err := json.Unmarshal(cached, &profile); err != nil {
		return map[string]any{"linked": false}, nil
	}
	// 相手が返したアイコン URL は**相手のインスタンスの**プロキシを指す。
	// そのまま出すと CSP (img-src 'self') で表示できないので、こちらの
	// プロキシ経由に貼り替える。
	rewriteAssetHosts(profile)
	return profile, nil
}

// ask sends the lookup, remembering which username it was for.
func ask(c context.Context, ctx plugin.Context, db *sql.DB, host, username string) {
	peer := ctx.Peer()
	ok, err := peer.Has(c, host)
	if err != nil || !ok {
		// 相手が同じプラグインを持っていない。**普通のこと**なので黙って諦める。
		return
	}
	id, err := peer.Send(c, host, peerRequest{Username: username})
	if err != nil {
		ctx.Logger().Debug("リモートへの問い合わせを出せませんでした", "host", host, "err", err)
		return
	}
	if err := rememberPending(c, db, id, host, username); err != nil {
		ctx.Logger().Warn("問い合わせの記録に失敗しました", "id", id, "err", err)
	}
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
