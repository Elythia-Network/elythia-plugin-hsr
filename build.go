/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

package hsr

import (
	"context"
	"strconv"
)

/*
 * Enka の応答を、表示に使う形へ整える。
 *
 * 表示に使う文字列 (「通常攻撃」「頭」など) は**コードのまま返す**。取得元の
 * テキストにこれらのラベルが無く、こちらで日本語を焼き込むと言語設定を変えた
 * ときに日本語だけ残ってしまうため、対応付けはフロントエンドに置く。
 */

// Stat is a labelled number. Percent が true なら "%" を付けて表示する。
type Stat struct {
	Label   string  `json:"label"`
	Value   float64 `json:"value"`
	Percent bool    `json:"percent"`
}

// relicSub is a substat with how it grew.
//
// **強化回数と上振れは原神には無い情報。** サブが何回伸びたか (cnt) と、
// その伸びが上振れているか (step) が分かるので、そのまま渡して見せる。
type relicSub struct {
	Stat
	// Count is how many times this substat rolled (Enka の cnt)。
	Count int `json:"count"`
	// Step is the roll quality (Enka の step)。
	Step int `json:"step"`
}

// relicPiece is one equipped relic.
type relicPiece struct {
	// Slot is the piece kind (HEAD / HAND / BODY / FOOT / NECK / OBJECT)。
	Slot    string     `json:"slot"`
	SetName string     `json:"setName"`
	Icon    string     `json:"icon"`
	Rarity  int        `json:"rarity"`
	Level   int        `json:"level"`
	Main    Stat       `json:"main"`
	Subs    []relicSub `json:"subs"`
}

// lightCone is the equipped light cone.
type lightCone struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon"`
	Rarity int    `json:"rarity"`
	Level  int    `json:"level"`
	// Rank is the superimposition level (重畳)。
	Rank  int    `json:"rank"`
	Path  string `json:"path"`
	Stats []Stat `json:"stats"`
}

// traceNode is one node of the skill tree.
type traceNode struct {
	ID   int    `json:"id"`
	Icon string `json:"icon"`
	// Kind is one of normal / skill / ultra / talent / technique / passive /
	// stat / servant.
	Kind  string `json:"kind"`
	Level int    `json:"level"`
	// Unlocked is false for nodes the player has not taken.
	//
	// **解放していないノードは応答に現れない。** マスター側の定義と突き合わせて
	// 初めて「取っていない」と分かる。
	Unlocked bool `json:"unlocked"`
}

// traceBranch groups nodes as the in-game tree lays them out.
type traceBranch struct {
	Nodes []traceNode `json:"nodes"`
}

// eidolon is one unlocked/locked eidolon slot.
type eidolon struct {
	Icon     string `json:"icon"`
	Unlocked bool   `json:"unlocked"`
}

