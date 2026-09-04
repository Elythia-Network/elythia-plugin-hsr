// Package hsr shows a user's Honkai: Star Rail profile on their Misskey
// profile.
//
// データ元は Enka.Network (https://enka.network/)。認証不要の公開 API だが、
// ttl に従ったキャッシュを求められているのでそれに従う。
//
// **原神版とは API の癖が違う。** スターレイル側は公式のドキュメントが無く
// (API-docs には gi と zzz のみ)、形は実データから起こしている。
package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/peercache"
)

// Plugin is the entry point referenced by the generated registration code.
var Plugin = plugin.Definition{
	Name:       "hsr",
	Version:    "0.1.0",
	APIVersion: plugin.APIVersion,
	Migrations: append(migrations, peerCacheMigration...),
	Routes:     routes,
	Jobs:       jobs,
	// 同じプラグインを入れた mk-go 同士で、リモート利用者の戦績を取り寄せる。
	// ActivityPub には出ない経路 (mk-go #2537)。
	Peered: true,
	// **登録はここ (mk-go #2819)。** Routes の中でやると、ロールを分割した
	// 構成で応答が届かない (送信の POST は queue ロールで走る)。
	Peer: peer,
}

// settings mirrors the `plugins.hsr` section of the instance config.
type settings struct {
	// Endpoint is the Enka.Network base URL. テストで差し替えられるように
	// 設定にしている。
	Endpoint string `json:"endpoint"`
	// UserAgent identifies this instance to Enka.Network. 向こうが
	// 「追跡できるように付けてほしい」と明示しているので既定でも名乗る。
	UserAgent string `json:"userAgent"`
	// TimeoutSeconds bounds one upstream request.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// Language selects the slice of Enka's localisation file to use.
	Language string `json:"language"`
	// MasterEndpoint is where the master data (キャラ / 光円錐 / 遺物の定義) lives.
	//
	// 取得元とは別のホストなので分けてある。**末尾のスラッシュまで含める。**
	MasterEndpoint string `json:"masterEndpoint"`
}

func loadSettings(ctx plugin.Context) (settings, error) {
	s := settings{
		Endpoint:       "https://enka.network",
		UserAgent:      "mk-go-plugin-hsr/0.1 (+https://github.com/shiroha-a/mk)",
		TimeoutSeconds: 10,
		Language:       "ja",
		MasterEndpoint: masterBase,
	}
	if err := ctx.Config().Unmarshal(&s); err != nil {
		return s, err
	}
	return s, nil
}

// migrations creates everything up front.
//
// 原神版は列を足しながら育てたが、こちらは最初から必要な形が分かっているので
// 1 本にまとめる。
var migrations = []plugin.Migration{
	{Version: 1, SQL: `
		CREATE TABLE accounts (
			user_id    text PRIMARY KEY,
			uid        text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE snapshots (
			uid             text PRIMARY KEY,
			nickname        text NOT NULL,
			signature       text NOT NULL DEFAULT '',
			level           int  NOT NULL DEFAULT 0,
			world_level     int  NOT NULL DEFAULT 0,
			region          text NOT NULL DEFAULT '',
			platform        text NOT NULL DEFAULT '',
			friend_count    int  NOT NULL DEFAULT 0,
			head_icon       int  NOT NULL DEFAULT 0,
			achievements    int  NOT NULL DEFAULT 0,
			book_count      int  NOT NULL DEFAULT 0,
			avatar_count    int  NOT NULL DEFAULT 0,
			equipment_count int  NOT NULL DEFAULT 0,
			relic_count     int  NOT NULL DEFAULT 0,
			music_count     int  NOT NULL DEFAULT 0,
			rogue_score     int  NOT NULL DEFAULT 0,
			memory_level    int  NOT NULL DEFAULT 0,
			characters      jsonb NOT NULL DEFAULT '[]',
			fetched_at      timestamptz NOT NULL DEFAULT now(),
			expires_at      timestamptz NOT NULL
		);
	`},
}

