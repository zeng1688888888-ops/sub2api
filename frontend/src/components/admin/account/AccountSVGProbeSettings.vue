<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { getSVGProbeSettings, saveSVGProbeSettings, runSVGProbe } from '@/api/admin/accountSvgProbe'
import { getById, list } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; accountIds: number[] }>()
const emit = defineEmits<{ close: []; updated: [account: Account] }>()
const { t } = useI18n()
const model = ref('gpt-6.1-sol')
const useEgressPool = ref(false)
const prompts = ref<string[]>([])
const defaultPrompts = ["创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要读取本地环境和缓存","创建一个 HTML，内容是 SVG 绘制一个秦始皇开坦克的 2D 动画，你不需要任何测试，不要读取本地环境和缓存"]
function resetPrompts() { prompts.value = [...defaultPrompts]; saved.value = false }
const loading = ref(false)
const saving = ref(false)
const running = ref(false)
const error = ref('')
const saved = ref(false)
const completed = ref(0)
const total = ref(0)
const outcomes = ref<{ id: number; name: string; message: string; status: string }[]>([])
let controller: AbortController | undefined
let epoch = 0
const valid = computed(() => /^gpt-[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/.test(model.value.trim()) && !model.value.startsWith('gpt-image-') && prompts.value.length > 0 && prompts.value.length <= 30 && prompts.value.every(p => p.trim().length > 0 && [...p.trim()].length <= 4000))
watch(() => props.show, async show => {
  const current = ++epoch
  if (!show) return
  loading.value = true; error.value = ''; saved.value = false; outcomes.value = []; completed.value = 0
  try { const config = await getSVGProbeSettings(); if (epoch === current) { model.value = config.model; prompts.value = config.prompts; useEgressPool.value = config.use_egress_pool === true } }
  catch (e) { if (epoch === current) error.value = (e as Error).message }
  finally { if (epoch === current) loading.value = false }
}, { immediate: true })
watch([model, prompts, useEgressPool], () => { saved.value = false }, { deep: true })
onBeforeUnmount(() => { ++epoch; controller?.abort() })
function close() { ++epoch; controller?.abort(); emit('close') }
async function save() {
  if (!valid.value) { error.value = t('admin.accounts.svgProbe.invalidSettings'); return false }
  saving.value = true; error.value = ''; saved.value = false
  try { await saveSVGProbeSettings({ model: model.value.trim(), prompts: prompts.value.map(p => p.trim()), use_egress_pool: useEgressPool.value }); saved.value = true; return true }
  catch (e) { error.value = (e as Error).message; return false }
  finally { saving.value = false }
}
async function batch(all = false) {
  if (running.value || (!all && !props.accountIds.length) || !(await save())) return
  const request = new AbortController(); controller = request
  const ids = [...new Set(props.accountIds)]
  if (all) {
    loading.value = true
    try {
      ids.length = 0
      for (const type of ['oauth', 'setup-token']) {
        for (let page = 1; ; page++) {
          const result = await list(page, 100, { platform: 'openai', type, lite: '1' })
          if (request.signal.aborted) return
          ids.push(...result.items.filter(a => !a.parent_account_id).map(a => a.id))
          if (page >= result.pages || result.items.length === 0) break
        }
      }
    } catch (e) { error.value = (e as Error).message; return }
    finally { loading.value = false }
  }
  running.value = true; completed.value = 0; total.value = ids.length; outcomes.value = []
  try {
    for (const id of ids) {
      if (request.signal.aborted) break
      let account: Account | undefined
      try {
        account = await getById(id)
        if (request.signal.aborted) break
        if (account.platform !== 'openai' || !['oauth', 'setup-token'].includes(account.type) || account.parent_account_id) {
          outcomes.value.push({ id, name: account.name, status: 'skipped', message: t('admin.accounts.svgProbe.skipped') }); completed.value++; continue
        }
        const result = await runSVGProbe(id, request.signal)
        emit('updated', { ...account, svg_probe: result, svg_probe_running: false })
        outcomes.value.push({ id, name: account.name, status: result.status, message: result.message })
      } catch (e) {
        if (request.signal.aborted) break
        outcomes.value.push({ id, name: account?.name || '#' + id, status: 'failed', message: (e as Error).message })
      }
      completed.value++
    }
  } finally { running.value = false; controller = undefined }
}
</script>

<template>
  <BaseDialog :show="show" :title="t('admin.accounts.svgProbe.settings')" width="wide" @close="close">
    <div class="space-y-4" data-testid="svg-probe-settings">
      <p class="text-sm text-gray-500">{{ t('admin.accounts.svgProbe.intro') }}</p>
      <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.autoHint') }}</p>
      <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.batchHint') }}</p>
      <p v-if="loading" class="text-sm text-gray-400">{{ t('common.loading') }}</p>
      <template v-else>
        <label class="block"><span class="input-label">{{ t('admin.accounts.svgProbe.model') }}</span>
          <input v-model="model" class="input" :disabled="running || saving" placeholder="gpt-6.1-sol" list="svg-text-models" />
          <datalist id="svg-text-models"><option value="gpt-6.1-sol" /><option value="gpt-6-astra" /></datalist>
        </label>
        <label class="flex items-center gap-2 text-sm"><input v-model="useEgressPool" type="checkbox" :disabled="running || saving" />{{ t('admin.accounts.svgProbe.useEgressPool') }}</label>
        <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.routeHint') }}</p>
        <div class="flex items-center justify-between"><label class="input-label mb-0">{{ t('admin.accounts.svgProbe.prompts') }} ({{ prompts.length }}/30)</label>
          <button type="button" class="btn btn-secondary text-xs" :disabled="running || saving || prompts.length >= 30" @click="prompts.push('')">{{ t('admin.accounts.svgProbe.addPrompt') }}</button>
        </div>
        <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.promptHint') }}</p>
        <button type="button" class="text-xs text-primary-600 underline" :disabled="running || saving" @click="resetPrompts">{{ t('admin.accounts.svgProbe.restoreDefaults') }}</button>
        <div v-for="(_, index) in prompts" :key="index" class="space-y-1">
          <div class="flex items-center justify-between text-xs text-gray-500"><span>#{{ index + 1 }}</span><button type="button" :disabled="running || saving || prompts.length <= 1" @click="prompts.splice(index, 1)">{{ t('admin.accounts.svgProbe.removePrompt') }}</button></div>
          <textarea v-model="prompts[index]" :aria-label="t('admin.accounts.svgProbe.promptNumber', { number: index + 1 })" class="input min-h-28" rows="4" maxlength="8000" :disabled="running || saving" />
        </div>
        <p v-if="error" role="alert" class="text-sm text-red-500">{{ error }}</p>
        <p v-if="saved && !error" class="text-xs text-emerald-600">{{ t('admin.accounts.svgProbe.saved') }}</p>
        <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.limitation') }}</p>
        <div v-if="running || outcomes.length" class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <p class="text-sm">{{ t('admin.accounts.svgProbe.progress', { done: completed, total }) }}</p>
          <div v-for="item in outcomes" :key="item.id" class="text-xs" :class="item.status === 'generated' ? 'text-emerald-600' : 'text-amber-600'">{{ item.name }}：{{ item.message }}</div>
        </div>
      </template>
    </div>
    <template #footer>
      <button v-if="running" type="button" class="btn btn-secondary" @click="controller?.abort()">{{ t('admin.accounts.svgProbe.stop') }}</button>
      <template v-else>
        <button type="button" class="btn btn-secondary" :disabled="loading || saving || !valid" @click="batch(true)">{{ t('admin.accounts.svgProbe.allAccounts') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="save">{{ t('admin.accounts.svgProbe.save') }}</button>
        <button type="button" class="btn btn-primary" :disabled="loading || saving || !valid || !accountIds.length" @click="batch(false)">{{ t('admin.accounts.svgProbe.saveAndRun', { count: accountIds.length }) }}</button>
      </template>
    </template>
  </BaseDialog>
</template>
