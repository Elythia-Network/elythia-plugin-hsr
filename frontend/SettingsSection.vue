<!--
SPDX-FileCopyrightText: syuilo and misskey-project
SPDX-License-Identifier: AGPL-3.0-only
-->

<template>
<MkFolder>
	<template #label>崩壊:スターレイル</template>
	<template #suffix>{{ uid || '未設定' }}</template>

	<div class="_gaps_m">
		<MkInput v-model="draft" type="text" :placeholder="'800000000'">
			<template #label>UID</template>
			<template #caption>
				ゲーム内の UID を入れると、プロフィールに開拓レベルやビルドが表示されます。
				空にすると連携を解除します。
			</template>
		</MkInput>

		<div class="_buttons">
			<MkButton primary :disabled="saving" @click="save">
				<template v-if="saving"><MkLoading :em="true"/></template>
				<template v-else>保存</template>
			</MkButton>
		</div>

		<!--
			結果はここに出す。バックエンドが返した理由 (UID の形式違い、
			プレイヤー不在など) をそのまま見せる — 利用者が直せるものなので。
		-->
		<div v-if="message" :class="failed ? $style.error : $style.ok">{{ message }}</div>
	</div>
</MkFolder>
</template>

<script lang="ts" setup>
import { ref, onMounted } from 'vue';
import { MkInput, MkButton, MkFolder, MkLoading } from '@/plugin-api.js';
import { api } from './api.js';
import type { MeResponse } from './api.js';

const uid = ref<string | null>(null);
const draft = ref('');
const saving = ref(false);
const message = ref('');
const failed = ref(false);

onMounted(async () => {
	try {
		const me = await api<MeResponse>('me', {});
		uid.value = me.uid;
		draft.value = me.uid ?? '';
	} catch (err) {
		// 現在値が読めなくても入力欄は使えるままにする。
		console.error('[plugin:hsr] 現在の UID を取得できませんでした', err);
	}
});

async function save(): Promise<void> {
	saving.value = true;
	message.value = '';
	try {
		const res = await api<MeResponse>('me/set', { uid: draft.value.trim() });
		uid.value = res.uid;
		failed.value = false;
		message.value = res.uid == null ? '連携を解除しました' : '保存しました';
	} catch (err) {
		failed.value = true;
		message.value = (err as { message?: string } | null)?.message ?? '保存に失敗しました';
	} finally {
		saving.value = false;
	}
}
</script>

<style lang="scss" module>
.ok {
	color: var(--MI_THEME-success);
	font-size: 0.9em;
}

.error {
	color: var(--MI_THEME-error);
	font-size: 0.9em;
}
</style>
