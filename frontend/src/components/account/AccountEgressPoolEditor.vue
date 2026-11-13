<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountEgressLane, Proxy } from '@/types'

const props = defineProps<{ modelValue: AccountEgressLane[]; proxies: Proxy[]; accountConcurrency: number }>()
const emit = defineEmits<{ 'update:modelValue': [value: AccountEgressLane[]] }>()
const { t } = useI18n()
const enabled = computed(() => props.modelValue.length > 0)
const capacity = computed(() => Math.min(props.accountConcurrency, props.modelValue.reduce((sum, lane) => sum + (Number(lane.concurrency) || 0), 0)))
const usable = (p: Proxy) => p.status === 'active' && (!p.expires_at || Date.parse(p.expires_at) > Date.now())
function add() {
  const proxy = props.proxies.find(p => usable(p) && !props.modelValue.some(l => l.proxy_id === p.id))
  emit('update:modelValue', [...props.modelValue, { proxy_id: proxy?.id ?? 0, concurrency: 5 }])
}
function update(index: number, field: keyof AccountEgressLane, value: string) {
  emit('update:modelValue', props.modelValue.map((lane, i) => i === index ? { ...lane, [field]: Number(value) } : lane))
}
</script>

<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-testid="egress-pool-editor">
    <label class="flex cursor-pointer items-center gap-2 text-sm font-medium">
      <input type="checkbox" :checked="enabled" @change="enabled ? emit('update:modelValue', []) : add()" />
      {{ t('admin.accounts.egressPool.title') }}
    </label>
    <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.egressPool.hint') }}</p>
    <div v-if="enabled" class="mt-3 space-y-3">
      <div v-for="(lane, index) in modelValue" :key="index" class="grid grid-cols-[minmax(0,1fr)_5rem_auto] items-end gap-2">
        <label class="min-w-0 text-xs text-gray-500">
          {{ t('admin.accounts.egressPool.exit', { index: index + 1 }) }}
          <select class="input mt-1" :value="lane.proxy_id" :aria-label="t('admin.accounts.egressPool.exit', { index: index + 1 })" @change="update(index, 'proxy_id', ($event.target as HTMLSelectElement).value)">
            <option :value="0" disabled>{{ t('admin.accounts.egressPool.selectProxy') }}</option>
            <option v-if="lane.proxy_id > 0 && !proxies.some(p => p.id === lane.proxy_id)" :value="lane.proxy_id" disabled>#{{ lane.proxy_id }} — {{ t('admin.accounts.egressPool.unavailable') }}</option>
            <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id" :disabled="!usable(proxy) || modelValue.some((l, i) => i !== index && l.proxy_id === proxy.id)">{{ proxy.name }}{{ proxy.ip_address ? ' · ' + proxy.ip_address : '' }}</option>
          </select>
        </label>
        <label class="text-xs text-gray-500">
          {{ t('admin.accounts.concurrency') }}
          <input class="input mt-1" type="number" min="1" max="2000" step="1" :value="lane.concurrency" :aria-label="t('admin.accounts.egressPool.laneConcurrency', { index: index + 1 })" @input="update(index, 'concurrency', ($event.target as HTMLInputElement).value)" />
        </label>
        <button type="button" class="btn btn-secondary" :aria-label="t('admin.accounts.egressPool.remove')" @click="emit('update:modelValue', modelValue.filter((_, i) => i !== index))">×</button>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <button type="button" class="btn btn-secondary text-xs" :disabled="modelValue.length >= 32" @click="add">{{ t('admin.accounts.egressPool.add') }}</button>
        <span class="text-xs text-gray-500">{{ t('admin.accounts.egressPool.capacity', { count: capacity }) }}</span>
      </div>
      <p class="text-xs text-gray-500">{{ t('admin.accounts.egressPool.defaultProxyHint') }}</p>
    </div>
  </section>
</template>
