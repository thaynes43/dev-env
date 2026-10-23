// Package maintenancecontract validates one upgrade-shepherd operation
// envelope and correlates a supplied snapshot. It is pure: no network, no
// clock source of its own, and no store. A candidate reports that the
// unverified input meets this source contract. It does not defer an alert,
// suppress one, or authorize an operation.
package maintenancecontract
