// The environment a workflow step hands a script.

// requireEnv is the variable name, refusing one unset or empty with an error
// that names it.
export function requireEnv(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new Error(`${name} is not set`);
  }
  return value;
}

// optionalEnv is the variable name, or undefined when it is unset or empty:
// Actions renders a missing event field as an empty string.
export function optionalEnv(name: string): string | undefined {
  const value = process.env[name];
  return value === undefined || value === "" ? undefined : value;
}
