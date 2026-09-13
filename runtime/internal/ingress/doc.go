// Package ingress is the webhook listener: its options and bounds, the route, the
// signature, the identity, the acceptance's timing, the drop reconciliation and the
// refusal counting. It serves deliveries and nothing else — it shares only the record
// store with internal/api, and reaches the guard and the executor through an interface
// serve fills in, so that it never imports either
// (specs/004-webhook/plan.md, *Project Structure*).
package ingress
