<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->

<template>
<div v-if="data?.linked" :class="$style.root">
	<!--
		既定は 1 行だけ。プロフィールは他の情報と並ぶ場所なので、開いていない
		ときに縦を占有しないようにする。
	-->
	<button type="button" class="_button" :class="$style.label" :aria-expanded="open" @click="toggle">
		<img v-if="data.profileIcon" :class="$style.labelIcon" :src="data.profileIcon" alt=""/>
		<span :class="$style.labelTitle">崩壊:スターレイル</span>
		<span :class="$style.labelName">{{ data.nickname }}</span>
		<span :class="$style.labelMeta">開拓Lv.{{ data.level }}</span>
		<i :class="[$style.chevron, open ? 'ti ti-chevron-up' : 'ti ti-chevron-down']"></i>
	</button>

	<div v-if="open" :class="$style.panel">
		<div :class="$style.records">
			<span>均衡{{ data.worldLevel }}</span>
			<span v-if="data.memoryLevel > 0">忘却の庭 {{ data.memoryLevel }}層</span>
			<span v-if="data.rogueScore > 0">模擬宇宙 {{ data.rogueScore }}</span>
			<span v-if="data.achievements > 0">実績 {{ data.achievements }}</span>
			<span v-if="data.avatarCount > 0">キャラ {{ data.avatarCount }}</span>
			<span v-if="data.equipmentCount > 0">光円錐 {{ data.equipmentCount }}</span>
			<span v-if="data.relicCount > 0">遺物 {{ data.relicCount }}</span>
			<span v-if="data.musicCount > 0">楽曲 {{ data.musicCount }}</span>
			<span v-if="data.bookCount > 0">書籍 {{ data.bookCount }}</span>
			<span v-if="data.region">{{ data.region }}</span>
		</div>

		<div v-if="data.signature" :class="$style.signature">{{ data.signature }}</div>

		<!--
			ショーケースは 5 体まで。詳細を公開していないと空になるので、
			その場合は戦績だけで終わる。
		-->
		<div v-if="data.characters.length > 0" :class="$style.roster">
			<button
				v-for="(c, i) in data.characters"
				:key="c.avatarId"
				type="button"
				class="_button"
				:class="[$style.chara, { [$style.charaActive]: selected === i }]"
				:style="{ borderColor: selected === i ? elementColor(c.element) : 'transparent' }"
				@click="selected = i"
			>
				<img v-if="c.icon" :class="$style.charaIcon" :src="c.icon" alt=""/>
				<span :class="$style.charaLv">Lv.{{ c.level }}</span>
			</button>
		</div>

		<div v-if="build" :class="$style.build">
			<div :class="$style.buildHead">
				<span :class="$style.buildName">{{ build.name || `#${build.avatarId}` }}</span>
				<span :class="$style.rarity">{{ '★'.repeat(build.rarity) }}</span>
				<span :style="{ color: elementColor(build.element) }">{{ elementLabel(build.element) }}</span>
				<span :class="$style.meta">{{ pathLabel(build.path) }}</span>
				<span :class="$style.meta">Lv.{{ build.level }}</span>
				<span v-if="build.assist" :class="$style.assist">サポート</span>
			</div>

			<!-- 星魂。取っていないものは暗く出して、何番目かが分かるようにする。 -->
			<div v-if="build.eidolons.length > 0" :class="$style.eidolons">
				<img
					v-for="(e, i) in build.eidolons"
					:key="i"
					:class="[$style.eidolon, { [$style.locked]: !e.unlocked }]"
					:src="e.icon"
					alt=""
				/>
				<span :class="$style.meta">星魂{{ build.rank }}</span>
			</div>

			<div v-if="build.skills.length > 0" :class="$style.skills">
				<span v-for="s in build.skills" :key="s.id" :class="$style.skill">
					<img v-if="s.icon" :class="$style.skillIcon" :src="s.icon" alt=""/>
					<span :class="$style.skillLabel">{{ traceLabel(s.kind) }}</span>
					<b>{{ s.level }}</b>
				</span>
			</div>

			<!--
				軌跡のツリー。解放していないノードは応答に来ないので、マスター側の
				定義と突き合わせた結果を点灯 / 消灯で見せる。
			-->
			<div v-if="build.branches.length > 0" :class="$style.branches">
				<div v-for="(b, i) in build.branches" :key="i" :class="$style.branch">
					<img
						v-for="n in b.nodes"
						:key="n.id"
						:class="[$style.node, { [$style.locked]: !n.unlocked }]"
						:src="n.icon"
						:title="traceLabel(n.kind)"
						alt=""
					/>
				</div>
			</div>

			<div v-if="build.lightCone" :class="$style.cone">
				<img v-if="build.lightCone.icon" :class="$style.coneIcon" :src="build.lightCone.icon" alt=""/>
				<div :class="$style.coneBody">
					<div>
						<span :class="$style.coneName">{{ build.lightCone.name }}</span>
						<span :class="$style.meta">Lv.{{ build.lightCone.level }} / 重畳{{ build.lightCone.rank }}</span>
					</div>
					<div :class="$style.statLine">
						<span v-for="(st, i) in build.lightCone.stats" :key="i">{{ st.label }} {{ fmtStat(st) }}</span>
					</div>
				</div>
			</div>

			<div v-if="build.stats.length > 0" :class="$style.statGrid">
				<div v-for="(st, i) in build.stats" :key="i" :class="$style.statRow">
					<span :class="$style.statLabel">{{ st.label }}</span>
					<span :class="$style.statValue">{{ fmtStat(st) }}</span>
				</div>
			</div>

			<div v-if="build.relics.length > 0" :class="$style.relics">
				<div v-for="(r, i) in build.relics" :key="i" :class="$style.relic">
					<img v-if="r.icon" :class="$style.relicIcon" :src="r.icon" alt=""/>
					<div :class="$style.relicBody">
						<div :class="$style.relicHead">
							<span :class="$style.relicSlot">{{ slotLabel(r.slot) }}</span>
							<span :class="$style.relicMain">{{ r.main.label }} {{ fmtStat(r.main) }}</span>
							<span :class="$style.meta">+{{ r.level }}</span>
						</div>
						<div :class="$style.relicSet">{{ r.setName }}</div>
						<div :class="$style.statLine">
							<!--
								伸びた回数を併記する。原神には無い情報で、同じ数値でも
								「1 回で伸びた」のか「4 回積んだ」のかが分かる。
							-->
							<span v-for="(st, j) in r.subs" :key="j">
								{{ st.label }} {{ fmtStat(st) }}<span v-if="st.count > 1" :class="$style.rolls">×{{ st.count }}</span>
							</span>
						</div>
					</div>
				</div>
			</div>
		</div>

		<div :class="$style.footer">UID {{ data.uid }}</div>
	</div>
