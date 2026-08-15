package hsr

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 受け取った文字列がそのまま取得先 URL の一部になる。**通してはいけない形**を
// 明示的に並べる。
func TestValidAssetPath(t *testing.T) {
	ok := []string{
		"SpriteOutput/AvatarRoundIcon/1415.png",
		"SpriteOutput/ItemIcon/RelicIcons/IconRelic_101_1.png",
		"a.png",
	}
	for _, p := range ok {
		if !validAssetPath(p) {
			t.Errorf("通るべきパスを弾いた: %q", p)
		}
	}

	ng := []string{
		"",                                // 空
		"../../etc/passwd.png",            // 親を辿る
		"a/../b.png",                      // 途中で辿る
		"/ui/hsr/x.png",                   // 先頭スラッシュ (//host に化ける)
		"//evil.example/x.png",            // 別ホスト
		"a//b.png",                        // 空セグメント
		"x.jpg",                           // 拡張子違い
		"x.png?a=1",                       // クエリ
		"x%2e%2e/y.png",                   // エンコードされた相対参照
		".hidden.png",                     // ドット始まり
		"a/b/c/d/e/f/g.png",               // 深すぎる
		strings.Repeat("a", 200) + ".png", // 長すぎる
	}
	for _, p := range ng {
		if validAssetPath(p) {
			t.Errorf("弾くべきパスを通した: %q", p)
		}
	}
}

func TestAssetURL(t *testing.T) {
	got := assetURL("/ui/hsr/SpriteOutput/AvatarRoundIcon/1415.png")
	if got != "/api/plugin/hsr/asset/SpriteOutput/AvatarRoundIcon/1415.png" {
		t.Errorf("proxy URL: %q", got)
	}
	// 前置きが違うものは表示しない (組み替えられないので)。
	if assetURL("/ui/gi/x.png") != "" {
		t.Error("前置きの違うパスを通している")
	}
	if assetURL("") != "" {
		t.Error("空を通している")
	}
}

// 縮小は光円錐のフルアート (1.8MB) 対策。小さいものまで再エンコードすると
// 画質を落とすだけになる。
func TestShrink(t *testing.T) {
	big := pngBytes(t, 800, 1200)
	small := shrink(big)
	if len(small) >= len(big) {
		t.Error("大きい画像が縮んでいない")
	}
	img, err := png.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() > maxAssetWidth || img.Bounds().Dy() > maxAssetWidth {
		t.Errorf("縮小後も大きい: %v", img.Bounds())
	}

	tiny := pngBytes(t, 64, 64)
	if got := shrink(tiny); !bytes.Equal(got, tiny) {
		t.Error("十分小さい画像を作り直している")
	}

	// 画像として読めないものは触らずに返す (縮められないことは表示できない
	// 理由にならない)。
	junk := []byte("not an image")
	if got := shrink(junk); !bytes.Equal(got, junk) {
		t.Error("読めない入力を書き換えている")
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAssetFetcher_FetchAndCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui/hsr/SpriteOutput/AvatarRoundIcon/1415.png" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 64, 64))
	}))
	defer srv.Close()

	f := newAssetFetcher(&http.Client{Timeout: 5 * time.Second}, "test/1.0", srv.URL)
	ctx := context.Background()

	body, err := f.Fetch(ctx, "SpriteOutput/AvatarRoundIcon/1415.png")
	if err != nil || len(body) == 0 {
		t.Fatalf("取得に失敗: %v", err)
	}
	// 2 回目は取りに行かない。1.8MB の取得とデコードを繰り返さないため。
	if _, err := f.Fetch(ctx, "SpriteOutput/AvatarRoundIcon/1415.png"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("取得回数: %d (キャッシュが効いていない)", hits)
	}

	if _, err := f.Fetch(ctx, "SpriteOutput/Nope.png"); err == nil {
		t.Error("404 をエラーにしていない")
	}
	if _, err := f.Fetch(ctx, "../escape.png"); err == nil {
		t.Error("不正なパスを取りに行っている")
	}
}

// 取得元が画像以外を返したら捨てる。Content-Type をそのまま流さない。
func TestAssetFetcher_RejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer srv.Close()

	f := newAssetFetcher(&http.Client{Timeout: 5 * time.Second}, "test/1.0", srv.URL)
	if _, err := f.Fetch(context.Background(), "x.png"); err == nil {
		t.Error("画像でない応答を通している")
	}
}

func TestAssetCache_Evicts(t *testing.T) {
	c := newAssetCache(10)
	c.put("a", []byte("12345"))
	c.put("b", []byte("12345"))
	if _, ok := c.get("a"); !ok {
		t.Fatal("入れたものが取れない")
	}

	// 上限を超えたら古い方から捨てる。
	c.put("c", []byte("12345"))
	if _, ok := c.get("a"); ok {
		t.Error("古いものが残っている")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("新しいものが入っていない")
	}

	// 上限より大きいものは持たない (1 つで全部追い出してしまう)。
	c.put("huge", make([]byte, 99))
	if _, ok := c.get("huge"); ok {
		t.Error("上限を超えるものを抱えている")
	}

	var nilCache *assetCache
	nilCache.put("x", []byte("y"))
	if _, ok := nilCache.get("x"); ok {
		t.Error("nil キャッシュが値を返している")
	}
}