// peerCacheMigration replaces the hand-written remote cache with
// plugin/peercache (mk-go #2820)。**中身はキャッシュなので捨ててよい。**
var peerCacheMigration = append([]plugin.Migration{{
	Version: 2,
	SQL: `
		DROP TABLE IF EXISTS remote_snapshots;
		DROP TABLE IF EXISTS remote_pending;
	`,
}}, peercache.Migrations(3)...)

// peer registers both directions of the plugin channel.
func peer(ctx plugin.Context, p plugin.Peer) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	return registerPeer(ctx, p, ctx.Storage().DB(), newEnkaClient(set))
}

// uidPattern matches a Star Rail UID.
//
// 9 桁が基本だが、将来 10 桁になっても弾かないよう幅を持たせる。形式が違う
// ものは upstream に投げる前に落とす (向こうのレート制限を無駄にしない)。
var uidPattern = regexp.MustCompile(`^[1-9][0-9]{8,9}$`)

func routes(ctx plugin.Context, r plugin.Router) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	db := ctx.Storage().DB()
	client := newEnkaClient(set)

	r.POST("/me", func(req plugin.Request) (any, error) {
		me := req.UserID()
		if me == "" {
			return nil, plugin.Errorf(http.StatusUnauthorized, "ログインが必要です")
		}
		var uid string
		var updated *time.Time
		err := db.QueryRowContext(req.Context(),
			`SELECT uid, updated_at FROM accounts WHERE user_id = $1`, me).Scan(&uid, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"uid": nil}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"uid": uid, "updatedAt": updated}, nil
	})

	r.POST("/me/set", func(req plugin.Request) (any, error) {
		me := req.UserID()
		if me == "" {
			return nil, plugin.Errorf(http.StatusUnauthorized, "ログインが必要です")
		}
		var body struct {
			UID string `json:"uid"`
		}
		if err := req.Bind(&body); err != nil {
			return nil, plugin.Errorf(http.StatusBadRequest, "リクエストを読めません")
		}

		// 空文字は登録解除として扱う。UI から消したときに消せないと不便。
		if body.UID == "" {
			if _, err := db.ExecContext(req.Context(), `DELETE FROM accounts WHERE user_id = $1`, me); err != nil {
				return nil, err
			}
			return map[string]any{"uid": nil}, nil
		}
		if !uidPattern.MatchString(body.UID) {
			return nil, plugin.Errorf(http.StatusBadRequest, "UID の形式が正しくありません")
		}

		// **登録時に 1 度だけ取得して存在を確かめる。** 存在しない UID を黙って
		// 保存すると、プロフィールに何も出ない理由が利用者に分からない。
		snap, err := client.fetch(req.Context(), body.UID)
		if err != nil {
			var ue *upstreamError
			if errors.As(err, &ue) && ue.userFacing != "" {
				return nil, plugin.Errorf(ue.status, "%s", ue.userFacing)
			}
			// 上流の一時的な不調で登録を拒むと、直るまで設定できない。
			// 保存だけして、表示は次の更新に任せる。
			ctx.Logger().Warn("登録時の取得に失敗しました (保存は行います)", "err", err)
		} else if err := saveSnapshot(req.Context(), db, snap); err != nil {
			return nil, err
		}

		if _, err := db.ExecContext(req.Context(), `
			INSERT INTO accounts (user_id, uid, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (user_id) DO UPDATE SET uid = EXCLUDED.uid, updated_at = now()
		`, me, body.UID); err != nil {
			return nil, err
		}
		return map[string]any{"uid": body.UID}, nil
	})

	r.POST("/profile", func(req plugin.Request) (any, error) {
		var body struct {
			UserID string `json:"userId"`
		}
		if err := req.Bind(&body); err != nil || body.UserID == "" {
			return nil, plugin.Errorf(http.StatusBadRequest, "userId が必要です")
		}

		// まず自分のところの利用者として引く。
		profile, err := buildProfile(req.Context(), db, client, body.UserID)
		if err != nil {
			return nil, err
		}
		if profile != nil {
			return profile, nil
		}

		// 見つからなければリモート利用者かもしれない。相手のインスタンスに
		// 取り寄せを頼む (mk-go #2537 の peer channel、AP には出ない)。
		return remoteLookup(req.Context(), ctx, db, req.UserID(), body.UserID)
	})

	// 画像プロキシ。本体の CSP は `img-src 'self'` なので、外部の画像を
	// <img> で直接読めない。
	//
	// **ワイルドカードで受ける。** スターレイルのマスターはアイコンを
	// `SpriteOutput/AvatarRoundIcon/1415.png` という階層付きのパスで持つので、
	// 原神版のような単一セグメントでは表せない。
	r.GET("/asset/*", func(req plugin.Request) (any, error) {
		body, err := client.assets.Fetch(req.Context(), req.Param("*"))
		if err != nil {
			var ue *upstreamError
			if errors.As(err, &ue) && ue.status == http.StatusBadRequest {
				return nil, plugin.Errorf(http.StatusBadRequest, "asset のパスが不正です")
			}
			return nil, plugin.ErrNotFound("asset が見つかりません")
		}
		return plugin.Blob{
			ContentType: "image/png",
			Body:        body,
			// 静的アセットなので長めに持たせる。取得元の負荷も減る。
			CacheControl: "public, max-age=86400, immutable",
		}, nil
	})

	return nil
}

