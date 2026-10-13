// The semantic-release config: that of the module RELEASE_MODULE names, the
// root module when it is unset. modules.cjs holds every module and says how
// a release is cut.

const { MODULE_ENV, ROOT_MODULE, configFor } = require("./modules.cjs");

module.exports = configFor(process.env[MODULE_ENV] || ROOT_MODULE);
