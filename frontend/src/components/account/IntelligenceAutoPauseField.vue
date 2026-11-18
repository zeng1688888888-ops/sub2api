<template>
  <div class="flex items-start justify-between gap-4 border-t border-gray-200 pt-4 dark:border-dark-600">
    <div>
      <div class="input-label mb-0">{{ t('admin.accounts.intelligenceScheduling.autoPauseLabel') }}</div>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.intelligenceScheduling.autoPauseHint') }}</p>
    </div>
    <button
      type="button" role="switch" :aria-checked="modelValue"
      :aria-label="t('admin.accounts.intelligenceScheduling.autoPauseLabel')"
      data-testid="intelligence-auto-pause-toggle"
      class="relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
      :class="modelValue ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'"
      @click="emit('update:modelValue', !modelValue)"
    >
      <span class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow transition" :class="modelValue ? 'translate-x-5' : 'translate-x-0'" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: boolean): void }>()
const { t } = useI18n()
</script>
