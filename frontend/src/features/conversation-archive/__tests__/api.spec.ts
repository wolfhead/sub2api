import { describe, expect, it, vi, beforeEach } from 'vitest'

// vi.mock is hoisted above the imports, so the spy has to be hoisted with it.
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { filterParams, listConversations, listUsers, getConversation, listRequests } from '../api'
import { emptyFilters } from '../types'

describe('conversation archive api', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: { items: [], total: 0 } })
  })

  it('drops blank filters so the query carries only real constraints', () => {
    const filters = { ...emptyFilters(), user: '  alice  ', model: '', q: 'secret' }
    expect(filterParams(filters)).toEqual({ user: 'alice', q: 'secret' })
  })

  it('sends paging alongside the filters', async () => {
    await listConversations({ ...emptyFilters(), user_id: '42' }, 50, 100)
    expect(get).toHaveBeenCalledWith('/admin/archive/conversations', {
      params: { user_id: '42', limit: 50, offset: 100 },
    })
  })

  // The roll-up answers "who is in what I am looking at", so it must not be
  // narrowed by the very user filter it is used to choose.
  it('omits user_id from the user roll-up', async () => {
    await listUsers({ ...emptyFilters(), user_id: '42', q: 'deploy' })
    expect(get).toHaveBeenCalledWith('/admin/archive/users', { params: { q: 'deploy' } })
  })

  it('reads one conversation and its request timeline', async () => {
    get.mockResolvedValueOnce({ data: { id: 7 } })
    await getConversation(7)
    expect(get).toHaveBeenCalledWith('/admin/archive/conversations/7')

    get.mockResolvedValueOnce({ data: { items: [{ id: 1 }] } })
    const rows = await listRequests(7)
    expect(get).toHaveBeenLastCalledWith('/admin/archive/conversations/7/requests')
    expect(rows).toHaveLength(1)
  })

  it('tolerates a response without an items array', async () => {
    get.mockResolvedValueOnce({ data: {} })
    await expect(listRequests(1)).resolves.toEqual([])
  })
})
