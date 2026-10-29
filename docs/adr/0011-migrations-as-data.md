# ADR-0011: pgstore's migrations as data, for a migration tool of the consumer's

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0007](0007-pgstore-components.md)

## Context

`pgstore/migrate` offers `Setup`, `Steps` and `Grants`, so a consumer can
apply pgstore's schema with a migration tool of its own, such as Atlas,
golang-migrate or Flyway. A consumer that did so found four kinds of
friction.

1. **A transactional step's `SQL()` opens with
   `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`.** PostgreSQL accepts
   that only as a transaction's first statement, so two steps cannot share a
   transaction. Nothing said so.
2. **A non-transactional step's `SQL()` is several statements in one
   string,** as in 014 and 017. A tool that sends one string runs it as an
   implicit transaction, and `CREATE INDEX CONCURRENTLY` refuses that. The
   consumer had to split the statements itself.
3. **`SQL()` ends without a statement terminator.**
4. **Package `migrate` imports tern.** That links sprig, mergo, goutils,
   xstrings, copystructure and reflectwalk into a program that only wants
   the SQL text.

The `READ COMMITTED` is not incidental. Migration 016 marks one level of
records a statement at a time, and reads the next level by the next
statement's snapshot. Under `REPEATABLE READ` it would miss a child inserted
in the meantime. `SET LOCAL transaction_isolation` has the same
first-statement rule, so no prelude can set the level in the middle of a
transaction.

## Decision

1. **`pgstore/migrate/ddl`** holds the migrations as data: the embedded
   files, `Step`, `Setup`, `Steps`, `Grants` and `Version`.
   - It imports neither tern nor pgx. It needs the rule for quoting a
     PostgreSQL identifier, and a test holds that rule to pgx's.
   - `migrate` keeps `Run`, which needs tern. It also keeps its `Setup`,
     `Steps` and `Grants`, which return `ddl`'s values. `migrate.Step` stays
     a type of its own, whose `SQL` and `Statements` are `ddl.Step`'s:
     making it an alias would change its identity, which apidiff counts as a
     break. Nothing breaks.
2. **`Step.Statements()`** is a step as a tool sends it.
   - A non-transactional step gives each statement on its own, then the
     version record, to send one at a time outside a transaction.
   - A transactional step gives `SQL()` alone, to run as one transaction.
   - Splitting is sound because a non-transactional step holds no
     dollar-quoted body and ends each statement at a line's end. A test
     holds every such migration, and every future one, to that.
3. **Every `SQL()` ends with `;`.**
4. **Each transactional step runs in a transaction of its own, by
   contract.** The reason above is documented on `Step` and in the guide,
   which gains a vendoring recipe:
   - setup, steps and grants;
   - Atlas's `-- atlas:txmode none` for a non-transactional step;
   - how to find a step the vendored copy is missing, by comparing the
     version table against `ddl.Version`.

## Consequences

- **The binary of a consumer that vendors the SQL** links `ddl` and the
  standard library alone.
- **The pgstore module still requires tern,** for `migrate`. go.sum keeps
  its lines, but the binary never links it.
- **`ddl.Steps` reads the files by tern's naming rule** (`NNN_name.sql`, no
  gap). A test holds its output equal to what tern loads, so the two never
  drift.
