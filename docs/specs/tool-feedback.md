# Correctable tool refusals

A business module may explicitly mark a proven no-effect refusal with
`kernel.Recoverable(err)`. Scope/authorization, cancellation, storage failures and
ambiguous external outcomes remain errors. The kernel does not infer business
error classes or retry effects on its own.

An explicit refusal becomes an ordinary tool result for model correction. The
native Eino state clears return-direct routing for a refused batch, so a rejected
terminal does not finish the agent. Later tools in that same sequential batch are
also refused without invoking them. A new valid model batch may correct the call,
within the existing physical-call budget. Successful effects retain their normal
receipts; no state is rolled back or declared absent by this protocol.