func jobs(ctx plugin.Context, j plugin.Jobs) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	db := ctx.Storage().DB()
	client := newEnkaClient(set)

	j.Handle("refresh", func(c context.Context, _ json.RawMessage) error {
		return refreshExpired(c, ctx, db, client)
	})
	j.Schedule("*/10 * * * *", "refresh", nil)
	return nil
}

// refreshExpired re-fetches snapshots whose ttl has run out.
//
// **上流が落ちていても古いデータは消さない。** 取れないせいで表示が空になる
// 方が困る。
func refreshExpired(c context.Context, ctx plugin.Context, db *sql.DB, client *enkaClient) error {
	rows, err := db.QueryContext(c, `
		SELECT DISTINCT a.uid FROM accounts a
		LEFT JOIN snapshots s ON s.uid = a.uid
		WHERE s.uid IS NULL OR s.expires_at <= now()
		LIMIT 50
	`)
	if err != nil {
		return err
	}
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			_ = rows.Close()
			return err
		}
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, uid := range uids {
		snap, err := client.fetch(c, uid)
		if err != nil {
			// 1 件の失敗で全体を止めない。次回の実行で再試行される。
			ctx.Logger().Warn("取得に失敗しました", "uid", uid, "err", err)
			continue
		}
		if err := saveSnapshot(c, db, snap); err != nil {
			ctx.Logger().Warn("保存に失敗しました", "uid", uid, "err", err)
		}
	}
	return nil
}

func saveSnapshot(c context.Context, db *sql.DB, s *snapshot) error {
	characters, err := json.Marshal(s.characters)
	if err != nil {
		return err
	}
	if s.characters == nil {
		characters = []byte("[]")
	}
	_, err = db.ExecContext(c, `
		INSERT INTO snapshots (
			uid, nickname, signature, level, world_level, region, platform,
			friend_count, head_icon, achievements, book_count, avatar_count,
			equipment_count, relic_count, music_count, rogue_score, memory_level,
			characters, fetched_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17, $18, now(), now() + make_interval(secs => $19))
		ON CONFLICT (uid) DO UPDATE SET
			nickname = EXCLUDED.nickname, signature = EXCLUDED.signature,
			level = EXCLUDED.level, world_level = EXCLUDED.world_level,
			region = EXCLUDED.region, platform = EXCLUDED.platform,
			friend_count = EXCLUDED.friend_count, head_icon = EXCLUDED.head_icon,
			achievements = EXCLUDED.achievements, book_count = EXCLUDED.book_count,
			avatar_count = EXCLUDED.avatar_count, equipment_count = EXCLUDED.equipment_count,
			relic_count = EXCLUDED.relic_count, music_count = EXCLUDED.music_count,
			rogue_score = EXCLUDED.rogue_score, memory_level = EXCLUDED.memory_level,
			characters = EXCLUDED.characters,
			fetched_at = EXCLUDED.fetched_at, expires_at = EXCLUDED.expires_at
	`, s.uid, s.nickname, s.signature, s.level, s.worldLevel, s.region, s.platform,
		s.friendCount, s.headIcon, s.achievements, s.bookCount, s.avatarCount,
		s.equipmentCount, s.relicCount, s.musicCount, s.rogueScore, s.memoryLevel,
		characters, s.ttl)
	return err
}