// character is one showcased character, fully resolved.
type character struct {
	AvatarID int    `json:"avatarId"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Element  string `json:"element"`
	// Path is the character's 運命 (Knight / Rogue / Memory ...)。
	Path      string `json:"path"`
	Rarity    int    `json:"rarity"`
	Level     int    `json:"level"`
	Promotion int    `json:"promotion"`
	// Rank is the eidolon level (星魂)。
	Rank int `json:"rank"`
	// Assist marks the character set as the player's support unit.
	Assist    bool          `json:"assist"`
	Stats     []Stat        `json:"stats"`
	LightCone *lightCone    `json:"lightCone,omitempty"`
	Relics    []relicPiece  `json:"relics"`
	Skills    []traceNode   `json:"skills"`
	Branches  []traceBranch `json:"branches"`
	Eidolons  []eidolon     `json:"eidolons"`
}

// skillKinds names the main skills in the order the master lists them.
//
// AvatarSkills の並びは 通常攻撃 / 戦闘スキル / 必殺技 / 天賦 / 秘技 で固定。
// 位置で決めるので、想定より多いものは kind 無しで通す。
var skillKinds = []string{"normal", "skill", "ultra", "talent", "technique"}

// buildCharacter resolves one entry of avatarDetailList.
func (c *enkaClient) buildCharacter(ctx context.Context, a rawAvatar) character {
	m := c.masters
	out := character{
		AvatarID:  a.AvatarID,
		Level:     a.Level,
		Promotion: a.Promotion,
		Rank:      a.Rank,
		Assist:    a.Assist,
		Relics:    make([]relicPiece, 0, len(a.RelicList)),
	}

	info, hasInfo := m.avatar(ctx, a.AvatarID)
	if hasInfo {
		out.Name = m.text(ctx, info.AvatarName.Hash)
		out.Icon = assetURL(info.IconPath)
		out.Element = info.Element
		out.Path = info.BaseType
		out.Rarity = info.Rarity
	}

	out.Stats = finalStats(ctx, m, collectProps(ctx, m, a), out.Element)

	if a.Equipment != nil {
		out.LightCone = c.buildLightCone(ctx, *a.Equipment)
	}
	for _, r := range a.RelicList {
		out.Relics = append(out.Relics, c.buildRelic(ctx, r))
	}
	if hasInfo {
		out.Skills, out.Branches = c.buildTraces(ctx, info, a)
		out.Eidolons = c.buildEidolons(ctx, info, a.Rank)
	}
	return out
}

// buildLightCone resolves the equipped light cone.
func (c *enkaClient) buildLightCone(ctx context.Context, e rawEquipment) *lightCone {
	lc := &lightCone{
		ID: e.TID, Level: e.Level, Rank: e.Rank,
		// **名前は `_flat.name` から引く。** マスター側の
		// `EquipmentName.Hash` と同じ値だが、応答に載っているものを優先すると
		// マスターの更新が遅れていても名前が出る。
		Name: c.masters.text(ctx, e.Flat.Name),
	}
	if info, ok := c.masters.weapon(ctx, e.TID); ok {
		lc.Icon = assetURL(info.ImagePath)
		lc.Rarity = info.Rarity
		lc.Path = info.BaseType
		if lc.Name == "" {
			lc.Name = c.masters.text(ctx, info.EquipmentName.Hash)
		}
	}
	// 光円錐そのものの基礎値 (HP / 攻撃力 / 防御力)。取得元が解決済み。
	for _, p := range e.Flat.Props {
		lc.Stats = append(lc.Stats, Stat{
			Label:   c.statLabel(ctx, p.Type),
			Value:   round(p.Value, 1),
			Percent: percentProps[normalizeProp(p.Type)],
		})
	}
	return lc
}

// buildRelic resolves one relic piece.
//
// **`_flat.props` の先頭がメインステータス**で、残りがサブ。実データの全部位で
// この並びだった (mainAffixId から引ける期待値と一致する)。
func (c *enkaClient) buildRelic(ctx context.Context, r rawRelic) relicPiece {
	out := relicPiece{
		Level:   r.Level,
		SetName: c.masters.text(ctx, r.Flat.SetName),
	}
	if item, ok := c.masters.relics.Get(ctx).Items[strconv.Itoa(r.TID)]; ok {
		out.Slot = item.Type
		out.Icon = assetURL(item.Icon)
		out.Rarity = item.Rarity
	}
	if out.SetName == "" {
		// 応答のハッシュで引けなければセット表を経由する。
		if set, ok := c.masters.relics.Get(ctx).Sets[strconv.Itoa(r.Flat.SetID)]; ok {
			out.SetName = c.masters.text(ctx, set.Name)
		}
	}

	for i, p := range r.Flat.Props {
		st := Stat{
			Label:   c.statLabel(ctx, p.Type),
			Value:   round(p.Value, 1),
			Percent: percentProps[normalizeProp(p.Type)],
		}
		if st.Percent {
			// 取得元は 0-1 の小数で持つので、表示に合わせて 100 倍する。
			st.Value = round(p.Value*100, 1)
		}
		if i == 0 {
			out.Main = st
			continue
		}
		sub := relicSub{Stat: st}
		// サブの伸び方は subAffixList 側にある。並びは props の 2 件目以降と
		// 対応する。
		if j := i - 1; j < len(r.SubAffixList) {
			sub.Count = r.SubAffixList[j].Cnt
			sub.Step = r.SubAffixList[j].Step
		}
		out.Subs = append(out.Subs, sub)
	}
	return out
}

// buildTraces resolves the skill tree.
//
// マスターの定義と応答を突き合わせて、取っていないノードを `Unlocked: false`
// として残す。**応答には解放済みのものしか来ない**ので、定義側を基準にする。
func (c *enkaClient) buildTraces(ctx context.Context, info avatarInfo, a rawAvatar) ([]traceNode, []traceBranch) {
	taken := make(map[int]int, len(a.SkillTreeList))
	for _, pt := range a.SkillTreeList {
		taken[pt.PointID] = pt.Level
	}

	tree, ok := info.SkillTree["0"]
	if !ok {
		return nil, nil
	}
	node := func(id int, kind string) traceNode {
		n := traceNode{ID: id, Kind: kind}
		if lv, ok := taken[id]; ok {
			n.Level = lv
			n.Unlocked = true
		}
		if s, ok := c.masters.skills.Get(ctx)[strconv.Itoa(id)]; ok {
			n.Icon = assetURL(s.IconPath)
		}
		return n
	}

	skills := make([]traceNode, 0, len(tree.AvatarSkills))
	for i, id := range tree.AvatarSkills {
		kind := ""
		if i < len(skillKinds) {
			kind = skillKinds[i]
		}
		skills = append(skills, node(id, kind))
	}
	// 憶霊を持つキャラ (記憶の運命) は召喚側のスキルも並べる。
	for _, ids := range tree.SummonSkills {
		for _, id := range ids {
			skills = append(skills, node(id, "servant"))
		}
	}

	branches := make([]traceBranch, 0, len(tree.PropSkills))
	for _, ids := range tree.PropSkills {
		b := traceBranch{Nodes: make([]traceNode, 0, len(ids))}
		for _, id := range ids {
			// 枝の先頭は大パッシブ、以降はステータス強化。マスターの
			// PointType で分けられるので、それに従う。
			kind := "stat"
			if s, ok := c.masters.skills.Get(ctx)[strconv.Itoa(id)]; ok && s.PointType == 3 {
				kind = "passive"
			}
			b.Nodes = append(b.Nodes, node(id, kind))
		}
		branches = append(branches, b)
	}
	return skills, branches
}

// buildEidolons lists every eidolon slot with whether it is unlocked.
func (c *enkaClient) buildEidolons(ctx context.Context, info avatarInfo, rank int) []eidolon {
	ranks := c.masters.ranks.Get(ctx)
	out := make([]eidolon, 0, len(info.RankIDList))
	for i, id := range info.RankIDList {
		e := eidolon{Unlocked: i < rank}
		if r, ok := ranks[strconv.Itoa(id)]; ok {
			e.Icon = assetURL(r.IconPath)
		}
		out = append(out, e)
	}
	return out
}

// statLabel resolves a stat key to its display name.
//
// **ステータス名まで取得元のテキストにある** ので自前で持たない。引けない
// ものはキーをそのまま出す (表示が崩れるより、何のことか分かる方がよい)。
func (c *enkaClient) statLabel(ctx context.Context, key string) string {
	if v := c.masters.text(ctx, key); v != "" {
		return v
	}
	return key
}
