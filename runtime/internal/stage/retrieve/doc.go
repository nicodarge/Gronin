// Package retrieve is the stage between gather and the agent. For each retrieval a
// playbook declares, in order, it resolves the query, brings the collection's index up to
// date with its sources, searches it, and writes what it found into the run's working
// directory, where the agent reads it as it reads gathered input.
//
// It imports the index, the catalogue and the record's types, and never the run package:
// it returns what the record needs, and the executor records it.
package retrieve
