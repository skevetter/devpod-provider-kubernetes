---
name: nolint revive cleanup needed in kubernetes provider
description: Remove //nolint:revive from runCommand/runCommandWithDir in kubernetes.go and refactor to use arg struct
type: project
---

The `runCommand` and `runCommandWithDir` methods in `pkg/kubernetes/kubernetes.go` have `//nolint:revive` comments suppressing the argument-limit rule. These need to be removed in a follow-up PR by introducing a struct to group the parameters (stdin, stdout, stderr → io struct, or similar).

**Why:** User explicitly asked for these to be cleaned up in a separate PR after the current lint fix PR (#7) merges.

**How to apply:** After PR #7 merges, create a new PR that refactors `runCommand` and `runCommandWithDir` to use a params struct, then remove the nolint comments.
