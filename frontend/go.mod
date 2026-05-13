// This file exists only to make ./... in the parent Go module stop at the
// frontend boundary, preventing Go from descending into node_modules.
// The frontend itself is a Vite + React + TypeScript project — not Go code.
module github.com/kuroky/claude-code-monitor/frontend

go 1.26