</div>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue';
import { type SlotContext } from '@/plugin-api.js';
import { api, slotLabel, traceLabel, elementLabel, elementColor, pathLabel, fmtStat } from './api.js';
import type { ProfileResponse, LinkedProfile } from './api.js';

const props = defineProps<{ ctx: SlotContext }>();

const data = ref<LinkedProfile | null>(null);
const open = ref(false);
const selected = ref<number | null>(null);

const build = computed(() => {
	if (data.value == null || selected.value == null) return null;
	return data.value.characters[selected.value] ?? null;
});

function toggle(): void {
	open.value = !open.value;
	// 開いた直後に空欄を見せない。1 体目を選んだ状態から始める。
	if (open.value && selected.value == null && (data.value?.characters.length ?? 0) > 0) {
		selected.value = 0;
	}
}

onMounted(async () => {
	const user = props.ctx.user;
	if (user == null) return;
	// リモート利用者も引く。相手が同じプラグインを入れた mk-go なら、
	// バックエンドが取り寄せて返す (初回は間に合わないので出ない)。

	try {
		const res = await api<ProfileResponse>('profile', { userId: user.id });
		if (res.linked) data.value = res;
	} catch (err) {
		// 表示できないだけで済ませる。データが取れないせいでプロフィール全体が
		// 壊れてはいけない。
		console.error('[plugin:hsr] プロフィールの取得に失敗しました', err);
	}
});
</script>

<style lang="scss" module>
.root {
	margin: 8px 0;
}

.label {
	display: flex;
	align-items: center;
	gap: 8px;
	width: 100%;
	padding: 6px 10px;
	border-radius: var(--MI-radius-sm, 8px);
	background: var(--MI_THEME-buttonBg);
	font-size: 0.9em;
	text-align: left;

	&:hover {
		background: var(--MI_THEME-buttonHoverBg);
	}
}

.labelIcon {
	width: 24px;
	height: 24px;
	border-radius: 100%;
	background: var(--MI_THEME-bg);
	flex-shrink: 0;
}

.labelTitle {
	font-weight: 700;
	flex-shrink: 0;
}

.labelName {
	opacity: 0.9;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}

.labelMeta {
	opacity: 0.7;
	font-size: 0.9em;
	flex-shrink: 0;
}

/* シェブロンは右端に寄せる。押せる場所だと分かるようにする。 */
.chevron {
	margin-left: auto;
	opacity: 0.6;
	flex-shrink: 0;
}

