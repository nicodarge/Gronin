// Package testsecret holds the one value the suite uses wherever a test needs something
// that must never be written down.
//
// It is a single constant on purpose. SC-005 is asserted twice — once inside the suite,
// by scanning the record store after writing this value into every text field it has,
// and once outside it, by scanning the suite's own output for the same string. Two
// checks against one literal, so neither can drift into asserting a different secret
// from the one the tests actually use.
package testsecret

// Value is the sentinel. It is deliberately unmistakable: anything matching it in a
// record, a log line or a test's output is a leak and nothing else.
const Value = "gronin-sentinel-2f8c41d6e0b97a35-never-record-this"
