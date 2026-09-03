export type Json = string | number | boolean | null | { [key: string]: Json | undefined } | Json[]

export interface Database {
  public: {
    Tables: {
      accounts: {
        Row: {
          id: string; user_id: string; side: 'asset' | 'liability'
          account_type: 'bank_account' | 'brokerage' | 'robo_advisor' | 'retirement_account' | 'digital_wallet' | 'crypto_wallet' | 'crypto_exchange' | 'rsu' | 'credit_card' | 'personal_loan' | 'other'
          name: string; institution_name: string; account_identifier: string | null; notes: string | null; metadata: Json; sort_order: number; deleted_at: string | null; created_at: string; updated_at: string
        }
        Insert: { id?: string; user_id: string; side: 'asset' | 'liability'; account_type: 'bank_account' | 'brokerage' | 'robo_advisor' | 'retirement_account' | 'digital_wallet' | 'crypto_wallet' | 'crypto_exchange' | 'rsu' | 'credit_card' | 'personal_loan' | 'other'; name: string; institution_name: string; account_identifier?: string | null; notes?: string | null; metadata?: Json; sort_order?: number; deleted_at?: string | null }
        Update: { side?: 'asset' | 'liability'; account_type?: 'bank_account' | 'brokerage' | 'robo_advisor' | 'retirement_account' | 'digital_wallet' | 'crypto_wallet' | 'crypto_exchange' | 'rsu' | 'credit_card' | 'personal_loan' | 'other'; name?: string; institution_name?: string; account_identifier?: string | null; notes?: string | null; metadata?: Json; sort_order?: number; deleted_at?: string | null }
        Relationships: []
      }
    }
    Views: Record<string, never>; Functions: Record<string, never>; Enums: Record<string, never>; CompositeTypes: Record<string, never>
  }
}
