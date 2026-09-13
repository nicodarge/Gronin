// Package sources holds the deployment's source catalogue: sources.json, beside the
// deployment configuration, following internal/mcpcatalog's pattern. A source is
// declared by the deployment and never by a playbook, and its secret entry is a
// reference into the deployment configuration rather than a value of its own.
package sources
