// Package index keeps one collection's passages searchable: it walks a source into
// documents, cuts them into passages, and holds them as whole generations in one SQLite
// database per collection, searched lexically through FTS5.
//
// It is derived data. Deleting a collection's database loses nothing a walk of its
// sources does not restore, which is why nothing a run saw is ever kept only here: the
// record copies what a retrieval returned.
//
// It knows nothing of runs, playbooks or the record. A source is handed to it as
// documents, and what it returns is passages with the generation they came from.
package index
