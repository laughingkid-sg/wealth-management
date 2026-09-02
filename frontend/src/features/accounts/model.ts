import type { Database, Json } from '../../lib/database.types'

export type Account = Database['public']['Tables']['accounts']['Row']
export type AccountSide = 'asset' | 'liability'
export type AccountType = Account['account_type']
export type DeletedFilter = 'active' | 'deleted' | 'all'
export type SortOption = 'custom' | 'name_asc' | 'name_desc'

const assetTypes: Array<{ value: AccountType; label: string }> = [
  { value: 'bank_account', label: 'Bank account' }, { value: 'brokerage', label: 'Brokerage' }, { value: 'crypto_wallet', label: 'Crypto wallet' }, { value: 'crypto_exchange', label: 'Crypto exchange' }, { value: 'rsu', label: 'RSU' },
]
const liabilityTypes: Array<{ value: AccountType; label: string }> = [
  { value: 'credit_card', label: 'Credit card' }, { value: 'personal_loan', label: 'Personal loan' },
]
export const accountTypesForSide = (side: AccountSide) => side === 'asset' ? assetTypes : liabilityTypes
export const accountTypeLabel = (type: AccountType) => [...assetTypes, ...liabilityTypes].find((item) => item.value === type)?.label ?? type
export const isJsonObject = (value: Json): value is { [key: string]: Json } => typeof value === 'object' && value !== null && !Array.isArray(value)
