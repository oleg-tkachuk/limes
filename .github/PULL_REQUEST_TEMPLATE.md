## What and why

<!-- The diff shows what changed. Use this space for why it needed to. -->

## Checklist

- [ ] `task verify-all` passes
- [ ] Commit subjects follow Conventional Commits
- [ ] New behaviour has a test
- [ ] No caller gets another answer — including what no CI job compares:
  an error answered with another code, a call refused that was accepted,
  or accepted that was refused (`docs/compatibility.md`); such a change
  waits for a v2
- [ ] A wire-format change regenerates the golden fixtures in the same commit
