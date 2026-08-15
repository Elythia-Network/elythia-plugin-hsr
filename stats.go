package hsr

import (
	"context"
	"strconv"
)

/*
 * 最終ステータスの計算。
 *
 * # なぜ計算が要るのか
 *
 * 原神の Enka は `fightPropMap` で実数ステータスを返すが、**スターレイルは
 * 返さない**。手に入るのは装備の内訳だけなので、
 *
 *   キャラ基礎値 + 光円錐基礎値 + 光円錐効果 + 遺物 + セット効果 + 軌跡
 *
 * を自分で足し合わせる。材料はすべてマスターに揃っている。
 *
 * # 踏みやすい罠: 同じステータスが 2 つの名前で来る
 *
 * **メインステータスは `Base` 付き、サブステータスは `Base` 無し**で来る。
 * 実データの 12 部位すべてでこの並びだった。
 *
 *   胴のメイン → CriticalDamageBase
 *   靴のサブ   → CriticalDamage
 *
 * 別のキーとして足すと**遺物のメイン分が最終値に入らない**。実測では会心
 * ダメージが 193.6% になるべきところ 91.5% になった (メインの胴が丸ごと消える)。
 * 取得元のテキストでも両者は同じラベル (「会心ダメージ」) を指しており、
 * 同一のものと扱ってよい。
 */

// aliasedProps maps the "Base" spelling onto the plain one.
//
// **接尾辞を機械的に剥がしてはいけない。** `HPBase` / `AttackBase` は
// 「基礎HP」であって `HP` とは別物なので、ここに挙げた分だけを畳む。
var aliasedProps = map[string]string{
	"CriticalChanceBase":        "CriticalChance",
	"CriticalDamageBase":        "CriticalDamage",
	"StatusResistanceBase":      "StatusResistance",
	"StatusProbabilityBase":     "StatusProbability",
	"BreakDamageAddedRatioBase": "BreakDamageAddedRatio",
	"SPRatioBase":               "SPRatio",
	"HealRatioBase":             "HealRatio",
}

func normalizeProp(key string) string {
	if v, ok := aliasedProps[key]; ok {
		return v
	}
	return key
}

// props accumulates stat contributions from every source.
type props map[string]float64

func (p props) add(key string, v float64) { p[normalizeProp(key)] += v }

func (p props) addAll(m map[string]float64) {
	for k, v := range m {
		p.add(k, v)
	}
}

// percentProps are shown with a "%" suffix.
//
// 実数で来るもの (HPDelta / SpeedDelta など) と混ぜないよう、割合の側を
// 明示する。取得元は 0-1 の小数で持っているので、表示時に 100 倍する。
var percentProps = map[string]bool{
	"CriticalChance":        true,
	"CriticalDamage":        true,
	"BreakDamageAddedRatio": true,
	"StatusProbability":     true,
	"StatusResistance":      true,
	"SPRatio":               true,
	"HealRatio":             true,
	"PhysicalAddedRatio":    true,
	"FireAddedRatio":        true,
	"IceAddedRatio":         true,
	"ThunderAddedRatio":     true,
	"WindAddedRatio":        true,
	"QuantumAddedRatio":     true,
	"ImaginaryAddedRatio":   true,
}

