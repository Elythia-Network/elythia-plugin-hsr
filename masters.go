/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

package hsr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

/*
 * Enka が公開しているマスターデータ。
 *
 * # honker_ 付きのファイルを使わないこと
 *
 * 同じ内容が `honker_characters.json` と `avatars.json` の 2 系統で置かれて
 * いるが、**honker_ 側は名前のハッシュが壊れている**。JS の数値を経由した跡が
 * あり、`7809981386909966580` が `7809981386909966000` と丸められている
 * (double の仮数部に収まらない 19 桁)。
 *
 * 実測では honker_characters.json でキャラ名を引くと **82 件中 0 件**しか
 * 解決しない。接頭辞の無い `avatars.json` は Hash が文字列で保たれており、
 * 95 件すべて引ける。光円錐も同様 (165 件すべて解決)。
 *
 * さらに接頭辞の無い方は Promotion (レベルごとの基礎値) と EquipmentSkill
 * (重畳ごとの効果) を含むので、`honker_meta.json` (443KB) も要らない。
 */

const masterBase = "https://raw.githubusercontent.com/EnkaNetwork/API-docs/master/store/hsr/"

// maxMasterBytes bounds one master file.
//
// 一番大きい skills.json でも 700KB 程度。壊れた応答でメモリを使い切らせない。
const maxMasterBytes = 16 << 20

// masterTTL is how long a master file is reused.
//
// ゲームの更新でしか変わらないので、日次で十分。
const masterTTL = 24 * time.Hour

// textRef is a reference into the localisation table.
//
// **数値ではなく文字列で受ける。** 19 桁のハッシュは float64 に収まらないので、
// 数値として扱うと下 3 桁が失われる (honker_ 系が壊れているのがまさにこれ)。
type textRef struct {
	Hash string `json:"Hash"`
}

// propsHolder is the shape shared by set bonuses, light-cone effects and
// skill-tree nodes.
type propsHolder struct {
	Props map[string]float64 `json:"props"`
}

// promotion holds a character's base stats at one ascension step.
type promotion struct {
	HPBase         float64 `json:"HPBase"`
	HPAdd          float64 `json:"HPAdd"`
	AttackBase     float64 `json:"AttackBase"`
	AttackAdd      float64 `json:"AttackAdd"`
	DefenceBase    float64 `json:"DefenceBase"`
	DefenceAdd     float64 `json:"DefenceAdd"`
	SpeedBase      float64 `json:"SpeedBase"`
	CriticalChance float64 `json:"CriticalChance"`
	CriticalDamage float64 `json:"CriticalDamage"`
}

// avatarInfo is the subset of avatars.json we use.
type avatarInfo struct {
	AvatarName textRef `json:"AvatarName"`
	Rarity     int     `json:"Rarity"`
	// Element is the combat type (Ice / Fire / ...).
	Element string `json:"Element"`
	// BaseType is the path (Knight / Rogue / Memory / ...).
	BaseType string `json:"AvatarBaseType"`
	// IconPath is the round portrait. フルアートもあるが 3.5MB あるので使わない。
	IconPath  string               `json:"AvatarSideIconPath"`
	Promotion map[string]promotion `json:"Promotion"`
	// SkillTree describes how the in-game tree is laid out.
	//
	// キーは 1 つ ("0") しか無いが、取得元が map で持っているので合わせる。
	SkillTree map[string]skillTreeDef `json:"SkillTree"`
	// RankIDList lists the eidolons in unlock order.
	RankIDList []int `json:"RankIDList"`
}

// skillTreeDef is the shape of one character's skill tree.
type skillTreeDef struct {
	// AvatarSkills are the main skills, in display order
	// (通常攻撃 / 戦闘スキル / 必殺技 / 天賦 / 秘技)。
	AvatarSkills []int `json:"AvatarSkills"`
	// PropSkills are the branches. 各枝は大パッシブ 1 つとステータス強化の
	// 小ノードで構成される。
	PropSkills [][]int `json:"PropSkills"`
	// SummonSkills belong to the servant of 記憶 characters.
	SummonSkills [][]int `json:"SummonSkills"`
}

// weaponPromotion holds a light cone's base stats at one ascension step.
//
// **キャラ側と鍵名が違う。** キャラは `HPBase` / `HPAdd`、光円錐は `BaseHP` /
// `BaseHPAdd` で前後が入れ替わっている。同じ構造体を使い回せない。
type weaponPromotion struct {
	BaseHP         float64 `json:"BaseHP"`
	BaseHPAdd      float64 `json:"BaseHPAdd"`
	BaseAttack     float64 `json:"BaseAttack"`
	BaseAttackAdd  float64 `json:"BaseAttackAdd"`
	BaseDefence    float64 `json:"BaseDefence"`
	BaseDefenceAdd float64 `json:"BaseDefenceAdd"`
}

// weaponInfo is the subset of weapons.json we use.
type weaponInfo struct {
	EquipmentName textRef                    `json:"EquipmentName"`
	Rarity        int                        `json:"Rarity"`
	BaseType      string                     `json:"AvatarBaseType"`
	ImagePath     string                     `json:"ImagePath"`
	Promotion     map[string]weaponPromotion `json:"Promotion"`
	// EquipmentSkill maps the superimposition level to its passive stats.
	//
	// 条件付きの効果 (「戦闘開始時」など) は入っていない。ここにあるのは
	// 常時掛かる分だけなので、そのまま足してよい。
	EquipmentSkill map[string]propsHolder `json:"EquipmentSkill"`
}

// relicItem is one relic piece master.
type relicItem struct {
	Rarity int    `json:"Rarity"`
	Type   string `json:"Type"`
	Icon   string `json:"Icon"`
	SetID  int    `json:"SetID"`
}

