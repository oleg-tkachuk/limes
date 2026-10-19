-- The schema's functions are the store's: the revocation checks read every
-- tenant's revocations with the owner's rights, so a role that may call them
-- learns of any tenant's, and the scope functions are what the policies call.
-- PostgreSQL grants EXECUTE on a new function to every role; here only the
-- roles Migrate grants (MigrateOptions.RuntimeRoles) keep it. A schema's
-- default privileges cannot take a grant to every role away, so a later
-- migration that creates a function revokes it the same way.

REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA limes FROM PUBLIC;