// collectProps sums every stat source for one character.
func collectProps(ctx context.Context, m *masters, a rawAvatar) props {
	p := props{}

	// 1. キャラの基礎値。昇格段階ごとの表を引き、レベル分を足す。
	if info, ok := m.avatar(ctx, a.AvatarID); ok {
		if base, ok := info.Promotion[strconv.Itoa(a.Promotion)]; ok {
			p["HPBase"] += base.HPBase + base.HPAdd*float64(a.Level-1)
			p["AttackBase"] += base.AttackBase + base.AttackAdd*float64(a.Level-1)
			p["DefenceBase"] += base.DefenceBase + base.DefenceAdd*float64(a.Level-1)
			p["SpeedBase"] += base.SpeedBase
			p["CriticalChance"] += base.CriticalChance
			p["CriticalDamage"] += base.CriticalDamage
		}
	}

	// 2. 光円錐。基礎値はキャラと同じ形で足し、重畳ごとの常時効果も足す。
	if a.Equipment != nil {
		if info, ok := m.weapon(ctx, a.Equipment.TID); ok {
			if base, ok := info.Promotion[strconv.Itoa(a.Equipment.Promotion)]; ok {
				lv := float64(a.Equipment.Level - 1)
				p["HPBase"] += base.BaseHP + base.BaseHPAdd*lv
				p["AttackBase"] += base.BaseAttack + base.BaseAttackAdd*lv
				p["DefenceBase"] += base.BaseDefence + base.BaseDefenceAdd*lv
			}
			if skill, ok := info.EquipmentSkill[strconv.Itoa(a.Equipment.Rank)]; ok {
				p.addAll(skill.Props)
			}
		}
	}

	// 3. 遺物。`_flat` は取得元が解決済みなので、値をそのまま足す。
	setCount := map[int]int{}
	for _, r := range a.RelicList {
		for _, pr := range r.Flat.Props {
			p.add(pr.Type, pr.Value)
		}
		setCount[r.Flat.SetID]++
	}

	// 4. セット効果。2 セット / 4 セットの常時分だけがマスターに入っている
	// (条件付きのものは props が空なので、足しても影響しない)。
	sets := m.relics.Get(ctx).Sets
	for id, n := range setCount {
		set, ok := sets[strconv.Itoa(id)]
		if !ok {
			continue
		}
		for _, need := range []int{2, 4} {
			if n < need {
				continue
			}
			if skill, ok := set.SetSkills[strconv.Itoa(need)]; ok {
				p.addAll(skill.Props)
			}
		}
	}

	// 5. 軌跡。ステータスを持たないノード (スキル本体や解放系) は表に無いので、
	// 引けなくても異常ではない。
	tree := m.tree.Get(ctx)
	for _, pt := range a.SkillTreeList {
		levels, ok := tree[strconv.Itoa(pt.PointID)]
		if !ok {
			continue
		}
		if node, ok := levels[strconv.Itoa(pt.Level)]; ok {
			p.addAll(node.Props)
		}
	}
	return p
}

// finalStats turns accumulated contributions into what the game shows.
//
// 実数系は `基礎値 * (1 + 割合) + 実数加算` の形。割合系はそのまま。
func finalStats(ctx context.Context, m *masters, p props, element string) []Stat {
	label := func(key string) string {
		if v := m.text(ctx, key); v != "" {
			return v
		}
		return key
	}
	combine := func(base, ratio, delta string) float64 {
		return p[base]*(1+p[ratio]) + p[delta]
	}

	out := []Stat{
		{Label: label("MaxHP"), Value: round(combine("HPBase", "HPAddedRatio", "HPDelta"), 0)},
		{Label: label("Attack"), Value: round(combine("AttackBase", "AttackAddedRatio", "AttackDelta"), 0)},
		{Label: label("Defence"), Value: round(combine("DefenceBase", "DefenceAddedRatio", "DefenceDelta"), 0)},
		{Label: label("Speed"), Value: round(combine("SpeedBase", "SpeedAddedRatio", "SpeedDelta"), 1)},
		{Label: label("CriticalChance"), Value: round(p["CriticalChance"]*100, 1), Percent: true},
		{Label: label("CriticalDamage"), Value: round(p["CriticalDamage"]*100, 1), Percent: true},
	}

	// ここから下は 0 のときに出さない。全キャラに並ぶと表が間延びするうえ、
	// 「撃破特効 0%」に情報が無い。
	optional := []struct {
		key   string
		value float64
	}{
		{"BreakDamageAddedRatio", p["BreakDamageAddedRatio"]},
		{"StatusProbability", p["StatusProbability"]},
		{"StatusResistance", p["StatusResistance"]},
		{"HealRatio", p["HealRatio"]},
		// EP 回復効率は 100% が基準。上振れている分だけ見せる。
		{"SPRatio", p["SPRatio"]},
	}
	for _, o := range optional {
		if o.value == 0 {
			continue
		}
		v := o.value * 100
		if o.key == "SPRatio" {
			v += 100
		}
		out = append(out, Stat{Label: label(o.key), Value: round(v, 1), Percent: true})
	}

	// 属性ダメージはキャラの属性の分だけ出す。他属性のボーナスが乗ることも
	// あるが、実戦で意味を持つのは自分の属性なので絞る。
	if element != "" {
		key := element + "AddedRatio"
		if v := p[key]; v != 0 {
			out = append(out, Stat{Label: label(key), Value: round(v*100, 1), Percent: true})
		}
	}
	return out
}

// round trims floating point noise to the given number of decimals.
//
// 0.1 + 0.2 のような誤差がそのまま JSON に出ると `38.900000000000006` になる。
func round(v float64, decimals int) float64 {
	shift := 1.0
	for range decimals {
		shift *= 10
	}
	r := float64(int64(v*shift + 0.5))
	if v < 0 {
		r = float64(int64(v*shift - 0.5))
	}
	return r / shift
}
