import type { Json } from '../../lib/database.types'
import { accountTypesForSide, isJsonObject, type AccountSide, type AccountType } from './model'

export type MetadataEntry = { key: string; value: string }
export type AccountDraft = { side: AccountSide; account_type: AccountType; name: string; institution_name: string; account_identifier: string; notes: string; sort_order: number; metadataEntries: MetadataEntry[] }
export function metadataEntries(metadata: Json): MetadataEntry[] { return isJsonObject(metadata) ? Object.entries(metadata).map(([key, value]) => ({ key, value: typeof value === 'string' ? value : JSON.stringify(value) })) : [] }
export function buildMetadata(entries: MetadataEntry[]): Record<string, string> { return entries.reduce<Record<string, string>>((result, entry) => { if (entry.key.trim()) result[entry.key.trim()] = entry.value.trim(); return result }, {}) }
export function validateAccountDraft(draft: AccountDraft): Record<string, string> {
  const errors: Record<string, string> = {}
  if (!draft.name.trim()) errors.name = 'Enter an account name.'
  if (!draft.institution_name.trim()) errors.institution_name = 'Enter an institution or platform.'
  if (!accountTypesForSide(draft.side).some((type) => type.value === draft.account_type)) errors.account_type = 'Select an account type compatible with the chosen side.'
  const keys = draft.metadataEntries.map((entry) => entry.key.trim()).filter(Boolean)
  if (keys.length !== new Set(keys).size) errors.metadata = 'Metadata field names must be unique.'
  if (draft.metadataEntries.some((entry) => entry.value.trim() && !entry.key.trim())) errors.metadata = 'Add a field name for every metadata value.'
  return errors
}
