import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import type { Model } from '@/api/client'
import { buildSourceCreate, exportFilename, exportSourcesBundle } from './sourceManage'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('sourceManage', () => {
  it('validates URL/name/JSON and builds SourceCreate payload', () => {
    const payload = buildSourceCreate({
      url: ' https://novel.example.com ',
      name: ' 示例书源 ',
      group: '',
      type: 0,
      enabled: true,
      enabledExplore: false,
      weight: 10,
      rules: '{"search":{"bookList":"ul li"}}',
      header: '{"User-Agent":"YLReader"}',
    })
    expect(payload).toEqual({
      book_source_url: 'https://novel.example.com',
      book_source_name: '示例书源',
      book_source_group: '默认',
      book_source_type: 0,
      enabled: true,
      enabled_explore: false,
      weight: 10,
      rules: { search: { bookList: 'ul li' } },
      header: { 'User-Agent': 'YLReader' },
    })

    expect(() =>
      buildSourceCreate({
        url: 'ftp://invalid.example.com',
        name: 'bad',
        group: '默认',
        type: 0,
        enabled: true,
        enabledExplore: true,
        weight: 0,
        rules: '{}',
        header: '{}',
      }),
    ).toThrow('http:// 或 https://')
  })

  it('exports sources as formatted JSON with dated filename', async () => {
    const mocked = [{ book_source_url: 'https://a.example.com', book_source_name: 'A' }] as unknown as Model<'Source'>[]
    const spy = vi.spyOn(api, 'exportSources').mockResolvedValue(mocked)
    const bundle = await exportSourcesBundle()
    expect(spy).toHaveBeenCalledWith(undefined)
    expect(bundle.count).toBe(1)
    expect(bundle.filename).toBe(exportFilename())
    expect(JSON.parse(bundle.json)).toEqual(mocked)
  })
})