.panel {
	margin-top: 6px;
	padding: 12px 14px;
	border-radius: var(--MI-radius, 12px);
	background: var(--MI_THEME-panel);
	border: 1px solid var(--MI_THEME-divider);
}

.records {
	display: flex;
	flex-wrap: wrap;
	gap: 6px 10px;
	font-size: 0.9em;
	opacity: 0.9;
}

.signature {
	margin-top: 6px;
	font-size: 0.85em;
	opacity: 0.7;
}

.roster {
	display: flex;
	gap: 8px;
	margin-top: 10px;
	flex-wrap: wrap;
}

.chara {
	display: flex;
	flex-direction: column;
	align-items: center;
	width: 46px;
	padding: 2px 0;
	border-radius: 8px;
	/* 選択中は属性色の枠を出す。 */
	border: 2px solid transparent;
}

.charaActive {
	background: var(--MI_THEME-buttonBg);
}

.charaIcon {
	width: 42px;
	height: 42px;
	border-radius: 8px;
	background: var(--MI_THEME-bg);
}

.charaLv {
	font-size: 0.72em;
	opacity: 0.8;
}

.build {
	margin-top: 10px;
	padding: 10px;
	border-radius: 8px;
	background: var(--MI_THEME-bg);
	font-size: 0.85em;
}

.buildHead {
	display: flex;
	flex-wrap: wrap;
	align-items: baseline;
	gap: 4px 8px;
}

.buildName {
	font-weight: 700;
}

.rarity {
	color: #f0c060;
	font-size: 0.8em;
}

.meta {
	opacity: 0.7;
	font-size: 0.9em;
}

.assist {
	padding: 0 6px;
	border-radius: 999px;
	background: var(--MI_THEME-accentedBg);
	color: var(--MI_THEME-accent);
	font-size: 0.8em;
}

.eidolons {
	display: flex;
	align-items: center;
	gap: 4px;
	margin-top: 8px;
}

.eidolon {
	width: 22px;
	height: 22px;
	border-radius: 100%;
	background: var(--MI_THEME-panel);
}

/* 未解放は暗く落とす。並びが残るので何番目かが分かる。 */
.locked {
	opacity: 0.25;
	filter: grayscale(1);
}

.skills {
	display: flex;
	flex-wrap: wrap;
	gap: 4px 12px;
	margin-top: 8px;
}

.skill {
	display: flex;
	align-items: center;
	gap: 3px;
}

.skillIcon {
	width: 20px;
	height: 20px;
}

.skillLabel {
	opacity: 0.7;
	font-size: 0.85em;
}

.branches {
	display: flex;
	flex-wrap: wrap;
	gap: 4px 14px;
	margin-top: 6px;
}

.branch {
	display: flex;
	gap: 2px;
}

.node {
	width: 16px;
	height: 16px;
}

.cone {
	display: flex;
	gap: 8px;
	margin-top: 10px;
	align-items: center;
}

.coneIcon {
	width: 34px;
	height: 44px;
	border-radius: 4px;
	object-fit: cover;
	background: var(--MI_THEME-panel);
	flex-shrink: 0;
}

.coneBody {
	min-width: 0;
}

.coneName {
	font-weight: 600;
	margin-right: 6px;
}

.statGrid {
	display: grid;
	grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
	gap: 2px 12px;
	margin-top: 10px;
}

.statRow {
	display: flex;
	justify-content: space-between;
	gap: 8px;
}

.statLabel {
	opacity: 0.7;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}

.statValue {
	font-variant-numeric: tabular-nums;
	flex-shrink: 0;
}

.statLine {
	display: flex;
	flex-wrap: wrap;
	gap: 2px 10px;
	opacity: 0.8;
	font-size: 0.9em;
}

.rolls {
	opacity: 0.55;
	font-size: 0.85em;
	margin-left: 1px;
}

.relics {
	display: grid;
	grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
	gap: 8px;
	margin-top: 10px;
}

.relic {
	display: flex;
	gap: 8px;
}

.relicIcon {
	width: 34px;
	height: 34px;
	border-radius: 6px;
	background: var(--MI_THEME-panel);
	flex-shrink: 0;
}

.relicBody {
	min-width: 0;
	flex: 1;
}

.relicHead {
	display: flex;
	align-items: baseline;
	gap: 6px;
}

.relicSlot {
	opacity: 0.7;
	flex-shrink: 0;
}

.relicMain {
	font-weight: 600;
}

.relicSet {
	font-size: 0.85em;
	opacity: 0.6;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}

.footer {
	margin-top: 10px;
	font-size: 0.75em;
	opacity: 0.55;
}
</style>
