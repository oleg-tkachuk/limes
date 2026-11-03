# Compatibility

What limes promises a consumer between releases, what counts as breaking
that promise, and how a break is announced.

## The contract

It covers every module of the repository alike: limes, pgstore,
redisreplay, otelmetric, exchange and biscuit.

- **The Go API** of every module: its exported names, their signatures,
  and the types they take and return.
- **The error contract** ([errors.md](errors.md)): every code, the HTTP
  status it is answered with, its category, and the reason a refusal is
  challenged with.
- **Defaults**: what a zero `Config` field, or an option left unset, does;
  [defaults.md](defaults.md) lists every field.
- **Documented behaviour**: what a function's comment says it accepts,
  refuses and returns. A function that passed an input and now refuses it
  has changed, even when its signature has not.
- **What is stored and signed**: the token formats and the database
  schema, read by every replica of a rolling upgrade.

## Breaking

Any of these breaks a consumer:

- removing or renaming an exported name, or changing its signature;
- removing an error code, or answering one with another HTTP status,
  category or reason;
- changing a default;
- moving the line between what a documented function accepts and what it
  refuses, either way: refusing what it accepted, or accepting what it
  refused;
- a stored or signed form an earlier release cannot read.

These do not:

- adding a name, a code, a category, a field whose zero value keeps the
  old behaviour, or an option;
- rewording an error's text, which is never a contract.

A consumer must therefore map a code or a category it does not know, as
its own default, rather than fail on it.

## Before 1.0

A break is allowed, and announced three ways: the commit is marked
breaking (`!` or a `BREAKING CHANGE:` footer), which cuts a minor release;
[migration.md](migration.md) says what it was and what it is; and the
release notes list it, with every error code added, removed or answered
otherwise under **Codes**, and every default changed or removed under
**Defaults**. CI holds the first two to the change: a Go API break, a change
to the error contract in [errors.md](errors.md), or a change to a default in
[defaults.md](defaults.md), with no marker fails, and so does a marker with
no migration entry. What a documented function accepts and refuses is held
by its tests alone.

## From 1.0

Every module reaches v1 together with limes, and from then on gives the
same promise: no break lands on the v1 line of any of them. A name that is to go is marked
`// Deprecated:` with what replaces it, and stays, working, for the whole
of v1; it goes only with v2, under a new module path. An error code keeps
its status, category and reason for the whole of v1.
