import { createRouter, createWebHistory } from 'vue-router'

import AdminView from '@/views/admin/AdminView.vue'
import BookshelfView from '@/views/bookshelf/BookshelfView.vue'
import ReaderView from '@/views/reader/ReaderView.vue'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/bookshelf' },
    { path: '/bookshelf', name: 'bookshelf', component: BookshelfView },
    { path: '/admin', name: 'admin', component: AdminView },
    { path: '/reader', name: 'reader', component: ReaderView },
  ],
})

export default router
