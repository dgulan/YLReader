import { createRouter, createWebHistory } from 'vue-router'
import { getSession } from '@/api/client'
const router = createRouter({ history: createWebHistory(import.meta.env.BASE_URL), routes: [
  { path: '/', redirect: '/bookshelf' },
  { path: '/login', name: 'login', component: () => import('@/views/admin/LoginView.vue') },
  { path: '/bookshelf', name: 'bookshelf', component: () => import('@/views/bookshelf/BookshelfView.vue') },
  { path: '/admin', name: 'admin', component: () => import('@/views/admin/AdminView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/admin/SettingsView.vue') },
  { path: '/settings/replace-rules', name: 'replace-rules', component: () => import('@/views/admin/ReplaceRulesView.vue') },
  { path: '/books/:bookId/cache', name: 'cache-task', component: () => import('@/views/admin/CacheTaskView.vue') },
  { path: '/books/:bookId/edit', name: 'book-editor', component: () => import('@/views/bookshelf/BookEditorView.vue') },
  { path: '/admin/sources/:sourceId', name: 'source-editor', component: () => import('@/views/admin/SourceEditorView.vue') },
  { path: '/reader/:bookId', name: 'reader', component: () => import('@/views/reader/ReaderView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/bookshelf' },
] })
router.beforeEach(to => { if (to.name !== 'login' && !getSession()) return { name: 'login', query: { redirect: to.fullPath } }; if (to.name === 'login' && getSession()) return '/bookshelf' })
export default router
