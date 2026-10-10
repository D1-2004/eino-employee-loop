// Package contract is the portable, dependency-free contract of the Employee
// Loop: the events an employee receives, how each event is routed to a scene
// session, the tools and Host methods a wake may use, and the storage ports
// that back them.
//
// The model is "event + scene drives the employee's workflow":
//
//   - Every input is an Event. Its Class says what happened (business,
//     execution, completion, schedule, enablement); its Mode says why the
//     employee received it (triggered: someone addressed it or its own work
//     reported back; listening: it subscribed, observes or keeps time).
//   - Every event belongs to exactly one Scene: one employee x tenant x
//     locus (group, direct conversation, enterprise, or a Host-defined kind).
//   - Each scene owns one foreground (interactive) session and any number of
//     background sessions. Triggered human interaction goes to the
//     foreground session; everything the employee listens for, and every
//     report about work in flight, is queued on a background session so it
//     never holds the scene's interactive turn.
//
// The package holds two kinds of definitions:
//
//   - Cross-service values and interfaces (services.go, event.go): the
//     events a gateway or Task Manager publishes (EventSink), the requests
//     the loop sends to the Task Manager (TaskManager) and the deliveries it
//     hands to a channel (Deliverer). The JSON Schemas in schema/ describe
//     their wire form.
//   - The loop's own storage ports (store.go: EventLog, SessionQueue,
//     Journal, Outbox, MemoryStore, Conversation) and the Host interface
//     that executes tools (tool.go). The kernel and scene packages depend
//     only on these contracts.
//
// The module ships one implementation of the storage ports, the in-memory
// store/memstore. schema/loop.*.sql is reference DDL for a durable
// implementation; none ships with the module. A Task Manager's storage is
// its own business and is not part of this contract (schema/taskmgr.*.sql
// is reference DDL for one).
//
// This package carries no natural-language strings. Human-readable names and
// descriptions are message keys resolved through the i18n package.
package contract
