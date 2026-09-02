import { createClient } from '@supabase/supabase-js'
import type { Database } from './database.types'

const url = import.meta.env.VITE_SUPABASE_URL
const key = import.meta.env.VITE_SUPABASE_PUBLISHABLE_KEY

export const isSupabaseConfigured = Boolean(url && key)

// The fallback lets the app display setup guidance before a local or hosted project is configured.
export const supabase = createClient<Database>(url ?? 'http://127.0.0.1:54321', key ?? 'missing-publishable-key')
