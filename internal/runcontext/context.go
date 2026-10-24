// Package runcontext keeps the operation's cancellation and trusted context
// while forwarding Eino node values (for example trace/span context).
package runcontext

import "context"

type operation struct {
	context.Context
	values context.Context
}

func (c operation) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

// Operation preserves the original deadline, cancellation and cause. Values
// introduced by a graph node override inherited values; the node's detached
// scheduling cancellation never replaces the original operation's lifetime.
func Operation(original, node context.Context) context.Context {
	return operation{Context: original, values: context.WithoutCancel(node)}
}
