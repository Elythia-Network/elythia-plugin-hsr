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
	"strconv"
	"time"
)

// buildProfile assembles the display payload for a local user.
//
// 未登録なら (nil, nil)。**エラーと区別する** — 「登録していない」は普通の
// 状態で、表示側はそれを見て何も描かない。
func buildProfile(c context.Context, db *sql.DB, client *enkaClient, userID string) (map[string]any, error) {
	var (
		uid, nickname, signature, region, platform string
		level, worldLevel, friendCount, headIcon   int
		achievements, bookCount, avatarCount       int
		equipmentCount, relicCount, musicCount     int
		rogueScore, memoryLevel                    int
		charactersRaw                              []byte
		fetchedAt                                  time.Time
	)
	err := db.QueryRowContext(c, `
		SELECT a.uid, s.nickname, s.signature, s.level, s.world_level, s.region,
		       s.platform, s.friend_count, s.head_icon, s.achievements, s.book_count,
		       s.avatar_count, s.equipment_count, s.relic_count, s.music_count,
		       s.rogue_score, s.memory_level, s.characters, s.fetched_at
		FROM accounts a JOIN snapshots s ON s.uid = a.uid
		WHERE a.user_id = $1
	`, userID).Scan(&uid, &nickname, &signature, &level, &worldLevel, &region,
		&platform, &friendCount, &headIcon, &achievements, &bookCount,
		&avatarCount, &equipmentCount, &relicCount, &musicCount,
		&rogueScore, &memoryLevel, &charactersRaw, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// 壊れた JSON でカード全体を落とさない。詳細が出ないだけで済ませる。
	characters := []character{}
	if len(charactersRaw) > 0 {
		_ = json.Unmarshal(charactersRaw, &characters)
	}

	// アイコンは**自分のプロキシ経由の URL**として返す。CSP が
	// `img-src 'self'` なので、取得元の URL を渡しても表示できない。
	icon := ""
	if headIcon != 0 {
		if p, ok := client.masters.pfps.Get(c)[strconv.Itoa(headIcon)]; ok {
			icon = assetURL(p.Icon)
		}
	}

	return map[string]any{
		"linked":         true,
		"uid":            uid,
		"nickname":       nickname,
		"signature":      signature,
		"level":          level,
		"worldLevel":     worldLevel,
		"region":         region,
		"platform":       platform,
		"friendCount":    friendCount,
		"achievements":   achievements,
		"bookCount":      bookCount,
		"avatarCount":    avatarCount,
		"equipmentCount": equipmentCount,
		"relicCount":     relicCount,
		"musicCount":     musicCount,
		"rogueScore":     rogueScore,
		"memoryLevel":    memoryLevel,
		"profileIcon":    icon,
		"characters":     characters,
		"fetchedAt":      fetchedAt,
	}, nil
}
