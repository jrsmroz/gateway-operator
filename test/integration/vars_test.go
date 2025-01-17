//go:build integration_tests
// +build integration_tests

package integration

import "time"

// -----------------------------------------------------------------------------
// Global Testing Vars & Consts
// -----------------------------------------------------------------------------

const (
	// defaultKongResponseBody is the default response body that will be returned
	// from the Kong Gateway when it is first provisioned and when no default
	// routes are configured.
	defaultKongResponseBody = `{"message":"no Route matched with those values"}`

	// objectUpdateTimeout is the amount of time that will be allowed for
	// conflicts to be resolved before an object update will be considered failed.
	objectUpdateTimeout = time.Second * 30

	// subresourceReadinessWait is the maximum amount of time allowed for
	// sub-resources to become "Ready" after being created on behalf of a
	// parent resource.
	subresourceReadinessWait = time.Second * 30
)
