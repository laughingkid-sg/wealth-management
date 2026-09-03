alter table public.accounts
  drop constraint accounts_type_side_check;

alter table public.accounts
  add constraint accounts_type_side_check check (
    (side = 'asset' and account_type in (
      'bank_account',
      'brokerage',
      'digital_wallet',
      'crypto_wallet',
      'crypto_exchange',
      'rsu',
      'robo_advisor',
      'retirement_account',
      'other'
    ))
    or
    (side = 'liability' and account_type in (
      'credit_card',
      'personal_loan',
      'other'
    ))
  );