// --- Enka.Network ---

type snapshot struct {
	uid            string
	nickname       string
	signature      string
	level          int
	worldLevel     int
	region         string
	platform       string
	friendCount    int
	headIcon       int
	achievements   int
	bookCount      int
	avatarCount    int
	equipmentCount int
	relicCount     int
	musicCount     int
	rogueScore     int
	// memoryLevel is the furthest 忘却の庭 floor.
	memoryLevel int
	characters  []character
	ttl         int
}

// rawProp is one resolved stat value.
type rawProp struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// rawFlat is Enka's pre-resolved view of an item.
//
// **キー名がアンダースコア始まり。** 原神は `flat` だが、スターレイルは
// `_flat` で来る。
type rawFlat struct {
	Props []rawProp `json:"props"`
	// Name is the localisation key of a light cone.
	Name string `json:"name"`
	// SetName is the localisation key of a relic set.
	SetName string `json:"setName"`
	SetID   int    `json:"setID"`
}

type rawEquipment struct {
	TID int `json:"tid"`
	// Rank is the superimposition level (重畳)。
	Rank      int     `json:"rank"`
	Level     int     `json:"level"`
	Promotion int     `json:"promotion"`
	Flat      rawFlat `json:"_flat"`
}

type rawSubAffix struct {
	AffixID int `json:"affixId"`
	// Cnt is how many times the substat rolled.
	Cnt int `json:"cnt"`
	// Step is the roll quality.
	Step int `json:"step"`
}

type rawRelic struct {
	TID          int           `json:"tid"`
	Type         int           `json:"type"`
	Level        int           `json:"level"`
	MainAffixID  int           `json:"mainAffixId"`
	SubAffixList []rawSubAffix `json:"subAffixList"`
	Flat         rawFlat       `json:"_flat"`
}

type rawSkillTreePoint struct {
	PointID int `json:"pointId"`
	Level   int `json:"level"`
}

type rawAvatar struct {
	AvatarID int `json:"avatarId"`
	Level    int `json:"level"`
	// Promotion is the ascension step.
	Promotion int `json:"promotion"`
	// Rank is the eidolon level (星魂)。
	Rank          int                 `json:"rank"`
	Equipment     *rawEquipment       `json:"equipment"`
	RelicList     []rawRelic          `json:"relicList"`
	SkillTreeList []rawSkillTreePoint `json:"skillTreeList"`
	// Assist marks the character the player set as their support unit.
	Assist bool `json:"_assist"`
}

type upstreamError struct {
	status int
	// userFacing is non-empty when the failure is the user's fault and should
	// be shown to them (invalid UID / no such player).
	userFacing string
	msg        string
}

func (e *upstreamError) Error() string { return e.msg }

type enkaClient struct {
	set     settings
	http    *http.Client
	masters *masters
	assets  *assetFetcher
}

func newEnkaClient(set settings) *enkaClient {
	hc := &http.Client{Timeout: time.Duration(set.TimeoutSeconds) * time.Second}
	base := set.MasterEndpoint
	if base == "" {
		base = masterBase
	}
	return &enkaClient{
		set: set, http: hc,
		masters: newMastersAt(hc, set.Language, base),
		assets:  newAssetFetcher(hc, set.UserAgent, set.Endpoint),
	}
}

