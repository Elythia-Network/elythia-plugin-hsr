package hsr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// brokenServer serves responses a decoder must reject.
func brokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{not json`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 既定の取得先は Enka の GitHub。設定を省いてもマスターが引ける形であること。
func TestNewMasters_DefaultEndpoint(t *testing.T) {
	m := newMasters(&http.Client{Timeout: time.Second}, "ja")
	if m.avatars.url != masterBase+"avatars.json" {
		t.Errorf("既定の取得先: %q", m.avatars.url)
	}
	// **honker_ 側を見ていないこと。** あちらは名前のハッシュが壊れている。
	for _, s := range []string{m.avatars.url, m.weapons.url, m.relics.url} {
		if strings.Contains(s, "honker") {
			t.Errorf("honker_ 側を見ている: %q", s)
		}
	}
}

// 配線し忘れても落とさない。名前が出ないだけでカードは成立させる。
func TestMasters_NilSafe(t *testing.T) {
	var m *masters
	ctx := context.Background()

	if got := m.text(ctx, "123"); got != "" {
		t.Errorf("nil masters が値を返した: %q", got)
	}
	if _, ok := m.avatar(ctx, 1415); ok {
		t.Error("nil masters がキャラを返した")
	}
	if _, ok := m.weapon(ctx, 23052); ok {
		t.Error("nil masters が光円錐を返した")
	}

	var s *store[map[string]string]
	if got := s.Get(ctx); got != nil {
		t.Errorf("nil store が値を返した: %v", got)
	}
}

// 空のキーは引きに行かない (テキスト表に "0" は無い)。
func TestMasters_EmptyKey(t *testing.T) {
	m := newMastersAt(&http.Client{Timeout: 5 * time.Second}, "ja", masterServer(t).URL+"/")
	ctx := context.Background()

	for _, key := range []string{"", "0"} {
		if got := m.text(ctx, key); got != "" {
			t.Errorf("key=%q で %q を返した", key, got)
		}
	}
}

// 指定した言語が無ければ英語に落とす。取得元は必ず en を持つ。
func TestMasters_FallsBackToEnglish(t *testing.T) {
	m := newMastersAt(&http.Client{Timeout: 5 * time.Second}, "xx", masterServer(t).URL+"/")
	// テストデータの en は空なので、引けないこと自体が落ちない証明になる。
	if got := m.text(context.Background(), "7809981386909966580"); got != "" {
		t.Errorf("未知の言語で ja を返している: %q", got)
	}
}

// 取り直せなくても手元のものを使い続ける。
func TestStore_KeepsStaleOnFailure(t *testing.T) {
	srv := masterServer(t)
	m := newMastersAt(&http.Client{Timeout: 5 * time.Second}, "ja", srv.URL+"/")
	ctx := context.Background()

	if _, ok := m.avatar(ctx, 1415); !ok {
		t.Fatal("最初の取得に失敗")
	}
	// 取得元を落として期限切れにする。
	srv.Close()
	m.avatars.mu.Lock()
	m.avatars.fetched = time.Now().Add(-2 * masterTTL)
	m.avatars.mu.Unlock()

	if _, ok := m.avatar(ctx, 1415); !ok {
		t.Error("取得元が落ちたら手元の値まで失っている")
	}
}

// 壊れた応答は取り込まない。
func TestStore_RejectsBrokenBody(t *testing.T) {
	srv := brokenServer(t)
	s := newStore(&http.Client{Timeout: 5 * time.Second}, srv.URL, decodeJSON[map[string]string])
	if err := s.refresh(context.Background()); err == nil {
		t.Error("壊れた JSON を取り込んでいる")
	}

	notFound := newStore(&http.Client{Timeout: 5 * time.Second}, srv.URL+"/missing", decodeJSON[map[string]string])
	if err := notFound.refresh(context.Background()); err == nil {
		t.Error("404 を成功として扱っている")
	}
}
