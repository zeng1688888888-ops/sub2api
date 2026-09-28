<template>
  <BaseDialog :show="show" :title="t('admin.accounts.stateProbe.modalTitle')" width="wide" @close="close">
    <div v-if="account" class="space-y-4">
      <div class="flex items-center gap-3 rounded-xl border border-amber-200 bg-amber-50/70 p-3 dark:border-amber-800/60 dark:bg-amber-950/20">
        <Icon name="brain" size="md" class="text-amber-500" />
        <div>
          <div class="font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateProbe.intro') }}</p>
        </div>
      </div>
      <Input v-model="model" :disabled="running" :label="t('admin.accounts.stateProbe.model')" />
      <p v-if="running" class="flex items-center gap-2 text-sm text-primary-600" role="status">
        <Icon name="refresh" size="sm" class="animate-spin" />{{ t('admin.accounts.stateProbe.testing') }}
      </p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20">{{ error }}</p>
      <div v-if="!results.length && !running && !error" class="rounded-lg border border-dashed border-gray-300 py-8 text-center text-sm text-gray-500 dark:border-dark-600">{{ t('admin.accounts.stateProbe.noResults') }}</div>
      <article v-for="(result, index) in results" :key="result.started_at + index" class="space-y-3 rounded-xl border bg-white p-4 dark:bg-dark-800" :class="result.verdict === 'healthy' ? 'border-emerald-300 dark:border-emerald-700' : result.verdict === 'degraded' ? 'border-red-300 dark:border-red-700' : 'border-gray-300 dark:border-dark-600'" data-testid="intelligence-result">
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm font-semibold" :class="result.verdict === 'healthy' ? 'text-emerald-600' : result.verdict === 'degraded' ? 'text-red-500' : 'text-gray-500'" data-testid="intelligence-result-verdict">{{ t('admin.accounts.stateProbe.' + result.verdict) }}</span>
          <span class="text-xs text-gray-500">{{ formatDateTimeToMinute(result.finished_at) }} · {{ (result.latency_ms / 1000).toFixed(1) }} s</span>
        </div>
        <p class="text-sm text-gray-700 dark:text-gray-200">{{ result.reason }}</p>
        <pre v-if="result.detail" class="max-h-28 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-xs text-gray-500 dark:bg-dark-900">{{ result.detail }}</pre>
        <dl class="grid grid-cols-2 gap-3 text-xs sm:grid-cols-4">
          <div><dt class="text-gray-500">{{ t('admin.accounts.stateProbe.route') }}</dt><dd class="font-medium" data-testid="intelligence-result-route">{{ result.route === 'bps' ? 'BPS' : 'Codex' }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.accounts.stateProbe.model') }}</dt><dd class="break-all font-medium">{{ result.model }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.accounts.stateProbe.answer') }}</dt><dd class="break-words font-medium" data-testid="intelligence-result-answer">{{ result.answer || '—' }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.accounts.stateProbe.passRate') }}</dt><dd class="font-medium">{{ result.snapshot?.health_rate == null ? '—' : result.snapshot.health_rate.toFixed(1) + '%' }}</dd></div>
        </dl>
      </article>
      <p class="text-xs text-gray-500">{{ t('admin.accounts.stateProbe.limitation') }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="close">{{ t('common.close') }}</button>
      <button type="button" class="btn btn-primary" :disabled="running || !account || !model.trim()" data-testid="intelligence-start" @click="start">{{ t('admin.accounts.stateProbe.start') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Input from '@/components/common/Input.vue'
import Icon from '@/components/icons/Icon.vue'
import { probeOpenAIAccountIntelligence, type OpenAICodexStateProbeResult } from '@/api/admin/accounts'
import { formatDateTimeToMinute } from '@/utils/format'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'updated', account: Account): void }>()
const { t } = useI18n()
const model = ref('gpt-6-astra')
const running = ref(false)
const error = ref('')
const results = ref<OpenAICodexStateProbeResult[]>([])
let controller: AbortController | undefined

function cancel() {
  controller?.abort()
  controller = undefined
  running.value = false
}
function close() { cancel(); emit('close') }
onBeforeUnmount(cancel)
watch(() => [props.show, props.account?.id] as const, () => {
  cancel()
  error.value = ''
  results.value = []
  model.value = 'gpt-6-astra'
})

async function start() {
  if (running.value || !props.show || !props.account || !model.value.trim()) return
  const id = props.account.id
  const request = new AbortController()
  controller = request
  running.value = true
  error.value = ''
  try {
    const result = await probeOpenAIAccountIntelligence(id, model.value.trim(), { signal: request.signal })
    if (request.signal.aborted || !props.show || props.account?.id !== id) return
    results.value = [result, ...results.value].slice(0, 20)
    if (result.snapshot) emit('updated', { ...props.account, openai_codex_state_probe: result.snapshot })
  } catch (cause) {
    if (!request.signal.aborted) error.value = (cause as { message?: string })?.message || t('admin.accounts.stateProbe.requestFailed')
  } finally {
    if (controller === request) {
      running.value = false
      controller = undefined
    }
  }
}
</script>
