/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

/*
 * バックエンド呼び出しの薄いラッパ。
 *
 * host.api は POST 固定 (misskeyApi と同じ) なので、バックエンド側も POST で
 * 揃えてある。画像だけは <img> が GET しか出せないため GET。
 */

let call: <T>(endpoint: string, params?: Record<string, unknown>) => Promise<T>;

/** Called once from the plugin's setup. */
export function initApi(fn: typeof call): void {
	call = fn;
}

export function api<T>(path: string, params: Record<string, unknown> = {}): Promise<T> {
	return call<T>(`plugin/hsr/${path}`, params);
}

export type MeResponse = { uid: string | null };

/** ラベル付きの数値。percent が true なら "%" を付けて表示する。 */
export type Stat = {
	label: string;
	value: number;
	percent: boolean;
};

/** サブステータス。原神には無い「何回伸びたか」が付く。 */
export type RelicSub = Stat & {
	/** 強化で上昇した回数。 */
	count: number;
	/** 上昇幅の段階。 */
	step: number;
};

export type RelicPiece = {
	/** HEAD / HAND / BODY / FOOT / NECK / OBJECT。 */
	slot: string;
	setName: string;
	icon: string;
	rarity: number;
	level: number;
	main: Stat;
	subs: RelicSub[];
};

export type LightCone = {
	id: number;
	name: string;
	icon: string;
	rarity: number;
	level: number;
	/** 重畳ランク。 */
	rank: number;
	path: string;
	stats: Stat[];
};

export type TraceNode = {
	id: number;
	icon: string;
	/** normal / skill / ultra / talent / technique / passive / stat / servant。 */
	kind: string;
	level: number;
	unlocked: boolean;
};

export type TraceBranch = { nodes: TraceNode[] };

export type Eidolon = { icon: string; unlocked: boolean };

export type Character = {
	avatarId: number;
	name: string;
	icon: string;
	element: string;
	path: string;
	rarity: number;
	level: number;
	promotion: number;
	/** 星魂の数。 */
	rank: number;
	/** サポートとして設定されているか。 */
	assist: boolean;
	stats: Stat[];
	lightCone?: LightCone;
	relics: RelicPiece[];
	skills: TraceNode[];
	branches: TraceBranch[];
	eidolons: Eidolon[];
};

export type LinkedProfile = {
	linked: true;
	uid: string;
	nickname: string;
	signature: string;
	/** 開拓レベル。 */
	level: number;
	/** 均衡レベル。 */
	worldLevel: number;
	region: string;
	platform: string;
	friendCount: number;
	achievements: number;
	bookCount: number;
	avatarCount: number;
	equipmentCount: number;
	relicCount: number;
	musicCount: number;
	rogueScore: number;
	/** 忘却の庭の到達層。 */
	memoryLevel: number;
	profileIcon: string;
	characters: Character[];
	fetchedAt: string;
};

export type ProfileResponse = { linked: false } | LinkedProfile;

/*
 * 表示名の対応付け。
 *
 * **バックエンドはコードのまま返す。** 取得元のテキストにこれらのラベルが
 * 無いので、日本語をサーバ側に焼き込むと言語設定を変えたときに日本語だけが
 * 残ってしまう。表示の都合はこちらに寄せる。
 */

const SLOT_LABELS: Record<string, string> = {
	HEAD: '頭',
	HAND: '手',
	BODY: '胴',
	FOOT: '足',
	NECK: 'オーブ',
	OBJECT: '縄',
};

export function slotLabel(slot: string): string {
	return SLOT_LABELS[slot] ?? slot;
}

const TRACE_LABELS: Record<string, string> = {
	normal: '通常',
	skill: 'スキル',
	ultra: '必殺技',
	talent: '天賦',
	technique: '秘技',
	servant: '憶霊',
	passive: '軌跡',
	stat: '強化',
};

export function traceLabel(kind: string): string {
	return TRACE_LABELS[kind] ?? kind;
}

const ELEMENT_LABELS: Record<string, string> = {
	Physical: '物理',
	Fire: '炎',
	Ice: '氷',
	Thunder: '雷',
	Wind: '風',
	Quantum: '量子',
	Imaginary: '虚数',
};

export function elementLabel(element: string): string {
	return ELEMENT_LABELS[element] ?? element;
}

/** 属性ごとの色。カードの縁取りに使う。 */
const ELEMENT_COLORS: Record<string, string> = {
	Physical: '#b9b9b9',
	Fire: '#f8735b',
	Ice: '#7ec7f0',
	Thunder: '#c88ce0',
	Wind: '#5fd6a0',
	Quantum: '#8085e0',
	Imaginary: '#f0d97e',
};

export function elementColor(element: string): string {
	return ELEMENT_COLORS[element] ?? 'var(--MI_THEME-accent)';
}

const PATH_LABELS: Record<string, string> = {
	Warrior: '壊滅',
	Knight: '存護',
	Rogue: '巡狩',
	Mage: '智識',
	Shaman: '調和',
	Warlock: '虚無',
	Priest: '豊穣',
	Memory: '記憶',
};

export function pathLabel(path: string): string {
	return PATH_LABELS[path] ?? path;
}

/** 会心率のような割合は "%" を付ける。整数なら小数点を出さない。 */
export function fmtStat(st: Stat): string {
	const v = Number.isInteger(st.value) ? String(st.value) : st.value.toFixed(1);
	return st.percent ? `${v}%` : v;
}
