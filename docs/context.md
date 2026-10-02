# Session and context

The existing Little P chat ID keys in-memory sessions. An explicit new topic produces a new ID; a return alone is not used as a reset heuristic. Storage is bounded (eight sessions, two-hour idle TTL, 64 completed pairs and text/image limits). A restart clears all sessions.

Only a successfully completed user/final-assistant pair is committed. Canceled, disconnected and failed requests discard pending turns. Reasoning, SSE controls and current-turn tool messages do not become conversation history. No initial system prompt, summary agent, database or long-term memory is added.

Default policy: model context ceiling 1,000,000, budget 128,000, output reserve 16,000, plus safety/tool allowances. This is a conservative **local estimate**, not a precise tokenizer or verified universal model limit. Text is counted conservatively by UTF-8 bytes; images receive fixed allowances. Configuration changes the budget within checked bounds. Oldest complete pairs are removed, recent exchanges and the latest image pair are prioritized. Admission fails safely if mandatory context alone is too large.

Each session retains one latest original scan (SHA unchanged) and latest structured source metadata. Source-index follow-ups add the relevant metadata without permanently replaying search JSON. New scans supersede old active images. Debug sessions expose counts/timestamps/IDs, never keys or full content; keep the endpoint localhost and avoid publishing its output.
