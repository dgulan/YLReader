import { it, expect } from 'vitest'
import { sourcePatch, jsonObject } from './sourceDraft'
it('preserves unknown rule keys and the exact optimistic-lock version', () => { const patch = sourcePatch(7, ' 名称 ', '分组', false, '{"search":{"name":"a@text"},"future":{"x":1}}', '{"X-Test":"value"}'); expect(patch.version).toBe(7); expect(patch.rules?.future).toEqual({ x: 1 }); expect(patch.header).toEqual({ 'X-Test': 'value' }); expect(patch.book_source_name).toBe('名称'); expect(patch).not.toHaveProperty('book_source_type') })
it('rejects arrays, null and malformed JSON instead of replacing the rules with an empty object', () => { for (const value of ['[]', 'null', '{bad']) expect(() => jsonObject(value)).toThrow() })
it('rejects an empty source name', () => { expect(() => sourcePatch(1, ' ', '', true, '{}', '{}')).toThrow('书源名称不能为空') })