// maxShowcase bounds how many characters we keep.
//
// ショーケースは 5 体 (原神の 8 体より少ない)。取得元が想定外の数を返しても
// 保存が膨らまないよう、ここでも切る。
const maxShowcase = 8

// fetch retrieves the full profile for a UID.
//
// **末尾にスラッシュを付けないこと。** 原神側は `/api/uid/<uid>/` だが、
// スターレイルは付けると 308 で飛ばされる。揃えようとすると片方が壊れる。
func (c *enkaClient) fetch(ctx context.Context, uid string) (*snapshot, error) {
	url := c.set.Endpoint + "/api/hsr/uid/" + uid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.set.UserAgent)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enka への接続に失敗しました: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		return nil, &upstreamError{status: http.StatusBadRequest,
			userFacing: "UID の形式が正しくありません", msg: "enka: 400"}
	case http.StatusNotFound:
		return nil, &upstreamError{status: http.StatusNotFound,
			userFacing: "その UID のプレイヤーが見つかりません", msg: "enka: 404"}
	default:
		// 429 (レート制限) / 424 (ゲーム側に届かない) / 5xx。いずれも
		// こちらの都合ではないので、利用者には見せずキャッシュで凌ぐ。
		return nil, &upstreamError{status: res.StatusCode,
			msg: fmt.Sprintf("enka: status %d", res.StatusCode)}
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		DetailInfo struct {
			UID        int    `json:"uid"`
			Nickname   string `json:"nickname"`
			Signature  string `json:"signature"`
			Level      int    `json:"level"`
			WorldLevel int    `json:"worldLevel"`
			// Platform is PC / IOS / ANDROID / ...
			Platform    string `json:"platform"`
			FriendCount int    `json:"friendCount"`
			HeadIcon    int    `json:"headIcon"`
			RecordInfo  struct {
				AchievementCount       int `json:"achievementCount"`
				BookCount              int `json:"bookCount"`
				AvatarCount            int `json:"avatarCount"`
				EquipmentCount         int `json:"equipmentCount"`
				MusicCount             int `json:"musicCount"`
				RelicCount             int `json:"relicCount"`
				MaxRogueChallengeScore int `json:"maxRogueChallengeScore"`
				// ChallengeInfo carries 忘却の庭 progress.
				//
				// **形が確かめられていない。** 手元の実データでは空だったので、
				// 既知のキーだけを拾う best-effort にしてある。
				ChallengeInfo map[string]int `json:"challengeInfo"`
			} `json:"recordInfo"`
			AvatarDetailList []rawAvatar `json:"avatarDetailList"`
		} `json:"detailInfo"`
		Region string `json:"region"`
		TTL    int    `json:"ttl"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("enka の応答を解釈できません: %w", err)
	}

	ttl := parsed.TTL
	if ttl <= 0 {
		// ttl が無い応答でも、間を置かずに再取得しない。
		ttl = 300
	}
	di := parsed.DetailInfo
	rec := di.RecordInfo
	snap := &snapshot{
		uid: uid, nickname: di.Nickname, signature: di.Signature,
		level: di.Level, worldLevel: di.WorldLevel, region: parsed.Region,
		platform: di.Platform, friendCount: di.FriendCount, headIcon: di.HeadIcon,
		achievements: rec.AchievementCount, bookCount: rec.BookCount,
		avatarCount: rec.AvatarCount, equipmentCount: rec.EquipmentCount,
		relicCount: rec.RelicCount, musicCount: rec.MusicCount,
		rogueScore:  rec.MaxRogueChallengeScore,
		memoryLevel: rec.ChallengeInfo["scheduleMaxLevel"],
		ttl:         ttl,
		characters:  make([]character, 0, len(di.AvatarDetailList)),
	}

	for i, a := range di.AvatarDetailList {
		if i >= maxShowcase {
			break
		}
		snap.characters = append(snap.characters, c.buildCharacter(ctx, a))
	}
	return snap, nil
}
