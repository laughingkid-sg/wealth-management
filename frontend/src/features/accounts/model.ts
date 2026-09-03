import type { Database, Json } from '../../lib/database.types'

export type Account = Database['public']['Tables']['accounts']['Row']
export type AccountSide = 'asset' | 'liability'
export type AccountType = Account['account_type']
export type DeletedFilter = 'active' | 'deleted' | 'all'
export type SortOption = 'custom' | 'name_asc' | 'name_desc'

type AccountTypeOption = { value: AccountType; label: string }
const otherType: AccountTypeOption = { value: 'other', label: 'Others' }
const assetTypes: AccountTypeOption[] = [
  { value: 'bank_account', label: 'Bank account' },
  { value: 'brokerage', label: 'Brokerage' },
  { value: 'robo_advisor', label: 'Robo Advisors' },
  { value: 'retirement_account', label: 'Retirement Account' },
  { value: 'digital_wallet', label: 'Digital wallet' },
  { value: 'crypto_wallet', label: 'Crypto wallet' },
  { value: 'crypto_exchange', label: 'Crypto exchange' },
  { value: 'rsu', label: 'RSU' },
  otherType,
]
const liabilityTypes: AccountTypeOption[] = [
  { value: 'credit_card', label: 'Credit card' },
  { value: 'personal_loan', label: 'Personal loan' },
  otherType,
]
const allTypes = [
  ...assetTypes.filter((item) => item.value !== 'other'),
  ...liabilityTypes.filter((item) => item.value !== 'other'),
  otherType,
]
export const accountTypesForSide = (side: AccountSide | 'all') => {
  if (side === 'asset') return assetTypes
  if (side === 'liability') return liabilityTypes
  return allTypes
}
export const accountTypeLabel = (type: AccountType) => allTypes.find((item) => item.value === type)?.label ?? type
export const isJsonObject = (value: Json): value is { [key: string]: Json } => typeof value === 'object' && value !== null && !Array.isArray(value)