// relicSet is one relic set master.
type relicSet struct {
	// Name is the localisation key. **オブジェクトではなく文字列で入っている**
	// (avatars.json の AvatarName とは形が違う)。
	Name      string                 `json:"Name"`
	SetSkills map[string]propsHolder `json:"SetSkills"`
}

type relicMasters struct {
	Items map[string]relicItem `json:"Items"`
	Sets  map[string]relicSet  `json:"Sets"`
}

// rankInfo is one eidolon master.
type rankInfo struct {
	IconPath string `json:"IconPath"`
	// SkillAddLevelList raises skill levels (星魂による強化)。
	SkillAddLevelList map[string]int `json:"SkillAddLevelList"`
}

// pfpInfo is one profile picture master.
type pfpInfo struct {
	Icon string `json:"Icon"`
}

// skillInfo is one skill master.
type skillInfo struct {
	IconPath string `json:"IconPath"`
	// PointType distinguishes 通常攻撃 / 戦闘スキル / 必殺技 / 天賦 / 秘技.
	PointType int `json:"PointType"`
}

// store caches one master file in the process.
//
// **取り直せなくても手元のもので凌ぐ。** マスターが引けないせいでカードごと
// 出ないより、名前やアイコンが欠けたまま出す方がよい。
type store[T any] struct {
	mu      sync.RWMutex
	value   T
	loaded  bool
	fetched time.Time

	client *http.Client
	url    string
	// decode turns the response body into the stored value. 言語ごとに分かれた
	// ファイルから 1 言語だけ抜く、といった絞り込みをここで行う。
	decode func([]byte) (T, error)
}

func newStore[T any](client *http.Client, url string, decode func([]byte) (T, error)) *store[T] {
	return &store[T]{client: client, url: url, decode: decode}
}

// Get returns the cached value, refreshing it when stale.
func (s *store[T]) Get(ctx context.Context) T {
	if s == nil {
		var zero T
		return zero
	}
	s.mu.RLock()
	fresh := s.loaded && time.Since(s.fetched) < masterTTL
	v := s.value
	s.mu.RUnlock()
	if fresh {
		return v
	}

	if err := s.refresh(ctx); err != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.value
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

func (s *store[T]) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("master %s: status %d", s.url, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxMasterBytes))
	if err != nil {
		return err
	}
	v, err := s.decode(body)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
	s.loaded = true
	s.fetched = time.Now()
	return nil
}

// decodeJSON is the decoder for files we take as-is.
func decodeJSON[T any](body []byte) (T, error) {
	var v T
	err := json.Unmarshal(body, &v)
	return v, err
}

// masters bundles every master file the plugin reads.
type masters struct {
	avatars *store[map[string]avatarInfo]
	weapons *store[map[string]weaponInfo]
	relics  *store[relicMasters]
	// tree maps a skill-tree point id to its stat bonus per level.
	//
	// **全ノードが載っているわけではない。** スキル本体や解放系のノードは
	// ステータスを持たないので、キー自体が無い。引けなくても異常ではない。
	tree   *store[map[string]map[string]propsHolder]
	ranks  *store[map[string]rankInfo]
	skills *store[map[string]skillInfo]
	// pfps resolves the profile picture id shown next to the nickname.
	pfps *store[map[string]pfpInfo]
	// texts resolves name hashes and stat labels.
	//
	// **ステータス名まで入っている** ので、表示文字列を自前で持たなくて済む
	// (原神版は UI 用のファイルを別に読む必要があった)。
	texts *store[map[string]string]
}

func newMasters(client *http.Client, lang string) *masters {
	return newMastersAt(client, lang, masterBase)
}

// newMastersAt is newMasters with the base URL spelled out, for tests.
func newMastersAt(client *http.Client, lang, base string) *masters {
	return &masters{
		avatars: newStore(client, base+"avatars.json", decodeJSON[map[string]avatarInfo]),
		weapons: newStore(client, base+"weapons.json", decodeJSON[map[string]weaponInfo]),
		relics:  newStore(client, base+"relics.json", decodeJSON[relicMasters]),
		tree:    newStore(client, base+"tree.json", decodeJSON[map[string]map[string]propsHolder]),
		ranks:   newStore(client, base+"ranks.json", decodeJSON[map[string]rankInfo]),
		skills:  newStore(client, base+"skills.json", decodeJSON[map[string]skillInfo]),
		pfps:    newStore(client, base+"pfps.json", decodeJSON[map[string]pfpInfo]),
		texts: newStore(client, base+"hsr.json", func(body []byte) (map[string]string, error) {
			// 13 言語で 1 ファイルなので、使う 1 言語だけ残す。
			var all map[string]map[string]string
			if err := json.Unmarshal(body, &all); err != nil {
				return nil, err
			}
			if v, ok := all[lang]; ok {
				return v, nil
			}
			// 設定された言語が無ければ英語に落とす (取得元は必ず en を持つ)。
			return all["en"], nil
		}),
	}
}

// text resolves a localisation key.
func (m *masters) text(ctx context.Context, key string) string {
	if m == nil || key == "" || key == "0" {
		return ""
	}
	return m.texts.Get(ctx)[key]
}

// avatar returns the master for a character id.
func (m *masters) avatar(ctx context.Context, id int) (avatarInfo, bool) {
	if m == nil {
		return avatarInfo{}, false
	}
	v, ok := m.avatars.Get(ctx)[strconv.Itoa(id)]
	return v, ok
}

// weapon returns the master for a light cone id.
func (m *masters) weapon(ctx context.Context, id int) (weaponInfo, bool) {
	if m == nil {
		return weaponInfo{}, false
	}
	v, ok := m.weapons.Get(ctx)[strconv.Itoa(id)]
	return v, ok
}
