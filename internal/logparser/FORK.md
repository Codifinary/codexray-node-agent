# Local fork of github.com/codifinary/logparser v1.0.0

Wired in through a `replace` directive in the node agent's `go.mod`. The first
commit vendors v1.0.0 unchanged; the second commit carries only these changes:

1. **Shutdown race (panic / goroutine leak).** `MultilineCollector.dispatch` closed
   `Messages` on context cancellation while the `Add` goroutine could still be
   blocked sending on it from `flushMessage`, which panics with "send on closed
   channel". If the consumer goroutine had already exited, the sender instead blocked
   forever while holding the collector lock, leaking the parser's goroutines and all
   of its retained state. Sends now `select` on the owner's context, `Messages` is
   never closed, `closed` is written under the lock, and `Add` is a no-op after
   shutdown.
2. **Unbounded pattern table.** Each distinct warning/error pattern was retained
   for the lifetime of the container, along with a full sample message (up to
   64 KiB), and every new message was matched against all of them with a linear
   scan. `Options.MaxPatterns` caps the table. Messages beyond the cap are counted
   under `pattern_hash="overflow"` (one bucket per level), so totals are preserved.
   `Options.MaxSampleLen` truncates stored samples (UTF-8 safe).
3. **`Options.DisablePatterns`** assembles multiline messages and forwards them to
   the callback (log export) without pattern extraction. This lets log collection
   be enabled independently of pattern parsing.

`NewParser` keeps its signature and uses the package-level `DefaultOptions`. With
zero `Options`, the fork behaves exactly like v1.0.0 apart from the shutdown fix.
