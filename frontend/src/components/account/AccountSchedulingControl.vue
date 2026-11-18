<template>
  <div class="space-y-1.5" data-testid="account-scheduling">
    <div class="flex items-center gap-2">
      <button
        type="button" role="switch" :aria-checked="account.schedulable" :disabled="busy"
        :aria-label="t('admin.accounts.columns.schedulable')"
        :title="account.schedulable ? t('admin.accounts.schedulableEnabled') : t('admin.accounts.schedulableDisabled')"
        class="relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-50"
        :class="account.schedulable ? 'bg-primary-500 hover:bg-primary-600' : 'bg-gray-200 hover:bg-gray-300 dark:bg-dark-600 dark:hover:bg-dark-500'"
        @click="emit('change', !account.schedulable)"
      >
        <span class="pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow transition" :class="account.schedulable ? 'translate-x-4' : 'translate-x-0'" />
      </button>
      <span v-if="supported" class="text-xs text-gray-500" data-testid="intelligence-auto-pause-status">
        {{ t(account.extra?.intelligence_auto_pause_enabled === true ? 'admin.accounts.intelligenceScheduling.enabled' : 'admin.accounts.intelligenceScheduling.monitorOnly') }}
      </span>
    </div>
    <template v-if="supported && !account.schedulable">
      <p v-if="account.extra?.intelligence_auto_paused === true" class="max-w-44 text-xs text-amber-600 dark:text-amber-400" data-testid="intelligence-paused">
        {{ t('admin.accounts.intelligenceScheduling.paused') }}
      </p>
      <button type="button" :disabled="busy" class="text-xs text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" data-testid="enable-scheduling" @click="emit('change', true)">
        {{ t('admin.accounts.intelligenceScheduling.enableManually') }}
      </button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { supportsIntelligenceScheduling } from '@/utils/accountIntelligence'

const props = defineProps<{ account: Account; busy?: boolean }>()
const emit = defineEmits<{ (event: 'change', schedulable: boolean): void }>()
const { t } = useI18n()
const supported = computed(() => supportsIntelligenceScheduling(props.account))
</script>
