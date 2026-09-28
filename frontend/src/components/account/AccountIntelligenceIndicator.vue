<template>
  <div v-if="supported" class="w-44 space-y-1.5" data-testid="account-intelligence" :aria-busy="running">
    <div class="flex items-center gap-1.5 text-xs">
      <Icon name="brain" size="sm" class="text-gray-400" />
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateProbe.title') }}</span>
      <span class="font-medium" :class="verdictClass" data-testid="intelligence-verdict" :title="detail">{{ label }}</span>
      <span v-if="snapshot?.route" class="text-[10px] text-gray-400">{{ snapshot.route === 'bps' ? 'BPS' : 'Codex' }}</span>
      <button type="button" class="ml-auto rounded p-0.5 text-gray-400 hover:text-primary-600 disabled:opacity-50" :disabled="running" :title="t('admin.accounts.stateProbe.retest')" :aria-label="t('admin.accounts.stateProbe.retest')" data-testid="intelligence-retest" @click.stop="probe">
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': running }" />
      </button>
    </div>
    <div class="flex h-3 gap-0.5" :title="t('admin.accounts.stateProbe.historyHint')" data-testid="intelligence-history">
      <span v-for="(verdict, index) in segments" :key="index" class="min-w-0 flex-1 rounded-[2px]" :class="verdict === 'healthy' ? 'bg-emerald-500' : verdict === 'degraded' ? 'bg-red-400' : 'bg-gray-200 dark:bg-dark-600'" :data-verdict="verdict" />
    </div>
    <div class="flex items-center justify-between gap-2 text-[10px] text-gray-500 dark:text-gray-400">
      <span>{{ sampleText }}</span>
      <span v-if="healthRate !== null" class="font-medium text-gray-700 dark:text-gray-200" data-testid="intelligence-rate">{{ t('admin.accounts.stateProbe.passRate') }} {{ healthRate.toFixed(1) }}%</span>
    </div>
    <div v-if="snapshot?.last_probe_at" class="text-[10px] text-gray-400" :title="detail">{{ formatDateTimeToMinute(snapshot.last_probe_at) }}</div>
    <p v-if="error" role="alert" class="max-w-44 break-words text-[10px] text-red-500">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { probeOpenAIAccountIntelligence } from '@/api/admin/accounts'
import { formatDateTimeToMinute } from '@/utils/format'
import type { Account, OpenAICodexStateProbeSnapshot } from '@/types'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ (event: 'updated', snapshot: OpenAICodexStateProbeSnapshot): void }>()
const { t } = useI18n()
const localSnapshot = ref<OpenAICodexStateProbeSnapshot | null>(null)
const running = ref(false)
const error = ref('')
let controller: AbortController | undefined
const supported = computed(() => props.account.platform === 'openai' && ['oauth', 'setup-token'].includes(props.account.type))
const snapshot = computed(() => {
  const value = localSnapshot.value ?? props.account.openai_codex_state_probe
  return value?.method === 'candy' ? value : null
})
const verdict = computed(() => snapshot.value?.verdict ?? snapshot.value?.status)
const label = computed(() => {
  if (running.value) return t('admin.accounts.stateProbe.testing')
  if (!snapshot.value) return t('admin.accounts.stateProbe.unprobed')
  if (verdict.value === 'healthy') return t('admin.accounts.stateProbe.healthy')
  if (verdict.value === 'degraded') return t('admin.accounts.stateProbe.degraded')
  return t('admin.accounts.stateProbe.inconclusive')
})
const verdictClass = computed(() => running.value ? 'text-primary-600' : verdict.value === 'healthy' ? 'text-emerald-600 dark:text-emerald-400' : verdict.value === 'degraded' ? 'text-red-500' : 'text-gray-500 dark:text-gray-400')
const healthRate = computed(() => {
  const rate = snapshot.value?.health_rate
  return typeof rate === 'number' && Number.isFinite(rate) ? Math.max(0, Math.min(100, rate)) : null
})
const segments = computed(() => {
  const history = snapshot.value?.history?.slice(-24) ?? []
  return [...Array<string>(24 - history.length).fill('unprobed'), ...history]
})
const sampleText = computed(() => snapshot.value?.sample_count ? t('admin.accounts.stateProbe.samples', { count: snapshot.value.sample_count }) : t('admin.accounts.stateProbe.noSamples'))
const detail = computed(() => [snapshot.value?.reason, snapshot.value?.detail, t('admin.accounts.stateProbe.limitation')].filter(Boolean).join('\n'))

watch(() => props.account, (next, previous) => {
  if (next.id !== previous.id) {
    controller?.abort()
    running.value = false
    error.value = ''
    localSnapshot.value = null
  } else if (next.openai_codex_state_probe !== previous.openai_codex_state_probe) {
    localSnapshot.value = null
  }
})
onBeforeUnmount(() => controller?.abort())

async function probe() {
  if (running.value) return
  const accountId = props.account.id
  const request = new AbortController()
  controller = request
  running.value = true
  error.value = ''
  try {
    const result = await probeOpenAIAccountIntelligence(accountId, undefined, { signal: request.signal })
    if (request.signal.aborted || props.account.id !== accountId) return
    if (result.snapshot) {
      localSnapshot.value = result.snapshot
      emit('updated', result.snapshot)
    } else {
      error.value = t('admin.accounts.stateProbe.requestFailed')
    }
  } catch (cause) {
    if (!request.signal.aborted) {
      error.value = (cause as { message?: string })?.message || t('admin.accounts.stateProbe.requestFailed')
    }
  } finally {
    if (controller === request) {
      running.value = false
      controller = undefined
    }
  }
}
</script>
