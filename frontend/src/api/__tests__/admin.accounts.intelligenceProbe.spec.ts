import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))
import { probeOpenAIAccountIntelligence } from '@/api/admin/accounts'

describe('account intelligence API', () => {
  beforeEach(() => { post.mockReset() })

  it('uses the actual-route endpoint with a timeout and cancellation', async () => {
    const result = { method: 'candy', route: 'bps', verdict: 'healthy' }
    const controller = new AbortController()
    post.mockResolvedValueOnce({ data: result })
    await expect(probeOpenAIAccountIntelligence(7, ' gpt-6-astra ', { signal: controller.signal })).resolves.toEqual(result)
    expect(post).toHaveBeenCalledWith('/admin/accounts/7/intelligence-probe', { model_id: 'gpt-6-astra' }, { timeout: 120_000, signal: controller.signal })
  })

  it('leaves the default model to the server', async () => {
    post.mockResolvedValue({ data: {} })
    await probeOpenAIAccountIntelligence(7)
    expect(post.mock.calls[0]?.[0]).toBe('/admin/accounts/7/intelligence-probe')
    expect(post.mock.calls[0]?.[1]).toEqual({ model_id: undefined })
  })
})
