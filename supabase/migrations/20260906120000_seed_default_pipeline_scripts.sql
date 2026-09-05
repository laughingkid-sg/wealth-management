-- Seed the global-default pre/post-process Tengo scripts consumed by the
-- source-parsing worker. Both are conservative first-cut defaults: the
-- pre-process script only trims/compacts whitespace in the normalized email
-- before the LLM, and the post-process script only tidies existing candidate
-- values (trim, uppercase currency, fill a blank title from the merchant). They
-- invent no financial facts; the worker re-derives every server-owned field and
-- re-runs full validation after the script, and any script error falls back to
-- the unmodified input. Idempotent: re-running does not create duplicate v1
-- rows. Checksums are the canonical sha256 hex of each source (scriptstore.Checksum).
begin;

insert into private.script_definitions (script_key, version, source, checksum, is_active, notes)
values (
  'email_pre_process',
  1,
  $script$text := import("text")

// email_pre_process (global default) — clean the normalized email before the
// LLM. It only trims and compacts whitespace; it never invents or removes
// financial facts. Input carries subject/sender/text/normalized_content and
// attachment metadata only (no account data, no attachment bytes). Output must
// be {normalized_content: string}.
content := input.normalized_content
if is_undefined(content) {
	content = ""
}

// Drop trailing spaces before newlines, then collapse 3+ blank lines into a
// single paragraph break so boilerplate spacing does not crowd the model.
content = text.re_replace(`[ \t]+\n`, content, "\n")
content = text.re_replace(`\n{3,}`, content, "\n\n")
content = text.trim_space(content)

output := {normalized_content: content}
$script$,
  'ce9de5ba0b0ccc5fbf49f10277f4df9bbd1a659c2688b33e8de249b589e34ec7',
  true,
  'Global default: whitespace-only cleanup of the normalized email before the LLM.'
)
on conflict (script_key, version) do nothing;

insert into private.script_definitions (script_key, version, source, checksum, is_active, notes)
values (
  'transaction_post_process',
  1,
  $script$text := import("text")

// transaction_post_process (global default) — normalize one parsed candidate
// after the LLM. It only tidies existing values (trim, uppercase currency, fill
// a blank title from the merchant); it never fabricates decisive financial
// fields. Server-owned user_id/confidence/auto_eligible are not exposed here and
// are re-derived by the worker afterwards. Output is the candidate object.
c := input

if !is_undefined(c.original_currency) {
	c.original_currency = text.to_upper(text.trim_space(c.original_currency))
}
if !is_undefined(c.merchant_name) {
	c.merchant_name = text.trim_space(c.merchant_name)
}
if !is_undefined(c.title) {
	c.title = text.trim_space(c.title)
}

// A blank title falls back to the merchant name so every candidate carries a
// readable label without inventing amounts, dates, or accounts.
if (is_undefined(c.title) || c.title == "") && !is_undefined(c.merchant_name) {
	c.title = c.merchant_name
}

output := c
$script$,
  '4d09f66874aebc59bfd0d671d1a3954c94aabd7462c26ca97d7e3c4f274f3594',
  true,
  'Global default: tidy candidate values (trim, uppercase currency, title fallback).'
)
on conflict (script_key, version) do nothing;

commit;
