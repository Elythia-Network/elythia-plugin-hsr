package hsr

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

/*
 * テスト用のマスターと応答は、実際の Enka の応答から 1 体分を抜き出したもの。
 * **手で書いた値ではない**ので、取得元の形が変わればここで落ちる。
 *
 * UID とニックネームだけは架空のものに差し替えてある (個人のアカウントを
 * 特定できる値を公開リポジトリの履歴に残さないため)。装備やステータスは
 * 実物なので、計算の検証はそのまま成り立つ。
 */

//go:embed testdata
var testdataFS embed.FS

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := testdataFS.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// masterServer serves the canned master files.
func masterServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, name := range []string{"avatars", "weapons", "relics", "tree", "ranks", "skills", "pfps", "hsr"} {
		body := testdata(t, name)
		mux.HandleFunc("/"+name+".json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testClient(t *testing.T, enkaURL string) *enkaClient {
	t.Helper()
	m := masterServer(t)
	hc := &http.Client{Timeout: 5 * time.Second}
	set := settings{Endpoint: enkaURL, UserAgent: "test/1.0", TimeoutSeconds: 5, Language: "ja"}
	return &enkaClient{
		set:     set,
		http:    hc,
		masters: newMastersAt(hc, "ja", m.URL+"/"),
		assets:  newAssetFetcher(hc, set.UserAgent, enkaURL),
	}
}

// sampleAvatar returns the one character in the canned response.
func sampleAvatar(t *testing.T) rawAvatar {
	t.Helper()
	var parsed struct {
		DetailInfo struct {
			AvatarDetailList []rawAvatar `json:"avatarDetailList"`
		} `json:"detailInfo"`
	}
	if err := json.Unmarshal(testdata(t, "uid"), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.DetailInfo.AvatarDetailList) == 0 {
		t.Fatal("テストデータにキャラが入っていない")
	}
	return parsed.DetailInfo.AvatarDetailList[0]
}

func buildSample(t *testing.T) character {
	t.Helper()
	return testClient(t, "").buildCharacter(context.Background(), sampleAvatar(t))
}

func TestBuildCharacter_Basics(t *testing.T) {
	c := buildSample(t)

	if c.Name != "キュレネ" {
		t.Errorf("キャラ名が解決できていない: %q", c.Name)
	}
	if c.Element != "Ice" || c.Path != "Memory" {
		t.Errorf("属性 / 運命: %q / %q", c.Element, c.Path)
	}
	if c.Level != 80 || c.Promotion != 6 || c.Rank != 4 {
		t.Errorf("レベル / 昇格 / 星魂: %d / %d / %d", c.Level, c.Promotion, c.Rank)
	}
	if c.Rarity != 5 {
		t.Errorf("レアリティ: %d", c.Rarity)
	}
	if !c.Assist {
		t.Error("_assist が拾えていない")
	}
	if c.Icon != "/api/plugin/hsr/asset/SpriteOutput/AvatarRoundIcon/1415.png" {
		t.Errorf("アイコンが proxy 経由でない: %q", c.Icon)
	}
}

// honker_ 側のマスターは Hash が丸められていて名前を引けない。接頭辞の無い
// ファイルを使っていることを、実データの Hash で確かめる。
func TestMasters_NameHashIsNotTruncated(t *testing.T) {
	m := newMastersAt(&http.Client{Timeout: 5 * time.Second}, "ja", masterServer(t).URL+"/")
	info, ok := m.avatar(context.Background(), 1415)
	if !ok {
		t.Fatal("キャラのマスターが引けない")
	}
	if info.AvatarName.Hash != "7809981386909966580" {
		t.Fatalf("Hash が壊れている (honker_ 側を見ている?): %q", info.AvatarName.Hash)
	}
}

// 最終ステータスは Enka が返さないので自前で足す。**メインとサブで名前が
// 割れている**のを畳まないと、遺物のメイン分が丸ごと落ちる。
func TestBuildCharacter_FinalStats(t *testing.T) {
	c := buildSample(t)

	got := map[string]float64{}
	percent := map[string]bool{}
	for _, s := range c.Stats {
		got[s.Label] = s.Value
		percent[s.Label] = s.Percent
	}

	want := map[string]float64{
		"HP":      5359,
		"攻撃力":     1351,
		"防御力":     1286,
		"速度":      180.8,
		"会心率":     51.8,
		"会心ダメージ":  193.6,
		"効果抵抗":    15.6,
		"効果命中":    11.2,
		"氷属性ダメージ": 38.9,
	}
	for label, v := range want {
		if got[label] != v {
			t.Errorf("%s: %v (期待 %v)", label, got[label], v)
		}
	}
	if !percent["会心率"] || percent["HP"] {
		t.Error("割合とそうでないものの区別がついていない")
	}
	// 撃破特効は 0 なので出さない。並べても情報が無い。
	if _, ok := got["撃破特効"]; ok {
		t.Error("0 のステータスを出している")
	}
}

// 光円錐の重畳効果 (常時発動分) を足し忘れると速度が 18% 低く出る。
func TestCollectProps_IncludesLightConeSkill(t *testing.T) {
	c := testClient(t, "")
	ctx := context.Background()
	a := sampleAvatar(t)

	withCone := collectProps(ctx, c.masters, a)
	a.Equipment = nil
	without := collectProps(ctx, c.masters, a)

	if withCone["SpeedAddedRatio"] == 0 {
		t.Fatal("光円錐の常時効果が入っていない")
	}
	if withCone["HPBase"] <= without["HPBase"] {
		t.Error("光円錐の基礎値が入っていない")
	}
}

func TestBuildCharacter_LightCone(t *testing.T) {
	c := buildSample(t)
	if c.LightCone == nil {
		t.Fatal("光円錐が空")
	}
	lc := c.LightCone
	if lc.Name != "愛はいま永遠に" {
		t.Errorf("光円錐名: %q", lc.Name)
	}
	if lc.Level != 80 || lc.Rank != 1 || lc.Rarity != 5 {
		t.Errorf("Lv / 重畳 / レア: %d / %d / %d", lc.Level, lc.Rank, lc.Rarity)
	}
	if len(lc.Stats) != 3 {
		t.Errorf("基礎値の数: %d", len(lc.Stats))
	}
	if lc.Icon == "" {
		t.Error("アイコンが空")
	}
}

// `_flat.props` は先頭がメイン、残りがサブ。並びを取り違えるとメインが
// サブに化ける。
func TestBuildCharacter_RelicMainIsFirst(t *testing.T) {
	c := buildSample(t)
	if len(c.Relics) != 6 {
		t.Fatalf("遺物の数: %d (6 部位あるはず)", len(c.Relics))
	}
	head := c.Relics[0]
	if head.Slot != "HEAD" {
		t.Errorf("部位: %q", head.Slot)
	}
	if head.Main.Label != "HP" {
		t.Errorf("頭のメインは HP 固定のはず: %q", head.Main.Label)
	}
	if len(head.Subs) != 4 {
		t.Errorf("サブの数: %d", len(head.Subs))
	}
	if head.SetName != "天地再創の救世主" {
		t.Errorf("セット名: %q", head.SetName)
	}
	if head.Level != 15 {
		t.Errorf("強化度: %d", head.Level)
	}
	// 伸びた回数が拾えていること (原神には無い情報)。
	found := false
	for _, s := range head.Subs {
		if s.Count > 1 {
			found = true
		}
	}
	if !found {
		t.Error("サブの上昇回数が拾えていない")
	}
}

// 割合のサブは 0-1 の小数で来る。100 倍を忘れると「効果抵抗 0.1%」になる。
func TestBuildCharacter_RelicPercentScaled(t *testing.T) {
	c := buildSample(t)
	for _, r := range c.Relics {
		for _, s := range append([]relicSub{{Stat: r.Main}}, r.Subs...) {
			if s.Percent && s.Value > 0 && s.Value < 1 {
				t.Errorf("%s が小数のまま: %v", s.Label, s.Value)
			}
		}
	}
}

func TestBuildCharacter_Traces(t *testing.T) {
	c := buildSample(t)

	// 主要スキル 5 つ + 憶霊 2 つ。
	if len(c.Skills) != 7 {
		t.Fatalf("スキルの数: %d", len(c.Skills))
	}
	if c.Skills[0].Kind != "normal" || c.Skills[2].Kind != "ultra" {
		t.Errorf("並びが違う: %q / %q", c.Skills[0].Kind, c.Skills[2].Kind)
	}
	if c.Skills[0].Level != 6 || c.Skills[1].Level != 10 {
		t.Errorf("スキルレベル: %d / %d", c.Skills[0].Level, c.Skills[1].Level)
	}
	if c.Skills[len(c.Skills)-1].Kind != "servant" {
		t.Error("憶霊スキルが並んでいない")
	}

	if len(c.Branches) != 4 {
		t.Fatalf("枝の数: %d", len(c.Branches))
	}
	// 枝の先頭は大パッシブ、残りはステータス強化。
	var passives int
	for _, b := range c.Branches {
		for _, n := range b.Nodes {
			if n.Kind == "passive" {
				passives++
			}
			if !n.Unlocked {
				t.Errorf("全解放のはずのノードが未解放: %d", n.ID)
			}
		}
	}
	if passives != 3 {
		t.Errorf("大パッシブの数: %d", passives)
	}
}

// 取っていないノードは応答に来ない。マスター側の定義と突き合わせて初めて
// 「未解放」と分かる。
func TestBuildCharacter_LockedTraces(t *testing.T) {
	a := sampleAvatar(t)
	a.SkillTreeList = a.SkillTreeList[:2]
	c := testClient(t, "").buildCharacter(context.Background(), a)

	var locked int
	for _, b := range c.Branches {
		for _, n := range b.Nodes {
			if !n.Unlocked {
				locked++
			}
		}
	}
	if locked == 0 {
		t.Error("未解放のノードが出ていない")
	}
}

func TestBuildCharacter_Eidolons(t *testing.T) {
	c := buildSample(t)
	if len(c.Eidolons) != 6 {
		t.Fatalf("星魂の枠: %d", len(c.Eidolons))
	}
	for i, e := range c.Eidolons {
		want := i < 4
		if e.Unlocked != want {
			t.Errorf("星魂 %d の解放状態: %v", i+1, e.Unlocked)
		}
		if e.Icon == "" {
			t.Errorf("星魂 %d のアイコンが空", i+1)
		}
	}
}

// マスターが引けない状況でも、カード自体は成立させる。
func TestBuildCharacter_MissingMasters(t *testing.T) {
	hc := &http.Client{Timeout: time.Second}
	c := &enkaClient{
		set:     settings{Language: "ja"},
		http:    hc,
		masters: newMastersAt(hc, "ja", "http://127.0.0.1:1/"),
		assets:  newAssetFetcher(hc, "test/1.0", ""),
	}
	got := c.buildCharacter(context.Background(), sampleAvatar(t))

	if got.AvatarID != 1415 || got.Level != 80 {
		t.Error("応答由来の値まで落ちている")
	}
	if len(got.Relics) != 6 {
		t.Error("遺物は応答だけで組めるはず")
	}
}

func TestNormalizeProp(t *testing.T) {
	// メインとサブで割れている分だけを畳む。
	if normalizeProp("CriticalDamageBase") != "CriticalDamage" {
		t.Error("Base 付きが畳まれていない")
	}
	// 基礎値は別物なので触らない。
	if normalizeProp("HPBase") != "HPBase" {
		t.Error("基礎値を畳んでしまっている")
	}
	if normalizeProp("SpeedDelta") != "SpeedDelta" {
		t.Error("関係ないキーを変えている")
	}
}

func TestRound(t *testing.T) {
	cases := []struct {
		in       float64
		decimals int
		want     float64
	}{
		{38.900000000000006, 1, 38.9},
		{5358.7, 0, 5359},
		{-1.25, 1, -1.3},
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := round(c.in, c.decimals); got != c.want {
			t.Errorf("round(%v, %d) = %v (期待 %v)", c.in, c.decimals, got, c.want)
		}
	}
}
