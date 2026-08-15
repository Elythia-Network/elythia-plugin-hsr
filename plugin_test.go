package hsr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeEnka serves a canned Enka.Network response.
func fakeEnka(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent を送っていない (enka が明示的に求めている)")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// **末尾にスラッシュを付けると 308 で飛ばされる。** 原神側は逆に付ける形なので、
// 揃えようとすると片方が壊れる。
func TestFetch_URLHasNoTrailingSlash(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(`{"detailInfo":{},"ttl":60}`))
	}))
	defer srv.Close()

	if _, err := testClient(t, srv.URL).fetch(context.Background(), "800000000"); err != nil {
		t.Fatal(err)
	}
	if got != "/api/hsr/uid/800000000" {
		t.Fatalf("取得先のパス: %q", got)
	}
}

func TestFetch_ParsesDetailInfo(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))

	got, err := testClient(t, srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if got.nickname != "開拓者" || got.level != 70 || got.worldLevel != 6 {
		t.Fatalf("プレイヤー情報: %+v", got)
	}
	if got.region != "ASIA" || got.platform != "PC" {
		t.Errorf("地域 / 環境: %q / %q", got.region, got.platform)
	}
	if got.achievements != 864 || got.relicCount != 945 || got.rogueScore != 9 {
		t.Errorf("戦績: 実績%d 遺物%d 模擬宇宙%d", got.achievements, got.relicCount, got.rogueScore)
	}
	if got.headIcon == 0 {
		t.Error("プロフィールアイコンが拾えていない")
	}
	if len(got.characters) != 1 {
		t.Fatalf("キャラの数: %d", len(got.characters))
	}
	if got.characters[0].Name != "キュレネ" {
		t.Errorf("キャラ名: %q", got.characters[0].Name)
	}
}

// ttl が無い応答でも、間を置かずに再取得しないこと。
func TestFetch_DefaultsTTL(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, []byte(`{"detailInfo":{"nickname":"x"}}`))

	got, err := testClient(t, srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if got.ttl <= 0 {
		t.Fatalf("ttl が既定値にならない: %d", got.ttl)
	}
}

// **利用者が直せるものだけ見せる。** 400/404 は UID の問題なので伝える。
func TestFetch_UserFacingErrors(t *testing.T) {
	for _, tt := range []struct {
		status int
		shown  bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusNotFound, true},
		{http.StatusTooManyRequests, false},
		{http.StatusFailedDependency, false},
		{http.StatusInternalServerError, false},
	} {
		srv := fakeEnka(t, tt.status, []byte(`{}`))
		_, err := testClient(t, srv.URL).fetch(context.Background(), "800000000")
		if err == nil {
			t.Fatalf("status %d でエラーにならない", tt.status)
		}
		ue, ok := err.(*upstreamError)
		if !ok {
			t.Fatalf("status %d: 型が違う (%T)", tt.status, err)
		}
		if (ue.userFacing != "") != tt.shown {
			t.Errorf("status %d: 利用者に見せる=%v", tt.status, ue.userFacing != "")
		}
	}
}

func TestFetch_BrokenBody(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, []byte(`{not json`))
	if _, err := testClient(t, srv.URL).fetch(context.Background(), "800000000"); err == nil {
		t.Fatal("壊れた応答を通している")
	}
}

// ショーケースが想定より多くても保存が膨らまないこと。
func TestFetch_CapsShowcase(t *testing.T) {
	body := []byte(`{"detailInfo":{"avatarDetailList":[` +
		`{"avatarId":1},{"avatarId":2},{"avatarId":3},{"avatarId":4},{"avatarId":5},` +
		`{"avatarId":6},{"avatarId":7},{"avatarId":8},{"avatarId":9},{"avatarId":10}]},"ttl":60}`)
	srv := fakeEnka(t, http.StatusOK, body)

	got, err := testClient(t, srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.characters) > maxShowcase {
		t.Fatalf("上限を超えて保持している: %d", len(got.characters))
	}
}

func TestUIDPattern(t *testing.T) {
	ok := []string{"800000000", "600000000", "1234567890"}
	for _, s := range ok {
		if !uidPattern.MatchString(s) {
			t.Errorf("通るべき UID を弾いた: %q", s)
		}
	}
	ng := []string{"", "abc", "0123456789", "12345678", "12345678901", "83464327 "}
	for _, s := range ng {
		if uidPattern.MatchString(s) {
			t.Errorf("弾くべき UID を通した: %q", s)
		}
	}
}
