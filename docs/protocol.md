# Bridge protocol

Little P sends `POST /teacherp/chat/ask/sse` as `multipart/form-data`. The parser reads `messageContents` metadata, scene/chat ID and optional original `messageImages` JPG part. It does not use client filenames as paths. Requests are bounded at 33 MiB, images at 32 MiB, fields at 64 KiB and 64 parts. Malformed/duplicate files are rejected. Exact accepted metadata shapes are exercised by synthetic parser tests.

DeepSeek Chat Completions uses separate `delta.reasoning_content` and `delta.content`. A single writer batches pending deltas at approximately 80 ms and calls HTTP Flush. Cancellation closes upstream requests; errors do not commit partial sessions.

The SSE adapter emits the minimal Little P-compatible message envelopes over existing channels. Search controls are framed by ASCII record/unit separators around `MYAI4:` JSON; the authored card removes complete/incomplete controls from displayed reasoning. No reasoning is mixed into final answer/history. Formula conversion affects display strings, not model history.

Model-native search uses an Anthropic-compatible endpoint inside the tool loop. Current-turn assistant reasoning/tool calls are returned as required by that API; completed history excludes them. This is an implementation compatibility layer, not a claim of a public vendor protocol. Tests cover ordering, cancellation, splits, malformed upstream frames and context commits.
