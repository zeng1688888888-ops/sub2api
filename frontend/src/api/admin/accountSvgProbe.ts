import { apiClient } from '../client'
import type { AccountSVGProbeResult } from '@/types'

export interface SVGProbeSettings { model: string; prompts: string[]; use_egress_pool?: boolean }
export async function getSVGProbeSettings(): Promise<SVGProbeSettings> {
  return (await apiClient.get<SVGProbeSettings>('/admin/accounts/svg-probe/settings')).data
}
export async function saveSVGProbeSettings(settings: SVGProbeSettings): Promise<SVGProbeSettings> {
  return (await apiClient.put<SVGProbeSettings>('/admin/accounts/svg-probe/settings', settings)).data
}
export async function runSVGProbe(id: number, signal?: AbortSignal): Promise<AccountSVGProbeResult> {
  return (await apiClient.post<AccountSVGProbeResult>(
    '/admin/accounts/' + id + '/svg-probe', {}, { timeout: 330000, signal }
  )).data
}
export async function getSVGProbeImage(id: number, resultId: string, signal?: AbortSignal): Promise<Blob> {
  return (await apiClient.get('/admin/accounts/' + id + '/svg-probe/' + encodeURIComponent(resultId) + '/image', {
    responseType: 'blob', signal
  })).data
}
