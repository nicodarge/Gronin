// Package collections holds the deployment's own catalogue of what a playbook may
// retrieve from: a collection's source, and for the semantic mode the endpoint, the model
// and a reference to the credential.
//
// It lives beside the deployment configuration and the MCP catalogue, and never inside a
// playbook: an entry carries a path on this host and often a credential reference. A
// playbook names an entry and nothing else about it, so the same playbook runs against a
// deployment whose `runbooks` is lexical and one whose `runbooks` is semantic.
//
// Nothing here reaches the network, and nothing here builds an index. Reading the
// catalogue is what a command does before it loads playbooks, which is the point in the
// run where a mistake is cheap.
package collections
