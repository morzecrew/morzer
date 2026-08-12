package domain

// A test fixture has to say which runtime it describes. The literal rule skips
// test files for that reason; the vocabulary rule does not skip them, and no
// name here says a runtime.

var fixture = map[string]string{"runtime": "compose", "tool": "docker"}
