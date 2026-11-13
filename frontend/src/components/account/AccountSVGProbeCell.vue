<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, AccountSVGProbeResult } from '@/types'
import { getSVGProbeImage, runSVGProbe } from '@/api/admin/accountSvgProbe'
import { formatDateTimeToMinute } from '@/utils/format'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ updated: [account: Account]; settings: [] }>()
const { t } = useI18n()
const local = ref<AccountSVGProbeResult | null>(null)
const result = computed(() => local.value ?? props.account.svg_probe)
const supported = computed(() => props.account.platform === 'openai' && ['oauth', 'setup-token'].includes(props.account.type) && !props.account.parent_account_id)
const running = ref(false)
const busy = computed(() => running.value || props.account.svg_probe_running)
const error = ref('')
const imageError = ref(false)
const imageURL = ref('')
const previewHTML = ref('')
const isHTML = computed(() => result.value?.mime_type === 'text/html')
const detail = ref(false)
let controller: AbortController | undefined
let imageController: AbortController | undefined
function releaseImage() { if (imageURL.value) URL.revokeObjectURL(imageURL.value); imageURL.value = ''; previewHTML.value = '' }
watch(() => [props.account.id, result.value?.id, result.value?.has_svg] as const, async ([id, resultId, hasSVG]) => {
  imageController?.abort(); releaseImage(); imageError.value = false
  if (!resultId || !hasSVG) return
  const request = new AbortController(); imageController = request
  try {
    const blob = await getSVGProbeImage(id, resultId, request.signal)
    const mime = result.value?.mime_type === 'text/html' ? 'text/html' : 'image/svg+xml'
    const html = mime === 'text/html' ? await blob.text() : ''
    if (!request.signal.aborted) {
      previewHTML.value = html
      imageURL.value = URL.createObjectURL(new Blob([blob], { type: mime }))
    }
  } catch { if (!request.signal.aborted) imageError.value = true }
}, { immediate: true })
watch(() => props.account.id, () => { controller?.abort(); local.value = null; error.value = ''; running.value = false; detail.value = false })
watch(() => props.account.svg_probe, (next) => { if (next && (!local.value || next.finished_at >= local.value.finished_at)) local.value = null })
onBeforeUnmount(() => { controller?.abort(); imageController?.abort(); releaseImage() })
async function run() {
  if (busy.value) return
  const id = props.account.id; const request = new AbortController(); controller = request
  running.value = true; error.value = ''
  try {
    const probe = await runSVGProbe(id, request.signal)
    if (request.signal.aborted || props.account.id !== id) return
    local.value = probe
    emit('updated', { ...props.account, svg_probe: probe, svg_probe_running: false })
  } catch (e) { if (!request.signal.aborted) error.value = (e as Error).message || t('admin.accounts.svgProbe.failed') }
  finally { if (controller === request) { running.value = false; controller = undefined } }
}
</script>

<template>
  <div v-if="supported" class="w-48 space-y-1.5" data-testid="account-svg-probe">
    <div class="flex items-center justify-between gap-2 text-xs">
      <span :class="busy ? 'text-primary-600' : result?.status === 'generated' ? 'text-emerald-600' : result ? 'text-amber-600' : 'text-gray-400'">
        {{ busy ? t('admin.accounts.svgProbe.running') : result?.status === 'generated' ? t('admin.accounts.svgProbe.generated') : result ? t('admin.accounts.svgProbe.failed') : t('admin.accounts.svgProbe.unprobed') }}
      </span>
      <button type="button" class="text-primary-600 disabled:opacity-50" :disabled="busy" @click.stop="run">{{ t('admin.accounts.svgProbe.retest') }}</button>
    </div>
    <button v-if="imageURL" type="button" class="block w-full overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-600" :aria-label="t('admin.accounts.svgProbe.preview')" @click.stop="detail = true">
      <div v-if="isHTML" class="h-28 w-full overflow-hidden" aria-hidden="true">
        <iframe :srcdoc="previewHTML" sandbox="" referrerpolicy="no-referrer" tabindex="-1" :title="t('admin.accounts.svgProbe.preview')" class="pointer-events-none h-[560px] w-[960px] origin-top-left scale-[0.2] border-0" />
      </div>
      <img v-else :src="imageURL" :alt="t('admin.accounts.svgProbe.preview')" class="h-28 w-full object-contain" />
    </button>
    <button v-else type="button" class="flex h-20 w-full items-center justify-center rounded-lg border border-dashed border-gray-300 px-3 text-center text-xs text-gray-400 dark:border-dark-600" @click.stop="result ? detail = true : emit('settings')">
      {{ imageError ? t('admin.accounts.svgProbe.imageMissing') : busy ? t('admin.accounts.svgProbe.running') : result?.message || t('admin.accounts.svgProbe.empty') }}
    </button>
    <div v-if="result" class="flex items-center justify-between gap-2 text-[10px] text-gray-400">
      <span class="truncate" :title="result.model">{{ result.model }}</span>
      <button type="button" class="shrink-0 underline" @click.stop="detail = true">{{ t('admin.accounts.svgProbe.details') }}</button>
    </div>
    <div v-if="result" class="text-[10px] text-gray-400">{{ formatDateTimeToMinute(result.finished_at) }}</div>
    <button type="button" class="text-xs text-primary-600 underline" @click.stop="emit('settings')">{{ t('admin.accounts.svgProbe.editPrompts') }}</button>
    <p class="text-[10px] text-gray-400" :title="t('admin.accounts.svgProbe.autoHint')">{{ t('admin.accounts.svgProbe.autoLabel') }}</p>
    <p v-if="error" role="alert" class="break-words text-xs text-red-500">{{ error }}</p>
    <BaseDialog :show="detail" :title="t('admin.accounts.svgProbe.preview')" width="wide" @close="detail = false">
      <div v-if="result" class="space-y-4">
        <iframe v-if="imageURL && isHTML" :srcdoc="previewHTML" sandbox="" referrerpolicy="no-referrer" :title="t('admin.accounts.svgProbe.preview')" class="h-[65vh] w-full rounded-lg border-0 bg-white" />
        <img v-else-if="imageURL" :src="imageURL" :alt="t('admin.accounts.svgProbe.preview')" class="max-h-[60vh] w-full rounded-lg bg-white object-contain" />
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ result.message }}</p>
        <div class="text-xs text-gray-400">{{ result.model }} · {{ (result.latency_ms / 1000).toFixed(1) }}s</div>
        <p v-if="result.route" class="text-xs text-gray-500">{{ t(result.route === 'egress_pool' ? 'admin.accounts.svgProbe.routePool' : 'admin.accounts.svgProbe.routeDefault') }}</p>
        <label class="input-label">{{ t('admin.accounts.svgProbe.usedPrompt') }} #{{ result.prompt_index + 1 }}</label>
        <pre class="whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-900">{{ result.prompt }}</pre>
        <p class="text-xs text-gray-500">{{ t('admin.accounts.svgProbe.limitation') }}</p>
      </div>
      <template #footer>
        <a v-if="imageURL" :href="imageURL" :download="'account-' + account.id + (isHTML ? '-pelican-test.html' : '-test.svg')" class="btn btn-secondary">{{ t('admin.accounts.svgProbe.download') }}</a>
        <button type="button" class="btn btn-primary" @click="detail = false">{{ t('common.close') }}</button>
      </template>
    </BaseDialog>
  </div>
  <span v-else class="text-xs text-gray-400">—</span>
</template>
