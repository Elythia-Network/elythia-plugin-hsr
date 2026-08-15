package hsr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/kovidgoyal/imaging"
)

/*
 * Enka の画像を同一オリジンで中継する。
 *
 * mk-go の CSP は `img-src 'self'` なので enka.network の画像を <img> で直接
 * 読めない。CSP を緩めると本体全体に効いてしまうので、プラグインが配信する。
 *
 * # 原神版との違い
 *
 * 原神は `UI_AvatarIcon_Ambor` のような**名前**が来るので、英数字だけ通せば
 * 済んだ。スターレイルのマスターは `/ui/hsr/SpriteOutput/AvatarRoundIcon/1415.png`
 * という**パス**を持つので、階層を許さざるを得ない。その分だけ検証を厳しくする。
 *
 * # 縮小する理由
 *
 * 光円錐の画像は `LightConeFigures` (フルアート) しか公開されておらず、1 枚で
 * **1.8MB** ある。小さい版を探したが存在しない (ItemIcon / LightConeMediumIcon
 * などは 404)。5 体分並べると 9MB になり、モバイルでは無視できない。
 */

// assetBase is where Enka serves the Star Rail UI images.
//
// マスターが持つパスは `/ui/hsr/...` から始まるので、その手前までを持つ。
const assetBase = "https://enka.network"

// assetPathPrefix is the only place we will fetch from.
//
// **ここを固定するのが SSRF 対策の主軸。** 受け取った文字列をそのまま URL に
// 混ぜない。
const assetPathPrefix = "/ui/hsr/"

// maxAssetBytes bounds one download. フルアートが 1.8MB、キャラのカットインは
// 3.5MB あるので、原神版 (4MB) より広く取る。
const maxAssetBytes = 8 << 20

// maxAssetWidth is the width we shrink to.
//
// 表示に使うのは 40-120px 程度。倍解像度の端末を考えても 360 あれば足りる。
const maxAssetWidth = 360

// maxAssetDepth bounds how deep a path may go.
//
// 実際に使うのは `SpriteOutput/ItemIcon/RelicIcons/x.png` の 4 段まで。
const maxAssetDepth = 6

// assetCacheLimit bounds what the process keeps.
//
// 縮小後は 1 枚 20-60KB なので、これで数百枚入る。
const assetCacheLimit = 32 << 20

// validAssetPath reports whether a requested path is safe to fetch.
//
// **リクエストの文字列が取得先 URL の一部になる。** `../` で `/ui/hsr/` の外に
// 出たり、別ホストへ逃げたりできないよう、通す形を絞る。
func validAssetPath(p string) bool {
	if p == "" || len(p) > 160 {
		return false
	}
	// 拡張子を固定する。Enka の UI 画像はすべて PNG で、他を通す理由が無い。
	if !strings.HasSuffix(p, ".png") {
		return false
	}
	// 先頭のスラッシュは許さない (`//host` と書かれると別ホストに化ける)。
	if strings.HasPrefix(p, "/") {
		return false
	}
	segs := strings.Split(p, "/")
	if len(segs) > maxAssetDepth {
		return false
	}
	for _, seg := range segs {
		// 空セグメント (`a//b`) と相対参照を弾く。`.` 始まりもまとめて落とす。
		if seg == "" || strings.HasPrefix(seg, ".") {
			return false
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '_' || r == '-' || r == '.':
			default:
				return false
			}
		}
	}
	return true
}

// assetCache keeps shrunken images in the process.
//
// **LRU ではなく投入順に捨てる。** 追い出しの精度より、取得と縮小をやり直さない
// ことの方が効く (1.8MB の取得 + デコードが毎リクエスト走るのを避けたい)。
type assetCache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
	bytes int
	limit int
}

func newAssetCache(limit int) *assetCache {
	return &assetCache{items: map[string][]byte{}, limit: limit}
}

func (c *assetCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *assetCache) put(key string, body []byte) {
	if c == nil || len(body) > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; exists {
		return
	}
	for c.bytes+len(body) > c.limit && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.items[oldest])
		delete(c.items, oldest)
	}
	c.items[key] = body
	c.order = append(c.order, key)
	c.bytes += len(body)
}

// assetFetcher downloads and shrinks Enka's UI images.
type assetFetcher struct {
	client    *http.Client
	userAgent string
	// endpoint is the origin to fetch from. テストで httptest へ向けられるように
	// 設定にしてある。
	endpoint string
	cache    *assetCache
}

func newAssetFetcher(client *http.Client, userAgent, endpoint string) *assetFetcher {
	return &assetFetcher{
		client: client, userAgent: userAgent, endpoint: endpoint,
		cache: newAssetCache(assetCacheLimit),
	}
}

// Fetch returns the (possibly shrunken) PNG for a path under /ui/hsr/.
func (f *assetFetcher) Fetch(ctx context.Context, path string) ([]byte, error) {
	if !validAssetPath(path) {
		return nil, &upstreamError{status: http.StatusBadRequest, msg: "asset のパスが不正です"}
	}
	if body, ok := f.cache.get(path); ok {
		return body, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint+assetPathPrefix+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	res, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て
	if res.StatusCode != http.StatusOK {
		return nil, &upstreamError{status: res.StatusCode, msg: fmt.Sprintf("asset: status %d", res.StatusCode)}
	}
	// **取得元の Content-Type をそのまま流さない。** 扱うのは PNG だけと決めて
	// いるので、応答が画像を名乗らなければそこで捨てる。
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, &upstreamError{status: http.StatusBadGateway, msg: "画像ではない応答が返りました"}
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxAssetBytes))
	if err != nil {
		return nil, err
	}

	body = shrink(body)
	f.cache.put(path, body)
	return body, nil
}

// shrink scales an image down to maxAssetWidth.
//
// 失敗したら元のまま返す。**縮められないことは表示できない理由にならない**
// (帯域を使うだけで、絵としては正しい)。
func shrink(body []byte) []byte {
	img, err := imaging.Decode(bytes.NewReader(body))
	if err != nil {
		return body
	}
	b := img.Bounds()
	if b.Dx() <= maxAssetWidth && b.Dy() <= maxAssetWidth {
		// 十分小さいものを再エンコードしない。画質を落とすだけ無駄。
		return body
	}

	// Fit は縦横比を保ったまま枠に収める。切り抜かないので絵柄が欠けない。
	small := imaging.Fit(img, maxAssetWidth, maxAssetWidth, imaging.Lanczos)
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, small, imaging.PNG); err != nil {
		return body
	}
	return buf.Bytes()
}

// assetURL builds the same-origin proxy URL for a master-data image path.
//
// マスターが持つのは `/ui/hsr/SpriteOutput/...`。**前置きを剥がして**こちらの
// 名前空間に載せ替える (剥がせない形のものは表示しない)。
func assetURL(masterPath string) string {
	rel, ok := strings.CutPrefix(masterPath, assetPathPrefix)
	if !ok || !validAssetPath(rel) {
		return ""
	}
	return assetRoutePrefix + rel
}
