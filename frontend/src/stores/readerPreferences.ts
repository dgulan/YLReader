import type { Model } from '@/api/client'
export type ReaderTheme = 'day' | 'night' | 'paper' | 'green'
export interface ReaderPreferences { theme: ReaderTheme; fontSize: number }
export const defaultPreferences = (): ReaderPreferences => ({ theme: 'paper', fontSize: 20 })
export function parsePreferences(settings: Model<'Settings'>): ReaderPreferences {
  const theme = settings['reader.theme'], size = settings['reader.font_size']
  return { theme: theme === 'day' || theme === 'night' || theme === 'green' || theme === 'paper' ? theme : 'paper', fontSize: typeof size === 'number' && Number.isInteger(size) && size >= 14 && size <= 32 ? size : 20 }
}
export type PreferencePatch = Partial<Record<'reader.theme' | 'reader.font_size', ReaderTheme | number | null>>
