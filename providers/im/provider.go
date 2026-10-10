// Package im provides concrete IM providers (stdin demo, DWS poll/send).
// The IMProvider interface lives in package core.
package im

import "github.com/dingtalk-fde/eino-employee-loop/core"

// Inbound and Provider are aliases of core ports so callers can stay in this package.
type Inbound = core.Inbound
type Provider = core.IMProvider
