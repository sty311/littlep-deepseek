# Web search

Search is optional (`web_search`), selected by the model through a function tool, not forced for every question. The provider uses the configured API base plus `/anthropic/v1/messages`, with DeepSeek's native `web_search_20250305` server tool, the same private key, `anthropic-version` header and model configuration.

The tool loop retains complete assistant sub-turn reasoning/content/tool calls until the current turn finishes. It caps searches/sub-turns (default 3), uses a 20-second search client timeout, caches normalized queries for five minutes, and deduplicates structured result URLs. Snippets derive from structured citations/results, not URL regexes over final prose. UI sources are limited; session metadata stores at most eight recent sources.

Search status travels over the existing reasoning control channel. The ordinary answer may include intermediate explanation before a tool call, as in the accepted baseline. Sources are displayed separately and are not permanently stored as tool JSON. Failed search is returned as a tool failure so the model can answer without falsely claiming current verified evidence.

External pages are untrusted reference data. The bridge does not execute page instructions or shell commands. No second search-service key, Node framework, Responses built-in search or external gateway is installed. API availability and costs remain the user's responsibility.
